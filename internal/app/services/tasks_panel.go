package services

import (
	"context"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type TaskPanelRequest struct {
	SessionRef     session.Ref `json:"session_ref,omitempty"`
	Limit          int         `json:"limit,omitempty"`
	IncludeHistory bool        `json:"include_history,omitempty"`
}

func (s TaskService) Panel(ctx context.Context, req TaskPanelRequest) (appviewmodel.TaskPanelView, error) {
	list, err := s.List(ctx, ListTasksRequest{
		SessionRef:     req.SessionRef,
		Limit:          req.Limit,
		IncludeHistory: req.IncludeHistory,
	})
	if err != nil {
		return appviewmodel.TaskPanelView{}, err
	}
	canOpen := s.services.sandbox != nil || s.services.tasks != nil
	return taskPanelFromList(list, s.services.sandbox != nil, canOpen), nil
}

func taskPanelFromList(list appviewmodel.TaskListView, canStart bool, canOpen bool) appviewmodel.TaskPanelView {
	view := appviewmodel.TaskPanelView{
		Supported: list.Supported,
		Tasks:     cloneTaskItems(list.Tasks),
	}
	if !list.Supported {
		view.Diagnostics = []appviewmodel.TaskPanelDiagnostic{{
			Severity: "info",
			Kind:     "task_runtime_unavailable",
			Message:  "task runtime is not available for this session",
		}}
		return view
	}
	view.Summary = taskPanelSummary(view.Tasks)
	view.Sections = taskPanelSections(view.Tasks)
	view.Actions = taskPanelActions(view.Tasks, canStart, canOpen)
	view.Diagnostics = taskPanelDiagnostics(view.Tasks)
	return view
}

func cloneTaskItems(items []appviewmodel.TaskItem) []appviewmodel.TaskItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]appviewmodel.TaskItem, len(items))
	copy(out, items)
	return out
}

func taskPanelSummary(items []appviewmodel.TaskItem) appviewmodel.TaskPanelSummary {
	summary := appviewmodel.TaskPanelSummary{Total: len(items)}
	for _, item := range items {
		if taskIsRunning(item) {
			summary.Running++
		}
		if taskIsWaiting(item) {
			summary.Waiting++
		}
		switch {
		case taskIsFailed(item):
			summary.Failed++
		case taskIsCancelled(item):
			summary.Cancelled++
		case taskIsCompleted(item):
			summary.Completed++
		}
		switch strings.ToLower(strings.TrimSpace(item.Kind)) {
		case "subagent":
			summary.Subagents++
		default:
			summary.Commands++
		}
		if !item.UpdatedAt.IsZero() && item.UpdatedAt.After(summary.UpdatedAt) {
			summary.UpdatedAt = item.UpdatedAt
		}
	}
	return summary
}

func taskPanelSections(items []appviewmodel.TaskItem) []appviewmodel.TaskPanelSection {
	sections := []appviewmodel.TaskPanelSection{
		{ID: "active", Title: "Active"},
		{ID: "attention", Title: "Needs attention"},
		{ID: "recent", Title: "Recent"},
	}
	index := map[string]int{
		"active":    0,
		"attention": 1,
		"recent":    2,
	}
	for _, item := range items {
		taskID := strings.TrimSpace(item.ID)
		if taskID == "" {
			continue
		}
		sectionID := "recent"
		switch {
		case taskIsRunning(item):
			sectionID = "active"
		case taskIsFailed(item) || taskIsCancelled(item):
			sectionID = "attention"
		}
		section := &sections[index[sectionID]]
		section.TaskIDs = append(section.TaskIDs, taskID)
		section.Count = len(section.TaskIDs)
	}
	out := sections[:0]
	for _, section := range sections {
		if section.Count > 0 {
			out = append(out, section)
		}
	}
	return out
}

func taskPanelActions(items []appviewmodel.TaskItem, canStart bool, canOpen bool) []appviewmodel.TaskPanelAction {
	out := []appviewmodel.TaskPanelAction{{
		ID:            "task.start",
		Kind:          "start",
		Label:         "Start task",
		Command:       "/task start -- ",
		Enabled:       canStart,
		RequiresInput: true,
	}}
	if !canOpen {
		return out
	}
	for _, item := range items {
		taskID := strings.TrimSpace(item.ID)
		if taskID == "" {
			continue
		}
		running := taskIsRunning(item)
		out = append(out, appviewmodel.TaskPanelAction{
			ID:      taskPanelActionID("tail", taskID),
			Kind:    "tail",
			Label:   "Tail",
			Command: "/task tail " + taskID,
			TaskID:  taskID,
			Enabled: true,
		})
		if running {
			out = append(out, appviewmodel.TaskPanelAction{
				ID:      taskPanelActionID("wait", taskID),
				Kind:    "wait",
				Label:   "Wait",
				Command: "/task wait " + taskID,
				TaskID:  taskID,
				Enabled: true,
			})
			out = append(out, appviewmodel.TaskPanelAction{
				ID:          taskPanelActionID("cancel", taskID),
				Kind:        "cancel",
				Label:       "Cancel",
				Command:     "/task cancel " + taskID,
				TaskID:      taskID,
				Enabled:     true,
				Destructive: true,
			})
			if item.SupportsInput {
				out = append(out, appviewmodel.TaskPanelAction{
					ID:            taskPanelActionID("write", taskID),
					Kind:          "write",
					Label:         "Write",
					Command:       "/task write " + taskID + " -- ",
					TaskID:        taskID,
					Enabled:       true,
					RequiresInput: true,
				})
			}
			continue
		}
		out = append(out, appviewmodel.TaskPanelAction{
			ID:      taskPanelActionID("release", taskID),
			Kind:    "release",
			Label:   "Release",
			Command: "/task release " + taskID,
			TaskID:  taskID,
			Enabled: true,
		})
	}
	return out
}

func taskPanelActionID(kind string, taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return "task." + strings.TrimSpace(kind)
	}
	return "task." + strings.TrimSpace(kind) + ":" + taskID
}

func taskPanelDiagnostics(items []appviewmodel.TaskItem) []appviewmodel.TaskPanelDiagnostic {
	var out []appviewmodel.TaskPanelDiagnostic
	for _, item := range items {
		taskID := strings.TrimSpace(item.ID)
		if taskIsFailed(item) {
			message := strings.TrimSpace(item.Error)
			if message == "" {
				message = "task failed"
			}
			out = append(out, appviewmodel.TaskPanelDiagnostic{
				Severity: "error",
				Kind:     "task_failed",
				Message:  message,
				TaskID:   taskID,
			})
		}
		if item.OutputTruncated {
			out = append(out, appviewmodel.TaskPanelDiagnostic{
				Severity: "warning",
				Kind:     "task_output_truncated",
				Message:  "task output preview is truncated",
				TaskID:   taskID,
			})
		}
	}
	return out
}

func taskIsRunning(task appviewmodel.TaskItem) bool {
	return task.Running || taskStateRunning(task.State)
}

func taskIsWaiting(task appviewmodel.TaskItem) bool {
	switch strings.ToLower(strings.TrimSpace(task.State)) {
	case "waiting_approval", "waiting_input":
		return true
	default:
		return false
	}
}

func taskIsFailed(task appviewmodel.TaskItem) bool {
	switch strings.ToLower(strings.TrimSpace(task.State)) {
	case "failed":
		return true
	case "cancelled", "canceled":
		return false
	}
	return !taskIsRunning(task) && strings.TrimSpace(task.Error) != ""
}

func taskIsCancelled(task appviewmodel.TaskItem) bool {
	switch strings.ToLower(strings.TrimSpace(task.State)) {
	case "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func taskIsCompleted(task appviewmodel.TaskItem) bool {
	if taskIsRunning(task) || taskIsFailed(task) || taskIsCancelled(task) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(task.State)) {
	case "completed", "complete", "done", "success", "succeeded":
		return true
	default:
		return false
	}
}
