package local

import (
	"strings"

	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// authorizeHostCapability verifies that an AppServer capability was bound to
// an authenticated principal. Host capabilities are scoped by the AppServer's
// Stack and workspace; they intentionally do not borrow Session ownership or
// agent-sdk Session.UserID as a second authorization boundary.
func authorizeHostCapability(principal appserver.Principal) error {
	if strings.TrimSpace(principal.ID) == "" {
		return appserver.ErrUnauthorized
	}
	return nil
}

// A Session grant cannot observe or mutate Host-global configuration objects.
func authorizeUnscopedHostCapability(principal appserver.Principal) error {
	if err := authorizeHostCapability(principal); err != nil {
		return err
	}
	if principal.ApplicationID != "" || principal.ConnectionID != "" {
		return appserver.ErrUnauthorized
	}
	return nil
}
