package gatewayapp

import (
	"context"
	"runtime"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestOrdinarySessionExecutionConfigPersistsAndActivates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell command fixture")
	}
	t.Setenv("CAELIS_TEST_SESSION_AMBIENT", "ambient")
	t.Setenv("CAELIS_CONTROL_TOKEN", "private")
	workspace := newWorkspaceRuntimeTestDir(t, "workspace", "Workspace rule.")
	stack, err := NewLocalStack(Config{
		StoreDir: t.TempDir(), WorkspaceKey: "workspace", WorkspaceCWD: workspace,
		SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client := newWorkspaceRuntimeHTTPClient(t, stack, "local-user")
	inherit := false
	requested := &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{
		Inherit: &inherit, Set: map[string]string{"CAELIS_TEST_SESSION_VALUE": "configured"},
	}}
	created, err := client.CreateSession(context.Background(), appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "create-execution"},
		PreferredSessionID: "session-execution", WorkspaceKey: "workspace", CWD: workspace,
		ExecutionConfig: requested,
	})
	if err != nil || created.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("CreateSession() = %#v, %v", created, err)
	}
	active, err := stack.composition.sessions.Session(context.Background(), session.SessionRef{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if active.ExecutionConfig == nil || active.ExecutionConfig.Environment.Inherit == nil || *active.ExecutionConfig.Environment.Inherit ||
		active.ExecutionConfig.Environment.Set["CAELIS_TEST_SESSION_VALUE"] != "configured" {
		t.Fatalf("durable creation config = %#v", active.ExecutionConfig)
	}
	state, err := client.InspectSession(context.Background(), appserver.StateRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if state.ExecutionConfig == nil || state.ExecutionConfig.Environment.Set["CAELIS_TEST_SESSION_VALUE"] != "configured" {
		t.Fatalf("Session readback config = %#v", state.ExecutionConfig)
	}
	changed := sandbox.CloneExecutionConfig(requested)
	changed.Environment.Set["CAELIS_TEST_SESSION_VALUE"] = "silently-changed"
	_, err = client.CreateSession(context.Background(), appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "reuse-execution"},
		PreferredSessionID: created.SessionID, WorkspaceKey: "workspace", CWD: workspace,
		ExecutionConfig: changed,
	})
	if errorcode.CodeOf(err) != errorcode.InvalidArgument {
		t.Fatalf("reusing Session with changed creation config: %v; want invalid_argument", err)
	}
	loaded := activateSessionRuntime(t, stack, created.SessionID)
	if loaded.instance.executionConfig == nil || loaded.instance.executionConfig.Environment.Set["CAELIS_TEST_SESSION_VALUE"] != "configured" {
		t.Fatalf("activation config = %#v", loaded.instance.executionConfig)
	}
	// Direct Runtime execution represents an already-approved Host route; a
	// product Turn still obtains approval before supplying these constraints.
	result, err := loaded.instance.exec.Run(context.Background(), sandbox.CommandRequest{
		Command: `printf '%s/%s/%s' "$CAELIS_TEST_SESSION_VALUE" "$CAELIS_TEST_SESSION_AMBIENT" "$CAELIS_CONTROL_TOKEN"`,
		Dir:     workspace,
		Constraints: sandbox.Constraints{
			Route: sandbox.RouteHost, Backend: sandbox.BackendHost, Permission: sandbox.PermissionFullAccess,
		},
	})
	if err != nil || result.ExitCode != 0 || result.Stdout != "configured//" {
		t.Fatalf("sandbox Run() = %#v, %v; want configured//", result, err)
	}
	defaultSession, err := client.CreateSession(context.Background(), appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "create-default-execution"},
		PreferredSessionID: "session-default-execution", WorkspaceKey: "workspace", CWD: workspace,
	})
	if err != nil || defaultSession.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("CreateSession(default) = %#v, %v", defaultSession, err)
	}
	defaultRuntime := activateSessionRuntime(t, stack, defaultSession.SessionID)
	if defaultRuntime.instance.executionConfig != nil {
		t.Fatalf("default Session has execution config %#v", defaultRuntime.instance.executionConfig)
	}
	result, err = defaultRuntime.instance.exec.Run(context.Background(), sandbox.CommandRequest{
		Command: `printf '%s/%s' "$CAELIS_TEST_SESSION_AMBIENT" "$CAELIS_CONTROL_TOKEN"`,
		Dir:     workspace,
		Constraints: sandbox.Constraints{
			Route: sandbox.RouteHost, Backend: sandbox.BackendHost, Permission: sandbox.PermissionFullAccess,
		},
	})
	if err != nil || result.ExitCode != 0 || result.Stdout != "ambient/" {
		t.Fatalf("sandbox Run(default) = %#v, %v; want ambient/", result, err)
	}
}
