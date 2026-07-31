package controlclient

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestValidateCommandRequestAcceptsTypedPromptContent(t *testing.T) {
	t.Parallel()

	target := TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	contentParts := []model.ContentPart{
		{Type: model.ContentPartImage, MimeType: "image/png", Data: "aW1n", FileName: "shot.png"},
		{Type: model.ContentPartText, Text: " describe this"},
	}
	requests := []any{
		PromptRequest{
			WriteBase:    WriteBase{SessionID: "session-1"},
			ContentParts: contentParts,
		},
		SteerRequest{
			WriteBase:    WriteBase{SessionID: "session-1"},
			Target:       target,
			ContentParts: contentParts,
		},
	}
	for _, request := range requests {
		if err := validateCommandRequest(ActionPrompt, request); err != nil {
			t.Fatalf("validateCommandRequest(%T) = %v", request, err)
		}
	}
}

func TestValidateCommandRequestRejectsInvalidTypedPromptContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		parts []model.ContentPart
	}{
		{name: "empty"},
		{
			name:  "unknown type",
			parts: []model.ContentPart{{Type: "audio", Data: "aW1n"}},
		},
		{
			name:  "empty text",
			parts: []model.ContentPart{{Type: model.ContentPartText}},
		},
		{
			name:  "text with image fields",
			parts: []model.ContentPart{{Type: model.ContentPartText, Text: "hello", MimeType: "image/png"}},
		},
		{
			name:  "image with text",
			parts: []model.ContentPart{{Type: model.ContentPartImage, Text: "hello", MimeType: "image/png", Data: "aW1n"}},
		},
		{
			name:  "image without MIME",
			parts: []model.ContentPart{{Type: model.ContentPartImage, Data: "aW1n"}},
		},
		{
			name:  "image without MIME subtype",
			parts: []model.ContentPart{{Type: model.ContentPartImage, MimeType: "image/", Data: "aW1n"}},
		},
		{
			name:  "invalid image data",
			parts: []model.ContentPart{{Type: model.ContentPartImage, MimeType: "image/png", Data: "%%%"}},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateCommandRequest(ActionPrompt, PromptRequest{
				WriteBase:    WriteBase{SessionID: "session-1"},
				ContentParts: test.parts,
			})
			if err == nil {
				t.Fatalf("validateCommandRequest() accepted %#v", test.parts)
			}
		})
	}
}
