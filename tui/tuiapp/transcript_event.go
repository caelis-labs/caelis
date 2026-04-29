package tuiapp

import (
	"strings"
	"time"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	"github.com/OnslaughtSnail/caelis/tui/acpprojector"
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

	ToolCallID          string
	ToolName            string
	ToolKind            string
	ToolTitle           string
	ToolArgs            string
	ToolOutput          string
	ToolStream          string
	ToolStatus          string
	ToolError           bool
	ToolTaskID          string
	DisableToolGrouping bool

	PlanEntries []PlanEntry

	ApprovalTool    string
	ApprovalCommand string
	ApprovalStatus  string

	State string

	Usage *appgateway.UsageSnapshot
}

func ProjectGatewayEventToTranscriptEvents(ev appgateway.Event) []TranscriptEvent {
	scope := gatewayEventScope(ev)
	scopeID := gatewayEventScopeID(ev)
	occurredAt := ev.OccurredAt
	disableToolGrouping := gatewayEventFromACP(ev)
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
	case appgateway.EventKindUserMessage:
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
	case appgateway.EventKindAssistantMessage:
		payload := ev.Narrative
		if payload != nil {
			actor := gatewayDisplayActor(ev, payload.Actor)
			switch payload.Role {
			case appgateway.NarrativeRoleUser:
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
			case appgateway.NarrativeRoleAssistant:
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
			case appgateway.NarrativeRoleSystem:
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
			case appgateway.NarrativeRoleNotice:
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
	case appgateway.EventKindToolCall:
		if payload := ev.ToolCall; payload != nil {
			toolName := gatewayToolDisplayName(payload.ToolName, payload.ToolTitle, payload.ToolKind)
			if strings.EqualFold(strings.TrimSpace(toolName), "PLAN") {
				break
			}
			status := strings.TrimSpace(string(payload.Status))
			if status == "" || payload.Status == appgateway.ToolStatusStarted {
				status = string(appgateway.ToolStatusRunning)
			}
			semanticName := toolSemanticName(toolName, payload.ToolKind)
			out = append(out, TranscriptEvent{
				Kind:                TranscriptEventTool,
				Scope:               scope,
				ScopeID:             scopeID,
				Actor:               gatewayDisplayActor(ev, payload.Actor),
				OccurredAt:          occurredAt,
				ToolCallID:          strings.TrimSpace(payload.CallID),
				ToolName:            toolName,
				ToolKind:            strings.TrimSpace(payload.ToolKind),
				ToolTitle:           strings.TrimSpace(payload.ToolTitle),
				ToolArgs:            toolDisplayArgs(semanticName, payload.RawInput, toolTitleDisplayArgs(semanticName, payload.ToolKind, payload.ToolTitle), acpprojector.FormatToolStart(toolName, gatewayToolArgsMap(payload.CommandPreview, payload.ArgsText))),
				ToolStatus:          status,
				ToolTaskID:          toolDisplayTaskID(payload.RawInput, nil),
				DisableToolGrouping: disableToolGrouping,
			})
		}
	case appgateway.EventKindToolResult:
		if payload := ev.ToolResult; payload != nil {
			toolName := gatewayToolDisplayName(payload.ToolName, payload.ToolTitle, payload.ToolKind)
			if strings.EqualFold(strings.TrimSpace(toolName), "PLAN") {
				break
			}
			status := strings.TrimSpace(string(payload.Status))
			if status == "" {
				if payload.Error {
					status = string(appgateway.ToolStatusFailed)
				} else {
					status = string(appgateway.ToolStatusCompleted)
				}
			}
			toolErr := payload.Error || strings.EqualFold(status, string(appgateway.ToolStatusFailed))
			semanticName := toolSemanticName(toolName, payload.ToolKind)
			toolOutput := toolDisplayOutput(semanticName, payload.RawInput, payload.RawOutput, acpprojector.FormatToolResult(toolName, gatewayToolArgsMap(payload.CommandPreview, ""), gatewayToolResultMap(payload.OutputText, toolErr), status), status, toolErr)
			toolArgs := toolDisplayArgs(semanticName, payload.RawInput, toolTitleDisplayArgs(semanticName, payload.ToolKind, payload.ToolTitle), acpprojector.FormatToolStart(toolName, gatewayToolArgsMap(payload.CommandPreview, "")))
			if !toolErr && (len(payload.RawInput) > 0 || len(payload.RawOutput) > 0) {
				if header := toolDisplayResultHeader(semanticName, toolOutput); header != "" {
					toolArgs = header
				}
			}
			toolOutput = toolDisplayPanelOutput(semanticName, toolOutput)
			out = append(out, TranscriptEvent{
				Kind:                TranscriptEventTool,
				Scope:               scope,
				ScopeID:             scopeID,
				Actor:               gatewayDisplayActor(ev, payload.Actor),
				OccurredAt:          occurredAt,
				ToolCallID:          strings.TrimSpace(payload.CallID),
				ToolName:            toolName,
				ToolKind:            strings.TrimSpace(payload.ToolKind),
				ToolTitle:           strings.TrimSpace(payload.ToolTitle),
				ToolArgs:            toolArgs,
				ToolOutput:          toolOutput,
				ToolStream:          transcriptToolStream(status, toolErr),
				ToolStatus:          status,
				ToolError:           toolErr,
				ToolTaskID:          toolDisplayTaskID(payload.RawInput, payload.RawOutput),
				DisableToolGrouping: disableToolGrouping,
				Final:               transcriptToolStatusFinal(status, toolErr),
			})
		}
	case appgateway.EventKindPlanUpdate:
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
	case appgateway.EventKindApprovalRequested:
		// Approval requests are transient composer overlays driven by
		// PromptRequestMsg. They intentionally do not persist into transcript.
	case appgateway.EventKindParticipant:
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
	case appgateway.EventKindLifecycle:
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
	case appgateway.EventKindNotice, appgateway.EventKindSystemMessage:
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

func gatewayEventFromACP(ev appgateway.Event) bool {
	if ev.Origin == nil {
		return false
	}
	source := strings.ToLower(strings.TrimSpace(ev.Origin.Source))
	if source == "acp" || source == "acp_participant" || source == "acp_subagent" || strings.HasPrefix(source, "acp_") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(ev.Origin.ParticipantKind), "acp")
}

func gatewayDisplayActor(ev appgateway.Event, fallback string) string {
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
