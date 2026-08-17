package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// startGatewayAppTestSession creates test Sessions through the same typed
// Control service consumed by embedded AppServer clients.
func startGatewayAppTestSession(ctx context.Context, s *Stack, preferredSessionID string) (session.Session, error) {
	if s == nil {
		return session.Session{}, fmt.Errorf("gatewayapp: stack is unavailable")
	}
	client, err := appserver.BindSessionClient(s.ControlClient(), appserver.Principal{ID: strings.TrimSpace(s.composition.userID)})
	if err != nil {
		return session.Session{}, err
	}
	result, err := client.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "gatewayapp-test-session-" + uuid.NewString()},
		PreferredSessionID: strings.TrimSpace(preferredSessionID),
		WorkspaceKey:       strings.TrimSpace(s.composition.workspace.Key),
		CWD:                strings.TrimSpace(s.composition.workspace.CWD),
	})
	if err != nil {
		return session.Session{}, err
	}
	if result.Outcome != appserver.OutcomeCommitted || strings.TrimSpace(result.SessionID) == "" {
		return session.Session{}, fmt.Errorf("gatewayapp: Session create ended with outcome %q", result.Outcome)
	}
	return s.composition.sessions.Session(ctx, session.SessionRef{SessionID: result.SessionID})
}
