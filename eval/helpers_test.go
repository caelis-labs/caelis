//go:build e2e

package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func repoRootForEval(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root")
		}
		dir = parent
	}
}

func startEvalSession(
	t *testing.T,
	ctx context.Context,
	stack *gatewayapp.Stack,
	preferredSessionID string,
) session.Session {
	t.Helper()
	server, err := local.NewAppServer(stack)
	if err != nil {
		t.Fatal(err)
	}
	clients, err := server.Bind(appserver.Principal{ID: stack.UserID()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := clients.Sessions.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "eval-session-" + uuid.NewString()},
		PreferredSessionID: preferredSessionID,
		WorkspaceKey:       stack.Workspace().Key,
		CWD:                stack.Workspace().CWD,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Sessions().Session(ctx, session.SessionRef{SessionID: result.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func evalAppServerClients(
	t *testing.T,
	stack *gatewayapp.Stack,
	principalID string,
) appserver.AppServerClients {
	t.Helper()
	server, err := local.NewAppServer(stack)
	if err != nil {
		t.Fatal(err)
	}
	clients, err := server.Bind(appserver.Principal{ID: principalID})
	if err != nil {
		t.Fatal(err)
	}
	return clients
}

func startEvalSessionTurn(
	t *testing.T,
	ctx context.Context,
	stack *gatewayapp.Stack,
	active session.Session,
	input string,
) (appserver.TargetTurn, error) {
	t.Helper()
	clients := evalAppServerClients(t, stack, active.UserID)
	turns, err := appserver.NewSessionTurnClient(clients.Sessions)
	if err != nil {
		return nil, err
	}
	return turns.Start(ctx, appserver.SessionTurnStartRequest{
		SessionID: active.SessionID,
		Input:     input,
	})
}
