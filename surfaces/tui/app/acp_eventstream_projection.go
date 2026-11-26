package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
	"github.com/caelis-labs/caelis/surfaces/tui/acpprojector"
)

func ProjectACPEventToTranscriptEvents(env eventstream.Envelope) []TranscriptEvent {
	return markSessionResultProjection(env, transcript.ProjectACPEventToEvents(env, tuiTranscriptProjector{desktopWorld: desktopWorldProjection(env)}))
}

func markSessionResultProjection(env eventstream.Envelope, events []TranscriptEvent) []TranscriptEvent {
	for i := range events {
		event := &events[i]
		switch {
		case event.Kind == TranscriptEventNarrative && event.NarrativeKind == TranscriptNarrativeAssistant:
			event.ResultSequence = eventstream.SessionResultSequence(env)
		case event.Kind == TranscriptEventLifecycle:
			event.ResultSequence = eventstream.TerminalSessionResultSequence(env)
		}
	}
	return events
}

// projectACPEventToTranscriptEvents resolves display handles without changing
// ScopeID: routing and replay use the same Task identity before and after the
// directory or Task subscription arrives.
func (m *Model) projectACPEventToTranscriptEvents(env eventstream.Envelope) []TranscriptEvent {
	events := ProjectACPEventToTranscriptEvents(env)
	if m == nil || env.Scope != eventstream.ScopeSubagent {
		return events
	}
	handle := strings.TrimSpace(m.taskStreamHandlesByID[strings.TrimSpace(env.ScopeID)])
	if handle == "" {
		return events
	}
	for index := range events {
		if strings.TrimSpace(events[index].ToolTaskHandle) == strings.TrimSpace(env.ScopeID) {
			events[index].ToolTaskHandle = handle
		}
	}
	return events
}

type tuiTranscriptProjector struct {
	desktopWorld bool
}

func (p tuiTranscriptProjector) ProjectToolCall(input transcript.ToolProjectionInput) transcript.Event {
	event := projectTranscriptToolCall(input)
	p.decorateTool(input, &event)
	return event
}

func (p tuiTranscriptProjector) ProjectToolResult(input transcript.ToolProjectionInput, defaultSuccessStatus string) (transcript.Event, bool) {
	event, ok := projectTranscriptToolResult(input, defaultSuccessStatus)
	if ok {
		p.decorateTool(input, &event)
	}
	return event, ok
}

func (p tuiTranscriptProjector) decorateTool(input transcript.ToolProjectionInput, event *transcript.Event) {
	if !p.desktopWorld {
		return
	}
	if summary := projectDesktopWorldDisplay(input); summary != nil {
		if transcript.ToolStatusFinal(event.ToolStatus, event.ToolError) {
			summary.recordProjectedResult(input, event)
		}
		if event.Meta == nil {
			event.Meta = make(map[string]any)
		}
		event.Meta[desktopWorldDisplayKey] = summary
	}
}

func (tuiTranscriptProjector) ApprovalCommandPreview(raw map[string]any) string {
	return approvalCommandPreview(raw)
}

func acpToolContentToDisplay(in []eventstream.ToolCallContent) []acpprojector.ToolContent {
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
			Changes:    item.Changes,
			Patch:      item.Patch,
		})
	}
	return out
}
