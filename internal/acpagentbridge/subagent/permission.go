package subagent

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
)

var errACPPermissionSessionMismatchChild = fmt.Errorf("ACP permission Session does not match bound child")

// boundChildPermissionHandler rejects a permission request unless the child
// already has a local bound Session ID and the request names that exact Session.
// Spawn fails closed until session/new stores that identity. Reconnect already
// has it on the anchor, so an authorized input may request permission during load.
// A read-only history observer cannot request execution permissions.
// Unknown or mismatched identity, including whitespace drift, fails closed
// before any permission bridge or requester.
func boundChildPermissionHandler(run *childRun, next PermissionHandler) client.PermissionHandler {
	return func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
		var boundID string
		observationOnly := false
		if run != nil {
			run.mu.Lock()
			boundID = strings.TrimSpace(run.anchor.SessionID)
			observationOnly = run.observationOnly
			run.mu.Unlock()
		}
		if boundID == "" || boundID != string(req.SessionId) {
			return client.RequestPermissionResponse{}, errACPPermissionSessionMismatchChild
		}
		if next == nil || observationOnly {
			return acputil.RejectOnce(), nil
		}
		return next(ctx, req)
	}
}
