package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/internal/toolutil"
	"github.com/OnslaughtSnail/caelis/impl/tool/internal/argparse"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const (
	BashToolName       = "BASH"
	defaultBashTimeout = 30 * time.Minute
	defaultBashIdle    = 0
)

type BashConfig struct {
	Timeout     time.Duration
	IdleTimeout time.Duration
	Runtime     sandbox.Runtime
}

type BashTool struct {
	cfg     BashConfig
	runtime sandbox.Runtime
}

func NewBash(cfg BashConfig) (*BashTool, error) {
	resolvedRuntime, err := runtimeOrDefault(cfg.Runtime)
	if err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultBashTimeout
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultBashIdle
	}
	return &BashTool{cfg: cfg, runtime: resolvedRuntime}, nil
}

func (t *BashTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        BashToolName,
		Description: "Run a shell command.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "Command to execute."},
				"workdir": map[string]any{"type": "string", "description": "Working directory."},
				"yield_time_ms": map[string]any{
					"type":        "integer",
					"description": "Wait before yielding async control.",
				},
				"sandbox_permissions": map[string]any{
					"type":        "string",
					"description": "Sandbox mode for this command.",
					"enum":        []string{"use_default", "with_additional_permissions", "require_escalated"},
				},
				"additional_permissions": map[string]any{
					"type":        "object",
					"description": "Extra sandbox grants for with_additional_permissions.",
					"properties": map[string]any{
						"network": map[string]any{
							"type":        "object",
							"description": "Network grant.",
							"properties": map[string]any{
								"enabled": map[string]any{"type": "boolean", "description": "Allow network."},
							},
							"additionalProperties": false,
						},
						"file_system": map[string]any{
							"type":        "object",
							"description": "Filesystem grants.",
							"properties": map[string]any{
								"read":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Readable paths."},
								"write": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Writable paths."},
							},
							"additionalProperties": false,
						},
					},
					"additionalProperties": false,
				},
				"justification": map[string]any{
					"type":        "string",
					"description": "Short approval question for require_escalated.",
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
	}
}

func (t *BashTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
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
	if strings.TrimSpace(workingDir) == "" && t.runtime != nil && t.runtime.FileSystem() != nil {
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
	timeoutMS, err := argparse.Int(args, "timeout_ms", int(t.cfg.Timeout/time.Millisecond))
	if err != nil {
		return tool.Result{}, err
	}

	var (
		result sandbox.CommandResult
	)
	if constraints, ok := constraintsFromMetadata(call.Metadata); ok {
		req := sandbox.CommandRequest{
			Command:     command,
			Dir:         workingDir,
			Timeout:     time.Duration(timeoutMS) * time.Millisecond,
			RouteHint:   constraints.Route,
			Backend:     constraints.Backend,
			Permission:  constraints.Permission,
			Constraints: constraints,
		}
		result, err = t.runtime.Run(ctx, req)
	} else {
		result, err = t.runtime.Run(ctx, sandbox.CommandRequest{
			Command:   command,
			Dir:       workingDir,
			Timeout:   time.Duration(timeoutMS) * time.Millisecond,
			RouteHint: sandbox.RouteSandbox,
			Constraints: sandbox.Constraints{
				Route:      sandbox.RouteSandbox,
				Permission: sandbox.PermissionWorkspaceWrite,
				Network:    sandbox.NetworkDisabled,
			},
		})
	}
	payload := bashCommandPayload(result, err)
	out, resultErr := toolutil.JSONResult(BashToolName, payload)
	if result.Route != "" {
		if out.Metadata == nil {
			out.Metadata = map[string]any{}
		}
		bashToolMetadata(out.Metadata)["route"] = result.Route
	}
	if result.Backend != "" {
		if out.Metadata == nil {
			out.Metadata = map[string]any{}
		}
		bashToolMetadata(out.Metadata)["backend"] = result.Backend
	}
	return out, resultErr
}

func bashToolMetadata(meta map[string]any) map[string]any {
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

func (t *BashTool) SandboxRuntime() sandbox.Runtime {
	return t.runtime
}

func bashCommandPayload(result sandbox.CommandResult, err error) map[string]any {
	merged := bashMergedOutput(result.Stdout, result.Stderr)
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
		} else if strings.TrimSpace(merged) == "" && !plainCommandExitError(err) {
			payload["error"] = strings.TrimSpace(err.Error())
		}
	}
	if strings.TrimSpace(merged) != "" {
		payload["result"] = strings.TrimSpace(merged)
	} else if err == nil {
		payload["result"] = "(no output)"
	}
	if commandExitCodeAvailable(result.ExitCode, err) {
		payload["exit_code"] = result.ExitCode
	}
	return payload
}

func bashMergedOutput(stdout string, stderr string) string {
	stdout = strings.TrimRight(stdout, "\r\n")
	stderr = strings.TrimRight(stderr, "\r\n")
	switch {
	case stdout != "" && stderr != "":
		return stdout + "\n" + stderr
	case stdout != "":
		return stdout
	case stderr != "":
		return stderr
	default:
		return ""
	}
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
	return strings.HasPrefix(text, "exit status ") || strings.HasPrefix(text, "signal: ")
}

func runtimeOrDefault(runtime sandbox.Runtime) (sandbox.Runtime, error) {
	if runtime == nil {
		return nil, fmt.Errorf("tool: sandbox runtime is required")
	}
	return runtime, nil
}

var _ tool.Tool = (*BashTool)(nil)

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
