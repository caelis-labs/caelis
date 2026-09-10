package tuiapp

import "encoding/json"

// Collaboration observations remain model-visible; only their TUI panels are hidden.
func collaborationObservationTool(name string) bool {
	switch name {
	case "ListThreads", "ReadThread", "WaitThread", "ReceiveMessages":
		return true
	default:
		return false
	}
}

// Mailbox results reuse the existing incoming-message presentation. The message
// ID supplies display identity; parsing here never consumes or delivers mail.
// Delivered child input is already typed Agent communication; Surfaces do not
// parse mailbox JSON out of user_message_chunk or Agent communication text.
func expandCollaborationMessages(events []TranscriptEvent) []TranscriptEvent {
	var out []TranscriptEvent
	for _, event := range events {
		visible := event
		if event.Kind == TranscriptEventTool && event.ToolName == surfaceToolSendMessage && !event.ToolError {
			// Expand returned mail before suppressing the outgoing send receipt.
			visible.ToolOutput = ""
			visible.ToolOutputSynthetic = true
		}
		out = append(out, visible)
		if event.Kind != TranscriptEventTool || !event.Final || event.ToolError {
			continue
		}
		type message struct {
			ID   string `json:"id"`
			From string `json:"from"`
			Text string `json:"message"`
		}
		var messages []message
		switch event.ToolName {
		case "ReceiveMessages":
			// Display-only compatibility for retained tool-result history; remove when those records are no longer supported.
			if json.Unmarshal([]byte(event.ToolOutput), &messages) != nil {
				continue
			}
		case "SendMessage", "WaitThread":
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
		for _, mail := range messages {
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
