package tuiapp

import (
	"encoding/json"
	"path"
	"strings"
)

func isCodeModeParent(ev SubagentEvent) bool {
	return ev.Kind == SEToolCall && ev.Name == "js_exec" && ev.CallID != "" && ev.ParentToolCallID == ""
}

func codeModeRootIndex(events []SubagentEvent, callID string) int {
	for i, ev := range events {
		if isCodeModeParent(ev) && ev.CallID == callID {
			return i
		}
	}
	return -1
}

// The enclosing block owns Turn identity. Only its typed parent relation may
// attach a call to this group; names and rendered arguments never infer a parent.
func codeModeParentIndex(events []SubagentEvent, child SubagentEvent) int {
	if child.ParentToolName != "js_exec" || child.ParentToolCallID == "" {
		return -1
	}
	return codeModeRootIndex(events, child.ParentToolCallID)
}

func codeModeReview(events []SubagentEvent, review SubagentEvent) bool {
	for _, ev := range events {
		if ev.Kind == SEToolCall && ev.CallID == review.CallID && (isCodeModeParent(ev) || codeModeParentIndex(events, ev) >= 0) {
			return true
		}
	}
	return false
}

// Script input/output uses the existing Shell panel; child receipts remain in
// the projection/audit chain and approvals render separately at their real target.
func renderCodeModeContainerRows(blockID string, events []SubagentEvent, idx int, status string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) []RenderedRow {
	parent := events[idx]
	for _, ev := range events {
		if isCodeModeParent(ev) && ev.CallID == parent.CallID {
			parent = ev
		}
	}
	code, file := codeModeScriptInput(parent)
	parent.Title, parent.ToolKind, parent.ExplorationVerb = "", "execute", ""
	parent.Args, parent.FullArgs = code, code
	if file != "" {
		parent.Args = path.Base(strings.ReplaceAll(file, "\\", "/"))
		parent.FullArgs = parent.Args
	}
	expanded := opts.ToolPanelExpanded == nil || opts.ToolPanelExpanded(parent.CallID)
	full := opts.ToolPanelFullOutput != nil && opts.ToolPanelFullOutput(parent.CallID)
	return renderACPTerminalLifecycleRows(blockID, parent, parent.CallID, parent.Output, width, ctx, parent.Err, expanded, parent.Done, full, opts)
}

// A missing parent stays visible as an ordinary tool; out-of-order and late
// events are grouped only after the original parent is present in this Turn.
func isCodeModeChild(events []SubagentEvent, ev SubagentEvent) bool {
	return ev.Kind == SEToolCall && strings.TrimSpace(ev.CallID) != "" && codeModeParentIndex(events, ev) >= 0
}

// FullArgs is the retained structured invocation, never a rendered title.
func codeModeScriptInput(ev SubagentEvent) (code, file string) {
	for _, raw := range []string{ev.FullArgs, ev.StartArgs, ev.Args} {
		var input struct {
			Code string `json:"code"`
			File string `json:"file"`
		}
		if json.Unmarshal([]byte(raw), &input) == nil && (input.Code != "" || input.File != "") {
			return input.Code, input.File
		}
	}
	return "", ""
}
