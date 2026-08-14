package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
)

// startGatewayAppTestSession creates test Sessions through the same typed
// Control service consumed by embedded AppServer clients.
func startGatewayAppTestSession(ctx context.Context, s *Stack, preferredSessionID string) (session.Session, error) {
	if s == nil {
		return session.Session{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	client, err := controlclient.BindSessionClient(s.ControlClient(), controlclient.Principal{ID: strings.TrimSpace(s.UserID)})
	if err != nil {
		return session.Session{}, err
	}
	result, err := client.CreateSession(ctx, controlclient.CreateSessionRequest{
		WriteBase:          controlclient.WriteBase{OperationID: "gatewayapp-test-session-" + uuid.NewString()},
		PreferredSessionID: strings.TrimSpace(preferredSessionID),
		WorkspaceKey:       strings.TrimSpace(s.Workspace.Key),
		CWD:                strings.TrimSpace(s.Workspace.CWD),
	})
	if err != nil {
		return session.Session{}, err
	}
	if result.Outcome != controlclient.OutcomeCommitted || strings.TrimSpace(result.SessionID) == "" {
		return session.Session{}, fmt.Errorf("gatewayapp: Session create ended with outcome %q", result.Outcome)
	}
	return s.Sessions.Session(ctx, session.SessionRef{SessionID: result.SessionID})
}
