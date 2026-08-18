package acpagentbridge

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func acpTaskStreamAnchorFromEnvelope(envelope eventstream.Envelope) (acpTaskStreamAnchor, bool) {
	if envelope.Kind != eventstream.KindSessionUpdate || (envelope.Scope != "" && envelope.Scope != eventstream.ScopeMain) {
		return acpTaskStreamAnchor{}, false
	}
	meta := eventstream.UpdateMeta(envelope.Update)
	toolName := metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName)
	var input, output map[string]any
	var status string
	switch update := envelope.Update.(type) {
	case schema.ToolCall:
		input, _ = update.RawInput.(map[string]any)
		output, _ = update.RawOutput.(map[string]any)
		status = update.Status
	case schema.ToolCallUpdate:
		input, _ = update.RawInput.(map[string]any)
		output, _ = update.RawOutput.(map[string]any)
		if update.Status != nil {
			status = *update.Status
		}
	default:
		return acpTaskStreamAnchor{}, false
	}
	callID := strings.TrimSpace(taskStreamToolCallID(envelope.Update))
	kind := task.Kind("")
	switch toolName {
	case shell.RunCommandToolName:
		kind = task.KindCommand
	case spawn.ToolName:
		kind = task.KindSubagent
	default:
		targetKind := strings.ToLower(strings.TrimSpace(display.ToolTaskTargetKind(input, output, meta)))
		terminalInfo, hasTerminalInfo := metautil.TerminalInfo(meta)
		switch {
		case targetKind == string(task.KindCommand) &&
			hasTerminalInfo &&
			strings.TrimSpace(terminalInfo.TerminalID) == callID:
			// Standard ACP strips Caelis' private runtime tool-name metadata,
			// but the typed RunCommand terminal anchor and target kind remain.
			// Directory matching still validates the parent before attaching.
			kind = task.KindCommand
		case display.MapString(output, "parent_tool") == spawn.ToolName &&
			strings.EqualFold(display.ToolTaskTargetKind(input, output, meta), "subagent"):
			kind = task.KindSubagent
		}
	}
	if kind == "" {
		return acpTaskStreamAnchor{}, false
	}
	handle := strings.TrimSpace(display.ToolTaskHandle(input, output, meta))
	if kind == task.KindSubagent {
		if parentCall := strings.TrimSpace(display.MapString(output, "parent_call")); parentCall != "" && parentCall != callID {
			return acpTaskStreamAnchor{}, false
		}
	}
	parentTerminal := acpToolStatusFinalString(status)
	if kind == task.KindSubagent {
		// Spawn returning completes the tool invocation, not the child Task.
		// Only the target state may close Task observation.
		targetState := strings.ToLower(strings.TrimSpace(display.MapString(output, "state")))
		parentTerminal = eventstream.IsTerminalLifecycleState(targetState) ||
			(targetState == "" && strings.EqualFold(strings.TrimSpace(status), schema.ToolStatusFailed))
	}
	return acpTaskStreamAnchor{
		callID:         callID,
		handle:         handle,
		kind:           kind,
		parentTerminal: parentTerminal,
	}, callID != "" && handle != ""
}

func acpTaskStreamEnvelopeAllowed(anchor acpTaskStreamAnchor, envelope eventstream.Envelope) bool {
	switch anchor.kind {
	case task.KindCommand:
		return envelope.Scope == eventstream.ScopeMain && envelope.Kind == eventstream.KindSessionUpdate && envelopeHasTerminalDelivery(envelope)
	case task.KindSubagent:
		if envelope.Scope != eventstream.ScopeSubagent || envelope.ParentTool == nil ||
			strings.TrimSpace(envelope.ParentTool.ToolCallID) != anchor.callID ||
			envelope.ParentTool.ToolName != spawn.ToolName {
			return false
		}
		switch envelope.Kind {
		case eventstream.KindSessionUpdate:
			return envelope.Update != nil
		case eventstream.KindNotice:
			return strings.TrimSpace(envelope.Notice) != ""
		case eventstream.KindLifecycle:
			return envelope.Lifecycle != nil
		}
	}
	return false
}

func taskStreamParentToolName(kind task.Kind) string {
	switch kind {
	case task.KindSubagent:
		return spawn.ToolName
	case task.KindCommand:
		return shell.RunCommandToolName
	default:
		return ""
	}
}

func taskStreamToolCallID(update schema.Update) string {
	switch typed := update.(type) {
	case schema.ToolCall:
		return typed.ToolCallID
	case schema.ToolCallUpdate:
		return typed.ToolCallID
	default:
		return ""
	}
}

func envelopeHasTerminalDelivery(envelope eventstream.Envelope) bool {
	meta := eventstream.UpdateMeta(envelope.Update)
	output, ok := metautil.TerminalOutput(meta)
	if ok && output.Data != "" {
		return true
	}
	_, ok = metautil.TerminalExit(meta)
	return ok
}
