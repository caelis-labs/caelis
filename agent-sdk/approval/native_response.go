package approval

import (
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ResolveNativeResponse validates a decision against the original permission,
// before settlement or publication. An offered option's canonical kind owns
// its executable meaning; Approved is only a legacy outcome hint. Presentation
// facts and option labels never grant authority.
func ResolveNativeResponse(permission *session.ProtocolApproval, response Response) (Response, error) {
	if response.Outcome == "cancelled" {
		if response.OptionID != "" {
			return Response{}, fmt.Errorf("approval: cancelled decision cannot select an option")
		}
		response.Approved = false
		return response, nil
	}
	switch response.Outcome {
	case "approved":
		response.Approved = true
	case "rejected":
		response.Approved = false
	case "", "selected":
	default:
		return Response{}, fmt.Errorf("approval: unsupported decision outcome %q", response.Outcome)
	}
	var options []Option
	if permission != nil {
		options = make([]Option, 0, len(permission.Options))
		for _, option := range permission.Options {
			options = append(options, Option{ID: option.ID, Name: option.Name, Kind: option.Kind})
		}
	}
	if len(options) == 0 {
		// Older SDK callers and durable records may carry a selection ID without
		// a catalog. Preserve its identity and use the explicit decision above.
		selected := response.Outcome == "selected"
		hasOptionID := response.OptionID != ""
		if selected != hasOptionID {
			return Response{}, fmt.Errorf("approval: optionless selection requires both selected outcome and an option id")
		}
		return response, nil
	}
	if response.Outcome == "selected" || response.OptionID != "" {
		if err := ValidateStrictOptions(options); err != nil {
			return Response{}, err
		}
		for _, option := range options {
			if option.ID != response.OptionID {
				continue
			}
			decision, _ := strictOptionDecision(option.Kind)
			allow := decision == OptionDecisionAllow
			if response.Outcome == "approved" && !allow || response.Outcome == "rejected" && allow {
				return Response{}, fmt.Errorf("approval: outcome conflicts with the selected option kind")
			}
			response.Approved = allow
			if response.Outcome == "" {
				response.Outcome = "selected"
			}
			return response, nil
		}
		return Response{}, fmt.Errorf("approval: selected option was not offered")
	}
	// An outcome-only approval needs an offered allow choice. Outcome-only
	// denial remains valid with any well-formed set of options.
	_, allowOffered, err := StrictOptionIDForDecision(options, OptionDecisionAllow)
	if err != nil {
		return Response{}, err
	}
	if response.Approved && !allowOffered {
		return Response{}, fmt.Errorf("approval: no allow option was offered")
	}
	return response, nil
}
