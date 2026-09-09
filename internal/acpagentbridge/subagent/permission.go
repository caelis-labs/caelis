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
// has it on the anchor, so a matching request may proceed during resume.
// Unknown or mismatched identity, including whitespace drift, fails closed
// before any permission bridge or requester.
func boundChildPermissionHandler(run *childRun, next PermissionHandler) client.PermissionHandler {
	return func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
		var boundID string
		if run != nil {
			run.mu.Lock()
			boundID = strings.TrimSpace(run.anchor.SessionID)
			run.mu.Unlock()
		}
		if boundID == "" || boundID != string(req.SessionId) {
			return client.RequestPermissionResponse{}, errACPPermissionSessionMismatchChild
		}
		if next == nil {
			return acputil.RejectOnce(), nil
		}
		return next(ctx, req)
	}
}
