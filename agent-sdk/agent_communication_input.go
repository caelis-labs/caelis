package agentsdk

import (
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ValidateSubmissionInputs rejects mixed singular/batch input and validates all
// batch members before any member can be admitted. Singular inputs retain their
// existing kind-specific validation at the Runner boundary.
func ValidateSubmissionInputs(sub Submission) error {
	if len(sub.Inputs) == 0 {
		return nil
	}
	if sub.Kind != SubmissionKindAgentCommunication || sub.Text != "" || sub.DisplayInput != "" ||
		len(sub.ContentParts) != 0 || len(sub.Metadata) != 0 || session.ActorRefHasIdentity(sub.Actor) {
		return errors.New("batch and singular Agent communication cannot be combined")
	}
	for _, item := range sub.Inputs {
		if err := session.ValidateAgentCommunicationActor(item.Source); err != nil {
			return err
		}
		if strings.TrimSpace(item.Input) == "" && len(item.ContentParts) == 0 {
			return errors.New("Agent communication batch requires nonempty inputs")
		}
	}
	return nil
}

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
