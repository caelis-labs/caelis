package appserver

import (
	"encoding/base64"
	"strings"
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

func TestValidateCommandRequestRejectsAggregateImagePayloadOverLimit(t *testing.T) {
	imageData := base64.StdEncoding.EncodeToString(make([]byte, MaxPromptImageTotalBytes/2+1))
	parts := []model.ContentPart{
		{Type: model.ContentPartImage, MimeType: "image/png", Data: imageData},
		{Type: model.ContentPartImage, MimeType: "image/png", Data: imageData},
	}

	err := validateCommandRequest(ActionPrompt, PromptRequest{
		WriteBase:    WriteBase{SessionID: "session-1"},
		ContentParts: parts,
	})
	if err == nil {
		t.Fatal("validateCommandRequest() accepted an aggregate image payload over the shared limit")
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
		{
			name:  "malformed trailing base64",
			parts: []model.ContentPart{{Type: model.ContentPartImage, MimeType: "image/png", Data: "YQ==AAAA"}},
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

func TestDecodedPromptImageByteCountRejectsMalformedTrailingBase64(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
	}{
		{name: "padding then valid quantum", data: "YQ==AAAA"},
		{name: "padding then invalid chars", data: "YQ==%%%%"},
		{name: "one-pad then valid quantum", data: "YWI=" + "AAAA"},
		{name: "newlines after padding then data", data: "YQ==\nAAAA"},
		{name: "decoder buffer boundary", data: paddedBase64OfLength(t, 1024) + "AAAA"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := base64.StdEncoding.DecodeString(test.data); err == nil {
				t.Fatal("DecodeString accepted malformed trailing base64")
			}
			if _, err := decodedPromptImageByteCount(test.data); err == nil {
				t.Fatal("decodedPromptImageByteCount accepted malformed trailing base64")
			}
			err := validatePromptContent("prompt", "", []model.ContentPart{{
				Type:     model.ContentPartImage,
				MimeType: "image/png",
				Data:     test.data,
			}})
			if err == nil || !strings.Contains(err.Error(), "must be non-empty base64") {
				t.Fatalf("validatePromptContent() error = %v, want invalid base64", err)
			}
		})
	}
}

func TestDecodedPromptImageByteCountEnforcesPerImageLimitBoundary(t *testing.T) {
	exact := base64.StdEncoding.EncodeToString(make([]byte, MaxPromptImageBytes))
	n, err := decodedPromptImageByteCount(exact)
	if err != nil {
		t.Fatalf("exact limit error = %v", err)
	}
	if n != MaxPromptImageBytes {
		t.Fatalf("exact limit count = %d, want %d", n, MaxPromptImageBytes)
	}
	if err := validatePromptContent("prompt", "", []model.ContentPart{{
		Type:     model.ContentPartImage,
		MimeType: "image/png",
		Data:     exact,
	}}); err != nil {
		t.Fatalf("validatePromptContent(exact limit) = %v", err)
	}

	over := base64.StdEncoding.EncodeToString(make([]byte, MaxPromptImageBytes+1))
	n, err = decodedPromptImageByteCount(over)
	if err != nil {
		t.Fatalf("over-limit count error = %v", err)
	}
	if n != MaxPromptImageBytes+1 {
		t.Fatalf("over-limit count = %d, want %d", n, MaxPromptImageBytes+1)
	}
	err = validatePromptContent("prompt", "", []model.ContentPart{{
		Type:     model.ContentPartImage,
		MimeType: "image/png",
		Data:     over,
	}})
	if err == nil || !strings.Contains(err.Error(), "is too large") {
		t.Fatalf("validatePromptContent(over limit) error = %v, want too-large rejection", err)
	}
}

func TestDecodedPromptImageByteCountAcceptsNewlines(t *testing.T) {
	t.Parallel()

	n, err := decodedPromptImageByteCount("YQ==\n")
	if err != nil {
		t.Fatalf("newline-padded base64 error = %v", err)
	}
	if n != 1 {
		t.Fatalf("newline-padded count = %d, want 1", n)
	}
}

func paddedBase64OfLength(t *testing.T, encodedLen int) string {
	t.Helper()
	if encodedLen%4 != 0 {
		t.Fatalf("encodedLen = %d, want a multiple of 4", encodedLen)
	}
	decodedLen := encodedLen/4*3 - 2
	encoded := base64.StdEncoding.EncodeToString(make([]byte, decodedLen))
	if len(encoded) != encodedLen {
		t.Fatalf("encoded length = %d, want %d", len(encoded), encodedLen)
	}
	if encoded[len(encoded)-2:] != "==" {
		t.Fatalf("encoded %q does not end in two padding bytes", encoded[len(encoded)-4:])
	}
	return encoded
}
