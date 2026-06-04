package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/internal/toolutil"
	"github.com/OnslaughtSnail/caelis/impl/tool/internal/argparse"
	"github.com/OnslaughtSnail/caelis/internal/commanddiag"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const (
	RunCommandToolName       = "RUN_COMMAND"
	defaultRunCommandTimeout = 30 * time.Minute
	defaultRunCommandIdle    = 0
)

type RunCommandConfig struct {
	Timeout     time.Duration
	IdleTimeout time.Duration
	Runtime     sandbox.Runtime
}

type RunCommandTool struct {
	cfg     RunCommandConfig
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
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultRunCommandIdle
	}
	return &RunCommandTool{cfg: cfg, runtime: resolvedRuntime}, nil
}

func (t *RunCommandTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        RunCommandToolName,
		Description: "Run a shell command from the session workspace or a specified workdir. Use this for repository inspection, tests, builds, formatting checks, git status/diff inspection, and commands that cannot be expressed by file tools. Do not prefix with cd; set workdir instead. Use yield_time_ms for long-running commands and sandbox_permissions=require_escalated only for the specific operation that needs escalation.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Command to execute.",
				},
				"workdir": map[string]any{
					"type":        "string",
					"description": "Working directory for the command; defaults to the session cwd.",
				},
				"yield_time_ms": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"description": "Wait before yielding async control.",
				},
				"sandbox_permissions": map[string]any{
					"type":        "string",
					"description": "Sandbox mode for this command.",
					"enum":        []string{"use_default", "require_escalated"},
				},
				"justification": map[string]any{
					"type":        "string",
					"description": "Short approval question for require_escalated.",
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
		Metadata: toolutil.AnnotationMetadata(false, true, false, true),
	}
}

func (t *RunCommandTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return tool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
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
	if _, err := argparse.String(args, "sandbox_permissions", false); err != nil {
		return tool.Result{}, err
	}
	if _, err := argparse.String(args, "justification", false); err != nil {
		return tool.Result{}, err
	}
	var (
		result sandbox.CommandResult
	)
	if constraints, ok := constraintsFromMetadata(call.Metadata); ok {
		req := sandbox.CommandRequest{
			Command:     command,
			Dir:         workingDir,
			Timeout:     t.cfg.Timeout,
			RouteHint:   constraints.Route,
			Backend:     constraints.Backend,
			Permission:  constraints.Permission,
			Constraints: constraints,
		}
		result, err = t.runtime.Run(ctx, req)
	} else {
		constraints := defaultRunCommandConstraints(t.runtime)
		result, err = t.runtime.Run(ctx, sandbox.CommandRequest{
			Command:     command,
			Dir:         workingDir,
			Timeout:     t.cfg.Timeout,
			RouteHint:   constraints.Route,
			Backend:     constraints.Backend,
			Permission:  constraints.Permission,
			Constraints: constraints,
		})
	}
	payload := runCommandPayloadForCommand(command, result, err)
	out, resultErr := toolutil.JSONResult(RunCommandToolName, payload)
	if result.Route != "" {
		if out.Metadata == nil {
			out.Metadata = map[string]any{}
		}
		runCommandToolMetadata(out.Metadata)["route"] = result.Route
	}
	if result.Backend != "" {
		if out.Metadata == nil {
			out.Metadata = map[string]any{}
		}
		runCommandToolMetadata(out.Metadata)["backend"] = result.Backend
	}
	return out, resultErr
}

func runCommandToolMetadata(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	caelis, _ := meta["caelis"].(map[string]any)
	if caelis == nil {
		caelis = map[string]any{}
		meta["caelis"] = caelis
	}
	if _, ok := caelis["version"]; !ok {
		caelis["version"] = 1
	}
	runtime, _ := caelis["runtime"].(map[string]any)
	if runtime == nil {
		runtime = map[string]any{}
		caelis["runtime"] = runtime
	}
	toolMeta, _ := runtime["tool"].(map[string]any)
	if toolMeta == nil {
		toolMeta = map[string]any{}
		runtime["tool"] = toolMeta
	}
	return toolMeta
}

func (t *RunCommandTool) SandboxRuntime() sandbox.Runtime {
	return t.runtime
}

func (t *RunCommandTool) CommandTimeout() time.Duration {
	if t == nil {
		return 0
	}
	return t.cfg.Timeout
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
	if constraints.Route == "" {
		constraints.Route = sandbox.RouteSandbox
	}
	if constraints.Permission == "" {
		constraints.Permission = sandbox.PermissionWorkspaceWrite
	}
	if constraints.Network == "" {
		constraints.Network = sandbox.NetworkEnabled
	}
	return constraints
}

func runCommandPayload(result sandbox.CommandResult, err error) map[string]any {
	return runCommandPayloadForCommand("", result, err)
}

func runCommandPayloadForCommand(command string, result sandbox.CommandResult, err error) map[string]any {
	merged := runCommandMergedOutput(result.Stdout, result.Stderr)
	payload := map[string]any{}
	if err != nil || result.ExitCode != 0 {
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
	if diag, ok := commanddiag.Best(commanddiag.Input{
		ToolName: RunCommandToolName,
		Command:  command,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Error:    firstNonEmptyString(result.Error, errorString(err)),
		ExitCode: result.ExitCode,
		Route:    result.Route,
		Backend:  result.Backend,
	}); ok {
		payload["hint_code"] = diag.Code
		payload["hint"] = diag.Hint
		if strings.TrimSpace(diag.Severity) != "" {
			payload["hint_severity"] = diag.Severity
		}
	}
	return payload
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
		return nil, fmt.Errorf("tool: sandbox runtime is required")
	}
	return runtime, nil
}

var _ tool.Tool = (*RunCommandTool)(nil)

func constraintsFromMetadata(meta map[string]any) (sandbox.Constraints, bool) {
	if meta == nil {
		return sandbox.Constraints{}, false
	}
	raw, ok := meta["sandbox_constraints"]
	if !ok || raw == nil {
		return sandbox.Constraints{}, false
	}
	if typed, ok := raw.(sandbox.Constraints); ok {
		return sandbox.NormalizeConstraints(typed), true
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return sandbox.Constraints{}, false
	}
	var out sandbox.Constraints
	if err := json.Unmarshal(bytes, &out); err != nil {
		return sandbox.Constraints{}, false
	}
	return sandbox.NormalizeConstraints(out), true
}
