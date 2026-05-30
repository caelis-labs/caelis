package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type TaskService struct {
	services Services
}

type ListTasksRequest struct {
	Limit int `json:"limit,omitempty"`
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
	lister, ok := s.services.sandbox.(sandbox.SessionLister)
	if s.services.sandbox == nil || !ok {
		return appviewmodel.TaskListView{}, nil
	}
	if req.Limit < 0 {
		req.Limit = 0
	}
	snapshots, err := lister.ListSessions(ctx, sandbox.SessionListQuery{Limit: req.Limit})
	if err != nil {
		return appviewmodel.TaskListView{}, err
	}
	view := appviewmodel.TaskListView{
		Supported: true,
		Tasks:     make([]appviewmodel.TaskItem, 0, len(snapshots)),
	}
	for _, snapshot := range snapshots {
		view.Tasks = append(view.Tasks, appviewmodel.TaskItemFromSnapshot(snapshot))
	}
	view.Count = len(view.Tasks)
	return view, nil
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
