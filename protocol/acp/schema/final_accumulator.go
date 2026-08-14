package schema

import "strings"

// FinalAssistantAccumulator retains the latest assistant message while
// appending ACP narrative frames with exact delta semantics.
type FinalAssistantAccumulator struct {
	messageID string
	text      string
}

type AssistantTextUpdate struct {
	Text      string
	Delta     string
	Assistant bool
	Barrier   bool
}

func (a *FinalAssistantAccumulator) ObserveUpdate(update Update) AssistantTextUpdate {
	if a == nil || update == nil {
		return AssistantTextUpdate{}
	}
	switch typed := update.(type) {
	case ContentChunk:
		return a.observeContentChunk(typed.SessionUpdate, typed.Content, typed.MessageID)
	case *ContentChunk:
		if typed == nil {
			return AssistantTextUpdate{}
		}
		return a.observeContentChunk(typed.SessionUpdate, typed.Content, typed.MessageID)
	case ToolCall, *ToolCall, ToolCallUpdate, *ToolCallUpdate, PlanUpdate, *PlanUpdate:
		a.Reset()
		return AssistantTextUpdate{Barrier: true}
	default:
		return AssistantTextUpdate{}
	}
}

func (a *FinalAssistantAccumulator) ObserveContentChunk(updateType string, content any) AssistantTextUpdate {
	if a == nil {
		return AssistantTextUpdate{}
	}
	return a.observeContentChunk(updateType, content, "")
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
		if a.messageID != "" && a.messageID != messageID {
			a.resetMessage()
		}
		a.messageID = messageID
	}
	delta := a.appendAssistantFrame(text)
	return AssistantTextUpdate{Text: a.text, Delta: delta, Assistant: true}
}

func (a *FinalAssistantAccumulator) FinalText() string {
	if a == nil {
		return ""
	}
	return a.text
}

func (a *FinalAssistantAccumulator) Reset() {
	if a != nil {
		a.messageID = ""
		a.text = ""
	}
}

func (a *FinalAssistantAccumulator) observeContentChunk(updateType string, content any, messageID string) AssistantTextUpdate {
	switch strings.TrimSpace(updateType) {
	case UpdateAgentMessage:
		return a.ObserveFrame(messageID, ExtractTextValue(content))
	case UpdateAgentThought:
		a.Reset()
		return AssistantTextUpdate{Barrier: true}
	default:
		return AssistantTextUpdate{}
	}
}

func (a *FinalAssistantAccumulator) resetMessage() {
	a.messageID = ""
	a.text = ""
}

func (a *FinalAssistantAccumulator) appendAssistantFrame(incoming string) string {
	if incoming == "" {
		return ""
	}
	a.text += incoming
	return incoming
}

// AppendAssistantText appends one exact ACP assistant delta.
func AppendAssistantText(existing string, incoming string) (string, string) {
	if incoming == "" {
		return existing, ""
	}
	return existing + incoming, incoming
}
