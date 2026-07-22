package server

import (
	"encoding/json"
	"net/url"
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
	raw, err := buildCodexImageResponsesRequest(codexDirectImagesGenerationsPath, []byte(`{"model":"gpt-image-2","prompt":"draw a fox","size":"1024x1024","quality":"high"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := lookupJSON(t, raw, "model"); got != codexImagesMainModel {
		t.Fatalf("main model = %v", got)
	}
	if got := lookupJSON(t, raw, "tools", 0, "action"); got != "generate" {
		t.Fatalf("action = %v", got)
	}
	if got := lookupJSON(t, raw, "tools", 0, "model"); got != "gpt-image-2" {
		t.Fatalf("image model = %v", got)
	}
	if got := lookupJSON(t, raw, "input", 0, "content", 0, "text"); got != "draw a fox" {
		t.Fatalf("prompt = %v", got)
	}
}

func TestBuildCodexImageResponsesRequestEdit(t *testing.T) {
	raw, err := buildCodexImageResponsesRequest(codexDirectImagesEditsPath, []byte(`{"model":"gpt-image-2","prompt":"make it blue","images":[{"image_url":"data:image/png;base64,AA=="}],"mask":{"image_url":"data:image/png;base64,AQ=="}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := lookupJSON(t, raw, "tools", 0, "action"); got != "edit" {
		t.Fatalf("action = %v", got)
	}
	if got := lookupJSON(t, raw, "input", 0, "content", 1, "image_url"); got != "data:image/png;base64,AA==" {
		t.Fatalf("input image = %v", got)
	}
	if got := lookupJSON(t, raw, "tools", 0, "input_image_mask", "image_url"); got != "data:image/png;base64,AQ==" {
		t.Fatalf("mask = %v", got)
	}
}

func TestBuildCodexImageResponsesRequestEditRequiresImage(t *testing.T) {
	if _, err := buildCodexImageResponsesRequest(codexDirectImagesEditsPath, []byte(`{"prompt":"edit"}`)); err == nil {
		t.Fatal("expected missing image error")
	}
}

func TestCodexImageResponseFromResponsesStream(t *testing.T) {
	stream := []byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"AA==\",\"revised_prompt\":\"fox\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"created_at\":123,\"usage\":{\"total_tokens\":7},\"output\":[]}}\n\n")
	raw, err := codexImageResponseFromResponsesStream(stream)
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
}

func TestCodexImageTargetURL(t *testing.T) {
	origin, _ := url.Parse("https://chatgpt.com")
	target := codexImageTargetURL(origin)
	if target.String() != "https://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("target = %s", target.String())
	}
}
