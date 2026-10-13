package gatewayapp

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// Keep text-mode envelope recovery bounded independently of provider
// output-token support. Rationale length is informational and must not overturn
// an otherwise valid approval decision; the whole assessment retains this cap.
const guardianMaxAssessmentBytes = 8 * 1024

type guardianReviewModelOutput struct {
	Outcome   string `json:"-"`
	OptionID  string `json:"option_id"`
	Rationale string `json:"rationale"`
}

// parseGuardianAssessmentForMode separates provider-envelope compatibility
// from decision validation. Native JSON modes require one standalone object;
// text mode may wrap one unambiguous object in Markdown or explanatory prose.
// In every mode the selected object is decoded and validated identically.
func parseGuardianAssessmentForMode(
	text string,
	mode model.OutputMode,
	options []kernel.ApprovalOption,
) (guardianReviewModelOutput, error) {
	candidate, err := guardianAssessmentCandidate(text, mode)
	if err != nil {
		return guardianReviewModelOutput{}, err
	}
	return decodeGuardianAssessmentCandidate(candidate, options)
}

func guardianAssessmentCandidate(text string, mode model.OutputMode) (string, error) {
	if len(text) > guardianMaxAssessmentBytes {
		return "", fmt.Errorf("approval reviewer assessment exceeds %d-byte limit", guardianMaxAssessmentBytes)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("approval reviewer returned invalid JSON")
	}

	switch mode {
	case model.OutputModeText:
		return guardianTextAssessmentCandidate(text)
	case "", model.OutputModeJSON, model.OutputModeSchema:
		return text, nil
	default:
		return "", fmt.Errorf("approval reviewer received unsupported output mode %q", mode)
	}
}

// guardianTextAssessmentCandidate finds all brace-balanced top-level objects
// before attempting to decode any of them. This ordering is intentional: an
// invalid example followed by a valid decision is still ambiguous and must not
// degrade into "first candidate that validates" selection.
func guardianTextAssessmentCandidate(text string) (string, error) {
	candidates, err := guardianTopLevelJSONObjectCandidates(text)
	if err != nil {
		return "", err
	}
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("approval reviewer returned no JSON object")
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("approval reviewer returned more than one top-level JSON object")
	}
}

func guardianTopLevelJSONObjectCandidates(text string) ([]string, error) {
	var candidates []string
	start := -1
	depth := 0
	inString := false
	escaped := false

	for index := 0; index < len(text); index++ {
		char := text[index]
		if depth == 0 {
			switch char {
			case '{':
				start = index
				depth = 1
			case '}':
				return nil, fmt.Errorf("approval reviewer returned unbalanced JSON object braces")
			}
			continue
		}

		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch char {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				candidates = append(candidates, text[start:index+1])
				start = -1
			}
		}
	}

	if depth != 0 || inString || escaped {
		return nil, fmt.Errorf("approval reviewer returned unbalanced JSON object braces")
	}
	return candidates, nil
}

func decodeGuardianAssessmentCandidate(candidate string, options []kernel.ApprovalOption) (guardianReviewModelOutput, error) {
	fields, err := decodeGuardianAssessmentObject(candidate)
	if err != nil {
		return guardianReviewModelOutput{}, err
	}
	var parsed guardianReviewModelOutput
	if raw, present := fields["option_id"]; present {
		if err := json.Unmarshal(raw, &parsed.OptionID); err != nil {
			return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer returned invalid option_id: %w", err)
		}
	}
	_, decision, err := approval.ResolveStrictOption(options, parsed.OptionID)
	if err != nil {
		return guardianReviewModelOutput{}, err
	}
	// Only the original option determines authority. Allow explanations and
	// extra fields are informational; never decode them into a decision or target.
	if decision == approval.OptionDecisionDeny {
		if raw, present := fields["rationale"]; present {
			if err := json.Unmarshal(raw, &parsed.Rationale); err != nil {
				return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer returned invalid rationale: %w", err)
			}
		}
	}
	return normalizeGuardianAssessment(parsed, options)
}

// Decode exact field names once: struct decoding would also accept case aliases
// and JSON's last-key-wins behavior would conceal conflicting duplicate keys.
func decodeGuardianAssessmentObject(candidate string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(candidate))
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
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("approval reviewer returned more than one JSON value")
		}
		return nil, fmt.Errorf("approval reviewer returned trailing content: %w", err)
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

func normalizeGuardianAssessment(parsed guardianReviewModelOutput, options []kernel.ApprovalOption) (guardianReviewModelOutput, error) {
	_, decision, err := approval.ResolveStrictOption(options, parsed.OptionID)
	if err != nil {
		return guardianReviewModelOutput{}, err
	}
	parsed.Outcome = string(decision)
	parsed.Rationale = strings.TrimSpace(parsed.Rationale)
	if decision == approval.OptionDecisionAllow {
		parsed.Rationale = ""
	}
	if decision == approval.OptionDecisionDeny && parsed.Rationale == "" {
		return guardianReviewModelOutput{}, fmt.Errorf("deny requires a specific rationale")
	}
	return parsed, nil
}
