package acputil

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
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
		raw, _ := json.Marshal(client.TextContent{Type: "text", Text: input})
		return []json.RawMessage{raw}
	}
	out := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case model.ContentPartImage:
			raw, _ := json.Marshal(client.ImageContent{
				Type: "image", MimeType: strings.TrimSpace(part.MimeType),
				Data: strings.TrimSpace(part.Data), Name: strings.TrimSpace(part.FileName),
			})
			out = append(out, raw)
		default:
			if part.Text == "" {
				continue
			}
			raw, _ := json.Marshal(client.TextContent{Type: "text", Text: part.Text})
			out = append(out, raw)
		}
	}
	if len(out) == 0 && strings.TrimSpace(input) != "" {
		raw, _ := json.Marshal(client.TextContent{Type: "text", Text: strings.TrimSpace(input)})
		out = append(out, raw)
	}
	return out
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
