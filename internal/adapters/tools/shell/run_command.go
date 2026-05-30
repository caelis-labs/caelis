// Package shell provides core-native command execution tools.
package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

const RunCommandToolName = "run_command"

type RunCommandTool struct {
	Sandbox        sandbox.Runtime
	DefaultTimeout time.Duration
}

type runCommandInput struct {
	Command     string `json:"command"`
	CWD         string `json:"cwd,omitempty"`
	TimeoutMS   int    `json:"timeout_ms,omitempty"`
	YieldTimeMS int    `json:"yield_time_ms,omitempty"`
	Stdin       string `json:"stdin,omitempty"`
}

func NewRunCommandTool(runtime sandbox.Runtime) (*RunCommandTool, error) {
	if runtime == nil {
		return nil, errors.New("tools/shell: sandbox runtime is required")
	}
	return &RunCommandTool{Sandbox: runtime, DefaultTimeout: 30 * time.Second}, nil
}

func (t *RunCommandTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        RunCommandToolName,
		Description: "Run a shell command in the configured sandbox runtime.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
				"cwd": map[string]any{
					"type":        "string",
					"description": "Optional working directory. Relative paths resolve from the runtime workspace.",
				},
				"timeout_ms": map[string]any{
					"type":        "integer",
					"description": "Optional command timeout in milliseconds.",
				},
				"yield_time_ms": map[string]any{
					"type":        "integer",
					"description": "Start the command as an async task and wait this many milliseconds before returning.",
				},
				"stdin": map[string]any{
					"type":        "string",
					"description": "Optional initial stdin for async commands.",
				},
			},
			"required": []any{"command"},
		},
	}
}

func (t *RunCommandTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t == nil || t.Sandbox == nil {
		return tool.Result{}, errors.New("tools/shell: sandbox runtime is required")
	}
	var input runCommandInput
	if err := json.Unmarshal(call.Input, &input); err != nil {
		return tool.Result{}, err
	}
	input.Command = strings.TrimSpace(input.Command)
	if input.Command == "" {
		return tool.Result{}, errors.New("tools/shell: command is required")
	}
	timeout := t.DefaultTimeout
	if input.TimeoutMS > 0 {
		timeout = time.Duration(input.TimeoutMS) * time.Millisecond
	}
	if input.YieldTimeMS > 0 {
		return t.callAsync(ctx, call, input, timeout)
	}
	result, runErr := t.Sandbox.Run(ctx, sandbox.CommandRequest{
		Command: input.Command,
		Dir:     strings.TrimSpace(input.CWD),
		Timeout: timeout,
	})
	out := tool.Result{
		ID:      strings.TrimSpace(call.ID),
		Name:    RunCommandToolName,
		IsError: runErr != nil || result.ExitCode != 0 || strings.TrimSpace(result.Error) != "",
		Meta: map[string]any{
			"exit_code": result.ExitCode,
			"backend":   string(result.Backend),
			"route":     string(result.Route),
		},
		Content: []model.Part{model.NewTextPart(formatCommandResult(result, runErr))},
	}
	if runErr != nil && ctx.Err() != nil {
		return out, runErr
	}
	return out, nil
}

func (t *RunCommandTool) callAsync(ctx context.Context, call tool.Call, input runCommandInput, timeout time.Duration) (tool.Result, error) {
	session, err := t.Sandbox.Start(ctx, sandbox.CommandRequest{
		Command: input.Command,
		Dir:     strings.TrimSpace(input.CWD),
		Timeout: timeout,
		Stdin:   []byte(input.Stdin),
	})
	if err != nil {
		return tool.Result{}, err
	}
	return SessionResult(ctx, call, RunCommandToolName, "start", session, sandbox.OutputCursor{}, time.Duration(input.YieldTimeMS)*time.Millisecond)
}

func formatCommandResult(result sandbox.CommandResult, err error) string {
	var parts []string
	if stdout := strings.TrimRight(result.Stdout, "\n"); stdout != "" {
		parts = append(parts, "stdout:\n"+stdout)
	}
	if stderr := strings.TrimRight(result.Stderr, "\n"); stderr != "" {
		parts = append(parts, "stderr:\n"+stderr)
	}
	if result.ExitCode != 0 {
		parts = append(parts, fmt.Sprintf("exit_code: %d", result.ExitCode))
	}
	if text := strings.TrimSpace(result.Error); text != "" {
		parts = append(parts, "error: "+text)
	} else if err != nil {
		parts = append(parts, "error: "+err.Error())
	}
	if len(parts) == 0 {
		return "exit_code: 0"
	}
	return strings.Join(parts, "\n\n")
}

var _ tool.Tool = (*RunCommandTool)(nil)
