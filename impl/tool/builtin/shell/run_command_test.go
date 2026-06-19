package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	"github.com/OnslaughtSnail/caelis/internal/commanddiag"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestRunCommandDefinitionExposesMinimalArguments(t *testing.T) {
	t.Parallel()

	rt, err := host.New(host.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("host.New() error = %v", err)
	}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	definition := runCommandTool.Definition()
	if definition.Name != RunCommandToolName {
		t.Fatalf("Name = %q, want %q", definition.Name, RunCommandToolName)
	}
	for _, required := range []string{"specified workdir", "tests, builds", "Host execution"} {
		if !strings.Contains(definition.Description, required) {
			t.Fatalf("Description missing %q: %q", required, definition.Description)
		}
	}
	if strings.Contains(definition.Description, "sandbox_permissions") {
		t.Fatalf("Description should leave sandbox_permissions details to the property schema: %q", definition.Description)
	}
	properties, _ := definition.InputSchema["properties"].(map[string]any)
	wantDescriptions := map[string]string{
		"command":             "Command to execute.",
		"workdir":             "Working directory for the command; defaults to the session cwd.",
		"yield_time_ms":       "Wait before yielding async control.",
		"sandbox_permissions": "Sandbox mode for this command only. use_default runs with default sandbox permissions; require_escalated requests Host execution for this task-necessary command or exact retry after sandbox/permission/lock denial.",
		"justification":       "Short approval question for require_escalated; state what will run and why sandbox/current permissions are insufficient.",
	}
	if len(properties) != len(wantDescriptions) {
		t.Fatalf("properties = %#v, want only %v", properties, sortedRunCommandPropertyKeys(wantDescriptions))
	}
	for key, want := range wantDescriptions {
		property, ok := properties[key].(map[string]any)
		if !ok {
			t.Fatalf("%s property missing or malformed", key)
		}
		if got, _ := property["description"].(string); got != want {
			t.Fatalf("%s description = %q, want %q", key, got, want)
		}
	}
	commandProperty, _ := properties["command"].(map[string]any)
	if got := commandProperty["minLength"]; got != 1 {
		t.Fatalf("command minLength = %#v, want 1", got)
	}
	yieldProperty, _ := properties["yield_time_ms"].(map[string]any)
	if got := yieldProperty["minimum"]; got != 0 {
		t.Fatalf("yield_time_ms minimum = %#v, want 0", got)
	}
	sandboxProperty, _ := properties["sandbox_permissions"].(map[string]any)
	enumValues, _ := sandboxProperty["enum"].([]string)
	if strings.Join(enumValues, ",") != "use_default,require_escalated" {
		t.Fatalf("sandbox_permissions enum = %#v, want use_default/require_escalated", sandboxProperty["enum"])
	}
	if _, ok := properties["additional_permissions"]; ok {
		t.Fatal("additional_permissions property unexpectedly exposed")
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
	if _, ok := properties["timeout_ms"]; ok {
		t.Fatal("timeout_ms property unexpectedly exposed")
	}
}

func TestRunCommandCallRejectsUnsupportedArgsAndSandboxPermissions(t *testing.T) {
	t.Parallel()

	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{Stdout: "ok", ExitCode: 0}}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	tests := []struct {
		name string
		args map[string]any
	}{
		{
			name: "legacy sandbox permission",
			args: map[string]any{
				"command":             "go test ./...",
				"sandbox_permissions": "with_additional_permissions",
			},
		},
		{
			name: "additional permissions",
			args: map[string]any{
				"command": "go test ./...",
				"additional_permissions": map[string]any{
					"network": map[string]any{"enabled": true},
				},
			},
		},
		{
			name: "timeout alias",
			args: map[string]any{
				"command":    "go test ./...",
				"timeout_ms": 1,
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(tt.args)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			_, err = runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw})
			if err == nil {
				t.Fatal("Call() error = nil, want validation failure")
			}
		})
	}

	raw, err := json.Marshal(map[string]any{
		"command":             "curl https://example.com",
		"sandbox_permissions": "require_escalated",
	})
	if err != nil {
		t.Fatalf("json.Marshal(require_escalated) error = %v", err)
	}
	if _, err := runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw}); err != nil {
		t.Fatalf("Call(require_escalated) error = %v", err)
	}
}

func sortedRunCommandPropertyKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func TestRunCommandCallAcceptsYieldTimeWithoutChangingSyncResult(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{Stdout: "ok", ExitCode: 0, Route: sandbox.RouteSandbox, Backend: sandbox.BackendHost}}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"command":       "printf 'ok'",
		"workdir":       dir,
		"yield_time_ms": 25,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("result.Content = empty, want json payload")
	}
}

func TestRunCommandCallUsesConfiguredHardTimeoutOnly(t *testing.T) {
	t.Parallel()

	var last sandbox.CommandRequest
	rt := sandboxPermissionRuntime{
		result: sandbox.CommandResult{Stdout: "ok", ExitCode: 0},
		last:   &last,
	}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt, Timeout: 45 * time.Second})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"command": "printf 'ok'",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if _, err := runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw}); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got := last.Timeout; got != 45*time.Second {
		t.Fatalf("command timeout = %v, want configured hard timeout", got)
	}
}

func TestRunCommandCallDefaultsNetworkToRuntimeDefault(t *testing.T) {
	t.Parallel()

	var last sandbox.CommandRequest
	rt := sandboxPermissionRuntime{
		result: sandbox.CommandResult{Stdout: "ok", ExitCode: 0},
		last:   &last,
		descriptor: sandbox.Descriptor{
			Backend: sandbox.BackendWindows,
			DefaultConstraints: sandbox.Constraints{
				Route:      sandbox.RouteSandbox,
				Backend:    sandbox.BackendWindows,
				Permission: sandbox.PermissionWorkspaceWrite,
				Isolation:  sandbox.IsolationProcess,
				Network:    sandbox.NetworkEnabled,
			},
		},
	}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{"command": "printf 'ok'"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if _, err := runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw}); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got := last.Constraints.Network; got != sandbox.NetworkEnabled {
		t.Fatalf("default network = %q, want enabled", got)
	}
	if got := last.Backend; got != sandbox.BackendWindows {
		t.Fatalf("backend = %q, want windows default", got)
	}
}

func TestRunCommandCallReturnsTerminalLikeCommandFailurePayload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{
		Stderr:   "module cache denied\n",
		ExitCode: 7,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendHost,
	}, err: fmt.Errorf("exit status 7")}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
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
	result, err := runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw})
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

func TestRunCommandCallPreservesSandboxPermissionStderrWithErrorHint(t *testing.T) {
	t.Parallel()

	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{
		Stderr:   "touch: cannot touch /home/test/go/pkg/mod/cache: Read-only file system\n",
		ExitCode: 1,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendBwrap,
	}, err: fmt.Errorf("exit status 1")}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"command": "printf 'touch: cannot touch /home/test/go/pkg/mod/cache: Read-only file system\\n' >&2; exit 1",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw})
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

func TestRunCommandCallDetectsSandboxPermissionErrorFromStdoutRedirect(t *testing.T) {
	t.Parallel()

	const deniedPath = "/home/test/go/pkg/mod/cache/download/work.ctyun.cn/git/ctstack_cmp_v2/system/@v/v0.0.0.tmp"
	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{
		Stdout:   "go: writing stat cache: open " + deniedPath + ": read-only file system\n",
		ExitCode: 1,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendBwrap,
	}, err: fmt.Errorf("exit status 1")}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"command": "go build ./... 2>&1",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw})
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

func TestRunCommandCallDoesNotLabelHostPermissionErrorsAsSandboxDenied(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{
		Stderr:   "git@github.com: Permission denied (publickey).\n",
		ExitCode: 128,
		Route:    sandbox.RouteHost,
		Backend:  sandbox.BackendHost,
	}, err: fmt.Errorf("exit status 128")}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"command": "printf 'git@github.com: Permission denied (publickey).\\n' >&2; exit 128",
		"workdir": dir,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw})
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

func TestRunCommandCallAddsWindowsMSYSSSHSignalPipeHint(t *testing.T) {
	t.Parallel()

	sshFailure := `      0 [main] ssh (17912) D:\xue\Git\usr\bin\ssh.exe: *** fatal error - couldn't create signal pipe, Win32 error 5
fatal: Could not read from remote repository.`
	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{
		Stderr:   sshFailure,
		ExitCode: 128,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendWindows,
	}, err: fmt.Errorf("exit status 128")}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{"command": "go build ./..."})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v, want structured tool result", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if text, _ := payload["result"].(string); !strings.Contains(text, "couldn't create signal pipe") {
		t.Fatalf("result = %q, want original ssh diagnostic", text)
	}
	if got, _ := payload["hint_code"].(string); got != commanddiag.CodeWindowsMSYSSSHSignalPipe {
		t.Fatalf("hint_code = %q, want %q", got, commanddiag.CodeWindowsMSYSSSHSignalPipe)
	}
	if got, _ := payload["hint"].(string); !strings.Contains(got, "GIT_SSH_COMMAND=C:/Windows/System32/OpenSSH/ssh.exe") {
		t.Fatalf("hint = %q, want native OpenSSH guidance", got)
	}
	if got, _ := payload["exit_code"].(float64); got != 128 {
		t.Fatalf("exit_code = %#v, want 128", payload["exit_code"])
	}
}

func TestRunCommandCallDoesNotHintNativeOpenSSHPublicKeyFailure(t *testing.T) {
	t.Parallel()

	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{
		Stderr:   "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.\n",
		ExitCode: 128,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendWindows,
	}, err: fmt.Errorf("exit status 128")}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{"command": "git ls-remote ssh://git@github.com/openai/openai-python.git HEAD"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v, want structured tool result", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if _, ok := payload["hint_code"]; ok {
		t.Fatalf("hint_code = %#v, want absent for ordinary SSH auth failure", payload["hint_code"])
	}
	if _, ok := payload["hint"]; ok {
		t.Fatalf("hint = %#v, want absent for ordinary SSH auth failure", payload["hint"])
	}
}

func TestRunCommandCallAddsGitIndexLockSandboxHint(t *testing.T) {
	t.Parallel()

	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{
		Stderr:   "fatal: Unable to create '/workspace/.git/index.lock': Read-only file system\n",
		ExitCode: 128,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendLandlock,
	}, err: fmt.Errorf("exit status 128")}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{"command": "git add ."})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v, want structured tool result", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if got, _ := payload["hint_code"].(string); got != commanddiag.CodeGitIndexLockSandboxDenied {
		t.Fatalf("hint_code = %q, want %q", got, commanddiag.CodeGitIndexLockSandboxDenied)
	}
	if got, _ := payload["hint"].(string); got != "Git index write is blocked by sandbox permissions; retry the original Git command with sandbox_permissions=require_escalated." {
		t.Fatalf("hint = %q, want short Git index sandbox guidance", got)
	}
}

func TestRunCommandCallAddsWindowsSChannelNoCredentialsHint(t *testing.T) {
	t.Parallel()

	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{
		Stderr:   "curl: (35) schannel: AcquireCredentialsHandle failed: SEC_E_NO_CREDENTIALS (0x8009030E) - 安全包中没有可用的凭证\n",
		ExitCode: 35,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendWindows,
	}, err: fmt.Errorf("exit status 35")}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{"command": "curl.exe -I https://example.com/"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v, want structured tool result", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if got, _ := payload["hint_code"].(string); got != commanddiag.CodeWindowsSChannelCredentials {
		t.Fatalf("hint_code = %q, want %q", got, commanddiag.CodeWindowsSChannelCredentials)
	}
	if got, _ := payload["hint"].(string); !strings.Contains(got, "SChannel TLS can fail") {
		t.Fatalf("hint = %q, want SChannel guidance", got)
	}
	if got, _ := payload["retryable_with_host"].(bool); !got {
		t.Fatalf("retryable_with_host = %#v, want true", payload["retryable_with_host"])
	}
	if got, _ := payload["suggested_sandbox_permissions"].(string); got != "require_escalated" {
		t.Fatalf("suggested_sandbox_permissions = %q, want require_escalated", got)
	}
	if _, ok := payload["suggested_prefix_rule"]; ok {
		t.Fatalf("suggested_prefix_rule = %#v, want omitted for broad curl retry", payload["suggested_prefix_rule"])
	}
	if text, _ := payload["result"].(string); !strings.Contains(text, "SEC_E_NO_CREDENTIALS") {
		t.Fatalf("result = %q, want original SChannel diagnostic", text)
	}
}

func TestRunCommandCallPreservesWindowsDACLRefreshFailure(t *testing.T) {
	t.Parallel()

	denied := `impl/sandbox/windows: refresh sandbox ACLs without elevation: acl: write D:\xue\code\storage DACL: Access is denied.`
	rt := sandboxPermissionRuntime{result: sandbox.CommandResult{
		ExitCode: 0,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendWindowsElevated,
	}, err: fmt.Errorf("%s", denied)}
	runCommandTool, err := NewRunCommand(RunCommandConfig{Runtime: rt})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"command": "echo hello",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	result, err := runCommandTool.Call(context.Background(), tool.Call{Name: RunCommandToolName, Input: raw})
	if err != nil {
		t.Fatalf("Call() error = %v, want structured tool error result", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if got, _ := payload["error_code"].(string); got != string(tool.ErrorCodeSandboxDenied) {
		t.Fatalf("error_code = %q, want sandbox_denied", got)
	}
	got, _ := payload["error"].(string)
	if !strings.Contains(got, `D:\xue\code\storage`) || strings.Contains(got, "/sandbox setup") {
		t.Fatalf("error = %q, want DACL path without setup command guidance", got)
	}
}

func TestRunCommandPayloadTreatsWindowsExitSummaryAsPlainExit(t *testing.T) {
	t.Parallel()

	payload := runCommandPayload(sandbox.CommandResult{ExitCode: 1}, fmt.Errorf("process exited with code 1"))
	if got, _ := payload["result"].(string); got != "" {
		t.Fatalf("result = %q, want no synthetic Windows exit summary", got)
	}
	if _, ok := payload["error"]; ok {
		t.Fatalf("error = %#v, want no synthetic Windows exit summary", payload["error"])
	}
	if got, _ := payload["exit_code"].(int); got != 1 {
		t.Fatalf("exit_code = %#v, want 1", payload["exit_code"])
	}
}

func TestRunCommandPayloadDoesNotSynthesizeNoOutputPlaceholder(t *testing.T) {
	t.Parallel()

	payload := runCommandPayload(sandbox.CommandResult{ExitCode: 0}, nil)
	if got, ok := payload["result"]; ok {
		t.Fatalf("result = %#v, want no UI placeholder in tool payload", got)
	}
	if got, _ := payload["state"].(string); got != "completed" {
		t.Fatalf("state = %q, want completed", got)
	}
	if got, _ := payload["exit_code"].(int); got != 0 {
		t.Fatalf("exit_code = %#v, want 0", payload["exit_code"])
	}
}

func TestRunCommandPayloadPreservesInternalStdoutNewlines(t *testing.T) {
	t.Parallel()

	payload := runCommandPayload(sandbox.CommandResult{
		Stdout:   "requests 2.34.2\r\nHTTP 200\r\n",
		ExitCode: 0,
	}, nil)
	if got, _ := payload["result"].(string); got != "requests 2.34.2\r\nHTTP 200\r\n" {
		t.Fatalf("result = %q, want raw stdout newlines preserved", got)
	}
}

func TestRunCommandPayloadSeparatesStdoutAndStderrWithoutTrimming(t *testing.T) {
	t.Parallel()

	payload := runCommandPayload(sandbox.CommandResult{
		Stdout:   "ok",
		Stderr:   "warning\n",
		ExitCode: 1,
	}, fmt.Errorf("exit status 1"))
	if got, _ := payload["result"].(string); got != "ok\nwarning\n" {
		t.Fatalf("result = %q, want stdout/stderr separated without trimming", got)
	}
}

type sandboxPermissionRuntime struct {
	result     sandbox.CommandResult
	err        error
	last       *sandbox.CommandRequest
	descriptor sandbox.Descriptor
}

func (r sandboxPermissionRuntime) Describe() sandbox.Descriptor {
	if r.descriptor.Backend != "" ||
		r.descriptor.Isolation != "" ||
		r.descriptor.DefaultConstraints.Route != "" ||
		r.descriptor.DefaultConstraints.Backend != "" ||
		r.descriptor.DefaultConstraints.Permission != "" ||
		r.descriptor.DefaultConstraints.Isolation != "" ||
		r.descriptor.DefaultConstraints.Network != "" ||
		len(r.descriptor.DefaultConstraints.PathRules) > 0 {
		return sandbox.CloneDescriptor(r.descriptor)
	}
	return sandbox.Descriptor{Backend: sandbox.BackendBwrap}
}

func (r sandboxPermissionRuntime) FileSystem() sandbox.FileSystem { return nil }

func (r sandboxPermissionRuntime) FileSystemFor(sandbox.Constraints) sandbox.FileSystem {
	return nil
}

func (r sandboxPermissionRuntime) Run(_ context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	if r.last != nil {
		*r.last = sandbox.CloneRequest(req)
	}
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
