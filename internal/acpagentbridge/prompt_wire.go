package acpagentbridge

import (
	"encoding/json"
	"fmt"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

const legacyPromptImageNameMetaKey = "caelis.dev/legacy-prompt-image-name"

// PromptInput is the Host-private normalized input consumed by the Runtime
// bridge after the Surface has validated the standard ACP request with the
// SDK. Raw blocks are retained only so the existing Runtime content adapter
// and the custom steering method share one conversion path; they can become
// typed content once the steering extension adopts SDK content blocks.
type PromptInput struct {
	SessionID string
	Prompt    []json.RawMessage
}

// PromptInputFromACP moves a validated SDK request across the Surface-to-Host
// boundary without keeping a second product wire DTO.
func PromptInputFromACP(request acpsdk.PromptRequest) (PromptInput, error) {
	prompt := make([]json.RawMessage, 0, len(request.Prompt))
	for index, block := range request.Prompt {
		raw, err := normalizedPromptBlock(block)
		if err != nil {
			return PromptInput{}, fmt.Errorf("internal/acpagentbridge: encode prompt[%d]: %w", index, err)
		}
		prompt = append(prompt, raw)
	}
	return PromptInput{
		SessionID: strings.TrimSpace(string(request.SessionId)),
		Prompt:    prompt,
	}, nil
}

// PreserveLegacyPromptImageNames carries the former Caelis image name
// extension across the SDK-owned Surface request. Standard fields have already
// been validated by the SDK; malformed or unrelated legacy fields are ignored.
// Remove this path once supported Caelis peers no longer send the non-standard
// image name field and use the ACP image URI instead.
func PreserveLegacyPromptImageNames(raw json.RawMessage, request *acpsdk.PromptRequest) {
	if request == nil || len(raw) == 0 || len(request.Prompt) == 0 {
		return
	}
	var wire struct {
		Prompt []json.RawMessage `json:"prompt"`
	}
	if json.Unmarshal(raw, &wire) != nil {
		return
	}
	for index := range request.Prompt {
		image := request.Prompt[index].Image
		if image == nil {
			continue
		}
		next := *image
		next.Meta = cloneRawMeta(image.Meta)
		delete(next.Meta, legacyPromptImageNameMetaKey)
		if index < len(wire.Prompt) {
			var legacy struct {
				Type string `json:"type"`
				Name string `json:"name"`
			}
			if json.Unmarshal(wire.Prompt[index], &legacy) == nil && legacy.Type == "image" {
				if name := strings.TrimSpace(legacy.Name); name != "" {
					next.Meta[legacyPromptImageNameMetaKey], _ = json.Marshal(name)
				}
			}
		}
		if len(next.Meta) == 0 {
			next.Meta = nil
		}
		request.Prompt[index].Image = &next
	}
}

func normalizedPromptBlock(block acpsdk.ContentBlock) (json.RawMessage, error) {
	if block.Image == nil {
		return json.Marshal(block)
	}
	next := block
	image := *block.Image
	image.Meta = cloneRawMeta(block.Image.Meta)
	var legacyName string
	if raw, ok := image.Meta[legacyPromptImageNameMetaKey]; ok {
		_ = json.Unmarshal(raw, &legacyName)
		delete(image.Meta, legacyPromptImageNameMetaKey)
	}
	if len(image.Meta) == 0 {
		image.Meta = nil
	}
	next.Image = &image
	raw, err := json.Marshal(next)
	if err != nil || strings.TrimSpace(legacyName) == "" {
		return raw, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	object["name"], _ = json.Marshal(strings.TrimSpace(legacyName))
	return json.Marshal(object)
}

func cloneRawMeta(meta map[string]json.RawMessage) map[string]json.RawMessage {
	if len(meta) == 0 {
		return map[string]json.RawMessage{}
	}
	out := make(map[string]json.RawMessage, len(meta))
	for key, value := range meta {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}
