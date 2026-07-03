package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/transcript"
	"github.com/caelis-labs/caelis/surfaces/tui/acpprojector"
)

func ProjectACPEventToTranscriptEvents(env eventstream.Envelope) []TranscriptEvent {
	return transcript.ProjectACPEventToEvents(env, tuiTranscriptProjector{})
}

type tuiTranscriptProjector struct{}

func (tuiTranscriptProjector) ResolveToolName(meta map[string]any, title string, kind string) string {
	return acpUpdateToolName(meta, title, kind)
}

func acpUpdateToolName(meta map[string]any, title string, kind string) string {
	if name := transcript.MetaString(meta, "caelis", "runtime", "tool", "name"); name != "" {
		return name
	}
	return transcriptToolDisplayName("", title, kind)
}

func (tuiTranscriptProjector) ProjectToolCall(input transcript.ToolProjectionInput) transcript.Event {
	return projectTranscriptToolCall(input)
}

func (tuiTranscriptProjector) ProjectToolResult(input transcript.ToolProjectionInput, defaultSuccessStatus string) (transcript.Event, bool) {
	return projectTranscriptToolResult(input, defaultSuccessStatus)
}

func (tuiTranscriptProjector) ApprovalCommandPreview(raw map[string]any) string {
	return approvalCommandPreview(raw)
}

func acpToolContentToDisplay(in []schema.ToolCallContent) []acpprojector.ToolContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]acpprojector.ToolContent, 0, len(in))
	for _, item := range in {
		out = append(out, acpprojector.ToolContent{
			Type:       strings.TrimSpace(item.Type),
			Content:    item.Content,
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
			OldText:    item.OldText,
			NewText:    item.NewText,
		})
	}
	return out
}
