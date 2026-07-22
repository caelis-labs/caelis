package gatewayapp

import (
	"context"

	controlclient "github.com/caelis-labs/caelis/control/client"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

// taskStreamAuthorizer adapts command-client Session ownership policy to the
// independent Task observation contract. The explicit principal mapping keeps
// taskstream free of command request vocabulary.
type taskStreamAuthorizer struct {
	inner controlclient.SessionAuthorizer
}

func (a taskStreamAuthorizer) AuthorizeTaskStream(ctx context.Context, principal controltaskstream.Principal, sessionID string) error {
	return a.inner.Authorize(ctx, controlclient.Principal{
		ID: principal.ID, Roles: append([]string(nil), principal.Roles...),
	}, controlclient.ActionSessionInspect, sessionID)
}

var _ controltaskstream.Authorizer = taskStreamAuthorizer{}
