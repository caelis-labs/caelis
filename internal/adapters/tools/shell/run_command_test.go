package shell

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
	sandboxhost "github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/host"
)

func TestRunCommandToolExecutesThroughSandbox(t *testing.T) {
	rt, err := sandboxhost.New(context.Background(), sandbox.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runTool, err := NewRunCommandTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	command := "printf hello"
	if runtime.GOOS == "windows" {
		command = "echo hello"
	}
	input, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}

	result, err := runTool.Call(context.Background(), tool.Call{ID: "call-1", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("result IsError = true: %#v", result)
	}
	if len(result.Content) != 1 || result.Content[0].Text == nil || !strings.Contains(result.Content[0].Text.Text, "hello") {
		t.Fatalf("result content = %#v, want stdout text", result.Content)
	}
}

func TestRunCommandToolReturnsModelVisibleCommandFailures(t *testing.T) {
	rt, err := sandboxhost.New(context.Background(), sandbox.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runTool, err := NewRunCommandTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{"command": "exit 7"})
	if err != nil {
		t.Fatal(err)
	}

	result, err := runTool.Call(context.Background(), tool.Call{Input: input})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("result IsError = false, want true")
	}
	if got := result.Meta["exit_code"]; got != 7 {
		t.Fatalf("exit code meta = %v, want 7", got)
	}
}

func TestRunCommandToolMapsEscalationToHostConstraints(t *testing.T) {
	rt := &recordingRuntime{}
	runTool, err := NewRunCommandTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{
		"command":             "git commit -m test",
		"sandbox_permissions": "require_escalated",
		"justification":       "create the requested commit",
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := runTool.Call(context.Background(), tool.Call{ID: "call-1", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	if rt.request.Constraints.Route != sandbox.RouteHost ||
		rt.request.Constraints.Backend != sandbox.BackendHost ||
		rt.request.Constraints.Permission != sandbox.PermissionFullAccess ||
		rt.request.Constraints.Isolation != sandbox.IsolationHost {
		t.Fatalf("constraints = %#v, want host escalation constraints", rt.request.Constraints)
	}
	if result.Meta["sandbox_permissions"] != string(sandbox.PermissionRequestRequireEscalated) ||
		result.Meta["justification"] != "create the requested commit" {
		t.Fatalf("result meta = %#v, want escalation metadata", result.Meta)
	}
}

func TestRunCommandToolRequiresEscalationJustification(t *testing.T) {
	rt := &recordingRuntime{}
	runTool, err := NewRunCommandTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{
		"command":             "git commit -m test",
		"sandbox_permissions": "require_escalated",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := runTool.Call(context.Background(), tool.Call{Input: input}); err == nil {
		t.Fatal("Call missing justification error = nil, want error")
	}
	if strings.TrimSpace(rt.request.Command) != "" {
		t.Fatalf("runtime request = %#v, want no execution", rt.request)
	}
}

type recordingRuntime struct {
	request sandbox.CommandRequest
}

func (r *recordingRuntime) Descriptor() sandbox.Descriptor {
	return sandbox.Descriptor{Backend: sandbox.BackendCustom}
}

func (r *recordingRuntime) Status() sandbox.Status {
	return sandbox.Status{ResolvedBackend: sandbox.BackendCustom}
}

func (r *recordingRuntime) FileSystem() sandbox.FileSystem {
	return nil
}

func (r *recordingRuntime) Run(_ context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	r.request = req
	return sandbox.CommandResult{
		Stdout:   "ok\n",
		ExitCode: 0,
		Route:    req.Constraints.Route,
		Backend:  req.Constraints.Backend,
	}, nil
}

func (r *recordingRuntime) Start(context.Context, sandbox.CommandRequest) (sandbox.Session, error) {
	return nil, errors.New("not implemented")
}

func (r *recordingRuntime) Open(context.Context, sandbox.SessionRef) (sandbox.Session, error) {
	return nil, errors.New("not implemented")
}

func (r *recordingRuntime) Close() error {
	return nil
}
