package acpbridge

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/google/uuid"
)

// FinalAssistantAccumulator retains the latest assistant message while
// appending ACP narrative frames with exact delta semantics.
type FinalAssistantAccumulator struct {
	messageID          string
	messageIDGenerated bool
	text               string
}

type AssistantTextUpdate struct {
	Text      string
	Delta     string
	MessageID string
	Assistant bool
	Barrier   bool
}

func (a *FinalAssistantAccumulator) ObserveUpdate(update eventstream.Update) AssistantTextUpdate {
	if a == nil || update == nil {
		return AssistantTextUpdate{}
	}
	switch typed := update.(type) {
	case eventstream.ContentChunk:
		return a.observeContentChunk(typed.SessionUpdate, typed.Content, typed.MessageID)
	case *eventstream.ContentChunk:
		if typed == nil {
			return AssistantTextUpdate{}
		}
		return a.observeContentChunk(typed.SessionUpdate, typed.Content, typed.MessageID)
	case eventstream.ToolCall, *eventstream.ToolCall, eventstream.ToolCallUpdate, *eventstream.ToolCallUpdate, eventstream.PlanUpdate, *eventstream.PlanUpdate:
		a.Reset()
		return AssistantTextUpdate{Barrier: true}
	default:
		return AssistantTextUpdate{}
	}
}

// ObserveFrame appends one ACP narrative text frame without applying an
// update-type barrier. Content is always treated as an exact delta; adapters
// for non-standard cumulative endpoints must normalize snapshots explicitly.
func (a *FinalAssistantAccumulator) ObserveFrame(messageID string, text string) AssistantTextUpdate {
	if a == nil {
		return AssistantTextUpdate{}
	}
	messageID = strings.TrimSpace(messageID)
	if messageID != "" {
		switch {
		case a.messageID == "":
			a.messageID = messageID
			a.messageIDGenerated = false
		case a.messageID == messageID:
		case a.messageIDGenerated:
			// The generated identity may already have been published. Keep it
			// stable until a real message barrier rather than dropping its prefix.
		default:
			a.resetMessage()
			a.messageID = messageID
		}
	} else if a.messageID == "" && text != "" {
		a.messageID = nextGeneratedNarrativeMessageID()
		a.messageIDGenerated = true
	}
	delta := a.appendAssistantFrame(text)
	return AssistantTextUpdate{Text: a.text, Delta: delta, MessageID: a.messageID, Assistant: true}
}

func (a *FinalAssistantAccumulator) FinalText() string {
	if a == nil {
		return ""
	}
	return a.text
}

func (a *FinalAssistantAccumulator) Reset() {
	if a != nil {
		a.resetMessage()
	}
}

func (a *FinalAssistantAccumulator) observeContentChunk(updateType string, content any, messageID string) AssistantTextUpdate {
	switch strings.TrimSpace(updateType) {
	case eventstream.UpdateAgentMessage:
		return a.ObserveFrame(messageID, session.ExtractProtocolText(content))
	case eventstream.UpdateAgentThought:
		a.Reset()
		return AssistantTextUpdate{Barrier: true}
	default:
		return AssistantTextUpdate{}
	}
}

func (a *FinalAssistantAccumulator) resetMessage() {
	a.messageID = ""
	a.messageIDGenerated = false
	a.text = ""
}

func nextGeneratedNarrativeMessageID() string {
	return "caelis-acp-message-" + uuid.NewString()
}

func (a *FinalAssistantAccumulator) appendAssistantFrame(incoming string) string {
	if incoming == "" {
		return ""
	}
	a.text += incoming
	return incoming
}
