package tuiapp

import (
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/acpprojector"
)

type TranscriptEventKind string

const (
	TranscriptEventNarrative   TranscriptEventKind = "narrative"
	TranscriptEventNotice      TranscriptEventKind = "notice"
	TranscriptEventPlan        TranscriptEventKind = "plan"
	TranscriptEventTool        TranscriptEventKind = "tool"
	TranscriptEventApproval    TranscriptEventKind = "approval"
	TranscriptEventParticipant TranscriptEventKind = "participant"
	TranscriptEventLifecycle   TranscriptEventKind = "lifecycle"
	TranscriptEventUsage       TranscriptEventKind = "usage"
)

type TranscriptNarrativeKind string

const (
	TranscriptNarrativeUser      TranscriptNarrativeKind = "user"
	TranscriptNarrativeAssistant TranscriptNarrativeKind = "assistant"
	TranscriptNarrativeReasoning TranscriptNarrativeKind = "reasoning"
	TranscriptNarrativeSystem    TranscriptNarrativeKind = "system"
	TranscriptNarrativeNotice    TranscriptNarrativeKind = "notice"
)

type TranscriptEvent struct {
	Kind       TranscriptEventKind
	Scope      ACPProjectionScope
	ScopeID    string
	Actor      string
	OccurredAt time.Time

	NarrativeKind TranscriptNarrativeKind
	Text          string
	Final         bool

	ToolCallID         string
	ToolName           string
	ToolKind           string
	ToolTitle          string
	ToolArgs           string
	ToolFullArgs       string
	ToolOutput         string
	ToolStream         string
	ToolStatus         string
	ToolError          bool
	ToolTaskID         string
	ToolTaskAction     string
	ToolTaskInput      string
	ToolTaskTargetKind string

	PlanEntries []PlanEntry

	ApprovalTool    string
	ApprovalCommand string
	ApprovalStatus  string
	ApprovalRisk    string
	ApprovalAuth    string
	ApprovalText    string

	State string

	Usage *kernel.UsageSnapshot

	AnchorToolCallID string
	AnchorToolName   string
}

func ProjectGatewayEventToTranscriptEvents(ev kernel.Event) []TranscriptEvent {
	scope := gatewayEventScope(ev)
	scopeID := gatewayEventScopeID(ev)
	occurredAt := ev.OccurredAt
	anchorToolCallID := kernel.EventMetaString(ev.Meta, "caelis", "runtime", "stream", "parent_call_id")
	anchorToolName := kernel.EventMetaString(ev.Meta, "caelis", "runtime", "stream", "parent_tool")
	out := make([]TranscriptEvent, 0, 4)

	appendUsage := func() {
		if ev.Usage == nil {
			return
		}
		usage := *ev.Usage
		out = append(out, TranscriptEvent{
			Kind:       TranscriptEventUsage,
			Scope:      scope,
			ScopeID:    scopeID,
			OccurredAt: occurredAt,
			Usage:      &usage,
		})
	}

	switch ev.Kind {
	case kernel.EventKindUserMessage:
		if scope != ACPProjectionMain {
			break
		}
		if text := strings.TrimSpace(gatewayUserText(ev)); text != "" {
			out = append(out, TranscriptEvent{
				Kind:          TranscriptEventNarrative,
				Scope:         scope,
				ScopeID:       scopeID,
				OccurredAt:    occurredAt,
				NarrativeKind: TranscriptNarrativeUser,
				Text:          text,
				Final:         true,
			})
		}
	case kernel.EventKindAssistantMessage:
		payload := ev.Narrative
		if payload != nil {
			actor := gatewayDisplayActor(ev, payload.Actor)
			switch payload.Role {
			case kernel.NarrativeRoleUser:
				if scope != ACPProjectionMain {
					break
				}
				if text := strings.TrimSpace(payload.Text); text != "" {
					out = append(out, TranscriptEvent{
						Kind:          TranscriptEventNarrative,
						Scope:         scope,
						ScopeID:       scopeID,
						OccurredAt:    occurredAt,
						NarrativeKind: TranscriptNarrativeUser,
						Text:          text,
						Final:         true,
					})
				}
			case kernel.NarrativeRoleAssistant:
				if payloadNarrativeChunkHasContent(payload.ReasoningText, payload.Final) {
					out = append(out, TranscriptEvent{
						Kind:          TranscriptEventNarrative,
						Scope:         scope,
						ScopeID:       scopeID,
						Actor:         actor,
						OccurredAt:    occurredAt,
						NarrativeKind: TranscriptNarrativeReasoning,
						Text:          payload.ReasoningText,
						Final:         payload.Final,
					})
				}
				if payloadNarrativeChunkHasContent(payload.Text, payload.Final) {
					out = append(out, TranscriptEvent{
						Kind:          TranscriptEventNarrative,
						Scope:         scope,
						ScopeID:       scopeID,
						Actor:         actor,
						OccurredAt:    occurredAt,
						NarrativeKind: TranscriptNarrativeAssistant,
						Text:          payload.Text,
						Final:         payload.Final,
					})
				}
			case kernel.NarrativeRoleSystem:
				if text := strings.TrimSpace(payload.Text); text != "" {
					out = append(out, TranscriptEvent{
						Kind:          TranscriptEventNotice,
						Scope:         scope,
						ScopeID:       scopeID,
						Actor:         actor,
						OccurredAt:    occurredAt,
						NarrativeKind: TranscriptNarrativeSystem,
						Text:          text,
						Final:         true,
					})
				}
			case kernel.NarrativeRoleNotice:
				if text := strings.TrimSpace(payload.Text); text != "" {
					out = append(out, TranscriptEvent{
						Kind:          TranscriptEventNotice,
						Scope:         scope,
						ScopeID:       scopeID,
						Actor:         actor,
						OccurredAt:    occurredAt,
						NarrativeKind: TranscriptNarrativeNotice,
						Text:          text,
						Final:         true,
					})
				}
			}
		}
	case kernel.EventKindToolCall:
		if payload := ev.ToolCall; payload != nil {
			toolName := gatewayToolDisplayName(payload.ToolName, payload.ToolTitle, payload.ToolKind)
			if strings.EqualFold(strings.TrimSpace(toolName), "PLAN") {
				break
			}
			status := strings.TrimSpace(string(payload.Status))
			if status == "" || payload.Status == kernel.ToolStatusStarted {
				status = string(kernel.ToolStatusRunning)
			}
			semanticName := toolSemanticName(toolName, payload.ToolKind)
			rawInput := gatewayProtocolRawInput(ev, payload.RawInput)
			out = append(out, TranscriptEvent{
				Kind:               TranscriptEventTool,
				Scope:              scope,
				ScopeID:            scopeID,
				Actor:              gatewayDisplayActor(ev, payload.Actor),
				OccurredAt:         occurredAt,
				ToolCallID:         strings.TrimSpace(payload.CallID),
				ToolName:           toolName,
				ToolKind:           strings.TrimSpace(payload.ToolKind),
				ToolTitle:          strings.TrimSpace(payload.ToolTitle),
				ToolArgs:           toolDisplayArgs(semanticName, rawInput, toolTitleDisplayArgs(semanticName, payload.ToolKind, payload.ToolTitle), acpprojector.FormatToolStart(toolName, rawInput)),
				ToolFullArgs:       toolDisplayFullArgs(semanticName, rawInput),
				ToolStatus:         status,
				ToolTaskID:         toolDisplayTaskID(rawInput, nil, ev.Meta),
				ToolTaskAction:     toolDisplayTaskAction(rawInput, nil, ev.Meta),
				ToolTaskInput:      toolDisplayTaskInput(rawInput, nil, ev.Meta),
				ToolTaskTargetKind: toolDisplayTaskTargetKind(rawInput, nil, ev.Meta),
			})
		}
	case kernel.EventKindToolResult:
		if payload := ev.ToolResult; payload != nil {
			toolName := gatewayToolDisplayName(payload.ToolName, payload.ToolTitle, payload.ToolKind)
			if strings.EqualFold(strings.TrimSpace(toolName), "PLAN") {
				break
			}
			status := strings.TrimSpace(string(payload.Status))
			if status == "" {
				if payload.Error {
					status = string(kernel.ToolStatusFailed)
				} else {
					status = string(kernel.ToolStatusCompleted)
				}
			}
			toolErr := payload.Error || strings.EqualFold(status, string(kernel.ToolStatusFailed))
			semanticName := toolSemanticName(toolName, payload.ToolKind)
			rawInput := gatewayProtocolRawInput(ev, payload.RawInput)
			rawOutput := gatewayProtocolRawOutput(ev, payload.RawOutput)
			displayInput := rawInput
			if strings.EqualFold(semanticName, "SPAWN") {
				displayInput = spawnDisplayInputForResult(rawInput, rawOutput)
			}
			toolOutput := toolDisplayOutput(semanticName, displayInput, rawOutput, acpprojector.FormatToolResult(toolName, displayInput, rawOutput, status), status, toolErr)
			toolArgs := toolDisplayArgs(semanticName, displayInput, toolTitleDisplayArgs(semanticName, payload.ToolKind, payload.ToolTitle), acpprojector.FormatToolStart(toolName, displayInput))
			if !toolErr && (len(rawInput) > 0 || len(rawOutput) > 0) {
				if header := toolDisplayResultHeader(semanticName, toolOutput); header != "" {
					toolArgs = header
				}
			}
			toolOutput = toolDisplayPanelOutput(semanticName, toolOutput)
			out = append(out, TranscriptEvent{
				Kind:               TranscriptEventTool,
				Scope:              scope,
				ScopeID:            scopeID,
				Actor:              gatewayDisplayActor(ev, payload.Actor),
				OccurredAt:         occurredAt,
				ToolCallID:         strings.TrimSpace(payload.CallID),
				ToolName:           toolName,
				ToolKind:           strings.TrimSpace(payload.ToolKind),
				ToolTitle:          strings.TrimSpace(payload.ToolTitle),
				ToolArgs:           toolArgs,
				ToolFullArgs:       toolDisplayFullArgs(semanticName, displayInput),
				ToolOutput:         toolOutput,
				ToolStream:         transcriptToolStream(status, toolErr),
				ToolStatus:         status,
				ToolError:          toolErr,
				ToolTaskID:         toolDisplayTaskID(rawInput, rawOutput, ev.Meta),
				ToolTaskAction:     toolDisplayTaskAction(rawInput, rawOutput, ev.Meta),
				ToolTaskInput:      toolDisplayTaskInput(rawInput, rawOutput, ev.Meta),
				ToolTaskTargetKind: toolDisplayTaskTargetKind(rawInput, rawOutput, ev.Meta),
				Final:              transcriptToolStatusFinal(status, toolErr),
			})
		}
	case kernel.EventKindPlanUpdate:
		if payload := ev.Plan; payload != nil {
			entries := make([]PlanEntry, 0, len(payload.Entries))
			for _, entry := range payload.Entries {
				entries = append(entries, PlanEntry{Content: entry.Content, Status: entry.Status})
			}
			if len(entries) > 0 {
				out = append(out, TranscriptEvent{
					Kind:        TranscriptEventPlan,
					Scope:       scope,
					ScopeID:     scopeID,
					OccurredAt:  occurredAt,
					PlanEntries: entries,
				})
			}
		}
	case kernel.EventKindApprovalRequested, kernel.EventKindApprovalReview:
		if ev.Kind == kernel.EventKindApprovalReview && ev.ApprovalPayload != nil && isAutomaticApprovalEvent(ev.ApprovalPayload) {
			if text := automaticApprovalReviewDisplayText(ev.ApprovalPayload); text != "" {
				out = append(out, TranscriptEvent{
					Kind:            TranscriptEventApproval,
					Scope:           scope,
					ScopeID:         scopeID,
					Actor:           gatewayDisplayActor(ev, ""),
					OccurredAt:      occurredAt,
					ToolCallID:      strings.TrimSpace(ev.ApprovalPayload.ToolCallID),
					ApprovalTool:    strings.TrimSpace(ev.ApprovalPayload.ToolName),
					ApprovalCommand: approvalCommandPreview(ev.ApprovalPayload.RawInput),
					ApprovalStatus:  strings.TrimSpace(string(ev.ApprovalPayload.ReviewStatus)),
					ApprovalRisk:    firstNonEmpty(strings.TrimSpace(ev.ApprovalPayload.Risk), approvalReviewValueFromText(text, "risk")),
					ApprovalAuth:    firstNonEmpty(strings.TrimSpace(ev.ApprovalPayload.Authorization), approvalReviewValueFromText(text, "authorization")),
					ApprovalText:    text,
					Final:           true,
				})
			}
		}
		// Manual approval requests are transient composer overlays driven by
		// PromptRequestMsg. They intentionally do not persist into transcript.
	case kernel.EventKindParticipant:
		if payload := ev.Participant; payload != nil {
			state := strings.TrimSpace(string(payload.Action))
			if state != "" {
				out = append(out, TranscriptEvent{
					Kind:       TranscriptEventParticipant,
					Scope:      scope,
					ScopeID:    scopeID,
					OccurredAt: occurredAt,
					State:      state,
				})
			}
		}
	case kernel.EventKindLifecycle:
		if payload := ev.Lifecycle; payload != nil {
			state := strings.ToLower(strings.TrimSpace(string(payload.Status)))
			if state != "" {
				out = append(out, TranscriptEvent{
					Kind:       TranscriptEventLifecycle,
					Scope:      scope,
					ScopeID:    scopeID,
					Actor:      gatewayDisplayActor(ev, payload.Actor),
					OccurredAt: occurredAt,
					State:      state,
				})
			}
		}
	case kernel.EventKindNotice, kernel.EventKindSystemMessage:
		if text := strings.TrimSpace(gatewayNoticeText(ev)); text != "" {
			out = append(out, TranscriptEvent{
				Kind:          TranscriptEventNotice,
				Scope:         scope,
				ScopeID:       scopeID,
				OccurredAt:    occurredAt,
				NarrativeKind: TranscriptNarrativeNotice,
				Text:          text,
				Final:         true,
			})
		}
	}

	for i := range out {
		out[i].AnchorToolCallID = anchorToolCallID
		out[i].AnchorToolName = anchorToolName
	}
	appendUsage()
	return out
}

func payloadNarrativeChunkHasContent(text string, final bool) bool {
	if text == "" {
		return false
	}
	if !final {
		return true
	}
	return strings.TrimSpace(text) != ""
}

func gatewayToolDisplayName(name string, title string, kind string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	if kind = strings.TrimSpace(kind); kind != "" {
		return kind
	}
	return strings.TrimSpace(title)
}

func gatewayEventFromACP(ev kernel.Event) bool {
	if ev.Origin == nil {
		return false
	}
	source := strings.ToLower(strings.TrimSpace(ev.Origin.Source))
	if source == "acp" || source == "acp_participant" || source == "acp_subagent" || strings.HasPrefix(source, "acp_") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(ev.Origin.ParticipantKind), "acp")
}

func gatewayDisplayActor(ev kernel.Event, fallback string) string {
	if ev.Origin != nil {
		if actor := strings.TrimSpace(ev.Origin.Actor); actor != "" {
			return actor
		}
	}
	return strings.TrimSpace(fallback)
}

func transcriptToolStream(status string, isErr bool) string {
	if isErr || strings.EqualFold(strings.TrimSpace(status), "failed") {
		return "stderr"
	}
	return "stdout"
}

func transcriptToolStatusFinal(status string, isErr bool) bool {
	if isErr {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "interrupted", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func gatewayProtocolRawInput(ev kernel.Event, fallback map[string]any) map[string]any {
	if ev.Protocol != nil && ev.Protocol.Update != nil && len(ev.Protocol.Update.RawInput) > 0 {
		return cloneAnyMap(ev.Protocol.Update.RawInput)
	}
	if ev.Protocol != nil && ev.Protocol.ToolCall != nil && len(ev.Protocol.ToolCall.RawInput) > 0 {
		return cloneAnyMap(ev.Protocol.ToolCall.RawInput)
	}
	return cloneAnyMap(fallback)
}

func gatewayProtocolRawOutput(ev kernel.Event, fallback map[string]any) map[string]any {
	if ev.Protocol != nil && ev.Protocol.Update != nil && len(ev.Protocol.Update.RawOutput) > 0 {
		return cloneAnyMap(ev.Protocol.Update.RawOutput)
	}
	if ev.Protocol != nil && ev.Protocol.ToolCall != nil && len(ev.Protocol.ToolCall.RawOutput) > 0 {
		return cloneAnyMap(ev.Protocol.ToolCall.RawOutput)
	}
	return cloneAnyMap(fallback)
}
