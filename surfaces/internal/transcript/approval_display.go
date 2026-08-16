package transcript

import "strings"

type ApprovalReviewDisplay struct {
	Status        string
	Risk          string
	Authorization string
	Rationale     string
}

type ApprovalReviewFields struct {
	Tool          string
	Command       string
	Status        string
	Risk          string
	Authorization string
	Text          string
}

func ApprovalReviewDisplayParts(status string, risk string, authorization string, text string) ApprovalReviewDisplay {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "⚠"))
	return ApprovalReviewDisplay{
		Status:        FirstNonEmpty(strings.TrimSpace(status), ApprovalReviewStatusFromText(text), "reviewed"),
		Risk:          FirstNonEmpty(strings.TrimSpace(risk), ApprovalReviewValueFromText(text, "risk")),
		Authorization: FirstNonEmpty(strings.TrimSpace(authorization), ApprovalReviewValueFromText(text, "authorization")),
		Rationale:     ApprovalReviewRationaleFromText(text),
	}
}

func ApprovalReviewTailOutput(fields ApprovalReviewFields) string {
	display := ApprovalReviewDisplayParts(fields.Status, fields.Risk, fields.Authorization, fields.Text)
	status := strings.TrimSpace(display.Status)
	if status == "" {
		status = "reviewed"
	}
	line := "Approval review " + status
	if tool := strings.TrimSpace(fields.Tool); tool != "" {
		line += " " + tool
	}
	if command := strings.TrimSpace(fields.Command); command != "" {
		line += " " + command
	}
	meta := make([]string, 0, 2)
	if risk := strings.TrimSpace(display.Risk); risk != "" {
		meta = append(meta, "risk: "+risk)
	}
	if authorization := strings.TrimSpace(display.Authorization); authorization != "" {
		meta = append(meta, "authorization: "+authorization)
	}
	if len(meta) > 0 {
		line += " (" + strings.Join(meta, ", ") + ")"
	}
	if rationale := strings.TrimSpace(display.Rationale); rationale != "" {
		line += "\n" + rationale
	}
	return line + "\n"
}

func ApprovalReviewStatusFromText(text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, status := range []string{"approved", "denied", "failed", "timed_out"} {
		if strings.Contains(lower, "approval review "+status) {
			return status
		}
	}
	return ""
}

func ApprovalReviewValueFromText(text string, key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return ""
	}
	lower := strings.ToLower(text)
	needle := key + ":"
	idx := strings.Index(lower, needle)
	if idx < 0 {
		return ""
	}
	valueStart := idx + len(needle)
	value := strings.TrimSpace(text[valueStart:])
	for _, sep := range []string{",", ")"} {
		if cut := strings.Index(value, sep); cut >= 0 {
			value = value[:cut]
		}
	}
	return strings.TrimSpace(value)
}

func ApprovalReviewRationaleFromText(text string) string {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "⚠"))
	if text == "" {
		return ""
	}
	if before, after, ok := strings.Cut(text, "):"); ok && strings.Contains(strings.ToLower(before), "approval review") {
		return strings.TrimSpace(after)
	}
	if before, after, ok := strings.Cut(text, ":"); ok && strings.Contains(strings.ToLower(before), "approval review") {
		return strings.TrimSpace(after)
	}
	if strings.Contains(strings.ToLower(text), "approval review") {
		return ""
	}
	return text
}
