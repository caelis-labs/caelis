package client

import "strings"

func PermissionSelectedOutcome(optionID string) RequestPermissionResponse {
	return RequestPermissionResponse{
		Outcome: PermissionOutcome{
			Outcome:  "selected",
			OptionID: strings.TrimSpace(optionID),
		},
	}
}

func SelectPermissionOptionID(options []PermissionOption, allowed bool) string {
	for _, option := range options {
		kind := strings.TrimSpace(strings.ToLower(option.Kind))
		switch {
		case allowed && kind == schemaPermAllowOnce:
			return strings.TrimSpace(option.OptionID)
		case !allowed && kind == schemaPermRejectOnce:
			return strings.TrimSpace(option.OptionID)
		}
	}
	if allowed {
		return "allow_once"
	}
	return "reject_once"
}

const (
	schemaPermAllowOnce  = "allow_once"
	schemaPermRejectOnce = "reject_once"
)
