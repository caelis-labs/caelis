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
	RiskLevel         string `json:"risk_level"`
	UserAuthorization string `json:"user_authorization"`
	Outcome           string `json:"outcome"`
	OptionID          string `json:"option_id"`
	Rationale         string `json:"rationale"`
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
	return normalizeGuardianAssessment(parsed, options)
}

func validateGuardianAssessmentObjectKeys(candidate string) error {
	allowed := map[string]struct{}{
		"option_id":          {},
		"risk_level":         {},
		"user_authorization": {},
		"outcome":            {},
		"rationale":          {},
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

func finalizeGuardianDecision(
	payload *kernel.ApprovalPayload,
	parsed guardianReviewModelOutput,
) (kernel.ApprovalReviewResult, error) {
	approved := parsed.Outcome == "allow"
	optionID := strings.TrimSpace(parsed.OptionID)
	outcome := approvalOutcome(approved)
	if payload != nil && len(payload.Options) > 0 {
		_, optionDecision, err := approval.ResolveStrictOption(payload.Options, optionID)
		if err != nil {
			return kernel.ApprovalReviewResult{}, err
		}
		if (optionDecision == approval.OptionDecisionAllow) != approved {
			return kernel.ApprovalReviewResult{}, fmt.Errorf("approval reviewer option %q does not match outcome %q", optionID, parsed.Outcome)
		}
		outcome = string(kernel.ApprovalStatusSelected)
	} else if optionID != "" {
		return kernel.ApprovalReviewResult{}, fmt.Errorf("approval reviewer returned option_id %q without approval options", optionID)
	}
	risk := normalizeReviewLabel(parsed.RiskLevel, "unknown")
	authorization := normalizeAuthorizationLabel(parsed.UserAuthorization, "unknown")
	rationale := firstNonEmpty(parsed.Rationale, "approval reviewer returned no rationale")
	result := kernel.ApprovalReviewResult{
		Approved:       approved,
		Outcome:        outcome,
		Risk:           risk,
		Authorization:  authorization,
		OptionID:       optionID,
		Rationale:      rationale,
		DecisionSource: "auto-review",
	}
	result.DisplayText = kernel.FormatApprovalReviewText(result.Approved, result.Risk, result.Authorization, result.Rationale)
	return result, nil
}

func guardianDeterministicDenial(payload *kernel.ApprovalPayload, rationale string) kernel.ApprovalReviewResult {
	result := kernel.ApprovalReviewResult{
		Approved:       false,
		Outcome:        string(kernel.ApprovalStatusRejected),
		Risk:           "unknown",
		Authorization:  "unknown",
		Rationale:      strings.TrimSpace(rationale),
		DecisionSource: "auto-review",
	}
	if payload != nil && len(payload.Options) > 0 {
		if optionID, ok, err := approval.StrictOptionIDForDecision(payload.Options, approval.OptionDecisionDeny); err == nil && ok {
			result.OptionID = optionID
			result.Outcome = string(kernel.ApprovalStatusSelected)
		}
	}
	result.DisplayText = kernel.FormatApprovalReviewText(false, result.Risk, result.Authorization, result.Rationale)
	return result
}

func parseGuardianAssessment(text string) (guardianReviewModelOutput, error) {
	return parseGuardianAssessmentForMode(text, model.OutputModeSchema, nil)
}

func parseGuardianAssessmentWithOptions(text string, options []kernel.ApprovalOption) (guardianReviewModelOutput, error) {
	return parseGuardianAssessmentForMode(text, model.OutputModeSchema, options)
}

func normalizeGuardianAssessment(parsed guardianReviewModelOutput, options []kernel.ApprovalOption) (guardianReviewModelOutput, error) {
	hasOptions := len(options) > 0
	if hasOptions {
		if strings.TrimSpace(parsed.OptionID) == "" ||
			strings.TrimSpace(parsed.RiskLevel) == "" ||
			strings.TrimSpace(parsed.UserAuthorization) == "" ||
			strings.TrimSpace(parsed.Outcome) == "" ||
			strings.TrimSpace(parsed.Rationale) == "" {
			return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer must return option_id, risk_level, user_authorization, outcome, and rationale when options are present")
		}
	} else if strings.TrimSpace(parsed.OptionID) != "" {
		return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer returned option_id without approval options")
	}

	outcome := strings.ToLower(strings.TrimSpace(parsed.Outcome))
	switch outcome {
	case "allow", "deny":
		parsed.Outcome = outcome
	default:
		return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer returned unsupported outcome %q", parsed.Outcome)
	}

	risk := strings.TrimSpace(parsed.RiskLevel)
	if risk == "" {
		if outcome == "allow" {
			parsed.RiskLevel = "low"
		} else {
			parsed.RiskLevel = "high"
		}
	} else if normalized, ok := canonicalGuardianRiskLabel(risk); ok {
		parsed.RiskLevel = normalized
	} else {
		return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer returned unsupported risk_level %q", parsed.RiskLevel)
	}

	authorization := strings.TrimSpace(parsed.UserAuthorization)
	if authorization == "" {
		parsed.UserAuthorization = "unknown"
	} else if normalized, ok := canonicalGuardianAuthorizationLabel(authorization); ok {
		parsed.UserAuthorization = normalized
	} else {
		return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer returned unsupported user_authorization %q", parsed.UserAuthorization)
	}

	if strings.TrimSpace(parsed.Rationale) == "" {
		if outcome == "allow" {
			if parsed.RiskLevel == "low" {
				parsed.Rationale = "Auto-review returned a low-risk allow decision."
			} else {
				parsed.Rationale = "Auto-review returned an allow decision without a rationale."
			}
		} else {
			parsed.Rationale = "Auto-review returned a deny decision without a rationale."
		}
	} else {
		parsed.Rationale = strings.TrimSpace(parsed.Rationale)
	}
	parsed.OptionID = strings.TrimSpace(parsed.OptionID)
	if hasOptions {
		_, optionDecision, err := approval.ResolveStrictOption(options, parsed.OptionID)
		if err != nil {
			return guardianReviewModelOutput{}, err
		}
		if (optionDecision == approval.OptionDecisionAllow) != (parsed.Outcome == "allow") {
			return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer option %q does not match outcome %q", parsed.OptionID, parsed.Outcome)
		}
	}
	if parsed.RiskLevel == "critical" && parsed.Outcome == "allow" {
		return guardianReviewModelOutput{}, fmt.Errorf("approval reviewer cannot allow a critical-risk action")
	}

	return parsed, nil
}

func canonicalGuardianRiskLabel(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return "low", true
	case "medium":
		return "medium", true
	case "high":
		return "high", true
	case "critical":
		return "critical", true
	default:
		return "", false
	}
}

func canonicalGuardianAuthorizationLabel(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "unknown":
		return "unknown", true
	case "low":
		return "low", true
	case "medium":
		return "medium", true
	case "high":
		return "high", true
	default:
		return "", false
	}
}

func approvalOutcome(approved bool) string {
	if approved {
		return string(kernel.ApprovalStatusApproved)
	}
	return string(kernel.ApprovalStatusRejected)
}

func normalizeReviewLabel(value string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "low", "medium", "high", "critical", "unknown":
		return value
	default:
		return strings.TrimSpace(fallback)
	}
}

func normalizeAuthorizationLabel(value string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "low", "medium", "high", "unknown":
		return value
	default:
		return strings.TrimSpace(fallback)
	}
}
