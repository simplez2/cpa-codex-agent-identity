package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	codexDirectImagesGenerationsPath = proxyPathPrefix + "/images/generations"
	codexDirectImagesEditsPath       = proxyPathPrefix + "/images/edits"
	codexResponsesPath               = proxyPathPrefix + "/responses"
	codexImagesMainModel             = "gpt-5.4-mini"
	codexDefaultImageModel           = "gpt-image-2"
	maxCodexImageResponseBytes       = 96 << 20
)

func isCodexDirectImagePath(requestPath string) bool {
	requestPath = strings.TrimSpace(requestPath)
	return requestPath == codexDirectImagesGenerationsPath || requestPath == codexDirectImagesEditsPath
}

// handleCodexDirectImage bridges CPA's direct /images/* route back to the Codex
// Responses image_generation tool. ChatGPT auth works on /responses while the
// direct image route rejects OAuth, PAT, and Agent Identity credentials.
func (s *Server) handleCodexDirectImage(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	body, err := readReplayableRequestBody(request)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "failed to read image request"})
		return
	}
	responsesBody, err := buildCodexImageResponsesRequest(request.URL.Path, body)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	target := codexImageTargetURL(s.config.UpstreamOrigin)
	upstreamRequest, err := http.NewRequestWithContext(request.Context(), http.MethodPost, target.String(), bytes.NewReader(responsesBody))
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "failed to build image request"})
		return
	}
	upstreamRequest.Header = request.Header.Clone()
	upstreamRequest.Header.Set("Content-Type", "application/json")
	upstreamRequest.Header.Set("Accept", "text/event-stream")
	for _, name := range []string{"Content-Length", "X-Agent-Identity-Sidecar-Key", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Agent-Identity-ID"} {
		upstreamRequest.Header.Del(name)
	}
	upstreamRequest.Host = s.config.UpstreamOrigin.Host
	upstreamRequest.ContentLength = int64(len(responsesBody))
	upstreamRequest.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(responsesBody)), nil
	}

	client := *s.upstream
	client.Timeout = 3 * time.Minute
	response, err := client.Do(upstreamRequest)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "image upstream unavailable"})
		return
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxCodexImageResponseBytes+1))
	if err != nil || len(responseBody) > maxCodexImageResponseBytes {
		writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "failed to read image response"})
		return
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		copyCodexImageResponseHeaders(writer.Header(), response.Header)
		writer.WriteHeader(response.StatusCode)
		_, _ = writer.Write(responseBody)
		return
	}

	imageResponse, err := codexImageResponseFromResponsesStream(responseBody)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "upstream did not return image output"})
		return
	}
	copyCodexImageResponseHeaders(writer.Header(), response.Header)
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Del("Content-Length")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(imageResponse)
}

func readReplayableRequestBody(request *http.Request) ([]byte, error) {
	if request == nil || request.Body == nil || request.Body == http.NoBody {
		return nil, errors.New("image request body is required")
	}
	reader := request.Body
	if request.GetBody != nil {
		clone, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		defer clone.Close()
		reader = clone
	}
	body, err := io.ReadAll(io.LimitReader(reader, defaultReplayBodyMax+1))
	if err != nil {
		return nil, err
	}
	if len(body) > defaultReplayBodyMax {
		return nil, errors.New("image request body is too large")
	}
	return body, nil
}

func codexImageTargetURL(origin *url.URL) url.URL {
	target := *origin
	target.Path = strings.TrimRight(origin.Path, "/") + codexResponsesPath
	target.RawPath, target.RawQuery, target.Fragment = "", "", ""
	return target
}

func buildCodexImageResponsesRequest(requestPath string, raw []byte) ([]byte, error) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, errors.New("image request must be valid JSON")
	}
	prompt, _ := payload["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, errors.New("image prompt is required")
	}
	model, _ := payload["model"].(string)
	if model = strings.TrimSpace(model); model == "" {
		model = codexDefaultImageModel
	}
	action := "generate"
	if requestPath == codexDirectImagesEditsPath {
		action = "edit"
	}
	tool := map[string]any{"type": "image_generation", "action": action, "model": model}
	for _, field := range []string{"size", "quality", "background", "output_format", "moderation", "output_compression", "partial_images"} {
		if value, ok := payload[field]; ok {
			tool[field] = value
		}
	}
	if mask, ok := payload["mask"].(map[string]any); ok {
		inputMask := map[string]any{}
		for _, key := range []string{"image_url", "file_id"} {
			if value, exists := mask[key]; exists {
				inputMask[key] = value
			}
		}
		if len(inputMask) > 0 {
			tool["input_image_mask"] = inputMask
		}
	}
	content := []any{map[string]any{"type": "input_text", "text": prompt}}
	if action == "edit" {
		for _, image := range collectCodexImageInputs(payload) {
			content = append(content, image)
		}
		if len(content) == 1 {
			return nil, errors.New("image edit requires an input image")
		}
	}
	return json.Marshal(map[string]any{
		"instructions": "", "stream": true,
		"reasoning":           map[string]any{"effort": "medium", "summary": "auto"},
		"parallel_tool_calls": true,
		"include":             []string{"reasoning.encrypted_content"},
		"model":               codexImagesMainModel,
		"store":               false,
		"tool_choice":         map[string]any{"type": "image_generation"},
		"input":               []any{map[string]any{"type": "message", "role": "user", "content": content}},
		"tools":               []any{tool},
	})
}

func collectCodexImageInputs(payload map[string]any) []map[string]any {
	var values []any
	if images, ok := payload["images"].([]any); ok {
		values = append(values, images...)
	}
	if image, ok := payload["image"]; ok {
		if images, isArray := image.([]any); isArray {
			values = append(values, images...)
		} else {
			values = append(values, image)
		}
	}
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		switch image := value.(type) {
		case string:
			if strings.TrimSpace(image) != "" {
				result = append(result, map[string]any{"type": "input_image", "image_url": image})
			}
		case map[string]any:
			part := map[string]any{"type": "input_image"}
			for _, key := range []string{"image_url", "file_id"} {
				if item, exists := image[key]; exists {
					part[key] = item
				}
			}
			if len(part) > 1 {
				result = append(result, part)
			}
		}
	}
	return result
}

func codexImageResponseFromResponsesStream(raw []byte) ([]byte, error) {
	type imageData struct {
		B64JSON       string `json:"b64_json"`
		RevisedPrompt string `json:"revised_prompt,omitempty"`
	}
	created := time.Now().Unix()
	var usage any
	results := make([]imageData, 0, 1)
	seen := map[string]struct{}{}
	appendItem := func(item map[string]any) {
		if itemType, _ := item["type"].(string); itemType != "image_generation_call" {
			return
		}
		result, _ := item["result"].(string)
		if strings.TrimSpace(result) == "" {
			return
		}
		if _, exists := seen[result]; exists {
			return
		}
		seen[result] = struct{}{}
		revised, _ := item["revised_prompt"].(string)
		results = append(results, imageData{B64JSON: result, RevisedPrompt: revised})
	}
	scanner := bufio.NewScanner(bytes.NewReader(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))))
	scanner.Buffer(make([]byte, 64*1024), maxCodexImageResponseBytes)
	var dataLines []string
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if data == "[DONE]" {
			return
		}
		var event map[string]any
		if json.Unmarshal([]byte(data), &event) != nil {
			return
		}
		if item, ok := event["item"].(map[string]any); ok {
			appendItem(item)
		}
		if response, ok := event["response"].(map[string]any); ok {
			if value, ok := response["created_at"].(float64); ok && value > 0 {
				created = int64(value)
			}
			if value, exists := response["usage"]; exists {
				usage = value
			}
			if output, ok := response["output"].([]any); ok {
				for _, rawItem := range output {
					if item, ok := rawItem.(map[string]any); ok {
						appendItem(item)
					}
				}
			}
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, errors.New("no image output")
	}
	response := map[string]any{"created": created, "data": results}
	if usage != nil {
		response["usage"] = usage
	}
	return json.Marshal(response)
}

func copyCodexImageResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		switch strings.ToLower(key) {
		case "content-length", "content-encoding", "transfer-encoding", "connection", "keep-alive":
			continue
		}
		dst[key] = append([]string(nil), values...)
	}
}
