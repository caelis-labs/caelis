package tuiapp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
)

func slashTaskWithContext(ctx context.Context, driver tuidriver.Driver, send func(tea.Msg), args string) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	tasks, ok := driver.(tuidriver.TaskController)
	if !ok || tasks == nil {
		return TaskResultMsg{Err: friendlyCommandError("task", fmt.Errorf("task controller unavailable"))}
	}
	sub, rest := splitFirst(strings.TrimSpace(args))
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "", "list", "ls":
		if strings.TrimSpace(rest) != "" {
			sendNotice(send, "usage: /task list")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		view, err := tasks.ListTasks(ctx, tuidriver.TaskListOptions{Limit: 30, IncludeHistory: true})
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("task list", err)}
		}
		sendNotice(send, formatTaskListView(view))
	case "tail", "show":
		taskID, _ := splitFirst(rest)
		if taskID == "" {
			sendNotice(send, "usage: /task tail <id>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		view, err := tasks.TailTask(ctx, tuidriver.TaskOutputOptions{TaskID: taskID})
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("task tail", err)}
		}
		sendNotice(send, formatTaskOutputView(view))
	case "wait":
		taskID, waitArg := splitFirst(rest)
		if taskID == "" {
			sendNotice(send, "usage: /task wait <id> [duration|ms]")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		yieldMS, err := parseTaskYieldMS(waitArg)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("task wait", err)}
		}
		view, err := tasks.WaitTask(ctx, tuidriver.TaskWaitOptions{
			TaskOutputOptions: tuidriver.TaskOutputOptions{TaskID: taskID},
			YieldTimeMS:       yieldMS,
		})
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("task wait", err)}
		}
		sendNotice(send, formatTaskOutputView(view))
	case "write":
		taskID, input := splitFirst(rest)
		input = trimTaskWriteInput(input)
		if taskID == "" {
			sendNotice(send, "usage: /task write <id> -- <input>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		view, err := tasks.WriteTask(ctx, tuidriver.TaskWriteOptions{
			TaskOutputOptions: tuidriver.TaskOutputOptions{TaskID: taskID},
			Input:             input,
		})
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("task write", err)}
		}
		sendNotice(send, formatTaskOutputView(view))
	case "cancel":
		taskID, _ := splitFirst(rest)
		if taskID == "" {
			sendNotice(send, "usage: /task cancel <id>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		view, err := tasks.CancelTask(ctx, tuidriver.TaskOutputOptions{TaskID: taskID})
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("task cancel", err)}
		}
		sendNotice(send, formatTaskOutputView(view))
	case "release", "close":
		taskID, _ := splitFirst(rest)
		if taskID == "" {
			sendNotice(send, "usage: /task release <id>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		if err := tasks.ReleaseTask(ctx, tuidriver.TaskOutputOptions{TaskID: taskID}); err != nil {
			return TaskResultMsg{Err: friendlyCommandError("task release", err)}
		}
		sendNotice(send, "task released: "+taskID)
	case "start", "run":
		command := trimTaskCommandSeparator(rest)
		if command == "" {
			sendNotice(send, "usage: /task start -- <command>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		view, err := tasks.StartTask(ctx, tuidriver.TaskStartOptions{Command: command})
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("task start", err)}
		}
		sendNotice(send, formatTaskOutputView(view))
	default:
		sendNotice(send, "usage: /task list|tail|wait|write|cancel|release|start")
	}
	return TaskResultMsg{SuppressTurnDivider: true}
}

func parseTaskYieldMS(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if value, err := strconv.Atoi(raw); err == nil {
		if value < 0 {
			return 0, fmt.Errorf("duration must be non-negative")
		}
		return value, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("duration must be milliseconds or Go duration")
	}
	if duration < 0 {
		return 0, fmt.Errorf("duration must be non-negative")
	}
	return int(duration / time.Millisecond), nil
}

func trimTaskWriteInput(input string) string {
	input = strings.TrimSpace(input)
	if input == "--" {
		return ""
	}
	if strings.HasPrefix(input, "-- ") {
		return strings.TrimSpace(strings.TrimPrefix(input, "--"))
	}
	return input
}

func trimTaskCommandSeparator(input string) string {
	return trimTaskWriteInput(input)
}

func formatTaskListView(view tuidriver.TaskListView) string {
	if !view.Supported {
		return "tasks: not available"
	}
	lines := []string{"tasks:"}
	if len(view.Tasks) == 0 {
		lines = append(lines, "  none")
		return strings.Join(lines, "\n")
	}
	for _, task := range view.Tasks {
		line := "  " + formatTaskItemLine(task)
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func formatTaskItemLine(task tuidriver.TaskItem) string {
	parts := []string{strings.TrimSpace(task.ID)}
	if state := taskStateLabel(task); state != "" {
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
	return strings.Join(compactNonEmpty(parts), "  ")
}

func taskStateLabel(task tuidriver.TaskItem) string {
	if state := strings.TrimSpace(task.State); state != "" {
		return state
	}
	if task.Running {
		return "running"
	}
	return ""
}

func formatTaskOutputView(view tuidriver.TaskOutputView) string {
	taskID := strings.TrimSpace(view.Task.ID)
	if taskID == "" {
		taskID = "task"
	}
	header := "task " + taskID
	if state := taskStateLabel(view.Task); state != "" {
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
		lines = append(lines, indentTaskBlock(view.Stdout)...)
	}
	if view.StderrDroppedBytes > 0 {
		lines = append(lines, fmt.Sprintf("  stderr dropped: %d bytes", view.StderrDroppedBytes))
	}
	if strings.TrimSpace(view.Stderr) != "" {
		lines = append(lines, "  stderr:")
		lines = append(lines, indentTaskBlock(view.Stderr)...)
	}
	if strings.TrimSpace(view.Stdout) == "" && strings.TrimSpace(view.Stderr) == "" {
		lines = append(lines, "  output: empty")
	}
	return strings.Join(lines, "\n")
}

func indentTaskBlock(text string) []string {
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
