package client

import (
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

func PermissionSelectedOutcome(optionID string) RequestPermissionResponse {
	return RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeSelected(
		acpsdk.PermissionOptionId(strings.TrimSpace(optionID)),
	)}
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
