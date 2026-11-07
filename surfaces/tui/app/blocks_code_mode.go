package tuiapp

// Code Mode grouping is a Surface projection of the typed parent relation. It
// does not alter the child identity, approval state, arguments, or event log.
func (b *MainACPTurnBlock) codeModeDisplayEvents() []SubagentEvent {
	var displayed []SubagentEvent
	for _, event := range b.Events {
		if event.ParentToolName == "js_exec" && b.CollapsedCodeMode[event.ParentToolCallID] {
			continue
		}
		if event.Kind == SEToolCall && event.Name == "js_exec" {
			event.Args = "JavaScript"
			event.StartArgs = "JavaScript"
		}
		displayed = append(displayed, event)
	}
	return displayed
}

func (b *MainACPTurnBlock) toggleCodeModeGroup(callID string) bool {
	for _, event := range b.Events {
		if event.Kind == SEToolCall && event.CallID == callID && event.Name == "js_exec" {
			if b.CollapsedCodeMode == nil {
				b.CollapsedCodeMode = map[string]bool{}
			}
			b.CollapsedCodeMode[callID] = !b.CollapsedCodeMode[callID]
			return true
		}
	}
	return false
}

func indentCodeModeChildren(rows []RenderedRow) []RenderedRow {
	for i := range rows {
		rows[i].Plain = "  " + rows[i].Plain
		rows[i].Styled = "  " + rows[i].Styled
		rows[i].selectionIndent += 2
		if rows[i].boundedClick() {
			rows[i].ClickStartCol += 2
			rows[i].ClickEndCol += 2
		}
	}
	return rows
}
