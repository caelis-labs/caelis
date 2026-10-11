package acpbridge

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/google/uuid"
)

// FinalAssistantAccumulator retains the latest assistant message while
// appending exact ACP deltas and applying explicitly identified replacements.
type FinalAssistantAccumulator struct {
	messageID          string
	messageIDGenerated bool
	text               string
	finalEligible      bool
	introduced         map[string]struct{}
	aliases            map[string]string
}

type AssistantTextUpdate struct {
	// Text is the currently selected final-answer snapshot. MessageID and Delta
	// identify the display frame, which may repair an older message instead.
	Text        string
	Delta       string
	MessageID   string
	Assistant   bool
	Barrier     bool
	Replacement bool
}

func (a *FinalAssistantAccumulator) ObserveUpdate(update eventstream.Update) AssistantTextUpdate {
	if a == nil || update == nil {
		return AssistantTextUpdate{}
	}
	switch typed := update.(type) {
	case eventstream.ContentChunk:
		return a.observeContentChunk(typed.SessionUpdate, typed.Content, typed.MessageID, typed.Replacement)
	case *eventstream.ContentChunk:
		if typed == nil {
			return AssistantTextUpdate{}
		}
		return a.observeContentChunk(typed.SessionUpdate, typed.Content, typed.MessageID, typed.Replacement)
	case eventstream.ToolCall, *eventstream.ToolCall, eventstream.ToolCallUpdate, *eventstream.ToolCallUpdate, eventstream.PlanUpdate, *eventstream.PlanUpdate:
		a.AdvanceBoundary()
		return AssistantTextUpdate{Barrier: true}
	default:
		return AssistantTextUpdate{}
	}
}

// ObserveFrame appends one ACP narrative text frame without applying an
// update-type barrier. Content is always treated as an exact delta; adapters
// for non-standard cumulative endpoints must normalize snapshots explicitly.
func (a *FinalAssistantAccumulator) ObserveFrame(messageID string, text string) AssistantTextUpdate {
	return a.observeFrame(messageID, text, false)
}

// ObserveReplacement replaces the current identified message rather than
// appending it. An empty replacement preserves the identity for later deltas.
func (a *FinalAssistantAccumulator) ObserveReplacement(messageID string, text string) AssistantTextUpdate {
	return a.observeFrame(messageID, text, true)
}

func (a *FinalAssistantAccumulator) observeFrame(messageID string, text string, replacement bool) AssistantTextUpdate {
	if a == nil {
		return AssistantTextUpdate{}
	}
	messageID = strings.TrimSpace(messageID)
	if canonical := a.aliases[messageID]; canonical != "" {
		messageID = canonical
	}
	if _, known := a.introduced[messageID]; messageID != "" && known && messageID != a.messageID {
		// First introduction selects the final answer. A later upsert repairs
		// an older display message without retaining its full text here.
		return AssistantTextUpdate{Text: a.FinalText(), Delta: text, MessageID: messageID, Assistant: true, Replacement: replacement}
	}
	if messageID != "" {
		switch {
		case a.messageID == "":
			a.messageID = messageID
			a.messageIDGenerated = false
		case a.messageID == messageID:
		case !a.finalEligible:
			a.resetMessage()
			a.messageID = messageID
		case a.messageIDGenerated:
			// The generated identity may already have been published. Keep it
			// stable until a real message barrier rather than dropping its prefix.
			if a.aliases == nil {
				a.aliases = map[string]string{}
			}
			a.aliases[messageID] = a.messageID
		default:
			a.resetMessage()
			a.messageID = messageID
		}
	} else if text != "" || replacement {
		if a.messageID == "" || !a.finalEligible {
			// Anonymous v1 frames after a barrier start a new segment. An
			// explicitly identified latest message can instead resume above.
			a.resetMessage()
			a.messageID = nextGeneratedNarrativeMessageID()
			a.messageIDGenerated = true
		}
	}
	delta := text
	if replacement {
		a.text = text
	} else {
		delta = a.appendAssistantFrame(text)
	}
	if text != "" || replacement {
		a.finalEligible = true
	}
	a.rememberIdentity(a.messageID)
	return AssistantTextUpdate{Text: a.FinalText(), Delta: delta, MessageID: a.messageID, Assistant: true, Replacement: replacement}
}

func (a *FinalAssistantAccumulator) FinalText() string {
	if a == nil || !a.finalEligible {
		return ""
	}
	return a.text
}

func (a *FinalAssistantAccumulator) Reset() {
	if a != nil {
		a.resetMessage()
		a.introduced, a.aliases = nil, nil
	}
}

// AdvanceBoundary makes the latest message ineligible as the final answer until
// it receives more content. Its one snapshot remains available for identified
// continuation; known older identities remain display repairs in this Turn.
func (a *FinalAssistantAccumulator) AdvanceBoundary() {
	if a != nil {
		a.finalEligible = false
	}
}

func (a *FinalAssistantAccumulator) observeContentChunk(updateType string, content any, messageID string, replacement bool) AssistantTextUpdate {
	switch strings.TrimSpace(updateType) {
	case eventstream.UpdateAgentMessage:
		return a.observeFrame(messageID, session.ExtractProtocolText(content), replacement)
	case eventstream.UpdateAgentThought:
		if session.ExtractProtocolText(content) != "" || replacement {
			a.AdvanceBoundary()
			return AssistantTextUpdate{Barrier: true}
		}
		return AssistantTextUpdate{}
	default:
		return AssistantTextUpdate{}
	}
}

func (a *FinalAssistantAccumulator) resetMessage() {
	a.messageID = ""
	a.messageIDGenerated = false
	a.text = ""
	a.finalEligible = false
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

func (a *FinalAssistantAccumulator) rememberIdentity(id string) {
	if id == "" {
		return
	}
	if a.introduced == nil {
		a.introduced = make(map[string]struct{})
	}
	a.introduced[id] = struct{}{}
}
