package tuiapp

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/displaypolicy"
	"github.com/OnslaughtSnail/caelis/surfaces/transcript"
)

// Transitional aliases keep the TUI renderer readable during the transcript
// extraction. Shared surface code should use surfaces/transcript directly.
type TranscriptEventKind = transcript.EventKind

const (
	TranscriptEventNarrative   = transcript.EventNarrative
	TranscriptEventNotice      = transcript.EventNotice
	TranscriptEventPlan        = transcript.EventPlan
	TranscriptEventTool        = transcript.EventTool
	TranscriptEventApproval    = transcript.EventApproval
	TranscriptEventParticipant = transcript.EventParticipant
	TranscriptEventLifecycle   = transcript.EventLifecycle
	TranscriptEventUsage       = transcript.EventUsage
)

type TranscriptNarrativeKind = transcript.NarrativeKind

const (
	TranscriptNarrativeUser      = transcript.NarrativeUser
	TranscriptNarrativeAssistant = transcript.NarrativeAssistant
	TranscriptNarrativeReasoning = transcript.NarrativeReasoning
	TranscriptNarrativeSystem    = transcript.NarrativeSystem
	TranscriptNarrativeNotice    = transcript.NarrativeNotice
)

type TranscriptEvent = transcript.Event

func mergeTranscriptMeta(base map[string]any, overlay map[string]any) map[string]any {
	return transcript.MergeMeta(base, overlay)
}

func transcriptToolDisplayName(name string, title string, kind string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	if kind = strings.TrimSpace(kind); kind != "" {
		return kind
	}
	return strings.TrimSpace(title)
}

func directedParticipantUserDisplay(event TranscriptEvent) string {
	if event.Scope != ACPProjectionParticipant {
		return ""
	}
	handle := firstNonEmpty(
		participantMentionFromHandle(asString(event.Meta["mention"])),
		participantMentionFromHandle(asString(event.Meta["handle"])),
		participantMentionFromHandle(event.Actor),
	)
	if handle == "" {
		return ""
	}
	text := firstNonEmpty(
		strings.TrimSpace(asString(event.Meta["display_input"])),
		strings.TrimSpace(asString(event.Meta["display_title"])),
		strings.TrimSpace(event.Text),
	)
	if text == "" {
		return handle
	}
	return handle + " " + text
}

func directedParticipantUserDequeueText(event TranscriptEvent) string {
	if event.Scope != ACPProjectionParticipant {
		return strings.TrimSpace(event.Text)
	}
	return firstNonEmpty(
		strings.TrimSpace(asString(event.Meta["display_input"])),
		strings.TrimSpace(asString(event.Meta["display_title"])),
		strings.TrimSpace(event.Text),
	)
}

func participantMentionFromHandle(handle string) string {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return ""
	}
	if strings.HasPrefix(handle, "@") {
		return handle
	}
	return "@" + handle
}

func taskControlResult(semanticName string, rawInput map[string]any, displayOutput map[string]any, meta map[string]any) bool {
	if !strings.EqualFold(strings.TrimSpace(semanticName), "TASK") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(displaypolicy.ToolTaskAction(rawInput, displayOutput, meta))) {
	case "wait", "cancel":
		return true
	default:
		return false
	}
}

func toolDisplayMetaOutput(toolName string, meta map[string]any) map[string]any {
	out := map[string]any{}
	toolMeta := transcript.RuntimeToolMeta(meta)
	taskMeta := transcript.RuntimeTaskMeta(meta)
	switch strings.ToUpper(strings.TrimSpace(toolName)) {
	case "RUN_COMMAND", "SPAWN", "TASK":
		if taskID := firstNonEmpty(asString(toolMeta["target_id"]), asString(taskMeta["task_id"])); taskID != "" {
			out["task_id"] = taskID
		}
		for _, key := range []string{"yield_time_ms", "effective_yield_time_ms", "yield_time_ms_defaulted"} {
			if value, ok := toolMeta[key]; ok {
				out[key] = value
			}
		}
		if strings.EqualFold(toolName, "RUN_COMMAND") {
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
