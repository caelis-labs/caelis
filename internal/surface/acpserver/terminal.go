package acpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

type terminalRegistry struct {
	mu      sync.RWMutex
	records map[string]terminalRecord
}

type terminalRecord struct {
	taskID string
	limit  int
}

func (r *terminalRegistry) put(terminalID string, taskID string, limit int) {
	terminalID = strings.TrimSpace(terminalID)
	taskID = strings.TrimSpace(taskID)
	if terminalID == "" || taskID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.records == nil {
		r.records = map[string]terminalRecord{}
	}
	r.records[terminalID] = terminalRecord{taskID: taskID, limit: limit}
}

func (r *terminalRegistry) resolve(terminalID string) terminalRecord {
	terminalID = strings.TrimSpace(terminalID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.records != nil {
		if record, ok := r.records[terminalID]; ok {
			return record
		}
	}
	return terminalRecord{taskID: terminalID}
}

func (r *terminalRegistry) delete(terminalID string) {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.records, terminalID)
}

func terminalSessionCapabilities(enabled bool) map[string]json.RawMessage {
	if !enabled {
		return nil
	}
	return map[string]json.RawMessage{"terminal": json.RawMessage("{}")}
}

func (s *Server) terminalAvailable(ctx context.Context) bool {
	if s == nil || s.services.Engine() == nil {
		return false
	}
	status, err := s.services.Sandbox().Status(ctx)
	return err == nil && status.SandboxRuntimeConfigured
}

func (s *Server) createTerminal(ctx context.Context, req schema.CreateTerminalRequest) (schema.CreateTerminalResponse, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return schema.CreateTerminalResponse{}, errors.New("surface/acpserver: session id is required")
	}
	cwd, err := s.terminalCWD(ctx, sessionID, req.CWD)
	if err != nil {
		return schema.CreateTerminalResponse{}, err
	}
	view, err := s.services.Tasks().Start(ctx, appservices.TaskStartRequest{
		Command: strings.TrimSpace(req.Command),
		Args:    append([]string(nil), req.Args...),
		Dir:     cwd,
		Env:     terminalEnvMap(req.Env),
	})
	if err != nil {
		return schema.CreateTerminalResponse{}, err
	}
	terminalID := firstNonEmpty(view.Task.TerminalID, view.Task.ID)
	if terminalID == "" {
		return schema.CreateTerminalResponse{}, errors.New("surface/acpserver: terminal id is unavailable")
	}
	s.terminals.put(terminalID, view.Task.ID, terminalOutputLimit(req.OutputByteLimit))
	return schema.CreateTerminalResponse{TerminalID: terminalID}, nil
}

func (s *Server) terminalOutput(ctx context.Context, req schema.TerminalOutputRequest) (schema.TerminalOutputResponse, error) {
	record, err := s.terminalRecord(req.SessionID, req.TerminalID)
	if err != nil {
		return schema.TerminalOutputResponse{}, err
	}
	view, err := s.services.Tasks().Tail(ctx, appservices.TaskOutputRequest{TaskID: record.taskID})
	if err != nil {
		return schema.TerminalOutputResponse{}, err
	}
	output, limited := terminalOutputText(view, record.limit)
	return schema.TerminalOutputResponse{
		Output:     output,
		Truncated:  limited || view.StdoutDroppedBytes > 0 || view.StderrDroppedBytes > 0,
		ExitStatus: terminalExitStatus(view.Task),
	}, nil
}

func (s *Server) terminalWaitForExit(ctx context.Context, req schema.TerminalWaitForExitRequest) (schema.TerminalWaitForExitResponse, error) {
	record, err := s.terminalRecord(req.SessionID, req.TerminalID)
	if err != nil {
		return schema.TerminalWaitForExitResponse{}, err
	}
	view, err := s.services.Tasks().WaitForExit(ctx, appservices.TaskOutputRequest{TaskID: record.taskID})
	if err != nil {
		return schema.TerminalWaitForExitResponse{}, err
	}
	status := terminalExitStatus(view.Task)
	if status == nil {
		return schema.TerminalWaitForExitResponse{}, nil
	}
	return schema.TerminalWaitForExitResponse{
		ExitCode: status.ExitCode,
		Signal:   status.Signal,
	}, nil
}

func (s *Server) terminalKill(ctx context.Context, req schema.TerminalKillRequest) error {
	record, err := s.terminalRecord(req.SessionID, req.TerminalID)
	if err != nil {
		return err
	}
	_, err = s.services.Tasks().Cancel(ctx, appservices.TaskCancelRequest{
		TaskOutputRequest: appservices.TaskOutputRequest{TaskID: record.taskID},
	})
	return err
}

func (s *Server) terminalRelease(ctx context.Context, req schema.TerminalReleaseRequest) error {
	record, err := s.terminalRecord(req.SessionID, req.TerminalID)
	if err != nil {
		return err
	}
	if err := s.services.Tasks().Release(ctx, appservices.TaskOutputRequest{TaskID: record.taskID}); err != nil {
		return err
	}
	s.terminals.delete(req.TerminalID)
	return nil
}

func (s *Server) terminalRecord(sessionID string, terminalID string) (terminalRecord, error) {
	if strings.TrimSpace(sessionID) == "" {
		return terminalRecord{}, errors.New("surface/acpserver: session id is required")
	}
	if strings.TrimSpace(terminalID) == "" {
		return terminalRecord{}, errors.New("surface/acpserver: terminal id is required")
	}
	return s.terminals.resolve(terminalID), nil
}

func (s *Server) terminalCWD(ctx context.Context, sessionID string, cwd string) (string, error) {
	if cwd = strings.TrimSpace(cwd); cwd != "" {
		return cwd, nil
	}
	snapshot, err := s.loadSnapshot(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(snapshot.Session.Workspace.CWD), nil
}

func terminalEnvMap(in []schema.EnvVariable) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, item := range in {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		out[name] = item.Value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func terminalOutputLimit(limit *int) int {
	if limit == nil || *limit <= 0 {
		return 0
	}
	return *limit
}

func terminalOutputText(view appviewmodel.TaskOutputView, limit int) (string, bool) {
	output := view.Stdout
	if view.Stderr != "" {
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		output += view.Stderr
	}
	if limit <= 0 || len([]byte(output)) <= limit {
		return output, false
	}
	data := []byte(output)
	return string(data[len(data)-limit:]), true
}

func terminalExitStatus(task appviewmodel.TaskItem) *schema.TerminalExitStatus {
	if task.Running {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(task.State)) {
	case "", "running":
		return nil
	default:
		code := task.ExitCode
		return &schema.TerminalExitStatus{ExitCode: &code}
	}
}
