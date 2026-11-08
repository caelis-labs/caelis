package tuiapp

import (
	"fmt"
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
	failed := 0
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
			failed++
		}
		if !ev.Done {
			current = i
		}
	}
	if parent.Err && failed == 0 {
		failed = 1
	}
	header := "• Code Mode"
	if len(children) > 0 {
		header += fmt.Sprintf(" · %d call", len(children))
		if len(children) != 1 {
			header += "s"
		}
	}
	if failed > 0 {
		header += fmt.Sprintf(" · %d failed", failed)
	}
	if !parent.StartedAt.IsZero() && !parent.EndedAt.IsZero() {
		header += " · " + formatTurnDuration(parent.EndedAt.Sub(parent.StartedAt))
	}
	if !isTerminalACPTranscriptStatus(status) {
		if current >= 0 {
			ev := events[current]
			header += " · " + ev.Name
			if args := compactACPToolInline(ev.Args, maxInt(8, width-displayColumns(header)-3)); args != "" {
				header += " " + args
			}
		} else if !parent.Done {
			header += " · running"
		}
	}
	tone, dim := acpToolHeaderMark(ctx, failed > 0, isTerminalACPTranscriptStatus(status) || parent.Done && current < 0)
	rows := []RenderedRow{renderACPTranscriptHeaderRowMarked(blockID, header, width, ctx, token, tone, dim)}
	if opts.ExplorationExpanded == nil || !opts.ExplorationExpanded(parent.CallID) {
		return rows
	}
	// Lay out inside a real gutter before wrapping. Its columns are decorative
	// for selection/copy, and child click ranges retain their original targets.
	innerWidth := maxInt(4, width-2)
	appendCall := func(index int, script bool) {
		callEvents := append([]SubagentEvent(nil), events...)
		ev := &callEvents[index]
		// Provider titles and coarse kinds are useful elsewhere. A grouped
		// child header must identify the actual called tool.
		ev.Title, ev.ToolKind, ev.ExplorationVerb = "", "", ""
		if script {
			if ev.FullArgs == "" {
				ev.FullArgs = ev.Args
			}
			ev.Args, ev.StartArgs = "JavaScript", "JavaScript"
		}
		callRows, _ := renderACPToolLifecycleRowsWithoutReview(blockID, callEvents, index, innerWidth, ctx, opts)
		for _, row := range callRows {
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
