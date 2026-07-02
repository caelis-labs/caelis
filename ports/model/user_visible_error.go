package model

import (
	"errors"
	"strings"
)

// UserDisplayableError lets runtime errors provide a provider-detail-free
// message for user-facing surfaces while preserving Error for diagnostics.
type UserDisplayableError interface {
	DisplayMessage() string
}

// UserVisibleError returns the user-facing text for err.
func UserVisibleError(err error) string {
	if err == nil {
		return ""
	}
	var displayErr UserDisplayableError
	if errors.As(err, &displayErr) {
		if text := strings.TrimSpace(displayErr.DisplayMessage()); text != "" {
			return text
		}
	}
	return strings.TrimSpace(err.Error())
}
