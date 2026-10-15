package gatewayapp

import (
	"context"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

// taskStreamAuthorizer adapts command-client Session ownership policy to the
// independent Task observation contract. The explicit principal mapping keeps
// taskstream free of command request vocabulary.
type taskStreamAuthorizer struct {
	inner appserver.SessionAuthorizer
}

func (a taskStreamAuthorizer) AuthorizeTaskStream(ctx context.Context, principal controltaskstream.Principal, sessionID string) error {
	return a.inner.Authorize(ctx, appserver.Principal{
		ID: principal.ID, Roles: append([]string(nil), principal.Roles...),
	}, appserver.ActionSessionInspect, sessionID)
}

var _ controltaskstream.Authorizer = taskStreamAuthorizer{}
