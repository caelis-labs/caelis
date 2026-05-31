// Package task provides core-native control for yielded sandbox sessions.
package task

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
	toolshell "github.com/OnslaughtSnail/caelis/internal/adapters/tools/shell"
)

const ToolName = "task"

type Tool struct {
	Sandbox     sandbox.Runtime
	Resolver    Resolver
	DefaultWait time.Duration
}

type Resolver interface {
	OpenTask(context.Context, sandbox.SessionRef) (sandbox.Session, bool, error)
	ListTasks(context.Context, sandbox.SessionListQuery) ([]sandbox.SessionSnapshot, error)
}

type input struct {
	Action       string `json:"action"`
	TaskID       string `json:"task_id"`
	Input        string `json:"input,omitempty"`
	YieldTimeMS  int    `json:"yield_time_ms,omitempty"`
	StdoutCursor int64  `json:"stdout_cursor,omitempty"`
	StderrCursor int64  `json:"stderr_cursor,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

func New(runtime sandbox.Runtime) (*Tool, error) {
	return NewWithResolver(runtime, nil)
}

func NewWithResolver(runtime sandbox.Runtime, resolver Resolver) (*Tool, error) {
	if runtime == nil && resolver == nil {
		return nil, errors.New("tools/task: task runtime is required")
	}
	return &Tool{Sandbox: runtime, Resolver: resolver, DefaultWait: time.Second}, nil
}

func (t *Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		Description: "List, tail, wait, write stdin to, or cancel yielded runtime tasks.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []any{"wait", "write", "cancel", "tail", "list"},
					"description": "Task action.",
				},
				"task_id": map[string]any{
					"type":        "string",
					"description": "Task handle returned by run_command. Required except for action=list.",
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
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum tasks to return for action=list.",
				},
			},
			"required":             []any{"action"},
			"additionalProperties": false,
		},
		Meta: map[string]any{
			"caelis.kind": "task",
		},
	}
}

func (t *Tool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t == nil || (t.Sandbox == nil && t.Resolver == nil) {
		return tool.Result{}, errors.New("tools/task: task runtime is required")
	}
	var in input
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return tool.Result{}, fmt.Errorf("tools/task: invalid json input: %w", err)
		}
	}
	in.Action = strings.ToLower(strings.TrimSpace(in.Action))
	in.TaskID = strings.TrimSpace(in.TaskID)
	if in.Action == "list" {
		return t.list(ctx, call, in)
	}
	if in.TaskID == "" {
		return tool.Result{}, errors.New("tools/task: task_id is required")
	}
	session, err := t.open(ctx, sandbox.SessionRef{ID: in.TaskID})
	if err != nil {
		return tool.Result{}, err
	}
	switch in.Action {
	case "wait":
	case "tail":
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
	if in.Action == "tail" {
		wait = 0
	}
	if in.Action == "cancel" {
		wait = 100 * time.Millisecond
	}
	return toolshell.SessionResult(ctx, call, ToolName, in.Action, session, sandbox.OutputCursor{
		Stdout: in.StdoutCursor,
		Stderr: in.StderrCursor,
	}, wait)
}

func (t *Tool) list(ctx context.Context, call tool.Call, in input) (tool.Result, error) {
	var snapshots []sandbox.SessionSnapshot
	seen := map[string]struct{}{}
	lister, ok := t.Sandbox.(sandbox.SessionLister)
	if ok {
		listed, err := lister.ListSessions(ctx, sandbox.SessionListQuery{Limit: in.Limit})
		if err != nil {
			return tool.Result{}, err
		}
		for _, snapshot := range listed {
			taskID := strings.TrimSpace(snapshot.Ref.ID)
			if taskID == "" {
				continue
			}
			seen[taskID] = struct{}{}
			snapshots = append(snapshots, snapshot)
		}
	}
	if t.Resolver != nil {
		listed, err := t.Resolver.ListTasks(ctx, sandbox.SessionListQuery{Limit: in.Limit})
		if err != nil {
			return tool.Result{}, err
		}
		for _, snapshot := range listed {
			taskID := strings.TrimSpace(snapshot.Ref.ID)
			if taskID == "" {
				continue
			}
			if _, ok := seen[taskID]; ok {
				continue
			}
			seen[taskID] = struct{}{}
			snapshots = append(snapshots, snapshot)
		}
	}
	if !ok && t.Resolver == nil {
		return tool.Result{}, errors.New("tools/task: sandbox runtime does not support task listing")
	}
	if in.Limit > 0 && len(snapshots) > in.Limit {
		snapshots = snapshots[:in.Limit]
	}
	tasks := make([]map[string]any, 0, len(snapshots))
	for _, snapshot := range snapshots {
		task := map[string]any{
			"task_id":        strings.TrimSpace(snapshot.Ref.ID),
			"backend":        string(snapshot.Ref.Backend),
			"state":          string(snapshot.State),
			"running":        snapshot.Running,
			"supports_input": snapshot.SupportsInput,
			"terminal_id":    strings.TrimSpace(snapshot.Terminal.ID),
			"command":        strings.TrimSpace(snapshot.Command),
			"cwd":            strings.TrimSpace(snapshot.Dir),
			"updated_at":     snapshot.UpdatedAt,
		}
		if !snapshot.Running {
			task["exit_code"] = snapshot.ExitCode
		}
		if errText := strings.TrimSpace(snapshot.Error); errText != "" {
			task["error"] = errText
		}
		if snapshot.OutputPreview != nil {
			for key, value := range tool.RuntimeTaskPreview(
				snapshot.OutputPreview.Stdout,
				snapshot.OutputPreview.Stderr,
				snapshot.OutputPreview.StdoutDroppedBytes,
				snapshot.OutputPreview.StderrDroppedBytes,
				snapshot.OutputPreview.Cursor.Stdout,
				snapshot.OutputPreview.Cursor.Stderr,
			) {
				task[key] = value
			}
		}
		for key, value := range snapshot.Metadata {
			key = strings.TrimSpace(key)
			if key != "" {
				task[key] = value
			}
		}
		tasks = append(tasks, task)
	}
	payload := map[string]any{
		"action": "list",
		"count":  len(tasks),
		"tasks":  tasks,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{
		ID:   strings.TrimSpace(call.ID),
		Name: ToolName,
		Content: []model.Part{
			model.NewTextPart(taskListSummary(tasks)),
			{
				Kind: model.PartJSON,
				JSON: &model.JSONPart{Value: raw},
			},
		},
		Meta: map[string]any{
			"task_count": len(tasks),
			"caelis": map[string]any{
				"version": 1,
				"runtime": map[string]any{
					"task": map[string]any{
						"action": "list",
						"count":  len(tasks),
					},
				},
			},
		},
	}, nil
}

func (t *Tool) open(ctx context.Context, ref sandbox.SessionRef) (sandbox.Session, error) {
	var sandboxErr error
	if t.Sandbox != nil {
		session, err := t.Sandbox.Open(ctx, ref)
		if err == nil {
			return session, nil
		}
		sandboxErr = err
	}
	if t.Resolver != nil {
		session, ok, err := t.Resolver.OpenTask(ctx, ref)
		if err != nil {
			return nil, err
		}
		if ok {
			return session, nil
		}
	}
	if sandboxErr != nil {
		return nil, sandboxErr
	}
	return nil, fmt.Errorf("tools/task: task %q not found", strings.TrimSpace(ref.ID))
}

func taskListSummary(tasks []map[string]any) string {
	if len(tasks) == 0 {
		return "tasks: none"
	}
	lines := make([]string, 0, len(tasks)+1)
	lines = append(lines, fmt.Sprintf("tasks: %d", len(tasks)))
	for _, task := range tasks {
		taskID, _ := task["task_id"].(string)
		state, _ := task["state"].(string)
		command, _ := task["command"].(string)
		line := strings.TrimSpace(taskID + " " + state)
		if command != "" {
			line = strings.TrimSpace(line + " " + command)
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

var _ tool.Tool = (*Tool)(nil)
