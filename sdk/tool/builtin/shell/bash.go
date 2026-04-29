package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
	"github.com/OnslaughtSnail/caelis/sdk/tool/builtin/internal/toolutil"
	"github.com/OnslaughtSnail/caelis/sdk/tool/internal/argparse"
)

const (
	BashToolName       = "BASH"
	defaultBashTimeout = 30 * time.Minute
	defaultBashIdle    = 0
)

type BashConfig struct {
	Timeout     time.Duration
	IdleTimeout time.Duration
	Runtime     sdksandbox.Runtime
}

type BashTool struct {
	cfg     BashConfig
	runtime sdksandbox.Runtime
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

func (t *BashTool) Definition() sdktool.Definition {
	return sdktool.Definition{
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
				"with_escalation": map[string]any{
					"type":        "boolean",
					"description": "Request host execution outside the sandbox. This should only be used when sandboxed execution cannot complete the task.",
				},
			},
			"required": []string{"command"},
		},
	}
}

func (t *BashTool) Call(ctx context.Context, call sdktool.Call) (sdktool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return sdktool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return sdktool.Result{}, err
	}
	command, err := argparse.String(args, "command", true)
	if err != nil {
		return sdktool.Result{}, err
	}
	workingDir, err := argparse.String(args, "workdir", false)
	if err != nil {
		return sdktool.Result{}, err
	}
	if strings.TrimSpace(workingDir) == "" && t.runtime != nil && t.runtime.FileSystem() != nil {
		workingDir, _ = t.runtime.FileSystem().Getwd()
	}
	if _, err := argparse.Int(args, "yield_time_ms", 0); err != nil {
		return sdktool.Result{}, err
	}
	if _, err := argparse.Bool(args, "with_escalation", false); err != nil {
		return sdktool.Result{}, err
	}
	timeoutMS, err := argparse.Int(args, "timeout_ms", int(t.cfg.Timeout/time.Millisecond))
	if err != nil {
		return sdktool.Result{}, err
	}

	var (
		result sdksandbox.CommandResult
	)
	if constraints, ok := constraintsFromMetadata(call.Metadata); ok {
		req := sdksandbox.CommandRequest{
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
		result, err = t.runtime.Run(ctx, sdksandbox.CommandRequest{
			Command:   command,
			Dir:       workingDir,
			Timeout:   time.Duration(timeoutMS) * time.Millisecond,
			RouteHint: sdksandbox.RouteSandbox,
			Constraints: sdksandbox.Constraints{
				Route:      sdksandbox.RouteSandbox,
				Permission: sdksandbox.PermissionWorkspaceWrite,
				Network:    sdksandbox.NetworkInherit,
			},
		})
	}
	if err != nil {
		payload := map[string]any{
			"stdout":    result.Stdout,
			"stderr":    result.Stderr,
			"exit_code": result.ExitCode,
			"route":     result.Route,
			"backend":   result.Backend,
			"error":     err.Error(),
		}
		if detail, ok := sdksandbox.SandboxPermissionDetail(result, err); ok {
			payload["sandbox_permission_denied"] = true
			payload["error"] = detail
		}
		return toolutil.JSONErrorResult(BashToolName, payload)
	}
	return toolutil.JSONResult(BashToolName, map[string]any{
		"stdout":    result.Stdout,
		"stderr":    result.Stderr,
		"exit_code": result.ExitCode,
		"route":     result.Route,
		"backend":   result.Backend,
	})
}

func (t *BashTool) SandboxRuntime() sdksandbox.Runtime {
	return t.runtime
}

func runtimeOrDefault(runtime sdksandbox.Runtime) (sdksandbox.Runtime, error) {
	if runtime == nil {
		return nil, fmt.Errorf("tool: sandbox runtime is required")
	}
	return runtime, nil
}

var _ sdktool.Tool = (*BashTool)(nil)

func constraintsFromMetadata(meta map[string]any) (sdksandbox.Constraints, bool) {
	if meta == nil {
		return sdksandbox.Constraints{}, false
	}
	raw, ok := meta["sandbox_constraints"]
	if !ok || raw == nil {
		return sdksandbox.Constraints{}, false
	}
	if typed, ok := raw.(sdksandbox.Constraints); ok {
		return sdksandbox.NormalizeConstraints(typed), true
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return sdksandbox.Constraints{}, false
	}
	var out sdksandbox.Constraints
	if err := json.Unmarshal(bytes, &out); err != nil {
		return sdksandbox.Constraints{}, false
	}
	return sdksandbox.NormalizeConstraints(out), true
}
