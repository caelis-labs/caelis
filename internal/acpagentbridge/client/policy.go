package client

import "strings"

func PermissionSelectedOutcome(optionID string) RequestPermissionResponse {
	return RequestPermissionResponse{Outcome: PermissionOutcome{
		Outcome:  "selected",
		OptionID: strings.TrimSpace(optionID),
	}}
}

func SelectPermissionOptionID(options []PermissionOption, allowed bool) string {
	for _, option := range options {
		kind := strings.TrimSpace(strings.ToLower(option.Kind))
		switch {
		case allowed && kind == "allow_once":
			return strings.TrimSpace(option.OptionID)
		case !allowed && kind == "reject_once":
			return strings.TrimSpace(option.OptionID)
		}
	}
	if allowed {
		return "allow_once"
	}
	return "reject_once"
}
