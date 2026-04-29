package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/host"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

func TestBashDefinitionExposesMinimalArguments(t *testing.T) {
	t.Parallel()

	rt, err := host.New(host.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	tool, err := NewBash(BashConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewBash() error = %v", err)
	}
	properties, _ := tool.Definition().InputSchema["properties"].(map[string]any)
	if _, ok := properties["command"]; !ok {
		t.Fatal("command property missing")
	}
	if _, ok := properties["workdir"]; !ok {
		t.Fatal("workdir property missing")
	}
	if _, ok := properties["yield_time_ms"]; !ok {
		t.Fatal("yield_time_ms property missing")
	}
	if _, ok := properties["timeout_ms"]; ok {
		t.Fatal("timeout_ms property unexpectedly exposed")
	}
	if _, ok := properties["tty"]; ok {
		t.Fatal("tty property unexpectedly exposed")
	}
	if _, ok := properties["env"]; ok {
		t.Fatal("env property unexpectedly exposed")
	}
	if _, ok := properties["dir"]; ok {
		t.Fatal("dir alias unexpectedly exposed")
	}
}

func TestBashCallAcceptsYieldTimeWithoutChangingSyncResult(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rt, err := host.New(host.Config{CWD: dir})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	tool, err := NewBash(BashConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewBash() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"command":       "printf 'ok'",
		"workdir":       dir,
		"yield_time_ms": 25,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := tool.Call(context.Background(), sdktool.Call{Name: BashToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("result.Content = empty, want json payload")
	}
}

func TestBashCallReturnsStructuredCommandErrorWithDiagnostics(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rt, err := host.New(host.Config{CWD: dir})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	tool, err := NewBash(BashConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewBash() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"command": "printf 'module cache denied\\n' >&2; exit 7",
		"workdir": dir,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := tool.Call(context.Background(), sdktool.Call{Name: BashToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v, want structured tool error result", err)
	}
	if !result.IsError {
		t.Fatal("result.IsError = false, want true")
	}
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("result.Content = %#v, want json payload", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if stderr, _ := payload["stderr"].(string); !strings.Contains(stderr, "module cache denied") {
		t.Fatalf("stderr = %q, want raw command diagnostics", stderr)
	}
	if exitCode, _ := payload["exit_code"].(float64); exitCode != 7 {
		t.Fatalf("exit_code = %v, want 7", payload["exit_code"])
	}
	if message, _ := payload["error"].(string); !strings.Contains(message, "exit status") {
		t.Fatalf("error = %q, want command failure detail", message)
	}
	if denied, _ := payload["sandbox_permission_denied"].(bool); denied {
		t.Fatal("sandbox_permission_denied = true for non-permission command failure")
	}
}

func TestBashCallPrefixesSandboxPermissionErrorWithOriginalDetail(t *testing.T) {
	t.Parallel()

	rt := sandboxPermissionRuntime{result: sdksandbox.CommandResult{
		Stderr:   "touch: cannot touch /home/test/go/pkg/mod/cache: Read-only file system\n",
		ExitCode: 1,
		Route:    sdksandbox.RouteSandbox,
		Backend:  sdksandbox.BackendBwrap,
	}, err: fmt.Errorf("exit status 1")}
	tool, err := NewBash(BashConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewBash() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"command": "printf 'touch: cannot touch /home/test/go/pkg/mod/cache: Read-only file system\\n' >&2; exit 1",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := tool.Call(context.Background(), sdktool.Call{Name: BashToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v, want structured tool error result", err)
	}
	if !result.IsError {
		t.Fatal("result.IsError = false, want true")
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if denied, _ := payload["sandbox_permission_denied"].(bool); !denied {
		t.Fatalf("sandbox_permission_denied = %#v, want true", payload["sandbox_permission_denied"])
	}
	stderr, _ := payload["stderr"].(string)
	if !strings.Contains(stderr, "/home/test/go/pkg/mod/cache") {
		t.Fatalf("stderr = %q, want original denied path", stderr)
	}
	message, _ := payload["error"].(string)
	if !strings.Contains(message, "Sandbox permission denied") ||
		!strings.Contains(message, "/home/test/go/pkg/mod/cache") {
		t.Fatalf("error = %q, want sandbox prefix plus original denied path", message)
	}
	if _, ok := payload["sandbox_diagnostic"]; ok {
		t.Fatalf("sandbox_diagnostic present = %#v, want generic sandbox error only", payload["sandbox_diagnostic"])
	}
}

func TestBashCallDoesNotLabelHostPermissionErrorsAsSandboxDenied(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rt, err := host.New(host.Config{CWD: dir})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	tool, err := NewBash(BashConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewBash() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"command": "printf 'git@github.com: Permission denied (publickey).\\n' >&2; exit 128",
		"workdir": dir,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := tool.Call(context.Background(), sdktool.Call{Name: BashToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v, want structured tool error result", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if denied, _ := payload["sandbox_permission_denied"].(bool); denied {
		t.Fatalf("sandbox_permission_denied = true for host permission failure: %#v", payload)
	}
	if message, _ := payload["error"].(string); strings.Contains(message, "Sandbox permission denied") {
		t.Fatalf("error = %q, should not suggest sandbox escalation for host failure", message)
	}
}

type sandboxPermissionRuntime struct {
	result sdksandbox.CommandResult
	err    error
}

func (r sandboxPermissionRuntime) Describe() sdksandbox.Descriptor {
	return sdksandbox.Descriptor{Backend: sdksandbox.BackendBwrap}
}

func (r sandboxPermissionRuntime) FileSystem() sdksandbox.FileSystem { return nil }

func (r sandboxPermissionRuntime) FileSystemFor(sdksandbox.Constraints) sdksandbox.FileSystem {
	return nil
}

func (r sandboxPermissionRuntime) Run(context.Context, sdksandbox.CommandRequest) (sdksandbox.CommandResult, error) {
	return r.result, r.err
}

func (r sandboxPermissionRuntime) Start(context.Context, sdksandbox.CommandRequest) (sdksandbox.Session, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r sandboxPermissionRuntime) OpenSession(string) (sdksandbox.Session, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r sandboxPermissionRuntime) OpenSessionRef(sdksandbox.SessionRef) (sdksandbox.Session, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r sandboxPermissionRuntime) SupportedBackends() []sdksandbox.Backend {
	return []sdksandbox.Backend{sdksandbox.BackendBwrap}
}

func (r sandboxPermissionRuntime) Status() sdksandbox.Status {
	return sdksandbox.Status{ResolvedBackend: sdksandbox.BackendBwrap}
}

func (r sandboxPermissionRuntime) Close() error { return nil }
