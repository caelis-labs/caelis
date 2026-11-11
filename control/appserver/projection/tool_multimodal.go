package projection

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// Project selected js_exec results and mixed canonical tool content from their
// original parts, not the lossy rawOutput summary. Other text-only and single-JSON
// results retain their tool-specific presentation.
func projectedMultimodalToolResultContent(message *model.Message) []eventstream.ToolCallContent {
	if message == nil {
		return nil
	}
	results := message.ToolResults()
	if len(results) != 1 {
		return nil
	}
	var hasImage, hasJSON bool
	for _, part := range results[0].Content {
		if part.Kind == model.PartKindMedia && part.Media != nil &&
			part.Media.Modality == model.MediaModalityImage && part.Media.Source.Kind == model.MediaSourceInline &&
			strings.TrimSpace(part.Media.Source.Data) != "" {
			hasImage = true
		}
		if part.Kind == model.PartKindJSON && part.JSON != nil {
			hasJSON = true
		}
	}
	if results[0].Name != "js_exec" && !hasImage && (len(results[0].Content) <= 1 || !hasJSON) {
		return nil
	}
	out := make([]eventstream.ToolCallContent, 0, len(results[0].Content))
	for _, part := range results[0].Content {
		var content any
		switch part.Kind {
		case model.PartKindText:
			if part.Text != nil {
				content = eventstream.TextContent{Type: "text", Text: part.Text.Text}
			}
		case model.PartKindJSON:
			if part.JSON != nil {
				content = eventstream.TextContent{Type: "text", Text: string(part.JSON.Value)}
			}
		case model.PartKindMedia:
			if part.Media != nil && part.Media.Modality == model.MediaModalityImage && part.Media.Source.Kind == model.MediaSourceInline {
				content = map[string]any{
					"type": "image", "mimeType": part.Media.MimeType, "data": part.Media.Source.Data,
				}
			}
		}
		if content != nil {
			out = append(out, eventstream.ToolCallContent{Type: "content", Content: content})
		}
	}
	return out
}
