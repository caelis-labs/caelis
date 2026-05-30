package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type TaskService struct {
	services Services
}

type ListTasksRequest struct {
	SessionRef     session.Ref `json:"session_ref,omitempty"`
	Limit          int         `json:"limit,omitempty"`
	IncludeHistory bool        `json:"include_history,omitempty"`
}

type TaskOutputRequest struct {
	TaskID       string `json:"task_id,omitempty"`
	StdoutCursor int64  `json:"stdout_cursor,omitempty"`
	StderrCursor int64  `json:"stderr_cursor,omitempty"`
}

type TaskWaitRequest struct {
	TaskOutputRequest
	YieldTimeMS int `json:"yield_time_ms,omitempty"`
}

type TaskWriteRequest struct {
	TaskOutputRequest
	Input       string `json:"input,omitempty"`
	YieldTimeMS int    `json:"yield_time_ms,omitempty"`
}

type TaskCancelRequest struct {
	TaskOutputRequest
}

func (s TaskService) List(ctx context.Context, req ListTasksRequest) (appviewmodel.TaskListView, error) {
	if req.Limit < 0 {
		req.Limit = 0
	}
	var items []appviewmodel.TaskItem
	supported := false

	if req.IncludeHistory && strings.TrimSpace(req.SessionRef.SessionID) != "" {
		snapshot, err := s.services.Sessions().Load(ctx, req.SessionRef)
		if err != nil {
			return appviewmodel.TaskListView{}, err
		}
		items = mergeTaskItemSlices(items, durableTaskItemsFromEvents(snapshot.Events))
		supported = true
	}

	lister, ok := s.services.sandbox.(sandbox.SessionLister)
	if s.services.sandbox != nil && ok {
		snapshots, err := lister.ListSessions(ctx, sandbox.SessionListQuery{Limit: req.Limit})
		if err != nil {
			return appviewmodel.TaskListView{}, err
		}
		live := make([]appviewmodel.TaskItem, 0, len(snapshots))
		for _, snapshot := range snapshots {
			live = append(live, appviewmodel.TaskItemFromSnapshot(snapshot))
		}
		items = mergeTaskItemSlices(items, live)
		supported = true
	}

	if !supported {
		return appviewmodel.TaskListView{}, nil
	}
	sortTaskItems(items)
	if req.Limit > 0 && len(items) > req.Limit {
		items = items[:req.Limit]
	}
	return appviewmodel.TaskListView{
		Supported: true,
		Count:     len(items),
		Tasks:     items,
	}, nil
}

func (s TaskService) Tail(ctx context.Context, req TaskOutputRequest) (appviewmodel.TaskOutputView, error) {
	session, err := s.open(ctx, req.TaskID)
	if err != nil {
		return appviewmodel.TaskOutputView{}, err
	}
	return s.output(ctx, session, req, 0)
}

func (s TaskService) Wait(ctx context.Context, req TaskWaitRequest) (appviewmodel.TaskOutputView, error) {
	session, err := s.open(ctx, req.TaskID)
	if err != nil {
		return appviewmodel.TaskOutputView{}, err
	}
	return s.output(ctx, session, req.TaskOutputRequest, waitDuration(req.YieldTimeMS, time.Second))
}

func (s TaskService) Write(ctx context.Context, req TaskWriteRequest) (appviewmodel.TaskOutputView, error) {
	session, err := s.open(ctx, req.TaskID)
	if err != nil {
		return appviewmodel.TaskOutputView{}, err
	}
	if err := session.Write(ctx, []byte(req.Input)); err != nil {
		return appviewmodel.TaskOutputView{}, err
	}
	return s.output(ctx, session, req.TaskOutputRequest, waitDuration(req.YieldTimeMS, 100*time.Millisecond))
}

func (s TaskService) Cancel(ctx context.Context, req TaskCancelRequest) (appviewmodel.TaskOutputView, error) {
	session, err := s.open(ctx, req.TaskID)
	if err != nil {
		return appviewmodel.TaskOutputView{}, err
	}
	if err := session.Cancel(ctx); err != nil {
		return appviewmodel.TaskOutputView{}, err
	}
	return s.output(ctx, session, req.TaskOutputRequest, 100*time.Millisecond)
}

func (s TaskService) open(ctx context.Context, taskID string) (sandbox.Session, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, errors.New("app/services: task id is required")
	}
	if s.services.sandbox == nil {
		return nil, errors.New("app/services: sandbox runtime is not configured")
	}
	session, err := s.services.sandbox.Open(ctx, sandbox.SessionRef{ID: taskID})
	if err != nil {
		return nil, fmt.Errorf("app/services: open task %q: %w", taskID, err)
	}
	return session, nil
}

func (s TaskService) output(ctx context.Context, session sandbox.Session, req TaskOutputRequest, wait time.Duration) (appviewmodel.TaskOutputView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if wait > 0 {
		waitCtx, cancel := context.WithTimeout(ctx, wait)
		_, err := session.Wait(waitCtx)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return appviewmodel.TaskOutputView{}, err
		}
	}
	snapshot, err := session.Snapshot(ctx)
	if err != nil {
		return appviewmodel.TaskOutputView{}, err
	}
	output, err := session.Read(ctx, sandbox.OutputCursor{
		Stdout: req.StdoutCursor,
		Stderr: req.StderrCursor,
	})
	if err != nil {
		return appviewmodel.TaskOutputView{}, err
	}
	return appviewmodel.TaskOutputFromSnapshot(snapshot, output), nil
}

func waitDuration(ms int, fallback time.Duration) time.Duration {
	if ms < 0 {
		return 0
	}
	if ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return fallback
}

func durableTaskItemsFromEvents(events []session.Event) []appviewmodel.TaskItem {
	var items []appviewmodel.TaskItem
	for _, event := range events {
		if session.IsTransient(event) {
			continue
		}
		if item, ok := durableTaskItemFromToolEvent(event); ok {
			items = mergeTaskItemSlices(items, []appviewmodel.TaskItem{item})
		}
		if item, ok := durableTaskItemFromParticipantEvent(event); ok {
			items = mergeTaskItemSlices(items, []appviewmodel.TaskItem{item})
		}
	}
	sortTaskItems(items)
	return items
}

func durableTaskItemFromToolEvent(event session.Event) (appviewmodel.TaskItem, bool) {
	if event.Tool == nil {
		return appviewmodel.TaskItem{}, false
	}
	tool := event.Tool
	taskMeta := taskRuntimeMeta(tool.Meta)
	taskID := firstNonEmpty(
		stringFromAny(taskMeta["task_id"]),
		stringFromAny(taskMeta["internal_task_id"]),
		stringFromAny(tool.Meta["task_id"]),
		stringFromAny(tool.Output["task_id"]),
	)
	if taskID == "" && strings.EqualFold(strings.TrimSpace(tool.Name), "SPAWN") {
		taskID = strings.TrimSpace(tool.ID)
	}
	if taskID == "" {
		return appviewmodel.TaskItem{}, false
	}
	state := firstNonEmpty(
		stringFromAny(taskMeta["state"]),
		stringFromAny(tool.Meta["state"]),
		stringFromAny(tool.Output["state"]),
		strings.TrimSpace(string(tool.Status)),
	)
	running, ok := firstBool(
		taskMeta["running"],
		tool.Meta["running"],
		tool.Output["running"],
	)
	if !ok {
		running = taskStateRunning(state)
	}
	startedAt := firstTime(taskMeta["started_at"], tool.Output["started_at"])
	updatedAt := firstTime(taskMeta["updated_at"], tool.Output["updated_at"])
	if updatedAt.IsZero() {
		updatedAt = event.Time
	}
	if startedAt.IsZero() {
		startedAt = updatedAt
	}
	kind := firstNonEmpty(
		stringFromAny(taskMeta["task_kind"]),
		stringFromAny(taskMeta["kind"]),
	)
	if kind == "" {
		if strings.EqualFold(strings.TrimSpace(tool.Name), "SPAWN") {
			kind = "subagent"
		} else {
			kind = "command"
		}
	}
	agent := firstNonEmpty(
		stringFromAny(taskMeta["agent"]),
		stringFromAny(tool.Meta["agent"]),
		stringFromAny(tool.Output["agent"]),
		stringFromAny(tool.Input["agent"]),
	)
	command := firstNonEmpty(
		stringFromAny(taskMeta["command"]),
		stringFromAny(tool.Output["command"]),
		stringFromAny(tool.Input["command"]),
		stringFromAny(tool.Input["cmd"]),
	)
	return appviewmodel.TaskItem{
		ID:              taskID,
		Kind:            kind,
		Source:          "history",
		Title:           taskTitle(*tool, kind, agent, command),
		Backend:         firstNonEmpty(stringFromAny(taskMeta["backend"]), stringFromAny(tool.Output["backend"])),
		Action:          firstNonEmpty(stringFromAny(taskMeta["action"]), stringFromAny(tool.Output["action"]), stringFromAny(tool.Input["action"])),
		State:           state,
		Running:         running,
		SupportsInput:   boolFromAny(taskMeta["supports_input"]) || boolFromAny(tool.Output["supports_input"]),
		Command:         command,
		CWD:             firstNonEmpty(stringFromAny(taskMeta["cwd"]), stringFromAny(tool.Output["cwd"]), stringFromAny(tool.Input["cwd"]), stringFromAny(tool.Input["workdir"])),
		TerminalID:      firstNonEmpty(stringFromAny(taskMeta["terminal_id"]), stringFromAny(tool.Output["terminal_id"])),
		Agent:           agent,
		RemoteSessionID: firstNonEmpty(stringFromAny(taskMeta["remote_session_id"]), stringFromAny(tool.Meta["remote_session_id"]), stringFromAny(tool.Output["remote_session_id"])),
		StdoutCursor:    firstInt64(taskMeta["stdout_cursor"], tool.Meta["stdout_cursor"], tool.Output["stdout_cursor"]),
		StderrCursor:    firstInt64(taskMeta["stderr_cursor"], tool.Meta["stderr_cursor"], tool.Output["stderr_cursor"]),
		EventID:         strings.TrimSpace(event.ID),
		TurnID:          eventTurnID(event),
		ExitCode:        firstInt(taskMeta["exit_code"], tool.Output["exit_code"]),
		Error:           firstNonEmpty(stringFromAny(taskMeta["error"]), stringFromAny(tool.Output["error"])),
		StartedAt:       startedAt,
		UpdatedAt:       updatedAt,
	}, true
}

func durableTaskItemFromParticipantEvent(event session.Event) (appviewmodel.TaskItem, bool) {
	if event.Scope == nil {
		return appviewmodel.TaskItem{}, false
	}
	participant := event.Scope.Participant
	if participant.Kind != session.ParticipantSubagent && strings.TrimSpace(participant.DelegationID) == "" {
		return appviewmodel.TaskItem{}, false
	}
	taskID := firstNonEmpty(participant.DelegationID, participant.ID)
	if taskID == "" {
		return appviewmodel.TaskItem{}, false
	}
	updatedAt := event.Time
	startedAt := participant.AttachedAt
	if startedAt.IsZero() {
		startedAt = updatedAt
	}
	agent := firstNonEmpty(participant.AgentName, participant.Label, participant.ID)
	return appviewmodel.TaskItem{
		ID:              taskID,
		Kind:            "subagent",
		Source:          "history",
		Title:           taskTitle(session.ToolEvent{Name: "SPAWN"}, "subagent", agent, ""),
		State:           stringFromAny(event.Meta["state"]),
		Running:         boolFromAny(event.Meta["running"]),
		Agent:           agent,
		RemoteSessionID: firstNonEmpty(participant.SessionID, event.Scope.ACP.SessionID),
		EventID:         strings.TrimSpace(event.ID),
		TurnID:          firstNonEmpty(event.Scope.TurnID, participant.ParentTurnID),
		StartedAt:       startedAt,
		UpdatedAt:       updatedAt,
	}, true
}

func taskRuntimeMeta(meta map[string]any) map[string]any {
	values, ok := mapAny(nestedAny(meta, "caelis", "runtime", "task"))
	if !ok {
		return nil
	}
	return values
}

func mergeTaskItemSlices(items []appviewmodel.TaskItem, next []appviewmodel.TaskItem) []appviewmodel.TaskItem {
	if len(next) == 0 {
		return items
	}
	indexes := make(map[string]int, len(items)+len(next))
	for idx, item := range items {
		if item.ID != "" {
			indexes[item.ID] = idx
		}
	}
	for _, item := range next {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			continue
		}
		if idx, ok := indexes[item.ID]; ok {
			items[idx] = mergeTaskItem(items[idx], item)
			continue
		}
		indexes[item.ID] = len(items)
		items = append(items, item)
	}
	return items
}

func mergeTaskItem(base appviewmodel.TaskItem, next appviewmodel.TaskItem) appviewmodel.TaskItem {
	out := base
	out.ID = firstNonEmpty(next.ID, out.ID)
	out.Kind = firstNonEmpty(next.Kind, out.Kind)
	out.Source = firstNonEmpty(next.Source, out.Source)
	out.Title = firstNonEmpty(next.Title, out.Title)
	out.Backend = firstNonEmpty(next.Backend, out.Backend)
	out.Action = firstNonEmpty(next.Action, out.Action)
	if next.State != "" {
		out.State = next.State
		out.Running = next.Running
	}
	out.SupportsInput = out.SupportsInput || next.SupportsInput
	out.Command = firstNonEmpty(next.Command, out.Command)
	out.CWD = firstNonEmpty(next.CWD, out.CWD)
	out.TerminalID = firstNonEmpty(next.TerminalID, out.TerminalID)
	out.Agent = firstNonEmpty(next.Agent, out.Agent)
	out.RemoteSessionID = firstNonEmpty(next.RemoteSessionID, out.RemoteSessionID)
	if next.StdoutCursor > out.StdoutCursor {
		out.StdoutCursor = next.StdoutCursor
	}
	if next.StderrCursor > out.StderrCursor {
		out.StderrCursor = next.StderrCursor
	}
	if next.EventID != "" && (out.EventID == "" || !next.UpdatedAt.Before(out.UpdatedAt)) {
		out.EventID = next.EventID
	}
	out.TurnID = firstNonEmpty(next.TurnID, out.TurnID)
	if next.ExitCode != 0 || (next.State != "" && !next.Running) {
		out.ExitCode = next.ExitCode
	}
	if next.Error != "" || (next.Source == "live" && next.State != "failed") {
		out.Error = next.Error
	}
	if !next.StartedAt.IsZero() && (out.StartedAt.IsZero() || next.StartedAt.Before(out.StartedAt)) {
		out.StartedAt = next.StartedAt
	}
	if !next.UpdatedAt.IsZero() && (out.UpdatedAt.IsZero() || next.UpdatedAt.After(out.UpdatedAt)) {
		out.UpdatedAt = next.UpdatedAt
	}
	return out
}

func sortTaskItems(items []appviewmodel.TaskItem) {
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i]
		right := items[j]
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			if left.UpdatedAt.IsZero() {
				return false
			}
			if right.UpdatedAt.IsZero() {
				return true
			}
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		return left.ID < right.ID
	})
}

func taskTitle(tool session.ToolEvent, kind string, agent string, command string) string {
	if title := strings.TrimSpace(tool.Title); title != "" {
		return title
	}
	switch strings.TrimSpace(kind) {
	case "subagent":
		if agent != "" {
			return "SPAWN " + agent
		}
		return "SPAWN"
	case "command":
		return firstNonEmpty(command, strings.TrimSpace(tool.Name))
	default:
		return firstNonEmpty(command, agent, strings.TrimSpace(tool.Name))
	}
}

func eventTurnID(event session.Event) string {
	if event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.TurnID)
}

func taskStateRunning(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "started", "running", "waiting_approval", "waiting_input":
		return true
	default:
		return false
	}
}

func firstBool(values ...any) (bool, bool) {
	for _, value := range values {
		switch typed := value.(type) {
		case bool:
			return typed, true
		case string:
			switch strings.ToLower(strings.TrimSpace(typed)) {
			case "true":
				return true, true
			case "false":
				return false, true
			}
		}
	}
	return false, false
}

func boolFromAny(value any) bool {
	out, _ := firstBool(value)
	return out
}

func firstInt(values ...any) int {
	for _, value := range values {
		if out := anyInt(value); out != 0 {
			return out
		}
	}
	return 0
}

func firstInt64(values ...any) int64 {
	for _, value := range values {
		switch typed := value.(type) {
		case int:
			if typed != 0 {
				return int64(typed)
			}
		case int64:
			if typed != 0 {
				return typed
			}
		case float64:
			if typed != 0 {
				return int64(typed)
			}
		}
	}
	return 0
}

func firstTime(values ...any) time.Time {
	for _, value := range values {
		switch typed := value.(type) {
		case time.Time:
			if !typed.IsZero() {
				return typed
			}
		case string:
			if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(typed)); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}
