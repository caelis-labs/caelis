package acpagentbridge

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/identity"
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
	switch identity.CanonicalOrSelf(toolName) {
	case identity.RunCommand:
		kind = task.KindCommand
	case identity.Spawn:
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
		case identity.CanonicalOrSelf(display.MapString(output, "parent_tool")) == identity.Spawn &&
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
	return acpTaskStreamAnchor{
		callID: callID,
		handle: handle,
		kind:   kind,
		// Only the typed ACP tool status closes discovery permanently. Raw
		// output is compatibility/display data and may carry a stale or
		// intermediate state before the parent tool call itself is terminal.
		parentTerminal: acpToolStatusFinalString(status),
	}, callID != "" && handle != ""
}

func acpTaskStreamEnvelopeAllowed(anchor acpTaskStreamAnchor, envelope eventstream.Envelope) bool {
	switch anchor.kind {
	case task.KindCommand:
		return envelope.Scope == eventstream.ScopeMain && envelope.Kind == eventstream.KindSessionUpdate && envelopeHasTerminalDelivery(envelope)
	case task.KindSubagent:
		if envelope.Scope != eventstream.ScopeSubagent || envelope.ParentTool == nil ||
			strings.TrimSpace(envelope.ParentTool.ToolCallID) != anchor.callID ||
			identity.CanonicalOrSelf(envelope.ParentTool.ToolName) != identity.Spawn {
			return false
		}
		switch envelope.Kind {
		case eventstream.KindSessionUpdate:
			return envelope.Update != nil
		case eventstream.KindNotice:
			return strings.TrimSpace(envelope.Notice) != ""
		}
	}
	return false
}

func acpSubagentTaskLifecycleAllowed(anchor acpTaskStreamAnchor, envelope eventstream.Envelope) bool {
	return anchor.kind == task.KindSubagent && envelope.Scope == eventstream.ScopeSubagent &&
		envelope.ParentTool != nil && strings.TrimSpace(envelope.ParentTool.ToolCallID) == anchor.callID &&
		identity.CanonicalOrSelf(envelope.ParentTool.ToolName) == identity.Spawn &&
		envelope.Lifecycle != nil && eventstream.IsTerminalLifecycleState(envelope.Lifecycle.State)
}

func taskStreamParentToolName(kind task.Kind) string {
	switch kind {
	case task.KindSubagent:
		return identity.Spawn
	case task.KindCommand:
		return identity.RunCommand
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
