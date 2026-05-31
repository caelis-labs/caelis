package services

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
	coretool "github.com/OnslaughtSnail/caelis/core/tool"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type TaskService struct {
	services Services
}

type TaskResolver interface {
	OpenTask(context.Context, sandbox.SessionRef) (sandbox.Session, bool, error)
	ListTasks(context.Context, sandbox.SessionListQuery) ([]sandbox.SessionSnapshot, error)
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

type TaskStartRequest struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Dir     string            `json:"dir,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
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

	if s.services.tasks != nil {
		snapshots, err := s.services.tasks.ListTasks(ctx, sandbox.SessionListQuery{Limit: req.Limit})
		if err != nil {
			return appviewmodel.TaskListView{}, err
		}
		resolved := make([]appviewmodel.TaskItem, 0, len(snapshots))
		for _, snapshot := range snapshots {
			resolved = append(resolved, appviewmodel.TaskItemFromSnapshot(snapshot))
		}
		items = mergeTaskItemSlices(items, resolved)
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

func (s TaskService) Start(ctx context.Context, req TaskStartRequest) (appviewmodel.TaskOutputView, error) {
	if s.services.sandbox == nil {
		return appviewmodel.TaskOutputView{}, errors.New("app/services: sandbox runtime is not configured")
	}
	command := taskCommandLine(req.Command, req.Args)
	if command == "" {
		return appviewmodel.TaskOutputView{}, errors.New("app/services: task command is required")
	}
	session, err := s.services.sandbox.Start(ctx, sandbox.CommandRequest{
		Command: command,
		Dir:     strings.TrimSpace(req.Dir),
		Env:     cloneStringMap(req.Env),
	})
	if err != nil {
		return appviewmodel.TaskOutputView{}, err
	}
	return s.output(ctx, session, TaskOutputRequest{TaskID: session.Ref().ID}, 0)
}

func (s TaskService) Wait(ctx context.Context, req TaskWaitRequest) (appviewmodel.TaskOutputView, error) {
	session, err := s.open(ctx, req.TaskID)
	if err != nil {
		return appviewmodel.TaskOutputView{}, err
	}
	return s.output(ctx, session, req.TaskOutputRequest, waitDuration(req.YieldTimeMS, time.Second))
}

func (s TaskService) WaitForExit(ctx context.Context, req TaskOutputRequest) (appviewmodel.TaskOutputView, error) {
	session, err := s.open(ctx, req.TaskID)
	if err != nil {
		return appviewmodel.TaskOutputView{}, err
	}
	if _, err := session.Wait(ctx); err != nil {
		return appviewmodel.TaskOutputView{}, err
	}
	return s.output(ctx, session, req, 0)
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

func (s TaskService) Release(ctx context.Context, req TaskOutputRequest) error {
	session, err := s.open(ctx, req.TaskID)
	if err != nil {
		return err
	}
	snapshot, err := session.Snapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.Running {
		return nil
	}
	return session.Close()
}

func (s TaskService) open(ctx context.Context, taskID string) (sandbox.Session, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, errors.New("app/services: task id is required")
	}
	if s.services.sandbox == nil {
		return s.openResolved(ctx, taskID, nil)
	}
	session, err := s.services.sandbox.Open(ctx, sandbox.SessionRef{ID: taskID})
	if err != nil {
		return s.openResolved(ctx, taskID, err)
	}
	return session, nil
}

func taskCommandLine(command string, args []string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if len(args) == 0 {
		return command
	}
	parts := []string{quoteTaskCommandArg(command)}
	for _, arg := range args {
		parts = append(parts, quoteTaskCommandArg(arg))
	}
	return strings.Join(parts, " ")
}

func quoteTaskCommandArg(value string) string {
	if value == "" {
		if runtime.GOOS == "windows" {
			return `""`
		}
		return "''"
	}
	if runtime.GOOS == "windows" {
		if !strings.ContainsAny(value, " \t\r\n\"") {
			return value
		}
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	if !strings.ContainsAny(value, " \t\r\n'\"\\$`!*?[]{}();&|<>") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func (s TaskService) openResolved(ctx context.Context, taskID string, sandboxErr error) (sandbox.Session, error) {
	if s.services.tasks != nil {
		session, ok, err := s.services.tasks.OpenTask(ctx, sandbox.SessionRef{ID: taskID})
		if err != nil {
			return nil, err
		}
		if ok {
			return session, nil
		}
	}
	if sandboxErr != nil {
		return nil, fmt.Errorf("app/services: open task %q: %w", taskID, sandboxErr)
	}
	return nil, errors.New("app/services: task runtime is not configured")
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
		items = mergeTaskItemSlices(items, durableTaskItemsFromCompactEvent(event))
		if item, ok := durableTaskItemFromToolEvent(event); ok {
			items = mergeTaskItemSlices(items, []appviewmodel.TaskItem{item})
		}
		if item, ok := durableTaskItemFromParticipantEvent(event); ok {
			items = mergeTaskItemSlices(items, []appviewmodel.TaskItem{item})
		}
		if item, ok := durableTaskItemFromLifecycleEvent(event); ok {
			items = mergeTaskItemSlices(items, []appviewmodel.TaskItem{item})
		}
	}
	sortTaskItems(items)
	return items
}

func durableTaskItemsFromCompactEvent(event session.Event) []appviewmodel.TaskItem {
	if !isCompactCheckpoint(event) {
		return nil
	}
	compact, ok := mapAny(event.Meta[compactMetaKey])
	if !ok {
		return nil
	}
	entries := compactTaskIndexEntries(compact[compactTaskIndexKey])
	if len(entries) == 0 {
		return nil
	}
	items := make([]appviewmodel.TaskItem, 0, len(entries))
	for _, entry := range entries {
		item, ok := taskItemFromRuntimeTaskMeta(entry, event)
		if ok {
			items = mergeTaskItemSlices(items, []appviewmodel.TaskItem{item})
		}
	}
	return items
}

func compactTaskIndexEntries(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := mapAny(item); ok {
				out = append(out, mapped)
			}
		}
		return out
	default:
		return nil
	}
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
	outputPreview := firstNonEmpty(
		coretool.RuntimeTaskOutputText(tool.Meta),
		coretool.JoinRuntimeTaskStreams(stringFromAny(tool.Output["stdout"]), stringFromAny(tool.Output["stderr"])),
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
		OutputPreview:   outputPreview,
		OutputTruncated: boolFromAny(taskMeta["output_truncated"]),
		EventID:         strings.TrimSpace(event.ID),
		TurnID:          eventTurnID(event),
		ExitCode:        firstInt(taskMeta["exit_code"], tool.Output["exit_code"]),
		Error:           firstNonEmpty(stringFromAny(taskMeta["error"]), stringFromAny(tool.Output["error"])),
		StartedAt:       startedAt,
		UpdatedAt:       updatedAt,
	}, true
}

func durableTaskItemFromLifecycleEvent(event session.Event) (appviewmodel.TaskItem, bool) {
	if event.Type != session.EventLifecycle || event.Lifecycle == nil {
		return appviewmodel.TaskItem{}, false
	}
	taskMeta := taskRuntimeMeta(event.Meta)
	return taskItemFromRuntimeTaskMeta(taskMeta, event)
}

func taskItemFromRuntimeTaskMeta(taskMeta map[string]any, event session.Event) (appviewmodel.TaskItem, bool) {
	taskID := stringFromAny(taskMeta["task_id"])
	if strings.TrimSpace(taskID) == "" {
		return appviewmodel.TaskItem{}, false
	}
	lifecycleStatus := ""
	if event.Lifecycle != nil {
		lifecycleStatus = strings.TrimSpace(string(event.Lifecycle.Status))
	}
	state := firstNonEmpty(
		stringFromAny(taskMeta["state"]),
		lifecycleStatus,
	)
	running, ok := firstBool(taskMeta["running"])
	if !ok {
		running = taskStateRunning(state)
	}
	updatedAt := firstTime(taskMeta["updated_at"])
	if updatedAt.IsZero() {
		updatedAt = event.Time
	}
	startedAt := firstTime(taskMeta["started_at"])
	if startedAt.IsZero() {
		startedAt = updatedAt
	}
	kind := firstNonEmpty(stringFromAny(taskMeta["task_kind"]), stringFromAny(taskMeta["kind"]))
	command := stringFromAny(taskMeta["command"])
	agent := stringFromAny(taskMeta["agent"])
	outputPreview := firstNonEmpty(
		stringFromAny(taskMeta["output_preview"]),
		coretool.RuntimeTaskOutputText(event.Meta),
	)
	return appviewmodel.TaskItem{
		ID:              strings.TrimSpace(taskID),
		Kind:            kind,
		Source:          firstNonEmpty(stringFromAny(taskMeta["source"]), "history"),
		Title:           firstNonEmpty(stringFromAny(taskMeta["title"]), taskTitle(session.ToolEvent{Name: "task"}, kind, agent, command)),
		Backend:         stringFromAny(taskMeta["backend"]),
		Action:          stringFromAny(taskMeta["action"]),
		State:           state,
		Running:         running,
		SupportsInput:   boolFromAny(taskMeta["supports_input"]),
		Command:         command,
		CWD:             stringFromAny(taskMeta["cwd"]),
		TerminalID:      stringFromAny(taskMeta["terminal_id"]),
		Agent:           agent,
		RemoteSessionID: stringFromAny(taskMeta["remote_session_id"]),
		StdoutCursor:    firstInt64(taskMeta["stdout_cursor"]),
		StderrCursor:    firstInt64(taskMeta["stderr_cursor"]),
		OutputPreview:   outputPreview,
		OutputTruncated: boolFromAny(taskMeta["output_truncated"]),
		EventID:         firstNonEmpty(stringFromAny(taskMeta["event_id"]), strings.TrimSpace(event.ID)),
		TurnID:          firstNonEmpty(stringFromAny(taskMeta["turn_id"]), eventTurnID(event)),
		ExitCode:        firstInt(taskMeta["exit_code"]),
		Error:           stringFromAny(taskMeta["error"]),
		StartedAt:       startedAt,
		UpdatedAt:       updatedAt,
	}, true
}

func taskItemRetentionMeta(item appviewmodel.TaskItem) map[string]any {
	taskID := strings.TrimSpace(item.ID)
	if taskID == "" {
		return nil
	}
	meta := map[string]any{
		"schema":         coretool.RuntimeTaskMetaName,
		"schema_version": coretool.RuntimeTaskMetaVersion,
		"task_id":        taskID,
		"source":         firstNonEmpty(strings.TrimSpace(item.Source), "history"),
		"running":        item.Running,
		"supports_input": item.SupportsInput,
	}
	for key, value := range map[string]string{
		"task_kind":         item.Kind,
		"title":             item.Title,
		"backend":           item.Backend,
		"action":            item.Action,
		"state":             item.State,
		"command":           item.Command,
		"cwd":               item.CWD,
		"terminal_id":       item.TerminalID,
		"agent":             item.Agent,
		"remote_session_id": item.RemoteSessionID,
		"output_preview":    item.OutputPreview,
		"event_id":          item.EventID,
		"turn_id":           item.TurnID,
		"error":             item.Error,
	} {
		if text := strings.TrimSpace(value); text != "" {
			meta[key] = text
		}
	}
	if item.OutputTruncated {
		meta["output_truncated"] = true
	}
	if item.StdoutCursor > 0 {
		meta["stdout_cursor"] = item.StdoutCursor
	}
	if item.StderrCursor > 0 {
		meta["stderr_cursor"] = item.StderrCursor
	}
	if item.ExitCode != 0 {
		meta["exit_code"] = item.ExitCode
	}
	if !item.StartedAt.IsZero() {
		meta["started_at"] = item.StartedAt.Format(time.RFC3339Nano)
	}
	if !item.UpdatedAt.IsZero() {
		meta["updated_at"] = item.UpdatedAt.Format(time.RFC3339Nano)
	}
	return meta
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
	return coretool.RuntimeTaskMeta(meta)
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
	if next.OutputPreview != "" && (out.OutputPreview == "" || !next.UpdatedAt.Before(out.UpdatedAt)) {
		out.OutputPreview = next.OutputPreview
	}
	out.OutputTruncated = out.OutputTruncated || next.OutputTruncated
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
