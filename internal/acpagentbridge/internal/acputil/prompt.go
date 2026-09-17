package acputil

import (
	"encoding/json"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// BuildPromptParts converts provider-neutral prompt content into standard ACP
// content blocks. Product runtimes keep model values outside the wire package;
// this adapter is the single bridge conversion used by controller and child
// steering/prompt paths.
func BuildPromptParts(input string, parts []model.ContentPart) []json.RawMessage {
	if len(parts) == 0 {
		input = strings.TrimSpace(input)
		if input == "" {
			return nil
		}
		raw, _ := json.Marshal(acpsdk.TextBlock(input))
		return []json.RawMessage{raw}
	}
	out := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case model.ContentPartImage:
			out = append(out, marshalPromptImage(part))
		default:
			if part.Text == "" {
				continue
			}
			raw, _ := json.Marshal(acpsdk.TextBlock(part.Text))
			out = append(out, raw)
		}
	}
	if len(out) == 0 && strings.TrimSpace(input) != "" {
		raw, _ := json.Marshal(acpsdk.TextBlock(strings.TrimSpace(input)))
		out = append(out, raw)
	}
	return out
}

func marshalPromptImage(part model.ContentPart) json.RawMessage {
	raw, _ := json.Marshal(acpsdk.ImageBlock(strings.TrimSpace(part.Data), strings.TrimSpace(part.MimeType)))
	name := strings.TrimSpace(part.FileName)
	if name == "" {
		return raw
	}

	// Inline image filenames are not part of standard ACP. Keep the legacy
	// top-level name only in this Host-private adapter so older Caelis peers can
	// round-trip attachment display names. Remove it when those peers no longer
	// need the extension; type, data, and mimeType remain SDK-owned.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return raw
	}
	fields["name"], _ = json.Marshal(name)
	raw, _ = json.Marshal(fields)
	return raw
}

// ContentPartsContainImage reports whether prompt content requires ACP image
// support.
func ContentPartsContainImage(parts []model.ContentPart) bool {
	for _, part := range parts {
		if part.Type == model.ContentPartImage {
			return true
		}
	}
	return false
}
