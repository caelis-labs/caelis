package local

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// acquireControlRuntimeFunc is the focused Host lifecycle capability shared by
// local AppServer services that need an authorized Session Runtime snapshot.
type acquireControlRuntimeFunc func(
	context.Context,
	appserver.Principal,
	appserver.Action,
	string,
	bool,
) (*gatewayapp.ControlRuntimeLease, error)

type resolveWorkspaceAddressFunc func(session.WorkspaceRef) (session.WorkspaceRef, error)
