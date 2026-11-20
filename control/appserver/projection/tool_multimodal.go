package projection

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// Project selected js_exec results and mixed canonical tool content from their
// model-visible parts, not the lossy rawOutput summary.
func projectedMultimodalToolResultContent(message *model.Message, result *tool.Result) []eventstream.ToolCallContent {
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
	showParts := results[0].Name == "js_exec" || hasImage || len(results[0].Content) > 1 && hasJSON
	if result != nil {
		provenance, _ := result.Metadata["application_call"].(map[string]any)
		if provenance["result_format"] == sessionprofile.ResultFormatContentV1 {
			// Only callback results with actual content were previously shown:
			// the old single receipt block contained both bookkeeping and data.
			receiptBlocks := 1
			if _, hasData := result.Structured(); hasData {
				receiptBlocks++
			}
			showParts = len(result.Content) > receiptBlocks && (hasImage || len(results[0].Content) > 1)
		}
	}
	if !showParts {
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
