package controller

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/protocol/acp/client"
)

func buildPromptParts(input string, parts []model.ContentPart) []json.RawMessage {
	if len(parts) == 0 {
		input = strings.TrimSpace(input)
		if input == "" {
			return nil
		}
		raw, _ := json.Marshal(client.TextContent{
			Type: "text",
			Text: input,
		})
		return []json.RawMessage{raw}
	}
	out := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case model.ContentPartImage:
			raw, _ := json.Marshal(client.ImageContent{
				Type:     "image",
				MimeType: strings.TrimSpace(part.MimeType),
				Data:     strings.TrimSpace(part.Data),
				Name:     strings.TrimSpace(part.FileName),
			})
			out = append(out, raw)
		default:
			text := part.Text
			if text == "" {
				continue
			}
			raw, _ := json.Marshal(client.TextContent{
				Type: "text",
				Text: text,
			})
			out = append(out, raw)
		}
	}
	if len(out) == 0 && strings.TrimSpace(input) != "" {
		raw, _ := json.Marshal(client.TextContent{
			Type: "text",
			Text: strings.TrimSpace(input),
		})
		out = append(out, raw)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func pickWorkDir(preferred string, fallback string) string {
	if strings.TrimSpace(preferred) != "" {
		return strings.TrimSpace(preferred)
	}
	return strings.TrimSpace(fallback)
}

func derefString(in *string) string {
	if in == nil {
		return ""
	}
	return strings.TrimSpace(*in)
}
