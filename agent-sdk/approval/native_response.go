package approval

import (
	"fmt"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ResolveNativeResponse validates a decision against the original permission,
// before settlement or publication. An offered option's canonical kind owns
// its executable meaning; Approved is only a legacy outcome hint. Presentation
// facts and option labels never grant authority.
func ResolveNativeResponse(permission *session.ProtocolApproval, response agent.ApprovalResponse) (agent.ApprovalResponse, error) {
	if response.Outcome == "cancelled" {
		if response.OptionID != "" {
			return agent.ApprovalResponse{}, fmt.Errorf("approval: cancelled decision cannot select an option")
		}
		response.Approved = false
		return response, nil
	}
	if permission == nil || len(permission.Options) == 0 {
		// Older SDK approvals and persisted tokens may have no option contract.
		// Their outcome/boolean contract is retained, without inventing choices.
		return response, nil
	}
	options := make([]Option, 0, len(permission.Options))
	for _, option := range permission.Options {
		options = append(options, Option{ID: option.ID, Name: option.Name, Kind: option.Kind})
	}
	if err := ValidateStrictOptions(options); err != nil {
		return agent.ApprovalResponse{}, err
	}
	switch response.Outcome {
	case "", "approved", "rejected", "selected":
	default:
		return agent.ApprovalResponse{}, fmt.Errorf("approval: unsupported decision outcome %q", response.Outcome)
	}
	if response.Outcome == "selected" || response.OptionID != "" {
		for _, option := range options {
			if option.ID != response.OptionID {
				continue
			}
			decision, _ := strictOptionDecision(option.Kind)
			allow := decision == OptionDecisionAllow
			if response.Outcome == "approved" && !allow || response.Outcome == "rejected" && allow {
				return agent.ApprovalResponse{}, fmt.Errorf("approval: outcome conflicts with the selected option kind")
			}
			response.Approved = allow
			if response.Outcome == "" {
				response.Outcome = "selected"
			}
			return response, nil
		}
		return agent.ApprovalResponse{}, fmt.Errorf("approval: selected option was not offered")
	}
	// Keep legacy outcome-only decisions usable for existing SDK callers. An
	// approval still needs an originally offered allow choice; denial does not
	// manufacture a grant or an option.
	switch response.Outcome {
	case "approved":
		response.Approved = true
	case "rejected":
		response.Approved = false
	}
	if response.Approved {
		if _, exists, err := StrictOptionIDForDecision(options, OptionDecisionAllow); err != nil || !exists {
			return agent.ApprovalResponse{}, fmt.Errorf("approval: no allow option was offered")
		}
	}
	return response, nil
}
