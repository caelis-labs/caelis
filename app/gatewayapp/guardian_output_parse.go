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
	if err := validateGuardianAssessmentObjectKeys(candidate); err != nil {
		return guardianReviewModelOutput{}, err
	}
	var parsed guardianReviewModelOutput
	decoder := json.NewDecoder(strings.NewReader(candidate))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer returned invalid JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer returned more than one JSON value")
		}
		return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer returned trailing content: %w", err)
	}
	parsed, err := normalizeGuardianAssessment(parsed, options)
	if err != nil {
		return guardianReviewModelOutput{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(candidate), &fields); err != nil {
		return guardianReviewModelOutput{}, err
	}
	if _, present := fields["rationale"]; present && parsed.Outcome == "allow" {
		return guardianReviewModelOutput{}, fmt.Errorf("allow must omit rationale")
	}
	return parsed, nil
}

func validateGuardianAssessmentObjectKeys(candidate string) error {
	allowed := map[string]struct{}{
		"option_id": {},
		"rationale": {},
	}
	decoder := json.NewDecoder(strings.NewReader(candidate))
	opening, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("approval reviewer returned invalid JSON: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return fmt.Errorf("approval reviewer assessment must be one JSON object")
	}
	seen := make(map[string]struct{}, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("approval reviewer returned invalid JSON: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("approval reviewer returned a non-string JSON field name")
		}
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("approval reviewer returned unsupported field %q", key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("approval reviewer returned duplicate field %q", key)
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("approval reviewer returned invalid field %q: %w", key, err)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("approval reviewer returned invalid JSON: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return fmt.Errorf("approval reviewer assessment must end with one JSON object")
	}
	return nil
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
	if decision == approval.OptionDecisionAllow && parsed.Rationale != "" {
		return guardianReviewModelOutput{}, fmt.Errorf("allow must not include rationale")
	}
	if decision == approval.OptionDecisionDeny && parsed.Rationale == "" {
		return guardianReviewModelOutput{}, fmt.Errorf("deny requires a specific rationale")
	}
	return parsed, nil
}
