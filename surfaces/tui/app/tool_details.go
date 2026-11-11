package tuiapp

import (
	"fmt"
	"strings"
)

const toolDetailsTokenPrefix = "tool_details:"

// activeTextDrawer reuses the existing scrollable read-only drawer. Its two
// presentation sources keep separate state; a tool view never owns BTW output.
func (m *Model) activeTextDrawer() *textDrawerState {
	if m == nil {
		return nil
	}
	if m.toolDetails != nil {
		return m.toolDetails
	}
	return m.btwOverlay
}
func toolDetailsClickToken(callID string) string { return toolDetailsTokenPrefix + callID }
func (m *Model) openToolDetails(blockID, callID string) bool {
	if m == nil || m.doc == nil || m.activePrompt != nil {
		return false
	}
	target := m.doc.Find(blockID)
	if target == nil {
		for _, view := range m.subagentOutputViews {
			if view.document != nil {
				if known := view.document.Find(blockID); known != nil {
					target = known
					break
				}
			}
		}
	}
	var events []SubagentEvent
	switch block := target.(type) {
	case *MainACPTurnBlock:
		events = block.Events
	case *ParticipantTurnBlock:
		events = block.Events
	}
	for _, event := range events {
		if event.Kind != SEToolCall || event.CallID != callID {
			continue
		}
		var body strings.Builder
		fmt.Fprintf(&body, "Tool: %s\nCall: %s\nRecorded error flag: %t\n", event.Name, event.CallID, event.Err)
		if event.Name == "js_exec" {
			completion := event.ScriptCompletion
			if completion == "" {
				completion = "not recorded"
			}
			fmt.Fprintf(&body, "Script completion: %s\n", completion)
		}
		fmt.Fprintf(&body, "\nInput\n%s\n", firstNonEmpty(event.FullArgs, event.StartArgs, event.Args))
		if !event.OutputSynthetic && renderableTextHasContent(event.Output) {
			fmt.Fprintf(&body, "\nOutput / diagnostics\n%s\n", event.Output)
		}
		for _, review := range events {
			if review.Kind == SEApproval && review.CallID == callID {
				fmt.Fprintf(&body, "\nApproval: %s\n%s\n", review.ApprovalStatus, review.ApprovalText)
			}
		}
		m.toolDetails = &textDrawerState{Question: event.Name + " details · Esc to close · ↑↓ scroll", Answer: sanitizeRenderableText(body.String())}
		m.ensureViewportLayout()
		return true
	}
	return false
}
