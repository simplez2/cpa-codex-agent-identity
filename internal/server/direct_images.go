package server

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
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

type codexDirectImageRequest struct {
	Body           []byte
	ResponseFormat string
	Stream         bool
	StreamPrefix   string
}

type codexImageResult struct {
	B64JSON       string
	RevisedPrompt string
	OutputFormat  string
	Size          string
	Background    string
	Quality       string
}

type codexImageAccumulator struct {
	created int64
	usage   any
	results []codexImageResult
	seen    map[string]struct{}
}

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
	body, err := readReplayableRequestBody(request, s.config.MaxReplayBodyBytes)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "failed to read image request"})
		return
	}
	prepared, err := buildCodexImageResponsesRequest(request.URL.Path, body, request.Header.Get("Content-Type"))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	target := codexImageTargetURL(s.config.UpstreamOrigin)
	upstreamRequest, err := http.NewRequestWithContext(request.Context(), http.MethodPost, target.String(), bytes.NewReader(prepared.Body))
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
	upstreamRequest.ContentLength = int64(len(prepared.Body))
	upstreamRequest.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(prepared.Body)), nil
	}

	client := *s.upstream
	client.Timeout = 3 * time.Minute
	response, err := client.Do(upstreamRequest)
	if err != nil {
		writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "image upstream unavailable"})
		return
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxCodexImageResponseBytes+1))
		if readErr != nil || len(responseBody) > maxCodexImageResponseBytes {
			writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "failed to read image response"})
			return
		}
		copyCodexImageResponseHeaders(writer.Header(), response.Header)
		writer.WriteHeader(response.StatusCode)
		_, _ = writer.Write(responseBody)
		return
	}

	if prepared.Stream {
		copyCodexImageResponseHeaders(writer.Header(), response.Header)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		writer.Header().Del("Content-Length")
		writer.WriteHeader(http.StatusOK)
		if err := streamCodexImageResponse(writer, response.Body, prepared.ResponseFormat, prepared.StreamPrefix); err != nil {
			writeCodexImageStreamError(writer, err)
		}
		return
	}

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxCodexImageResponseBytes+1))
	if err != nil || len(responseBody) > maxCodexImageResponseBytes {
		writeJSON(writer, http.StatusBadGateway, map[string]any{"error": "failed to read image response"})
		return
	}
	imageResponse, err := codexImageResponseFromResponsesStream(responseBody, prepared.ResponseFormat)
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

func readReplayableRequestBody(request *http.Request, limit int64) ([]byte, error) {
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
	if limit <= 0 {
		limit = defaultReplayBodyMax
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
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

func buildCodexImageResponsesRequest(requestPath string, raw []byte, contentType string) (codexDirectImageRequest, error) {
	payload, err := decodeCodexDirectImagePayload(requestPath, raw, contentType)
	if err != nil {
		return codexDirectImageRequest{}, err
	}
	prompt, _ := payload["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return codexDirectImageRequest{}, errors.New("image prompt is required")
	}
	model, _ := payload["model"].(string)
	if model = strings.TrimSpace(model); model == "" {
		model = codexDefaultImageModel
	}
	action := "generate"
	streamPrefix := "image_generation"
	if requestPath == codexDirectImagesEditsPath {
		action = "edit"
		streamPrefix = "image_edit"
	}
	tool := map[string]any{"type": "image_generation", "action": action, "model": model}
	for _, field := range []string{"size", "quality", "background", "output_format", "input_fidelity", "moderation", "output_compression", "partial_images"} {
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
			return codexDirectImageRequest{}, errors.New("image edit requires an input image")
		}
	}
	body, err := json.Marshal(map[string]any{
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
	if err != nil {
		return codexDirectImageRequest{}, err
	}
	return codexDirectImageRequest{
		Body:           body,
		ResponseFormat: normalizeCodexImageResponseFormat(stringValue(payload["response_format"])),
		Stream:         boolValue(payload["stream"]),
		StreamPrefix:   streamPrefix,
	}, nil
}

func decodeCodexDirectImagePayload(requestPath string, raw []byte, contentType string) (map[string]any, error) {
	mediaType, parameters, _ := mime.ParseMediaType(strings.TrimSpace(contentType))
	if requestPath == codexDirectImagesEditsPath && strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		boundary := strings.TrimSpace(parameters["boundary"])
		if boundary == "" {
			return nil, errors.New("multipart boundary is required")
		}
		return decodeCodexImageMultipart(raw, boundary)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return nil, errors.New("image request must be valid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("image request has trailing data")
	}
	return payload, nil
}

func decodeCodexImageMultipart(raw []byte, boundary string) (map[string]any, error) {
	reader := multipart.NewReader(bytes.NewReader(raw), boundary)
	form, err := reader.ReadForm(defaultReplayBodyMax)
	if err != nil {
		return nil, fmt.Errorf("parse multipart image edit: %w", err)
	}
	defer form.RemoveAll()

	payload := make(map[string]any)
	for key, values := range form.Value {
		key = strings.TrimSpace(key)
		if key == "" || len(values) == 0 {
			continue
		}
		value := strings.TrimSpace(values[len(values)-1])
		switch key {
		case "stream":
			payload[key] = parseCodexImageBool(value)
		case "output_compression", "partial_images", "n":
			if number, parseErr := strconv.ParseInt(value, 10, 64); parseErr == nil {
				payload[key] = number
			} else {
				payload[key] = value
			}
		case "mask[file_id]":
			setCodexImageMaskValue(payload, "file_id", value)
		case "mask[image_url]":
			setCodexImageMaskValue(payload, "image_url", value)
		default:
			payload[key] = value
		}
	}

	images := make([]any, 0)
	for _, field := range []string{"image", "image[]"} {
		for _, fileHeader := range form.File[field] {
			dataURL, dataErr := codexMultipartImageDataURL(fileHeader)
			if dataErr != nil {
				return nil, dataErr
			}
			images = append(images, map[string]any{"image_url": dataURL})
		}
	}
	if len(images) > 0 {
		payload["images"] = images
	}
	if masks := form.File["mask"]; len(masks) > 0 && masks[0] != nil {
		dataURL, dataErr := codexMultipartImageDataURL(masks[0])
		if dataErr != nil {
			return nil, dataErr
		}
		setCodexImageMaskValue(payload, "image_url", dataURL)
	}
	return payload, nil
}

func setCodexImageMaskValue(payload map[string]any, key string, value any) {
	mask, _ := payload["mask"].(map[string]any)
	if mask == nil {
		mask = make(map[string]any)
		payload["mask"] = mask
	}
	mask[key] = value
}

func codexMultipartImageDataURL(fileHeader *multipart.FileHeader) (string, error) {
	if fileHeader == nil {
		return "", errors.New("image file is missing")
	}
	file, err := fileHeader.Open()
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, defaultReplayBodyMax+1))
	if err != nil {
		return "", err
	}
	if len(data) > defaultReplayBodyMax {
		return "", errors.New("image file is too large")
	}
	contentType := strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
	if contentType == "" || strings.EqualFold(contentType, "application/octet-stream") {
		contentType = http.DetectContentType(data)
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func parseCodexImageBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return parseCodexImageBool(typed)
	case json.Number:
		return typed.String() == "1"
	case float64:
		return typed == 1
	default:
		return false
	}
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

func newCodexImageAccumulator() *codexImageAccumulator {
	return &codexImageAccumulator{created: time.Now().Unix(), seen: make(map[string]struct{})}
}

func (a *codexImageAccumulator) appendItem(item map[string]any) {
	if itemType, _ := item["type"].(string); itemType != "image_generation_call" {
		return
	}
	result, _ := item["result"].(string)
	if strings.TrimSpace(result) == "" {
		return
	}
	if _, exists := a.seen[result]; exists {
		return
	}
	a.seen[result] = struct{}{}
	a.results = append(a.results, codexImageResult{
		B64JSON:       result,
		RevisedPrompt: strings.TrimSpace(stringValue(item["revised_prompt"])),
		OutputFormat:  strings.TrimSpace(stringValue(item["output_format"])),
		Size:          strings.TrimSpace(stringValue(item["size"])),
		Background:    strings.TrimSpace(stringValue(item["background"])),
		Quality:       strings.TrimSpace(stringValue(item["quality"])),
	})
}

func (a *codexImageAccumulator) consume(event map[string]any) {
	if item, ok := event["item"].(map[string]any); ok {
		a.appendItem(item)
	}
	response, ok := event["response"].(map[string]any)
	if !ok {
		return
	}
	if created := int64Value(response["created_at"]); created > 0 {
		a.created = created
	}
	if toolUsage, ok := response["tool_usage"].(map[string]any); ok {
		if imageUsage, exists := toolUsage["image_gen"]; exists {
			a.usage = imageUsage
		}
	}
	if a.usage == nil {
		if usage, exists := response["usage"]; exists {
			a.usage = usage
		}
	}
	if output, ok := response["output"].([]any); ok {
		for _, rawItem := range output {
			if item, ok := rawItem.(map[string]any); ok {
				a.appendItem(item)
			}
		}
	}
}

func int64Value(value any) int64 {
	switch typed := value.(type) {
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	default:
		return 0
	}
}

func codexImageResponseFromResponsesStream(raw []byte, responseFormat string) ([]byte, error) {
	accumulator := newCodexImageAccumulator()
	completed := false
	err := scanCodexResponsesSSE(bytes.NewReader(raw), func(event map[string]any) error {
		accumulator.consume(event)
		if stringValue(event["type"]) == "response.completed" {
			completed = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !completed || len(accumulator.results) == 0 {
		return nil, errors.New("no image output")
	}
	return buildCodexImagesAPIResponse(accumulator, responseFormat)
}

func buildCodexImagesAPIResponse(accumulator *codexImageAccumulator, responseFormat string) ([]byte, error) {
	response := map[string]any{"created": accumulator.created, "data": make([]map[string]any, 0, len(accumulator.results))}
	if accumulator.usage != nil {
		response["usage"] = accumulator.usage
	}
	if len(accumulator.results) > 0 {
		first := accumulator.results[0]
		for key, value := range map[string]string{
			"background":    first.Background,
			"output_format": first.OutputFormat,
			"quality":       first.Quality,
			"size":          first.Size,
		} {
			if value != "" {
				response[key] = value
			}
		}
	}
	data := response["data"].([]map[string]any)
	for _, result := range accumulator.results {
		item := map[string]any{}
		if result.RevisedPrompt != "" {
			item["revised_prompt"] = result.RevisedPrompt
		}
		setCodexImageResult(item, result, responseFormat)
		data = append(data, item)
	}
	response["data"] = data
	return json.Marshal(response)
}

func streamCodexImageResponse(writer http.ResponseWriter, reader io.Reader, responseFormat, streamPrefix string) error {
	accumulator := newCodexImageAccumulator()
	completed := false
	err := scanCodexResponsesSSE(reader, func(event map[string]any) error {
		accumulator.consume(event)
		switch stringValue(event["type"]) {
		case "response.image_generation_call.partial_image":
			frame := map[string]any{
				"type":                streamPrefix + ".partial_image",
				"partial_image_index": int64Value(event["partial_image_index"]),
			}
			result := codexImageResult{
				B64JSON:      strings.TrimSpace(stringValue(event["partial_image_b64"])),
				OutputFormat: strings.TrimSpace(stringValue(event["output_format"])),
			}
			if result.B64JSON != "" {
				setCodexImageResult(frame, result, responseFormat)
				writeCodexImageSSEFrame(writer, streamPrefix+".partial_image", frame)
			}
		case "response.completed":
			if len(accumulator.results) == 0 {
				return errors.New("no image output")
			}
			for _, result := range accumulator.results {
				frame := map[string]any{"type": streamPrefix + ".completed"}
				if accumulator.usage != nil {
					frame["usage"] = accumulator.usage
				}
				setCodexImageResult(frame, result, responseFormat)
				writeCodexImageSSEFrame(writer, streamPrefix+".completed", frame)
			}
			completed = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !completed {
		return errors.New("image stream ended before completion")
	}
	return nil
}

func scanCodexResponsesSSE(reader io.Reader, consume func(map[string]any) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxCodexImageResponseBytes)
	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if data == "[DONE]" {
			return nil
		}
		decoder := json.NewDecoder(strings.NewReader(data))
		decoder.UseNumber()
		var event map[string]any
		if decoder.Decode(&event) != nil || event == nil {
			return nil
		}
		return consume(event)
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return scanner.Err()
}

func setCodexImageResult(target map[string]any, result codexImageResult, responseFormat string) {
	if normalizeCodexImageResponseFormat(responseFormat) == "url" {
		target["url"] = "data:" + codexImageMIMEType(result.OutputFormat) + ";base64," + result.B64JSON
	} else {
		target["b64_json"] = result.B64JSON
	}
}

func normalizeCodexImageResponseFormat(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "url") {
		return "url"
	}
	return "b64_json"
}

func codexImageMIMEType(outputFormat string) string {
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

func writeCodexImageSSEFrame(writer http.ResponseWriter, eventName string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", eventName, raw)
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeCodexImageStreamError(writer http.ResponseWriter, err error) {
	message := "upstream did not return image output"
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "ended before completion") {
		message = "image stream ended before completion"
	}
	writeCodexImageSSEFrame(writer, "error", map[string]any{
		"type":  "error",
		"error": map[string]any{"message": message},
	})
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
