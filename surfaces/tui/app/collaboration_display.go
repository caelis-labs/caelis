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
		out = append(out, event)
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
			if json.Unmarshal([]byte(event.ToolOutput), &messages) != nil {
				continue
			}
		case "WaitThread":
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
			incoming.SourceProjectionID = ""
			incoming.AgentSourceName = mail.From
			incoming.AgentSourceID = ""
			incoming.Text = mail.Text
			out = append(out, incoming)
		}
	}
	return out
}
