package tuiapp

import (
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
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

	ToolCallID          string
	ToolName            string
	ToolKind            string
	ToolTitle           string
	ToolArgs            string
	ToolFullArgs        string
	ToolOutput          string
	ToolStream          string
	ToolStatus          string
	ToolError           bool
	ToolOutputSynthetic bool
	ToolTaskID          string
	ToolTaskAction      string
	ToolTaskInput       string
	ToolTaskTargetKind  string

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
			toolTaskID := toolDisplayTaskID(rawInput, nil, ev.Meta)
			displayInput := rawInput
			if strings.EqualFold(semanticName, "TASK") {
				displayInput = taskDisplayInputForResult(rawInput, toolDisplayMetaOutput(semanticName, ev.Meta))
			}
			toolArgs := toolDisplayArgs(semanticName, displayInput, toolTitleDisplayArgs(semanticName, payload.ToolKind, payload.ToolTitle), acpprojector.FormatToolStart(toolName, displayInput))
			if strings.EqualFold(semanticName, "TASK") {
				toolArgs = taskDisplayArgsWithTaskID(toolArgs, toolTaskID)
			}
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
				ToolFullArgs:       toolDisplayFullArgs(semanticName, rawInput),
				ToolStatus:         status,
				ToolTaskID:         toolTaskID,
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
			displayOutput := toolDisplayMetaOutput(semanticName, ev.Meta)
			displayInput := rawInput
			if strings.EqualFold(semanticName, "SPAWN") {
				displayInput = spawnDisplayInputForResult(rawInput, displayOutput)
			}
			if strings.EqualFold(semanticName, "TASK") {
				displayInput = taskDisplayInputForResult(rawInput, displayOutput)
			}
			content := gatewayProtocolToolContent(ev, payload.Content)
			toolOutput := acpprojector.FormatToolContent(content)
			toolOutputSynthetic := false
			if strings.TrimSpace(toolOutput) == "" {
				if !terminalFinalWithoutContent(semanticName, payload.ToolKind, status, toolErr) {
					toolOutput = standardToolOutput(status, toolErr)
					toolOutputSynthetic = strings.TrimSpace(toolOutput) != ""
				}
			}
			toolArgs := toolDisplayArgs(semanticName, displayInput, toolTitleDisplayArgs(semanticName, payload.ToolKind, payload.ToolTitle), acpprojector.FormatToolStart(toolName, displayInput))
			toolTaskID := toolDisplayTaskID(rawInput, displayOutput, ev.Meta)
			if strings.EqualFold(semanticName, "TASK") {
				toolArgs = taskDisplayArgsWithTaskID(toolArgs, toolTaskID)
			}
			if !toolErr && (len(rawInput) > 0 || strings.TrimSpace(toolOutput) != "") {
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
				ToolFullArgs:        toolDisplayFullArgs(semanticName, displayInput),
				ToolOutput:          toolOutput,
				ToolStream:          transcriptToolStream(status, toolErr),
				ToolStatus:          status,
				ToolError:           toolErr,
				ToolOutputSynthetic: toolOutputSynthetic,
				ToolTaskID:          toolTaskID,
				ToolTaskAction:      toolDisplayTaskAction(rawInput, displayOutput, ev.Meta),
				ToolTaskInput:       toolDisplayTaskInput(rawInput, displayOutput, ev.Meta),
				ToolTaskTargetKind:  toolDisplayTaskTargetKind(rawInput, displayOutput, ev.Meta),
				Final:               transcriptToolStatusFinal(status, toolErr),
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

func gatewayProtocolToolContent(ev kernel.Event, fallback []session.ProtocolToolCallContent) []session.ProtocolToolCallContent {
	if ev.Protocol != nil && ev.Protocol.Update != nil {
		if content := session.ProtocolToolCallContentOf(ev.Protocol.Update); len(content) > 0 {
			return content
		}
	}
	if ev.Protocol != nil && ev.Protocol.ToolCall != nil {
		if content := session.CloneProtocolToolCallContent(ev.Protocol.ToolCall.Content); len(content) > 0 {
			return content
		}
	}
	if content := session.CloneProtocolToolCallContent(fallback); len(content) > 0 {
		return content
	}
	return nil
}

func standardToolOutput(status string, isErr bool) string {
	if isErr || strings.EqualFold(strings.TrimSpace(status), string(kernel.ToolStatusFailed)) {
		return "failed"
	}
	if transcriptToolStatusFinal(status, isErr) {
		return "completed"
	}
	return ""
}

func terminalFinalWithoutContent(toolName string, toolKind string, status string, isErr bool) bool {
	if !transcriptToolStatusFinal(status, isErr) {
		return false
	}
	if isErr || strings.EqualFold(strings.TrimSpace(status), string(kernel.ToolStatusFailed)) {
		return false
	}
	return isTerminalPanelToolKind(toolName, toolKind)
}

func toolDisplayMetaOutput(toolName string, meta map[string]any) map[string]any {
	out := map[string]any{}
	toolMeta := eventRuntimeToolMeta(meta)
	taskMeta := eventRuntimeTaskMeta(meta)
	switch strings.ToUpper(strings.TrimSpace(toolName)) {
	case "BASH", "SPAWN", "TASK":
		if taskID := firstNonEmpty(asString(toolMeta["target_id"]), asString(taskMeta["task_id"])); taskID != "" {
			out["task_id"] = taskID
		}
		for _, key := range []string{"effective_yield_time_ms", "yield_time_ms_defaulted"} {
			if value, ok := toolMeta[key]; ok {
				out[key] = value
			}
		}
		if strings.EqualFold(toolName, "BASH") {
			break
		}
		for _, key := range []string{"agent", "agent_id", "handle", "mention", "prompt", "target_kind", "action", "input"} {
			if value, ok := toolMeta[key]; ok {
				out[key] = value
			}
			if value, ok := taskMeta[key]; ok {
				out[key] = value
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func eventRuntimeToolMeta(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	toolMeta, _ := runtimeMeta["tool"].(map[string]any)
	return toolMeta
}

func eventRuntimeTaskMeta(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	taskMeta, _ := runtimeMeta["task"].(map[string]any)
	return taskMeta
}
