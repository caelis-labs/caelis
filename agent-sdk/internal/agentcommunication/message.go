// Package agentcommunication prepares trusted Agent-to-Agent input for model
// providers without changing its durable display text.
package agentcommunication

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// AppendSender adds trusted sender identity after the original message.
// Text and media remain provider-neutral parts.
func AppendSender(message model.Message, actor session.ActorRef) (model.Message, error) {
	if err := session.ValidateAgentCommunicationActor(actor); err != nil {
		return model.Message{}, fmt.Errorf("agentcommunication: %w", err)
	}
	out := model.CloneMessage(message)
	out.Role = model.RoleUser
	footer := session.AgentCommunicationPromptFooter(actor)
	if n := len(out.Parts); n > 0 && out.Parts[n-1].Text != nil {
		out.Parts[n-1].Text.Text += footer
	} else {
		out.Parts = append(out.Parts, model.NewTextPart(footer))
	}
	return out, nil
}

// Prompt converts a plain prompt plus typed content into provider-visible
// content followed by trusted sender identity.
func Prompt(input string, parts []model.ContentPart, actor session.ActorRef) (string, []model.ContentPart, error) {
	message := model.MessageFromTextAndContentParts(model.RoleUser, input, parts)
	prepared, err := AppendSender(message, actor)
	if err != nil {
		return "", nil, err
	}
	return "", model.ContentPartsFromParts(prepared.Parts), nil
}
