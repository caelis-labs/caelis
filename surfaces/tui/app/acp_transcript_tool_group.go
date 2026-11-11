package tuiapp

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
	firstChild := ""
	failed, completed := false, true
	for _, ev := range group {
		if ev.Kind != SEToolCall {
			continue
		}
		failed, completed = failed || ev.Err, completed && ev.Done
		if firstChild == "" && !isResultOnlyToolEvent(ev) {
			firstChild = ev.CallID
		}
	}
	key := "tools:" + firstChild
	token := acpStableExplorationClickToken(key)
	expanded := opts.ExplorationExpanded != nil && opts.ExplorationExpanded(key)
	header := "• Called tools"
	if failed {
		header += " failed"
	}
	tone, dim := acpToolHeaderMark(ctx, failed, completed)
	rows := []RenderedRow{renderACPTranscriptHeaderRowMarked(blockID, header, width, ctx, token, tone, dim)}
	if !expanded {
		return rows, end, true
	}
	var result *SubagentEvent
	flushResult := func() {
		if result != nil && result.Done {
			resultRows := renderACPToolResultOnlyRows(blockID, *result, maxInt(8, width-4), ctx, opts)
			rows = append(rows, toolGroupChildRows(resultRows, ctx, len(rows) == 1)...)
		}
		result = nil
	}
	for index := 0; index < len(group); index++ {
		ev := group[index]
		switch ev.Kind {
		case SEReasoning:
			flushResult()
			rows = append(rows, renderExplorationNarrativeRows(blockID, ev.Text, width, ctx, ctx.Theme.ReasoningStyle(), token, len(rows) == 1)...)
		case SEToolCall:
			if isResultOnlyToolEvent(ev) {
				flushResult()
				result = &group[index]
				continue
			}
			native, consumed := renderACPToolLifecycleRows(blockID, group, index, maxInt(8, width-4), ctx, opts)
			rows = append(rows, toolGroupChildRows(native, ctx, len(rows) == 1)...)
			index = consumed
		}
	}
	flushResult()
	return rows, end, true
}
