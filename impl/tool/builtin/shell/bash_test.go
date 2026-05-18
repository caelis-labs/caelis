package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestBashDefinitionExposesMinimalArguments(t *testing.T) {
	t.Parallel()

	rt, err := host.New(host.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	bashTool, err := NewBash(BashConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewBash() error = %v", err)
	}
	definition := bashTool.Definition()
	for _, want := range []string{"platform shell", "historical tool name", "PowerShell syntax on Windows"} {
		if !strings.Contains(definition.Description, want) {
			t.Fatalf("Description = %q, want %q", definition.Description, want)
		}
	}
	properties, _ := definition.InputSchema["properties"].(map[string]any)
	if _, ok := properties["command"]; !ok {
		t.Fatal("command property missing")
	}
	command, _ := properties["command"].(map[string]any)
	description, _ := command["description"].(string)
	if !strings.Contains(description, "PowerShell syntax on Windows") {
		t.Fatalf("command description = %q, want Windows platform shell hint", description)
	}
	if _, ok := properties["workdir"]; !ok {
		t.Fatal("workdir property missing")
	}
	if _, ok := properties["yield_time_ms"]; !ok {
		t.Fatal("yield_time_ms property missing")
	}
	for _, key := range []string{"sandbox_permissions", "additional_permissions", "justification"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("%s property missing", key)
		}
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
	bashTool, err := NewBash(BashConfig{Runtime: rt})
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
	result, err := bashTool.Call(context.Background(), tool.Call{Name: BashToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("result.Content = empty, want json payload")
	}
}

func TestBashCallReturnsTerminalLikeCommandFailurePayload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rt, err := host.New(host.Config{CWD: dir})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	bashTool, err := NewBash(BashConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewBash() error = %v", err)
	}
	command := "printf 'module cache denied\\n' >&2; exit 7"
	if runtime.GOOS == "windows" {
		command = `[Console]::Error.WriteLine('module cache denied'); exit 7`
	}
	raw, err := json.Marshal(map[string]any{
		"command": command,
		"workdir": dir,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := bashTool.Call(context.Background(), tool.Call{Name: BashToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v, want structured tool error result", err)
	}
	if result.IsError {
		t.Fatal("result.IsError = true for command exit status, want false")
	}
	if len(result.Content) == 0 || result.Content[0].JSON == nil {
		t.Fatalf("result.Content = %#v, want json payload", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if resultText, _ := payload["result"].(string); !strings.Contains(resultText, "module cache denied") {
		t.Fatalf("result = %q, want raw command diagnostics", resultText)
	}
	if exitCode, _ := payload["exit_code"].(float64); exitCode != 7 {
		t.Fatalf("exit_code = %v, want 7", payload["exit_code"])
	}
	if _, ok := payload["error"]; ok {
		t.Fatalf("payload contains error = %#v, want command result and exit_code only", payload["error"])
	}
}

func TestBashCallPreservesSandboxPermissionStderrWithErrorHint(t *testing.T) {
	t.Parallel()

	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{
		Stderr:   "touch: cannot touch /home/test/go/pkg/mod/cache: Read-only file system\n",
		ExitCode: 1,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendBwrap,
	}, err: fmt.Errorf("exit status 1")}
	bashTool, err := NewBash(BashConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewBash() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"command": "printf 'touch: cannot touch /home/test/go/pkg/mod/cache: Read-only file system\\n' >&2; exit 1",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := bashTool.Call(context.Background(), tool.Call{Name: BashToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v, want structured tool error result", err)
	}
	if result.IsError {
		t.Fatal("result.IsError = true for shell command exit status, want false")
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	resultText, _ := payload["result"].(string)
	if !strings.Contains(resultText, "/home/test/go/pkg/mod/cache") {
		t.Fatalf("result = %q, want original denied path", resultText)
	}
	if got, _ := payload["error"].(string); got != sandbox.SandboxPermissionDeniedMessage {
		t.Fatalf("payload error = %q, want concise sandbox permission hint", got)
	}
	if _, exists := result.Meta["error"]; exists {
		t.Fatalf("result.Meta duplicated error output: %#v", result.Meta)
	}
}

func TestBashCallDetectsSandboxPermissionErrorFromStdoutRedirect(t *testing.T) {
	t.Parallel()

	const deniedPath = "/home/test/go/pkg/mod/cache/download/work.ctyun.cn/git/ctstack_cmp_v2/system/@v/v0.0.0.tmp"
	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{
		Stdout:   "go: writing stat cache: open " + deniedPath + ": read-only file system\n",
		ExitCode: 1,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendBwrap,
	}, err: fmt.Errorf("exit status 1")}
	bashTool, err := NewBash(BashConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewBash() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"command": "go build ./... 2>&1",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := bashTool.Call(context.Background(), tool.Call{Name: BashToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v, want structured tool error result", err)
	}
	if result.IsError {
		t.Fatal("result.IsError = true for shell command exit status, want false")
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	resultText, _ := payload["result"].(string)
	if !strings.Contains(resultText, deniedPath) {
		t.Fatalf("result = %q, want original denied path", resultText)
	}
	if got, _ := payload["error"].(string); got != sandbox.SandboxPermissionDeniedMessage {
		t.Fatalf("payload error = %q, want concise sandbox permission hint", got)
	}
	if _, exists := result.Meta["error"]; exists {
		t.Fatalf("result.Meta duplicated error output: %#v", result.Meta)
	}
}

func TestBashCallDoesNotLabelHostPermissionErrorsAsSandboxDenied(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rt, err := host.New(host.Config{CWD: dir})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	bashTool, err := NewBash(BashConfig{Runtime: rt})
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
	result, err := bashTool.Call(context.Background(), tool.Call{Name: BashToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v, want structured tool error result", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if _, ok := payload["error"]; ok {
		t.Fatalf("payload contains error = %#v, want command result and exit_code only", payload["error"])
	}
}

type sandboxPermissionRuntime struct {
	result sandbox.CommandResult
	err    error
}

func (r sandboxPermissionRuntime) Describe() sandbox.Descriptor {
	return sandbox.Descriptor{Backend: sandbox.BackendBwrap}
}

func (r sandboxPermissionRuntime) FileSystem() sandbox.FileSystem { return nil }

func (r sandboxPermissionRuntime) FileSystemFor(sandbox.Constraints) sandbox.FileSystem {
	return nil
}

func (r sandboxPermissionRuntime) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return r.result, r.err
}

func (r sandboxPermissionRuntime) Start(context.Context, sandbox.CommandRequest) (sandbox.Session, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r sandboxPermissionRuntime) OpenSession(string) (sandbox.Session, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r sandboxPermissionRuntime) OpenSessionRef(sandbox.SessionRef) (sandbox.Session, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r sandboxPermissionRuntime) SupportedBackends() []sandbox.Backend {
	return []sandbox.Backend{sandbox.BackendBwrap}
}

func (r sandboxPermissionRuntime) Status() sandbox.Status {
	return sandbox.Status{ResolvedBackend: sandbox.BackendBwrap}
}

func (r sandboxPermissionRuntime) Close() error { return nil }
