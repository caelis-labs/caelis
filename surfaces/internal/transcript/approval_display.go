package transcript

import "strings"

type ApprovalReviewDisplay struct {
	Status    string
	Rationale string
}

// ApprovalReviewDisplayParts separates one review record into the outcome the
// transcript shows and the rationale shown beneath it. Risk and authorization
// are review metadata without a transcript presentation.
func ApprovalReviewDisplayParts(status string, text string) ApprovalReviewDisplay {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "⚠"))
	return ApprovalReviewDisplay{
		Status:    FirstNonEmpty(strings.TrimSpace(status), ApprovalReviewStatusFromText(text), "reviewed"),
		Rationale: ApprovalReviewRationaleFromText(text),
	}
}

func ApprovalReviewStatusFromText(text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, status := range approvalReviewStatuses {
		if lower == status || strings.HasPrefix(lower, status+":") || strings.Contains(lower, "approval review "+status) {
			return status
		}
	}
	return ""
}

var approvalReviewStatuses = []string{"approved", "denied", "failed", "timed_out", "timed out", "needs_user", "needs user", "needs-user"}

func ApprovalReviewRationaleFromText(text string) string {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "⚠"))
	if text == "" {
		return ""
	}
	for _, status := range approvalReviewStatuses {
		if strings.EqualFold(text, status) {
			return ""
		}
		if strings.HasPrefix(strings.ToLower(text), status+":") {
			return strings.TrimSpace(text[len(status)+1:])
		}
	}
	if before, after, ok := strings.Cut(text, "):"); ok && strings.Contains(strings.ToLower(before), "approval review") {
		return strings.TrimSpace(after)
	}
	if before, after, ok := strings.Cut(text, ":"); ok && strings.Contains(strings.ToLower(before), "approval review") && !strings.Contains(before, "(") {
		return strings.TrimSpace(after)
	}
	if strings.Contains(strings.ToLower(text), "approval review") {
		return ""
	}
	return text
}
