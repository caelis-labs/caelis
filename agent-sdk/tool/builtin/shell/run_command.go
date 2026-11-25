package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/internal/sandboxmeta"
	"github.com/caelis-labs/caelis/agent-sdk/internal/toolpresentation"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/argparse"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
	"github.com/caelis-labs/caelis/agent-sdk/tool/commanddiag"
)

const (
	RunCommandToolName       = "RunCommand"
	defaultRunCommandTimeout = 30 * time.Minute
)

var runCommandAllowedArgs = []string{"command", "workdir", "yield_time_ms", "tty", "sandbox_permissions", "justification"}

// ValidateRunCommandArgs validates command options for the Runtime wrapper,
// which accepts the legacy spelling for default execution.
func ValidateRunCommandArgs(args map[string]any) error {
	_, err := validateRunCommandArgs(args, true)
	return err
}

func validateRunCommandArgs(args map[string]any, allowLegacy bool) (bool, error) {
	if err := tool.RejectUnknownArgs(args, runCommandAllowedArgs...); err != nil {
		return false, err
	}
	value, err := argparse.String(args, "sandbox_permissions", false)
	if err != nil {
		return false, tool.NewError(tool.ErrorCodeInvalidInput, err.Error())
	}
	permission, err := tool.NormalizeCommandSandboxPermission(value, allowLegacy)
	if err != nil {
		return false, err
	}
	justification, err := argparse.String(args, "justification", false)
	if err != nil {
		return false, tool.NewError(tool.ErrorCodeInvalidInput, err.Error())
	}
	tty, err := argparse.Bool(args, "tty", false)
	if err != nil {
		return false, err
	}
	if permission == tool.CommandSandboxPermissionRequireEscalated && strings.TrimSpace(justification) == "" {
		return false, tool.NewError(tool.ErrorCodeInvalidInput, "arg \"justification\" is required when sandbox_permissions is require_escalated")
	}
	return tty, nil
}

type RunCommandConfig struct {
	Timeout time.Duration
	Runtime sandbox.Runtime
}

type RunCommandTool struct {
	timeout time.Duration
	runtime sandbox.Runtime
}

func NewRunCommand(cfg RunCommandConfig) (*RunCommandTool, error) {
	resolvedRuntime, err := runtimeOrDefault(cfg.Runtime)
	if err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultRunCommandTimeout
	}
	return &RunCommandTool{timeout: cfg.Timeout, runtime: resolvedRuntime}, nil
}

func (t *RunCommandTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        RunCommandToolName,
		Description: "Run a shell command for repository inspection, tests, builds, formatting, version-control operations, or work unavailable through file tools. Use it when the command may complete now or continue as an async Task; prefer dedicated file tools for file operations.",
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"handle": map[string]any{"type": "string"}, "state": map[string]any{"type": "string"}, "result": map[string]any{"type": "string"}, "stdout": map[string]any{"type": "string"}, "stderr": map[string]any{"type": "string"}, "exit_code": map[string]any{"type": []string{"integer", "null"}},
		}},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Shell command to execute.",
				},
				"workdir": map[string]any{
					"type":        "string",
					"description": "Working directory; defaults to the session cwd. Set this instead of prefixing command with cd.",
				},
				"yield_time_ms": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"description": "Wait before yielding an async Task; not the command timeout. 10000 ms default. Use shorter (1000 ms) for servers/watchers, longer for finite jobs. waiting_approval means not started: keep the handle and yield control.",
				},
				"tty": map[string]any{
					"type":        "boolean",
					"default":     false,
					"description": "Allocate a terminal for interactive commands. Leave false for ordinary commands so stdin is closed and output streams remain separate.",
				},
				"sandbox_permissions": map[string]any{
					"type":        "string",
					"description": "Execution route. Use use_default when the trusted runtime boundary permits the command or is uncertain. Use require_escalated directly when it proves Host is required, or once after a matching sandbox denial; approval is one-shot.",
					"enum":        []string{"use_default", "require_escalated"},
				},
				"justification": map[string]any{
					"type":        "string",
					"description": "Required for require_escalated: one short sentence with command intent, the trusted boundary or matching denial, and task relevance.",
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
			"allOf": []any{map[string]any{
				"if": map[string]any{
					"properties": map[string]any{
						"sandbox_permissions": map[string]any{"const": tool.CommandSandboxPermissionRequireEscalated},
					},
					"required": []string{"sandbox_permissions"},
				},
				"then": map[string]any{"required": []string{"justification"}},
			}},
		},
	}
}

func (t *RunCommandTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	tty, err := validateRunCommandArgs(args, false)
	if err != nil {
		return tool.Result{}, err
	}
	command, err := argparse.String(args, "command", true)
	if err != nil {
		return tool.Result{}, err
	}
	workingDir, err := argparse.String(args, "workdir", false)
	if err != nil {
		return tool.Result{}, err
	}
	if workingDir == "" && t.runtime != nil && t.runtime.FileSystem() != nil {
		workingDir, _ = t.runtime.FileSystem().Getwd()
	}
	if _, err := argparse.Int(args, "yield_time_ms", 0); err != nil {
		return tool.Result{}, err
	}
	var (
		result sandbox.CommandResult
	)
	if constraints, ok, _ := sandboxmeta.DecodeConstraints(call.Metadata); ok {
		req := sandbox.CommandRequest{
			Command:     command,
			Dir:         workingDir,
			Timeout:     t.timeout,
			TTY:         tty,
			Constraints: constraints,
		}
		result, err = t.runtime.Run(ctx, req)
	} else {
		constraints := defaultRunCommandConstraints(t.runtime)
		result, err = t.runtime.Run(ctx, sandbox.CommandRequest{
			Command:     command,
			Dir:         workingDir,
			Timeout:     t.timeout,
			TTY:         tty,
			Constraints: constraints,
		})
	}
	payload := runCommandPayloadForCommand(command, result, err)
	toolpresentation.Command(ctx, string(result.Route), string(result.Backend))
	return toolutil.JSONResult(payload)
}

func (t *RunCommandTool) SandboxRuntime() sandbox.Runtime {
	return t.runtime
}

func (t *RunCommandTool) CommandTimeout() time.Duration {
	if t == nil {
		return 0
	}
	return t.timeout
}

func defaultRunCommandConstraints(runtime sandbox.Runtime) sandbox.Constraints {
	constraints := sandbox.Constraints{
		Route:      sandbox.RouteSandbox,
		Permission: sandbox.PermissionWorkspaceWrite,
		Network:    sandbox.NetworkEnabled,
	}
	if runtime != nil {
		defaults := sandbox.NormalizeConstraints(runtime.Describe().DefaultConstraints)
		if defaults.Route != "" {
			constraints.Route = defaults.Route
		}
		if defaults.Backend != "" {
			constraints.Backend = defaults.Backend
		}
		if defaults.Permission != "" {
			constraints.Permission = defaults.Permission
		}
		if defaults.Isolation != "" {
			constraints.Isolation = defaults.Isolation
		}
		if defaults.Network != "" {
			constraints.Network = defaults.Network
		}
		if len(defaults.PathRules) > 0 {
			constraints.PathRules = defaults.PathRules
		}
	}
	return constraints
}

func runCommandPayloadForCommand(command string, result sandbox.CommandResult, err error) map[string]any {
	merged := runCommandMergedOutput(result.Stdout, result.Stderr)
	payload := map[string]any{}
	if commandResultCancelled(result, err) {
		payload["state"] = "cancelled"
	} else if err != nil || result.ExitCode != 0 {
		payload["state"] = "failed"
	} else {
		payload["state"] = "completed"
	}
	if err != nil {
		if detail, ok := sandbox.SandboxPermissionDetail(result, err); ok {
			payload["error"] = detail
			payload["error_code"] = string(tool.ErrorCodeSandboxDenied)
		} else if !commandOutputHasNonBlankLine(merged) && !plainCommandExitError(err) {
			payload["error"] = strings.TrimSpace(err.Error())
		}
	}
	if commandOutputHasNonBlankLine(merged) {
		payload["result"] = merged
	}
	if commandExitCodeAvailable(result.ExitCode, err) {
		payload["exit_code"] = result.ExitCode
	}
	if hint := commanddiag.Hint(commanddiag.Input{
		Command:  command,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Error:    firstNonEmptyString(result.Error, errorString(err)),
		ExitCode: result.ExitCode,
		Route:    result.Route,
		Backend:  result.Backend,
	}); hint != "" {
		payload["system_hint"] = hint
	}
	return payload
}

func commandResultCancelled(result sandbox.CommandResult, err error) bool {
	return result.ExitCode < 0 && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func runCommandMergedOutput(stdout string, stderr string) string {
	switch {
	case stdout != "" && stderr != "":
		return joinCommandOutputStreams(stdout, stderr)
	case stdout != "":
		return stdout
	case stderr != "":
		return stderr
	default:
		return ""
	}
}

func joinCommandOutputStreams(stdout string, stderr string) string {
	if stdout == "" || stderr == "" {
		return stdout + stderr
	}
	if strings.HasSuffix(stdout, "\n") || strings.HasSuffix(stdout, "\r") ||
		strings.HasPrefix(stderr, "\n") || strings.HasPrefix(stderr, "\r") {
		return stdout + stderr
	}
	return stdout + "\n" + stderr
}

func commandOutputHasNonBlankLine(text string) bool {
	for _, line := range splitCommandOutputLines(text) {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

func splitCommandOutputLines(text string) []string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	return strings.Split(text, "\n")
}

func commandExitCodeAvailable(exitCode int, err error) bool {
	if exitCode < 0 {
		return false
	}
	if err != nil && exitCode == 0 && !plainCommandExitError(err) {
		return false
	}
	return true
}

func plainCommandExitError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.TrimSpace(err.Error())
	return strings.HasPrefix(text, "exit status ") ||
		strings.HasPrefix(text, "signal: ") ||
		strings.HasPrefix(text, "process exited with code ")
}

func runtimeOrDefault(runtime sandbox.Runtime) (sandbox.Runtime, error) {
	if runtime == nil {
		return nil, fmt.Errorf("sandbox runtime is required")
	}
	return runtime, nil
}

var _ tool.Tool = (*RunCommandTool)(nil)
