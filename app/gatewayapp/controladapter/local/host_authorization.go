package local

import (
	"strings"

	controlclient "github.com/caelis-labs/caelis/control/client"
)

// authorizeHostCapability verifies that an AppServer capability was bound to
// an authenticated principal. Host capabilities are scoped by the AppServer's
// Stack and workspace; they intentionally do not borrow Session ownership or
// agent-sdk Session.UserID as a second authorization boundary.
func authorizeHostCapability(principal controlclient.Principal) error {
	if strings.TrimSpace(principal.ID) == "" {
		return controlclient.ErrUnauthorized
	}
	return nil
}
