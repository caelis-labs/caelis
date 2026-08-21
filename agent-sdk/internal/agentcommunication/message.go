// Package agentcommunication prepares trusted Agent-to-Agent input for model
// providers without changing its durable display text.
package agentcommunication

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// PrefixMessage adds trusted sender identity before the original message
// parts. The original text and media remain separate provider-neutral parts.
func PrefixMessage(message model.Message, actor session.ActorRef) (model.Message, error) {
	if err := session.ValidateAgentCommunicationActor(actor); err != nil {
		return model.Message{}, fmt.Errorf("agentcommunication: %w", err)
	}
	out := model.CloneMessage(message)
	out.Role = model.RoleUser
	out.Parts = append([]model.Part{model.NewTextPart(session.AgentCommunicationPromptHeader(actor))}, out.Parts...)
	return out, nil
}

// Prompt converts a plain prompt plus typed content into provider-visible
// content prefixed with trusted sender identity.
func Prompt(input string, parts []model.ContentPart, actor session.ActorRef) (string, []model.ContentPart, error) {
	message := model.MessageFromTextAndContentParts(model.RoleUser, input, parts)
	prefixed, err := PrefixMessage(message, actor)
	if err != nil {
		return "", nil, err
	}
	return "", model.ContentPartsFromParts(prefixed.Parts), nil
}
