//go:build e2e

package eval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/surfaces/headless"
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

func repoRootForGatewayAppTest(t *testing.T) string { return repoRootForEval(t) }
func repoRootForRunnerTest(t *testing.T) string     { return repoRootForEval(t) }

func privateEvalTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("Chmod(%s) error = %v", dir, err)
	}
	return dir
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
	clients, _, err := server.Bind(controlclient.Principal{ID: stack.UserID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := clients.Sessions.CreateSession(ctx, controlclient.CreateSessionRequest{
		WriteBase:          controlclient.WriteBase{OperationID: "eval-session-" + uuid.NewString()},
		PreferredSessionID: preferredSessionID,
		WorkspaceKey:       stack.Workspace.Key,
		CWD:                stack.Workspace.CWD,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Sessions.Session(ctx, session.SessionRef{SessionID: result.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func evalAppServerClients(
	t *testing.T,
	stack *gatewayapp.Stack,
	principalID string,
) controlclient.AppServerClients {
	t.Helper()
	server, err := local.NewAppServer(stack)
	if err != nil {
		t.Fatal(err)
	}
	clients, _, err := server.Bind(controlclient.Principal{ID: principalID})
	if err != nil {
		t.Fatal(err)
	}
	return clients
}

func inspectEvalSession(
	t *testing.T,
	ctx context.Context,
	stack *gatewayapp.Stack,
	active session.Session,
) controlclient.SessionState {
	t.Helper()
	clients := evalAppServerClients(t, stack, active.UserID)
	state, err := clients.Sessions.InspectSession(ctx, controlclient.StateRequest{SessionID: active.SessionID})
	if err != nil {
		t.Fatalf("InspectSession(%s) error = %v", active.SessionID, err)
	}
	return state
}

func handoffEvalController(
	t *testing.T,
	ctx context.Context,
	stack *gatewayapp.Stack,
	active session.Session,
	target string,
	surface string,
) controlclient.SessionState {
	t.Helper()
	clients := evalAppServerClients(t, stack, active.UserID)
	state, err := clients.Sessions.InspectSession(ctx, controlclient.StateRequest{SessionID: active.SessionID})
	if err != nil {
		t.Fatalf("InspectSession(%s before handoff) error = %v", active.SessionID, err)
	}
	revision := state.Revision
	if _, err := clients.Agents.HandoffAgent(ctx, controlclient.HandoffAgentRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:             "eval-controller-handoff-" + uuid.NewString(),
			SessionID:               active.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: state.Controller.EpochID,
		},
		Target: target,
	}); err != nil {
		t.Fatalf("HandoffAgent(%s) error = %v", target, err)
	}
	state, err = clients.Sessions.InspectSession(ctx, controlclient.StateRequest{SessionID: active.SessionID})
	if err != nil {
		t.Fatalf("InspectSession(%s after handoff) error = %v", active.SessionID, err)
	}
	return state
}

func startEvalSessionTurn(
	t *testing.T,
	ctx context.Context,
	stack *gatewayapp.Stack,
	active session.Session,
	input string,
) (controlclient.TargetTurn, error) {
	t.Helper()
	clients := evalAppServerClients(t, stack, active.UserID)
	turns, err := controlclient.NewSessionTurnClient(clients.Sessions)
	if err != nil {
		return nil, err
	}
	return turns.Start(ctx, controlclient.SessionTurnStartRequest{
		SessionID: active.SessionID,
		Input:     input,
	})
}

func newEvalAppServerAdapter(
	t *testing.T,
	stack *gatewayapp.Stack,
	active session.Session,
	surface string,
) *controladapter.SessionClientAdapter {
	t.Helper()
	server, err := local.NewAppServer(stack)
	if err != nil {
		t.Fatal(err)
	}
	clients, _, err := server.Bind(controlclient.Principal{ID: active.UserID})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := controladapter.NewAppServerAdapter(controladapter.AppServerAdapterConfig{
		SessionID:     active.SessionID,
		WorkspaceKey:  active.WorkspaceKey,
		WorkspaceDir:  active.CWD,
		Surface:       surface,
		Sessions:      clients.Sessions,
		Participants:  clients.Participants,
		Status:        clients.Status,
		Configuration: clients.Configuration,
		Agents:        clients.Agents,
		Completion:    clients.Completion,
		Plugins:       clients.Plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func runEvalHeadlessOnce(
	t *testing.T,
	ctx context.Context,
	stack *gatewayapp.Stack,
	active session.Session,
	input string,
	opts headless.Options,
) (headless.Result, error) {
	t.Helper()
	server, err := local.NewAppServer(stack)
	if err != nil {
		return headless.Result{}, err
	}
	clients, _, err := server.Bind(controlclient.Principal{ID: active.UserID})
	if err != nil {
		return headless.Result{}, err
	}
	turns, err := controlclient.NewSessionTurnClient(clients.Sessions)
	if err != nil {
		return headless.Result{}, err
	}
	return headless.RunSessionOnce(ctx, turns, controlclient.SessionTurnStartRequest{
		SessionID: active.SessionID,
		Input:     input,
	}, opts)
}

type recordingStreams struct {
	frames []stream.Frame
}

func (s *recordingStreams) PublishStream(frame stream.Frame) {
	s.frames = append(s.frames, stream.CloneFrame(frame))
}
