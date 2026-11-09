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

// Code Mode uses the existing Explored disclosure state and ordinary tool
// panels. All call/approval/result state remains in the original projection.
func renderCodeModeContainerRows(blockID string, events []SubagentEvent, idx int, status string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) []RenderedRow {
	parent := events[idx]
	parentIndex := idx
	token := acpStableExplorationClickToken(parent.CallID)
	var children []int
	positions := map[string]int{}
	failed := false
	current := -1
	for i, ev := range events {
		if isCodeModeParent(ev) && ev.CallID == parent.CallID {
			parent, parentIndex = ev, i
		}
		if ev.Kind != SEToolCall || codeModeParentIndex(events, ev) != idx {
			continue
		}
		if position, exists := positions[ev.CallID]; exists {
			children[position] = i
		} else {
			positions[ev.CallID] = len(children)
			children = append(children, i)
		}
	}
	for _, i := range children {
		ev := events[i]
		if ev.Err {
			failed = true
		}
		if !ev.Done {
			current = i
		}
	}
	failed = failed || parent.Err
	settled := isTerminalACPTranscriptStatus(status) || parent.Done && current < 0
	header := "• Running script"
	if settled {
		header = "• Ran script"
	}
	code, file := codeModeScriptInput(parent)
	suffixWidth := 0
	if failed {
		suffixWidth = displayColumns(" [failed]")
	}
	if file != "" {
		header += " " + compactACPToolInline(path.Base(strings.ReplaceAll(file, "\\", "/")), maxInt(8, width-displayColumns(header)-1-suffixWidth))
	}
	if failed {
		header += " [failed]"
	}
	tone, dim := acpToolHeaderMark(ctx, failed, settled)
	rows := []RenderedRow{renderACPTranscriptHeaderRowMarked(blockID, header, width, ctx, token, tone, dim)}
	expanded := opts.ExplorationExpanded != nil && opts.ExplorationExpanded(parent.CallID)
	if !expanded {
		if code != "" {
			preview := strings.TrimSpace(strings.SplitN(strings.TrimSpace(code), "\n", 2)[0])
			preview = compactACPToolInline(preview, maxInt(4, width-2))
			rows = append(rows, renderACPToolOutputRowsWithToken(blockID, "└ ", preview, width, ctx, ctx.Theme.ToolOutputStyle(), token)...)
		}
		if !settled && current >= 0 {
			ev := events[current]
			detail := ev.Name
			if args := compactACPToolInline(ev.Args, maxInt(4, width-displayColumns(detail)-3)); args != "" {
				detail += " " + args
			}
			rows = append(rows, renderACPToolOutputRowsWithToken(blockID, "└ ", detail, width, ctx, ctx.Theme.ToolOutputMetaStyle(), token)...)
		}
		// A collapsed failure retains the original failing tool/error panel.
		for _, child := range children {
			if events[child].Err {
				callEvents := append([]SubagentEvent(nil), events...)
				callEvents[child].Title, callEvents[child].ToolKind, callEvents[child].ExplorationVerb = "", "", ""
				detail, _ := renderACPToolLifecycleRowsWithoutReview(blockID, callEvents, child, width, ctx, opts)
				rows = append(rows, detail...)
				return rows
			}
		}
		if parent.Err {
			detail, _ := renderACPToolLifecycleRowsWithoutReview(blockID, events, parentIndex, width, ctx, opts)
			for _, row := range detail {
				if !row.ACPHeader {
					rows = append(rows, row)
				}
			}
		}
		return rows
	}
	// Lay out inside a real gutter before wrapping. Its columns are decorative
	// for selection/copy, and child click ranges retain their original targets.
	innerWidth := maxInt(4, width-2)
	appendRows := func(callRows []RenderedRow) {
		for _, callRow := range callRows {
			wrapped := []RenderedRow{callRow}
			if callRow.ACPHeader {
				wrapped = wrapAgentMessageRows(callRow, innerWidth)
			}
			for _, row := range wrapped {
				row.Plain = "│ " + row.Plain
				row.Styled = ctx.Theme.ToolOutputMetaStyle().Render("│ ") + row.Styled
				row.selectionIndent += 2
				if row.boundedClick() {
					row.ClickStartCol += 2
					row.ClickEndCol += 2
				}
				rows = append(rows, row)
			}
		}
	}
	if code != "" {
		text := code
		if opts.ToolPanelFullOutput == nil || !opts.ToolPanelFullOutput(parent.CallID) {
			text = summarizeACPToolPanelText(code, true, maxInt(1, innerWidth-2))
		}
		appendRows(renderACPToolOutputRowsWithToken(blockID, "└ ", text, innerWidth, ctx, ctx.Theme.ToolOutputStyle(), acpToolPanelClickToken(parent.CallID)))
	}
	appendCall := func(index int, script bool) {
		callEvents := append([]SubagentEvent(nil), events...)
		ev := &callEvents[index]
		ev.Title, ev.ToolKind, ev.ExplorationVerb = "", "", ""
		if script {
			ev.Args, ev.StartArgs, ev.FullArgs = "", "", ""
		}
		callRows, _ := renderACPToolLifecycleRowsWithoutReview(blockID, callEvents, index, innerWidth, ctx, opts)
		if script {
			filtered := callRows[:0]
			for _, row := range callRows {
				if !row.ACPHeader {
					filtered = append(filtered, row)
				}
			}
			callRows = filtered
		}
		appendRows(callRows)
	}
	for _, child := range children {
		appendCall(child, false)
	}
	appendCall(parentIndex, true)
	return rows
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
