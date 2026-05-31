package services

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/session"
	coretool "github.com/OnslaughtSnail/caelis/core/tool"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

func (s CommandService) executeTask(ctx context.Context, ref session.Ref, args string) (appviewmodel.CommandExecutionView, error) {
	sub, rest, hasSub := splitCommandArg(args)
	if !hasSub {
		sub = "list"
	}
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "list", "ls":
		if strings.TrimSpace(rest) != "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /task list")
		}
		view, err := s.services.Tasks().List(ctx, ListTasksRequest{SessionRef: ref, Limit: 30, IncludeHistory: true})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled: true,
			Command: "task",
			Output:  formatCommandTaskList(view),
		}, nil
	case "tail", "show":
		taskID, _, ok := splitCommandArg(rest)
		if !ok || taskID == "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /task tail <id>")
		}
		view, err := s.services.Tasks().Tail(ctx, TaskOutputRequest{TaskID: taskID})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return commandTaskOutputView(view), nil
	case "wait":
		taskID, waitArg, ok := splitCommandArg(rest)
		if !ok || taskID == "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /task wait <id> [duration|ms]")
		}
		yieldMS, err := parseCommandTaskYieldMS(waitArg)
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		view, err := s.services.Tasks().Wait(ctx, TaskWaitRequest{
			TaskOutputRequest: TaskOutputRequest{TaskID: taskID},
			YieldTimeMS:       yieldMS,
		})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		if err := s.recordTaskLifecycle(ctx, ref, "wait", view.Task); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return commandTaskOutputView(view), nil
	case "write":
		taskID, input, ok := splitCommandArg(rest)
		if !ok || taskID == "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /task write <id> -- <input>")
		}
		view, err := s.services.Tasks().Write(ctx, TaskWriteRequest{
			TaskOutputRequest: TaskOutputRequest{TaskID: taskID},
			Input:             trimCommandTaskWriteInput(input),
		})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		if err := s.recordTaskLifecycle(ctx, ref, "write", view.Task); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return commandTaskOutputView(view), nil
	case "cancel":
		taskID, _, ok := splitCommandArg(rest)
		if !ok || taskID == "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /task cancel <id>")
		}
		view, err := s.services.Tasks().Cancel(ctx, TaskCancelRequest{TaskOutputRequest: TaskOutputRequest{TaskID: taskID}})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		if err := s.recordTaskLifecycle(ctx, ref, "cancel", view.Task); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return commandTaskOutputView(view), nil
	case "release", "close":
		taskID, _, ok := splitCommandArg(rest)
		if !ok || taskID == "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /task release <id>")
		}
		if err := s.services.Tasks().Release(ctx, TaskOutputRequest{TaskID: taskID}); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled: true,
			Command: "task",
			Output:  "task released: " + taskID,
		}, nil
	case "start", "run":
		command := trimCommandTaskSeparator(rest)
		if command == "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /task start -- <command>")
		}
		view, err := s.services.Tasks().Start(ctx, TaskStartRequest{Command: command})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		if err := s.recordTaskLifecycle(ctx, ref, "start", view.Task); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return commandTaskOutputView(view), nil
	default:
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /task list|tail|wait|write|cancel|release|start")
	}
}

func (s CommandService) recordTaskLifecycle(ctx context.Context, ref session.Ref, action string, task appviewmodel.TaskItem) error {
	ref = defaultSessionRef(s.services.Runtime(), ref)
	if s.services.engine == nil || strings.TrimSpace(ref.SessionID) == "" || strings.TrimSpace(task.ID) == "" {
		return nil
	}
	now := time.Now().UTC()
	status := taskLifecycleStatus(task)
	event := session.Event{
		Type:       session.EventLifecycle,
		Visibility: session.VisibilityCanonical,
		Time:       now,
		Actor:      session.ActorRef{Kind: session.ActorSystem, ID: "caelis", Name: "caelis"},
		Scope:      &session.EventScope{Source: "task"},
		Lifecycle: &session.LifecycleEvent{
			Status: status,
			Reason: taskLifecycleReason(action, task, status),
			Meta: map[string]any{
				"action":  strings.TrimSpace(action),
				"task_id": strings.TrimSpace(task.ID),
			},
		},
		Meta: coretool.WithRuntimeTaskMeta(nil, taskLifecycleMeta(action, task)),
	}
	if _, err := s.services.engine.RecordEvents(ctx, ref, []session.Event{event}); err != nil {
		return err
	}
	return nil
}

func taskLifecycleMeta(action string, task appviewmodel.TaskItem) map[string]any {
	meta := map[string]any{
		"source":         "command",
		"action":         strings.TrimSpace(action),
		"task_id":        strings.TrimSpace(task.ID),
		"task_kind":      strings.TrimSpace(task.Kind),
		"state":          commandTaskState(task),
		"running":        task.Running,
		"supports_input": task.SupportsInput,
		"backend":        strings.TrimSpace(task.Backend),
	}
	for key, value := range map[string]any{
		"title":             task.Title,
		"command":           task.Command,
		"cwd":               task.CWD,
		"terminal_id":       task.TerminalID,
		"agent":             task.Agent,
		"remote_session_id": task.RemoteSessionID,
		"output_preview":    task.OutputPreview,
		"error":             task.Error,
	} {
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
			meta[key] = text
		}
	}
	if task.OutputTruncated {
		meta["output_truncated"] = true
	}
	if task.StdoutCursor > 0 {
		meta["stdout_cursor"] = task.StdoutCursor
	}
	if task.StderrCursor > 0 {
		meta["stderr_cursor"] = task.StderrCursor
	}
	if task.ExitCode != 0 {
		meta["exit_code"] = task.ExitCode
	}
	if !task.StartedAt.IsZero() {
		meta["started_at"] = task.StartedAt.Format(time.RFC3339Nano)
	}
	if !task.UpdatedAt.IsZero() {
		meta["updated_at"] = task.UpdatedAt.Format(time.RFC3339Nano)
	}
	return meta
}

func taskLifecycleStatus(task appviewmodel.TaskItem) session.LifecycleStatus {
	state := strings.ToLower(commandTaskState(task))
	switch state {
	case "waiting_approval":
		return session.LifecycleWaitingApproval
	case "cancelled", "canceled":
		return session.LifecycleCancelled
	case "failed":
		return session.LifecycleFailed
	case "completed", "complete", "done":
		return session.LifecycleCompleted
	}
	if task.Running || taskStateRunning(state) {
		return session.LifecycleRunning
	}
	if strings.TrimSpace(task.Error) != "" {
		return session.LifecycleFailed
	}
	return session.LifecycleCompleted
}

func taskLifecycleReason(action string, task appviewmodel.TaskItem, status session.LifecycleStatus) string {
	parts := []string{"task", strings.TrimSpace(action), strings.TrimSpace(task.ID), string(status)}
	return strings.Join(commandNonEmpty(parts), " ")
}

func commandTaskOutputView(view appviewmodel.TaskOutputView) appviewmodel.CommandExecutionView {
	return appviewmodel.CommandExecutionView{
		Handled: true,
		Command: "task",
		Output:  formatCommandTaskOutput(view),
	}
}

func parseCommandTaskYieldMS(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if value, err := strconv.Atoi(raw); err == nil {
		if value < 0 {
			return 0, fmt.Errorf("app/services: task duration must be non-negative")
		}
		return value, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("app/services: task duration must be milliseconds or Go duration")
	}
	if duration < 0 {
		return 0, fmt.Errorf("app/services: task duration must be non-negative")
	}
	return int(duration / time.Millisecond), nil
}

func trimCommandTaskWriteInput(input string) string {
	input = strings.TrimSpace(input)
	if input == "--" {
		return ""
	}
	if strings.HasPrefix(input, "-- ") {
		return strings.TrimSpace(strings.TrimPrefix(input, "--"))
	}
	return input
}

func trimCommandTaskSeparator(input string) string {
	return trimCommandTaskWriteInput(input)
}

func formatCommandTaskList(view appviewmodel.TaskListView) string {
	if !view.Supported {
		return "tasks: not available"
	}
	lines := []string{"tasks:"}
	if len(view.Tasks) == 0 {
		lines = append(lines, "  none")
		return strings.Join(lines, "\n")
	}
	for _, task := range view.Tasks {
		lines = append(lines, "  "+formatCommandTaskItem(task))
	}
	return strings.Join(lines, "\n")
}

func formatCommandTaskItem(task appviewmodel.TaskItem) string {
	parts := []string{strings.TrimSpace(task.ID)}
	if state := commandTaskState(task); state != "" {
		parts = append(parts, state)
	}
	if kind := strings.TrimSpace(task.Kind); kind != "" {
		parts = append(parts, kind)
	}
	if title := firstNonEmpty(strings.TrimSpace(task.Title), strings.TrimSpace(task.Command), strings.TrimSpace(task.Agent)); title != "" {
		parts = append(parts, title)
	}
	if source := strings.TrimSpace(task.Source); source != "" && !strings.EqualFold(source, "live") {
		parts = append(parts, "source="+source)
	}
	return strings.Join(commandNonEmpty(parts), "  ")
}

func commandTaskState(task appviewmodel.TaskItem) string {
	if state := strings.TrimSpace(task.State); state != "" {
		return state
	}
	if task.Running {
		return "running"
	}
	return ""
}

func formatCommandTaskOutput(view appviewmodel.TaskOutputView) string {
	taskID := strings.TrimSpace(view.Task.ID)
	if taskID == "" {
		taskID = "task"
	}
	header := "task " + taskID
	if state := commandTaskState(view.Task); state != "" {
		header += ": " + state
	}
	lines := []string{header}
	if title := firstNonEmpty(strings.TrimSpace(view.Task.Title), strings.TrimSpace(view.Task.Command), strings.TrimSpace(view.Task.Agent)); title != "" {
		lines = append(lines, "  title: "+title)
	}
	if view.StdoutDroppedBytes > 0 {
		lines = append(lines, fmt.Sprintf("  stdout dropped: %d bytes", view.StdoutDroppedBytes))
	}
	if strings.TrimSpace(view.Stdout) != "" {
		lines = append(lines, "  stdout:")
		lines = append(lines, commandTaskIndentBlock(view.Stdout)...)
	}
	if view.StderrDroppedBytes > 0 {
		lines = append(lines, fmt.Sprintf("  stderr dropped: %d bytes", view.StderrDroppedBytes))
	}
	if strings.TrimSpace(view.Stderr) != "" {
		lines = append(lines, "  stderr:")
		lines = append(lines, commandTaskIndentBlock(view.Stderr)...)
	}
	if strings.TrimSpace(view.Stdout) == "" && strings.TrimSpace(view.Stderr) == "" {
		lines = append(lines, "  output: empty")
	}
	return strings.Join(lines, "\n")
}

func commandTaskIndentBlock(text string) []string {
	text = strings.TrimRight(text, "\r\n")
	if text == "" {
		return nil
	}
	raw := strings.Split(text, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		out = append(out, "    "+strings.TrimRight(line, "\r"))
	}
	return out
}

func commandNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
