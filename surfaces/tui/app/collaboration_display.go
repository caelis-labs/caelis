package tuiapp

import (
	"encoding/json"
	"strings"
)

// Collaboration observations remain model-visible; only their TUI panels are hidden.
func collaborationObservationTool(name string) bool {
	switch name {
	case "ListThreads", "ReadThread", "WaitThread", "ReadMessages", "ReceiveMessages":
		return true
	default:
		return false
	}
}

// Mailbox results reuse the existing incoming-message presentation. The message
// ID supplies display identity; parsing here never consumes or delivers mail.
// Shared-log reads show only mail addressed to the observing pane. The model's
// complete result and Control's independent read/delivery state remain unchanged.
// Delivered child input is already typed Agent communication; Surfaces do not
// parse mailbox JSON out of user_message_chunk or Agent communication text.
func (m *Model) expandCollaborationMessages(events []TranscriptEvent) []TranscriptEvent {
	var out []TranscriptEvent
	for _, event := range events {
		visible := event
		if event.Kind == TranscriptEventTool && event.ToolName == surfaceToolSendMessage && !event.ToolError {
			// Expand returned mail before suppressing the outgoing send receipt.
			visible.ToolOutput = ""
			visible.ToolOutputSynthetic = true
		}
		out = append(out, visible)
		if event.Kind != TranscriptEventTool || !event.Final || event.ToolError || strings.EqualFold(event.ToolStatus, "failed") {
			continue
		}
		type message struct {
			ID   string `json:"id"`
			From string `json:"from"`
			To   string `json:"to"`
			Text string `json:"message"`
		}
		var messages []message
		switch event.ToolName {
		case "ReceiveMessages":
			// Display-only compatibility for retained tool-result history; remove when those records are no longer supported.
			if json.Unmarshal([]byte(event.ToolOutput), &messages) != nil {
				continue
			}
		case "SendMessage", "WaitThread", "ReadMessages":
			var result struct {
				Messages []message `json:"messages"`
			}
			if json.Unmarshal([]byte(event.ToolOutput), &result) != nil {
				continue
			}
			messages = result.Messages
		default:
			continue
		}
		recipient := m.collaborationDisplayRecipient(event)
		for _, mail := range messages {
			if event.ToolName == "ReadMessages" && (recipient == "" || mail.To != recipient) {
				continue
			}
			if mail.ID == "" || mail.From == "" || mail.Text == "" {
				continue
			}
			incoming := event
			incoming.Kind = TranscriptEventAgentCommunication
			incoming.MessageID = mail.ID
			incoming.SourceEventID = ""
			// Each expanded mail item needs a stable display key across repeated
			// tool updates, separate from the enclosing tool's projection.
			incoming.SourceProjectionID = "collaboration-mail:" + mail.ID
			incoming.AgentSourceName = mail.From
			incoming.AgentSourceID = ""
			incoming.Text = mail.Text
			out = append(out, incoming)
		}
	}
	return out
}

// Resolve the public address from the pane's Host-resolved Spawn/Task relation,
// never from an actor label, opaque participant ID, or MCP title.
func (m *Model) collaborationDisplayRecipient(event TranscriptEvent) string {
	if event.Scope == ACPProjectionMain {
		return "parent"
	}
	if key := m.subagentOutputEventKey(event); key != "" {
		if view := m.subagentOutputViews[key]; view != nil {
			return view.taskHandle
		}
	}
	return ""
}
