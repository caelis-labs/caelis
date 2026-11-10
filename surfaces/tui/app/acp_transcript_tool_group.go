package tuiapp

import (
	"slices"
	"strings"
)

// invocationToolGroup is a disposable range of canonical direct-parent calls.
// It owns no lifecycle or membership beyond the current transcript snapshot.
func invocationToolGroup(events []SubagentEvent, start int) (int, bool) {
	if start >= len(events) || events[start].Kind != SEToolCall {
		return start, false
	}
	roots := map[string]int{}
	parents := map[string]string{}
	for i, ev := range events {
		if isResultOnlyToolEvent(ev) && ev.CallID != "" {
			roots[ev.CallID] = i
		}
	}
	for i, ev := range events {
		if root, ok := roots[ev.ParentToolCallID]; ok && root < i && ev.ParentToolName == "js_exec" {
			parents[ev.CallID] = ev.ParentToolCallID
		}
	}
	end, children := start-1, 0
	for i := start; i < len(events); i++ {
		ev := events[i]
		switch ev.Kind {
		case SEToolCall:
			if isResultOnlyToolEvent(ev) {
				end = i
				continue
			}
			if parents[ev.CallID] == "" {
				return end, children > 0
			}
			children++
			end = i
		case SEReasoning:
			continue
		case SEApproval:
			if parents[ev.CallID] == "" {
				return end, children > 0
			}
			end = i
		default:
			return end, children > 0
		}
	}
	return end, children > 0
}

func renderInvocationToolGroupRows(blockID string, events []SubagentEvent, start, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	end, ok := invocationToolGroup(events, start)
	if !ok {
		return nil, start, false
	}
	group := events[start : end+1]
	var children []int
	for i, ev := range group {
		if ev.Kind == SEToolCall && !isResultOnlyToolEvent(ev) {
			children = append(children, i)
		}
	}
	if len(children) == 0 {
		return nil, start, false
	}
	key := "tools:" + group[children[0]].CallID
	token := acpStableExplorationClickToken(key)
	expanded := opts.ExplorationExpanded != nil && opts.ExplorationExpanded(key)
	tone, dim := acpToolHeaderMark(ctx, false, true)
	rows := []RenderedRow{renderACPTranscriptHeaderRowMarked(blockID, "• Called tools", width, ctx, token, tone, dim)}
	previous := 0
	for n, index := range children {
		if expanded {
			for _, note := range group[previous:index] {
				if note.Kind == SEReasoning {
					rows = append(rows, renderExplorationNarrativeRows(blockID, note.Text, width, ctx, ctx.Theme.ReasoningStyle(), token, len(rows) == 1)...)
				}
			}
		}
		previous = index + 1
		ev := group[index]
		// Pending/failed invocations and reviewed decisions remain visible even
		// when an older successful prefix is folded.
		review := false
		for _, other := range group {
			if other.Kind == SEApproval && other.CallID == ev.CallID {
				review = true
			}
		}
		if !expanded && n < len(children)-2 && !ev.Err && ev.Done && !review {
			continue
		}
		childOpts := opts
		childOpts.ToolPanelExpanded = func(id string) bool {
			return ev.Err || review || (opts.ToolPanelFullOutput != nil && opts.ToolPanelFullOutput(id))
		}
		native, _ := renderACPToolLifecycleRows(blockID, group, index, maxInt(8, width-4), ctx, childOpts)
		rows = append(rows, toolGroupChildRows(native, ctx, len(rows) == 1)...)
	}
	for _, ev := range group {
		if !isResultOnlyToolEvent(ev) || !ev.Done {
			continue
		}
		var names []string
		for _, i := range children {
			child := group[i]
			if child.ParentToolCallID == ev.CallID && !slices.Contains(names, child.Name) {
				names = append(names, child.Name)
			}
		}
		rows = append(rows, renderInvocationResultRows(blockID, ev, strings.Join(names, ", "), width, ctx, opts)...)
	}
	return rows, end, true
}

func renderInvocationResultRows(blockID string, ev SubagentEvent, names string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) []RenderedRow {
	if !ev.Err && (ev.OutputSynthetic || strings.TrimSpace(ev.Output) == "" || strings.TrimSpace(ev.Output) == "null") {
		return nil
	}
	label := "Result"
	if names != "" {
		label += " (" + names + ")"
	}
	if strings.TrimSpace(ev.Output) == "" {
		ev.Output = "Tool execution failed"
	}
	ev.Output = label + ":\n" + ev.Output
	return renderACPToolResultOnlyRows(blockID, ev, width, ctx, opts)
}
