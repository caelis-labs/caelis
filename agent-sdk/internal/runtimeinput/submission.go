// Package runtimeinput owns submission kinds that are reserved for trusted
// Runtime orchestration and must never be accepted through the public Runner
// submission API.
package runtimeinput

import (
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/agentcommunication"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/userdisplay"
)

// ModelContext injects Runtime-authored context at the next safe model boundary
// without creating client-visible dialogue.
const ModelContext agent.SubmissionKind = "runtime_model_context"

// NewEvent constructs the model message and optional client projection for one
// admitted input. The caller owns admission, Turn scope and persistence.
func NewEvent(kind agent.SubmissionKind, input agent.AgentCommunicationInput, metadata map[string]any) (*session.Event, error) {
	if strings.TrimSpace(input.Input) == "" && len(input.ContentParts) == 0 {
		return nil, nil
	}
	message, text, meta := userdisplay.Resolve(input.Input, input.DisplayInput, input.ContentParts, metadata)
	event := &session.Event{
		Visibility: session.VisibilityCanonical, Actor: session.CloneActorRef(input.Source),
		Message: &message, Text: text, Meta: meta,
	}
	switch kind {
	case agent.SubmissionKindConversation:
		event.Type = session.EventTypeUser
		if !session.ActorRefHasIdentity(event.Actor) {
			event.Actor = session.ActorRef{Kind: session.ActorKindUser, Name: "user"}
		}
		event.Protocol = &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeUserMessage),
			Content:       session.ProtocolTextContent(text),
		}}
	case agent.SubmissionKindAgentCommunication:
		prepared, err := agentcommunication.AppendSender(message, event.Actor)
		if err != nil {
			return nil, err
		}
		event.Type = session.EventTypeContext
		event.Message = &prepared
		event.MessageID = strings.TrimSpace(input.MessageID)
		protocol := session.NewAgentCommunicationProtocol(session.ProtocolAgentCommunication{Text: text})
		event.Protocol = &protocol
	case ModelContext:
		if !session.ActorRefHasIdentity(event.Actor) {
			return nil, fmt.Errorf("context submission requires source identity")
		}
		event.Type = session.EventTypeContext
	default:
		return nil, fmt.Errorf("agent-sdk/runtime: unsupported input kind %q", kind)
	}
	return event, nil
}
