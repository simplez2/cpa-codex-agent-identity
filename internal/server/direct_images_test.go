package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func lookupJSON(t *testing.T, raw []byte, path ...any) any {
	t.Helper()
	var current any
	if err := json.Unmarshal(raw, &current); err != nil {
		t.Fatal(err)
	}
	for _, part := range path {
		switch key := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				t.Fatalf("%v is not an object", part)
			}
			current = object[key]
		case int:
			array, ok := current.([]any)
			if !ok || key < 0 || key >= len(array) {
				t.Fatalf("%v is not a valid array index", part)
			}
			current = array[key]
		default:
			t.Fatalf("unsupported path component %T", part)
		}
	}
	return current
}

func TestBuildCodexImageResponsesRequestGeneration(t *testing.T) {
	prepared, err := buildCodexImageResponsesRequest(codexDirectImagesGenerationsPath, []byte(`{"model":"gpt-image-2","prompt":"draw a fox","size":"1024x1024","quality":"high","response_format":"url","stream":true}`), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if got := lookupJSON(t, prepared.Body, "model"); got != codexImagesMainModel {
		t.Fatalf("main model = %v", got)
	}
	if got := lookupJSON(t, prepared.Body, "tools", 0, "action"); got != "generate" {
		t.Fatalf("action = %v", got)
	}
	if got := lookupJSON(t, prepared.Body, "tools", 0, "model"); got != "gpt-image-2" {
		t.Fatalf("image model = %v", got)
	}
	if got := lookupJSON(t, prepared.Body, "input", 0, "content", 0, "text"); got != "draw a fox" {
		t.Fatalf("prompt = %v", got)
	}
	if !prepared.Stream || prepared.ResponseFormat != "url" || prepared.StreamPrefix != "image_generation" {
		t.Fatalf("unexpected request metadata: %#v", prepared)
	}
}

func TestBuildCodexImageResponsesRequestEdit(t *testing.T) {
	prepared, err := buildCodexImageResponsesRequest(codexDirectImagesEditsPath, []byte(`{"model":"gpt-image-2","prompt":"make it blue","input_fidelity":"high","images":[{"image_url":"data:image/png;base64,AA=="}],"mask":{"image_url":"data:image/png;base64,AQ=="}}`), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if got := lookupJSON(t, prepared.Body, "tools", 0, "action"); got != "edit" {
		t.Fatalf("action = %v", got)
	}
	if got := lookupJSON(t, prepared.Body, "tools", 0, "input_fidelity"); got != "high" {
		t.Fatalf("input_fidelity = %v", got)
	}
	if got := lookupJSON(t, prepared.Body, "input", 0, "content", 1, "image_url"); got != "data:image/png;base64,AA==" {
		t.Fatalf("input image = %v", got)
	}
	if got := lookupJSON(t, prepared.Body, "tools", 0, "input_image_mask", "image_url"); got != "data:image/png;base64,AQ==" {
		t.Fatalf("mask = %v", got)
	}
	if prepared.StreamPrefix != "image_edit" {
		t.Fatalf("stream prefix = %q", prepared.StreamPrefix)
	}
}

func TestBuildCodexImageResponsesRequestMultipartEdit(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "gpt-image-2")
	_ = writer.WriteField("prompt", "make it blue")
	_ = writer.WriteField("input_fidelity", "high")
	_ = writer.WriteField("response_format", "url")
	_ = writer.WriteField("stream", "true")
	image, _ := writer.CreateFormFile("image", "input.png")
	_, _ = image.Write([]byte("\x89PNG\r\n\x1a\ninput-image"))
	mask, _ := writer.CreateFormFile("mask", "mask.png")
	_, _ = mask.Write([]byte("\x89PNG\r\n\x1a\nmask-image"))
	_ = writer.Close()

	prepared, err := buildCodexImageResponsesRequest(codexDirectImagesEditsPath, body.Bytes(), writer.FormDataContentType())
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Stream || prepared.ResponseFormat != "url" {
		t.Fatalf("unexpected request metadata: %#v", prepared)
	}
	if got := lookupJSON(t, prepared.Body, "tools", 0, "input_fidelity"); got != "high" {
		t.Fatalf("input fidelity = %v", got)
	}
	inputURL, _ := lookupJSON(t, prepared.Body, "input", 0, "content", 1, "image_url").(string)
	if !strings.HasPrefix(inputURL, "data:image/png;base64,") {
		t.Fatalf("input image URL = %q", inputURL)
	}
	maskURL, _ := lookupJSON(t, prepared.Body, "tools", 0, "input_image_mask", "image_url").(string)
	if !strings.HasPrefix(maskURL, "data:image/png;base64,") {
		t.Fatalf("mask URL = %q", maskURL)
	}
}

func TestBuildCodexImageResponsesRequestEditRequiresImage(t *testing.T) {
	if _, err := buildCodexImageResponsesRequest(codexDirectImagesEditsPath, []byte(`{"prompt":"edit"}`), "application/json"); err == nil {
		t.Fatal("expected missing image error")
	}
}

func TestCodexImageResponseFromResponsesStream(t *testing.T) {
	stream := []byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"AA==\",\"revised_prompt\":\"fox\",\"output_format\":\"webp\",\"quality\":\"high\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"created_at\":123,\"tool_usage\":{\"image_gen\":{\"total_tokens\":7}},\"output\":[]}}\n\n")
	raw, err := codexImageResponseFromResponsesStream(stream, "b64_json")
	if err != nil {
		t.Fatal(err)
	}
	if got := lookupJSON(t, raw, "created"); got != float64(123) {
		t.Fatalf("created = %v", got)
	}
	if got := lookupJSON(t, raw, "data", 0, "b64_json"); got != "AA==" {
		t.Fatalf("image = %v", got)
	}
	if got := lookupJSON(t, raw, "usage", "total_tokens"); got != float64(7) {
		t.Fatalf("usage = %v", got)
	}
	if got := lookupJSON(t, raw, "output_format"); got != "webp" {
		t.Fatalf("output format = %v", got)
	}
	if got := lookupJSON(t, raw, "quality"); got != "high" {
		t.Fatalf("quality = %v", got)
	}
}

func TestCodexImageResponseHonorsURLFormat(t *testing.T) {
	stream := []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"AA==\",\"output_format\":\"jpeg\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\n")
	raw, err := codexImageResponseFromResponsesStream(stream, "url")
	if err != nil {
		t.Fatal(err)
	}
	if got := lookupJSON(t, raw, "data", 0, "url"); got != "data:image/jpeg;base64,AA==" {
		t.Fatalf("url = %v", got)
	}
}

func TestStreamCodexImageResponseTranslatesPartialAndCompletedEvents(t *testing.T) {
	stream := strings.NewReader("data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"AA==\",\"partial_image_index\":1,\"output_format\":\"png\"}\r\n\r\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"AQ==\",\"output_format\":\"png\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"tool_usage\":{\"image_gen\":{\"total_tokens\":9}},\"output\":[]}}\n\n")
	recorder := httptest.NewRecorder()
	if err := streamCodexImageResponse(recorder, stream, "b64_json", "image_edit"); err != nil {
		t.Fatal(err)
	}
	out := recorder.Body.String()
	if !strings.Contains(out, "event: image_edit.partial_image") || !strings.Contains(out, `"partial_image_index":1`) || !strings.Contains(out, `"b64_json":"AA=="`) {
		t.Fatalf("partial frame missing: %s", out)
	}
	if !strings.Contains(out, "event: image_edit.completed") || !strings.Contains(out, `"b64_json":"AQ=="`) || !strings.Contains(out, `"total_tokens":9`) {
		t.Fatalf("completed frame missing: %s", out)
	}
}

func TestCodexImageTargetURL(t *testing.T) {
	origin, _ := url.Parse("https://chatgpt.com")
	target := codexImageTargetURL(origin)
	if target.String() != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("target = %s", target.String())
	}
}
