package acpagentbridge

import (
	"encoding/json"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

func TestPromptInputFromACPPreservesValidatedContent(t *testing.T) {
	uri := "file:///tmp/image.png"
	input, err := PromptInputFromACP(acpsdk.PromptRequest{
		SessionId: " session-1 ",
		Prompt: []acpsdk.ContentBlock{
			{Text: &acpsdk.ContentBlockText{Type: "text", Text: "hello"}},
			{Image: &acpsdk.ContentBlockImage{Type: "image", MimeType: "image/png", Data: "aGVsbG8=", Uri: &uri}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.SessionID != "session-1" || len(input.Prompt) != 2 {
		t.Fatalf("PromptInputFromACP() = %#v", input)
	}
	var textBlock map[string]any
	if err := json.Unmarshal(input.Prompt[0], &textBlock); err != nil {
		t.Fatal(err)
	}
	if textBlock["type"] != "text" || textBlock["text"] != "hello" {
		t.Fatalf("text block = %#v", textBlock)
	}
	var imageBlock map[string]any
	if err := json.Unmarshal(input.Prompt[1], &imageBlock); err != nil {
		t.Fatal(err)
	}
	if imageBlock["type"] != "image" || imageBlock["mimeType"] != "image/png" || imageBlock["data"] != "aGVsbG8=" || imageBlock["uri"] != uri {
		t.Fatalf("image block = %#v", imageBlock)
	}
}

func TestPromptInputFromACPRejectsInvalidUnion(t *testing.T) {
	_, err := PromptInputFromACP(acpsdk.PromptRequest{Prompt: []acpsdk.ContentBlock{{}}})
	if err == nil {
		t.Fatal("PromptInputFromACP() error = nil")
	}
}

func TestPromptInputFromACPRestoresLegacyImageNameWithoutLeakingSidecar(t *testing.T) {
	raw := json.RawMessage(`{"sessionId":"session-1","prompt":[{"type":"image","mimeType":"image/png","data":"aGVsbG8=","name":"shot.png","_meta":{"kept":true}}]}`)
	var request acpsdk.PromptRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	PreserveLegacyPromptImageNames(raw, &request)
	input, err := PromptInputFromACP(request)
	if err != nil {
		t.Fatal(err)
	}
	var block map[string]any
	if err := json.Unmarshal(input.Prompt[0], &block); err != nil {
		t.Fatal(err)
	}
	if block["name"] != "shot.png" {
		t.Fatalf("legacy image name = %#v", block["name"])
	}
	meta, _ := block["_meta"].(map[string]any)
	if meta["kept"] != true || meta[legacyPromptImageNameMetaKey] != nil {
		t.Fatalf("normalized image meta = %#v", meta)
	}
}
