package services

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/session"
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
		return commandTaskOutputView(view), nil
	default:
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /task list|tail|wait|write|cancel|release|start")
	}
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
	return strings.Join(commandTaskNonEmpty(parts), "  ")
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

func commandTaskNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
