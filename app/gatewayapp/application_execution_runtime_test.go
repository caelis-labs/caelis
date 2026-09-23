package gatewayapp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/application"
)

type applicationRunnerProbe struct {
	sandbox.Runtime
	launches int
	writes   int
}

func (r *applicationRunnerProbe) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	r.launches++
	return sandbox.CommandResult{}, nil
}
func (r *applicationRunnerProbe) Start(context.Context, sandbox.CommandRequest) (sandbox.Session, error) {
	r.launches++
	return applicationSessionProbe{runtime: r}, nil
}
func (r *applicationRunnerProbe) OpenSession(string) (sandbox.Session, error) {
	return applicationSessionProbe{runtime: r}, nil
}
func (r *applicationRunnerProbe) OpenSessionRef(sandbox.SessionRef) (sandbox.Session, error) {
	return applicationSessionProbe{runtime: r}, nil
}

type applicationSessionProbe struct {
	sandbox.Session
	runtime *applicationRunnerProbe
}

func (s applicationSessionProbe) WriteInput(context.Context, []byte) error {
	s.runtime.writes++
	return nil
}

type applicationToolProbe struct{ calls int }

func (t *applicationToolProbe) Definition() tool.Definition {
	return tool.Definition{Name: "ReadResource"}
}
func (t *applicationToolProbe) Call(context.Context, tool.Call) (tool.Result, error) {
	t.calls++
	return tool.Result{Content: []model.Part{model.NewTextPart("effect")}}, nil
}

func TestApplicationLeaseWithdrawalPreventsDelayedNativeEffects(t *testing.T) {
	ctx := t.Context()
	store, err := application.Open(filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connection, err := store.Register(ctx, "owner", application.Registration{
		OperationID: "register", Name: "client", Credential: "app-client-" + strings.Repeat("ab", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	probe := &applicationRunnerProbe{}
	runtime := &isolatedExecutionRuntime{Runtime: probe, cwd: cwd, lease: store, scope: connection.Scope}
	request := sandbox.CommandRequest{Dir: cwd}
	started, err := runtime.Start(ctx, request)
	if err != nil || probe.launches != 1 {
		t.Fatalf("first launch = %d, %v", probe.launches, err)
	}
	opened, err := runtime.OpenSession("already-running")
	if err != nil {
		t.Fatal(err)
	}
	openedByRef, err := runtime.OpenSessionRef(sandbox.SessionRef{})
	if err != nil {
		t.Fatal(err)
	}
	resource := &applicationToolProbe{}
	leased := applicationLeasedTool{Tool: resource, store: store, scope: connection.Scope}
	if err := store.Revoke(ctx, connection.Scope); err != nil {
		t.Fatal(err)
	}
	// These calls model a provider delayed beyond the lease and an approval
	// resolved only after revocation. No new native launch or resource effect.
	if _, err := runtime.Run(ctx, request); err == nil {
		t.Fatal("revoked lease admitted delayed Run")
	}
	if _, err := runtime.Start(ctx, request); err == nil {
		t.Fatal("revoked lease admitted pending approval Start")
	}
	for _, session := range []sandbox.Session{started, opened, openedByRef} {
		if err := session.WriteInput(ctx, []byte("new command")); err == nil {
			t.Fatal("revoked lease admitted interactive input")
		}
	}
	if _, err := leased.Call(ctx, tool.Call{Name: "ReadResource"}); err == nil {
		t.Fatal("revoked lease admitted resource materialization")
	}
	if probe.launches != 1 || probe.writes != 0 || resource.calls != 0 {
		t.Fatalf("new effects after revoke: launches=%d writes=%d resource=%d", probe.launches, probe.writes, resource.calls)
	}
}
