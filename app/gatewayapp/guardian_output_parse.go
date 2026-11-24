package gatewayapp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// Keep assessment recovery bounded independently of provider
// output-token support. Rationale length is informational and must not overturn
// an otherwise valid approval decision; the whole assessment retains this cap.
const guardianMaxAssessmentBytes = 8 * 1024

type guardianReviewModelOutput struct {
	Outcome   string `json:"-"`
	OptionID  string `json:"option_id"`
	Rationale string `json:"rationale"`
}

// Providers can wrap or repeat a decision even when asked for schema output.
// Across valid selections a denial wins, then the shortest offered grant.
func parseGuardianAssessmentForMode(auth *session.ApprovalAuthorization, text string, mode model.OutputMode, options []kernel.ApprovalOption) (guardianReviewModelOutput, error) {
	switch mode {
	case "", model.OutputModeText, model.OutputModeJSON, model.OutputModeSchema:
	default:
		return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer received unsupported output mode %q", mode)
	}
	if len(text) > guardianMaxAssessmentBytes {
		return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer assessment exceeds %d-byte limit", guardianMaxAssessmentBytes)
	}
	var decision guardianReviewModelOutput
	var selectedRank int
	for {
		start := strings.IndexAny(text, "{}[")
		if start < 0 {
			break
		}
		text = text[start:]
		if text[0] == '[' {
			// A prose label such as [decision] may precede the answer. Never
			// promote an object inside an array to a top-level decision.
			end := strings.IndexAny(text[1:], "[]{}\"")
			if end >= 0 && text[1+end] == ']' {
				text = text[end+2:]
				continue
			}
			return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer assessment must be a top-level object")
		}
		decoder := json.NewDecoder(strings.NewReader(text))
		fields, err := decodeGuardianAssessmentObject(decoder)
		if err != nil {
			return guardianReviewModelOutput{}, err
		}
		parsed, option, err := decodeGuardianAssessmentFields(fields, options)
		if err != nil {
			return guardianReviewModelOutput{}, err
		}
		rank := guardianDecisionRank(auth, option, parsed.Outcome)
		if decision.OptionID == "" || rank < selectedRank || parsed.OptionID == decision.OptionID {
			decision, selectedRank = parsed, rank
		}
		text = text[decoder.InputOffset():]
	}
	if decision.OptionID == "" {
		return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer returned no JSON object")
	}
	return decision, nil
}

// Lower ranks take precedence independent of the order of model messages.
func guardianDecisionRank(auth *session.ApprovalAuthorization, option approval.Option, outcome string) int {
	if outcome == string(approval.OptionDecisionDeny) {
		return 0
	}
	if duration, ok := auth.Choice(option.ID); ok {
		switch duration {
		case session.AuthorizationOnce:
			return 1
		case session.AuthorizationSession:
			return 2
		case session.AuthorizationPolicy:
			return 3
		}
	}
	if option.Kind == "allow_once" {
		return 1
	}
	return 3
}

func decodeGuardianAssessmentFields(fields map[string]json.RawMessage, options []kernel.ApprovalOption) (guardianReviewModelOutput, approval.Option, error) {
	var parsed guardianReviewModelOutput
	if raw, present := fields["option_id"]; present {
		if err := json.Unmarshal(raw, &parsed.OptionID); err != nil {
			return guardianReviewModelOutput{}, approval.Option{}, fmt.Errorf("approval reviewer returned invalid option_id: %w", err)
		}
	}
	option, decision, err := approval.ResolveStrictOption(options, parsed.OptionID)
	if err != nil {
		return guardianReviewModelOutput{}, approval.Option{}, err
	}
	// Only the original option determines authority. Allow explanations and
	// extra fields are informational; never decode them into a decision or target.
	if decision == approval.OptionDecisionDeny {
		if raw, present := fields["rationale"]; present {
			if err := json.Unmarshal(raw, &parsed.Rationale); err != nil {
				return guardianReviewModelOutput{}, approval.Option{}, fmt.Errorf("approval reviewer returned invalid rationale: %w", err)
			}
		}
	}
	parsed.OptionID, parsed.Outcome = option.ID, string(decision)
	parsed.Rationale = strings.TrimSpace(parsed.Rationale)
	if decision == approval.OptionDecisionDeny && parsed.Rationale == "" {
		return guardianReviewModelOutput{}, approval.Option{}, fmt.Errorf("deny requires a specific rationale")
	}
	return parsed, option, nil
}

// Decode exact field names once: struct decoding would also accept case aliases
// and JSON's last-key-wins behavior would conceal conflicting duplicate keys.
func decodeGuardianAssessmentObject(decoder *json.Decoder) (map[string]json.RawMessage, error) {
	opening, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("approval reviewer returned invalid JSON: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("approval reviewer assessment must be one JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("approval reviewer returned invalid JSON: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("approval reviewer returned a non-string JSON field name")
		}
		for _, canonical := range []string{"option_id", "rationale"} {
			if key != canonical && strings.EqualFold(key, canonical) {
				return nil, fmt.Errorf("approval reviewer returned non-canonical field %q", key)
			}
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, fmt.Errorf("approval reviewer returned duplicate field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("approval reviewer returned invalid field %q: %w", key, err)
		}
		fields[key] = value
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("approval reviewer returned invalid JSON: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return nil, fmt.Errorf("approval reviewer assessment must end with one JSON object")
	}
	return fields, nil
}

func finalizeGuardianDecision(payload *kernel.ApprovalPayload, parsed guardianReviewModelOutput) (kernel.ApprovalReviewResult, error) {
	if payload == nil {
		return kernel.ApprovalReviewResult{}, fmt.Errorf("missing approval")
	}
	_, decision, err := approval.ResolveStrictOption(payload.Options, parsed.OptionID)
	if err != nil {
		return kernel.ApprovalReviewResult{}, err
	}
	approved := decision == approval.OptionDecisionAllow
	display := "approved"
	if !approved {
		display = "denied"
		if parsed.Rationale != "" {
			display += ": " + parsed.Rationale
		}
	}
	return kernel.ApprovalReviewResult{Approved: approved, Outcome: string(kernel.ApprovalStatusSelected), OptionID: parsed.OptionID, Rationale: parsed.Rationale, DisplayText: display, DecisionSource: "auto-review"}, nil
}
