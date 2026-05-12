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
		Description: "Run a shell command. Use it for commands that are simpler in the shell than via file tools.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "shell command to execute"},
				"workdir": map[string]any{"type": "string", "description": "Optional working directory."},
				"yield_time_ms": map[string]any{
					"type":        "integer",
					"description": "Optional wait window before control returns when runtime-backed async execution is available.",
				},
				"sandbox_permissions": map[string]any{
					"type":        "string",
					"description": "Sandbox permissions for this command. Omit for default sandboxed execution. Use \"with_additional_permissions\" for a narrow sandboxed filesystem or network grant, or \"require_escalated\" when this command must run outside the sandbox.",
					"enum":        []string{"use_default", "with_additional_permissions", "require_escalated"},
				},
				"additional_permissions": map[string]any{
					"type":        "object",
					"description": "Only set when sandbox_permissions is \"with_additional_permissions\". Requests extra permissions while keeping execution inside the sandbox.",
					"properties": map[string]any{
						"network": map[string]any{
							"type":        "object",
							"description": "Optional network permission overlay.",
							"properties": map[string]any{
								"enabled": map[string]any{"type": "boolean", "description": "Set to true to request network access."},
							},
							"additionalProperties": false,
						},
						"file_system": map[string]any{
							"type":        "object",
							"description": "Optional filesystem permission overlay.",
							"properties": map[string]any{
								"read":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Paths to grant read access to."},
								"write": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Paths to grant write access to."},
							},
							"additionalProperties": false,
						},
					},
					"additionalProperties": false,
				},
				"justification": map[string]any{
					"type":        "string",
					"description": "Only set when sandbox_permissions is \"require_escalated\". A short user-facing approval question explaining why host execution is needed.",
				},
			},
			"required": []string{"command"},
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
				Network:    sandbox.NetworkInherit,
			},
		})
	}
	payload := bashCommandPayload(result, err)
	out, resultErr := toolutil.JSONResult(BashToolName, payload)
	if out.Meta == nil {
		out.Meta = map[string]any{}
	}
	if result.Route != "" {
		out.Meta["route"] = result.Route
	}
	if result.Backend != "" {
		out.Meta["backend"] = result.Backend
	}
	if err != nil {
		if detail, ok := sandbox.SandboxPermissionDetail(result, err); ok {
			out.Meta["error"] = detail
		} else {
			out.Meta["error"] = strings.TrimSpace(err.Error())
		}
	}
	return out, resultErr
}

func (t *BashTool) SandboxRuntime() sandbox.Runtime {
	return t.runtime
}

func bashCommandPayload(result sandbox.CommandResult, err error) map[string]any {
	stdout := result.Stdout
	stderr := result.Stderr
	exitCode := result.ExitCode
	if err != nil && strings.TrimSpace(stdout) == "" && strings.TrimSpace(stderr) == "" {
		stderr = strings.TrimSpace(err.Error())
		if exitCode == 0 {
			exitCode = -1
		}
	}
	payload := map[string]any{
		"stdout":    stdout,
		"stderr":    stderr,
		"exit_code": exitCode,
	}
	if err != nil {
		if detail, ok := sandbox.SandboxPermissionDetail(result, err); ok {
			payload["error"] = detail
		}
	}
	return payload
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
