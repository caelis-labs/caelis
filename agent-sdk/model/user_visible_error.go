package model

import "github.com/caelis-labs/caelis/agent-sdk/display"

// UserDisplayableError lets runtime errors provide a provider-detail-free
// message for user-facing surfaces while preserving Error for diagnostics.
type UserDisplayableError = display.UserDisplayableError

// UserVisibleError returns the user-facing text for err.
func UserVisibleError(err error) string {
	return display.UserVisibleError(err)
}
