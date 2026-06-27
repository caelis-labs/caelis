package schema

import "strings"

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
		messageID = strings.TrimSpace(messageID)
		if messageID != "" {
			if a.messageID != "" && a.messageID != messageID {
				a.text = ""
			}
			a.messageID = messageID
		}
		text := ExtractTextValue(content)
		cumulative, delta := AppendAssistantText(a.text, text)
		a.text = cumulative
		return AssistantTextUpdate{Text: cumulative, Delta: delta, Assistant: true}
	case UpdateAgentThought:
		a.Reset()
		return AssistantTextUpdate{Barrier: true}
	default:
		return AssistantTextUpdate{}
	}
}

func AppendAssistantText(existing string, incoming string) (string, string) {
	if incoming == "" {
		return existing, ""
	}
	if existing == "" {
		return incoming, incoming
	}
	if strings.HasPrefix(incoming, existing) {
		delta := incoming[len(existing):]
		return incoming, delta
	}
	if strings.HasPrefix(existing, incoming) {
		return existing, ""
	}
	return existing + incoming, incoming
}
