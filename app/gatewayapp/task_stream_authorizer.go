package gatewayapp

import (
	"context"

	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	internalcontrolclient "github.com/caelis-labs/caelis/internal/controlclient"
	controlclient "github.com/caelis-labs/caelis/ports/controlclient"
)

// taskStreamAuthorizer adapts the existing Session ownership policy without
// coupling the Control Task stream package to the transitional client port.
type taskStreamAuthorizer struct {
	inner internalcontrolclient.SessionAuthorizer
}

func (a taskStreamAuthorizer) AuthorizeTaskStream(ctx context.Context, principal controltaskstream.Principal, sessionID string) error {
	return a.inner.Authorize(ctx, controlclient.Principal{
		ID: principal.ID, Roles: append([]string(nil), principal.Roles...),
	}, controlclient.ActionSessionInspect, sessionID)
}

var _ controltaskstream.Authorizer = taskStreamAuthorizer{}
