package agentsdk

import (
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// AgentCommunicationInput is one source-attributed input in an ordered admission.
// Source is trusted embedding authority, never inferred from text or wire metadata.
type AgentCommunicationInput struct {
	Source       session.ActorRef    `json:"source"`
	Input        string              `json:"input,omitempty"`
	DisplayInput string              `json:"display_input,omitempty"`
	ContentParts []model.ContentPart `json:"content_parts,omitempty"`
}

// CloneAgentCommunicationInputs detaches ordered input from its caller.
func CloneAgentCommunicationInputs(in []AgentCommunicationInput) []AgentCommunicationInput {
	if in == nil {
		return nil
	}
	out := make([]AgentCommunicationInput, len(in))
	for i, item := range in {
		out[i] = item
		out[i].Source = session.CloneActorRef(item.Source)
		out[i].ContentParts = append([]model.ContentPart(nil), item.ContentParts...)
	}
	return out
}

// AgentInputBatchEntry binds input to an exact source attachment. Controller
// entries omit Participant; participant entries require the complete binding.
type AgentInputBatchEntry struct {
	Message     AgentCommunicationInput
	Participant *session.ParticipantBinding
}
