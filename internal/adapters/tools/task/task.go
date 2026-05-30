// Package task provides core-native control for yielded sandbox sessions.
package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
	toolshell "github.com/OnslaughtSnail/caelis/internal/adapters/tools/shell"
)

const ToolName = "task"

type Tool struct {
	Sandbox     sandbox.Runtime
	DefaultWait time.Duration
}

type input struct {
	Action       string `json:"action"`
	TaskID       string `json:"task_id"`
	Input        string `json:"input,omitempty"`
	YieldTimeMS  int    `json:"yield_time_ms,omitempty"`
	StdoutCursor int64  `json:"stdout_cursor,omitempty"`
	StderrCursor int64  `json:"stderr_cursor,omitempty"`
}

func New(runtime sandbox.Runtime) (*Tool, error) {
	if runtime == nil {
		return nil, errors.New("tools/task: sandbox runtime is required")
	}
	return &Tool{Sandbox: runtime, DefaultWait: time.Second}, nil
}

func (t *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		Description: "Wait, write stdin to, or cancel a yielded sandbox task.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []any{"wait", "write", "cancel"},
					"description": "Task action.",
				},
				"task_id": map[string]any{
					"type":        "string",
					"description": "Task handle returned by run_command.",
				},
				"input": map[string]any{
					"type":        "string",
					"description": "Input bytes for action=write.",
				},
				"yield_time_ms": map[string]any{
					"type":        "integer",
					"description": "Maximum milliseconds to wait before returning current state.",
				},
				"stdout_cursor": map[string]any{
					"type":        "integer",
					"description": "Previously returned stdout cursor.",
				},
				"stderr_cursor": map[string]any{
					"type":        "integer",
					"description": "Previously returned stderr cursor.",
				},
			},
			"required":             []any{"action", "task_id"},
			"additionalProperties": false,
		},
		Meta: map[string]any{
			"caelis.kind": "task",
		},
	}
}

func (t *Tool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t == nil || t.Sandbox == nil {
		return tool.Result{}, errors.New("tools/task: sandbox runtime is required")
	}
	var in input
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return tool.Result{}, fmt.Errorf("tools/task: invalid json input: %w", err)
		}
	}
	in.Action = strings.ToLower(strings.TrimSpace(in.Action))
	in.TaskID = strings.TrimSpace(in.TaskID)
	if in.TaskID == "" {
		return tool.Result{}, errors.New("tools/task: task_id is required")
	}
	session, err := t.Sandbox.Open(ctx, sandbox.SessionRef{ID: in.TaskID})
	if err != nil {
		return tool.Result{}, err
	}
	switch in.Action {
	case "wait":
	case "write":
		if err := session.Write(ctx, []byte(in.Input)); err != nil {
			return tool.Result{}, err
		}
	case "cancel":
		if err := session.Cancel(ctx); err != nil {
			return tool.Result{}, err
		}
	default:
		return tool.Result{}, fmt.Errorf("tools/task: unsupported action %q", in.Action)
	}
	wait := t.DefaultWait
	if in.YieldTimeMS > 0 {
		wait = time.Duration(in.YieldTimeMS) * time.Millisecond
	}
	if in.Action == "cancel" {
		wait = 100 * time.Millisecond
	}
	return toolshell.SessionResult(ctx, call, ToolName, in.Action, session, sandbox.OutputCursor{
		Stdout: in.StdoutCursor,
		Stderr: in.StderrCursor,
	}, wait)
}

var _ tool.Tool = (*Tool)(nil)
