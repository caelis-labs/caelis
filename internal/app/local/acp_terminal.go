package local

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

type externalTerminalHandler struct {
	runtime    sandbox.Runtime
	defaultCWD string

	mu      sync.RWMutex
	records map[string]externalTerminalRecord
}

type externalTerminalRecord struct {
	taskID string
	limit  int
}

func newExternalTerminalHandler(rt sandbox.Runtime, defaultCWD string) *externalTerminalHandler {
	if rt == nil {
		return nil
	}
	return &externalTerminalHandler{
		runtime:    rt,
		defaultCWD: strings.TrimSpace(defaultCWD),
		records:    map[string]externalTerminalRecord{},
	}
}

func (h *externalTerminalHandler) CreateTerminal(ctx context.Context, req schema.CreateTerminalRequest) (schema.CreateTerminalResponse, error) {
	if h == nil || h.runtime == nil {
		return schema.CreateTerminalResponse{}, errors.New("app/local: terminal runtime is not configured")
	}
	command := externalTerminalCommandLine(req.Command, req.Args)
	if command == "" {
		return schema.CreateTerminalResponse{}, errors.New("app/local: terminal command is required")
	}
	sess, err := h.runtime.Start(ctx, sandbox.CommandRequest{
		Command: command,
		Dir:     firstNonEmpty(strings.TrimSpace(req.CWD), h.defaultCWD),
		Env:     externalTerminalEnv(req.Env),
	})
	if err != nil {
		return schema.CreateTerminalResponse{}, err
	}
	snapshot, err := sess.Snapshot(ctx)
	if err != nil {
		_ = sess.Close()
		return schema.CreateTerminalResponse{}, err
	}
	taskID := strings.TrimSpace(sess.Ref().ID)
	terminalID := firstNonEmpty(snapshot.Terminal.ID, snapshot.Terminal.SessionID, taskID)
	if taskID == "" || terminalID == "" {
		_ = sess.Close()
		return schema.CreateTerminalResponse{}, errors.New("app/local: terminal session id is unavailable")
	}
	h.put(terminalID, externalTerminalRecord{
		taskID: taskID,
		limit:  externalTerminalOutputLimit(req.OutputByteLimit),
	})
	return schema.CreateTerminalResponse{TerminalID: terminalID}, nil
}

func (h *externalTerminalHandler) TerminalOutput(ctx context.Context, req schema.TerminalOutputRequest) (schema.TerminalOutputResponse, error) {
	sess, record, err := h.open(ctx, req.TerminalID)
	if err != nil {
		return schema.TerminalOutputResponse{}, err
	}
	snapshot, err := sess.Snapshot(ctx)
	if err != nil {
		return schema.TerminalOutputResponse{}, err
	}
	output, err := sess.Read(ctx, sandbox.OutputCursor{})
	if err != nil {
		return schema.TerminalOutputResponse{}, err
	}
	text, limited := externalTerminalOutputText(output.Stdout, output.Stderr, record.limit)
	return schema.TerminalOutputResponse{
		Output:     text,
		Truncated:  limited || output.StdoutDroppedBytes > 0 || output.StderrDroppedBytes > 0,
		ExitStatus: externalTerminalExitStatus(snapshot),
	}, nil
}

func (h *externalTerminalHandler) TerminalWaitForExit(ctx context.Context, req schema.TerminalWaitForExitRequest) (schema.TerminalWaitForExitResponse, error) {
	sess, _, err := h.open(ctx, req.TerminalID)
	if err != nil {
		return schema.TerminalWaitForExitResponse{}, err
	}
	result, err := sess.Wait(ctx)
	if err != nil {
		return schema.TerminalWaitForExitResponse{}, err
	}
	code := result.ExitCode
	return schema.TerminalWaitForExitResponse{ExitCode: &code}, nil
}

func (h *externalTerminalHandler) TerminalKill(ctx context.Context, req schema.TerminalKillRequest) error {
	sess, _, err := h.open(ctx, req.TerminalID)
	if err != nil {
		return err
	}
	return sess.Cancel(ctx)
}

func (h *externalTerminalHandler) TerminalRelease(ctx context.Context, req schema.TerminalReleaseRequest) error {
	sess, _, err := h.open(ctx, req.TerminalID)
	if err != nil {
		return err
	}
	snapshot, err := sess.Snapshot(ctx)
	if err != nil {
		return err
	}
	if !snapshot.Running {
		if err := sess.Close(); err != nil {
			return err
		}
	}
	h.delete(req.TerminalID)
	return nil
}

func (h *externalTerminalHandler) open(ctx context.Context, terminalID string) (sandbox.Session, externalTerminalRecord, error) {
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return nil, externalTerminalRecord{}, errors.New("app/local: terminal id is required")
	}
	record := h.resolve(terminalID)
	if record.taskID == "" {
		record.taskID = terminalID
	}
	sess, err := h.runtime.Open(ctx, sandbox.SessionRef{ID: record.taskID})
	if err != nil {
		return nil, externalTerminalRecord{}, err
	}
	return sess, record, nil
}

func (h *externalTerminalHandler) put(terminalID string, record externalTerminalRecord) {
	terminalID = strings.TrimSpace(terminalID)
	record.taskID = strings.TrimSpace(record.taskID)
	if h == nil || terminalID == "" || record.taskID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.records == nil {
		h.records = map[string]externalTerminalRecord{}
	}
	h.records[terminalID] = record
}

func (h *externalTerminalHandler) resolve(terminalID string) externalTerminalRecord {
	terminalID = strings.TrimSpace(terminalID)
	if h == nil || terminalID == "" {
		return externalTerminalRecord{}
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.records == nil {
		return externalTerminalRecord{taskID: terminalID}
	}
	if record, ok := h.records[terminalID]; ok {
		return record
	}
	return externalTerminalRecord{taskID: terminalID}
}

func (h *externalTerminalHandler) delete(terminalID string) {
	terminalID = strings.TrimSpace(terminalID)
	if h == nil || terminalID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.records, terminalID)
}

func externalTerminalCommandLine(command string, args []string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if len(args) == 0 {
		return command
	}
	parts := []string{quoteExternalTerminalArg(command)}
	for _, arg := range args {
		parts = append(parts, quoteExternalTerminalArg(arg))
	}
	return strings.Join(parts, " ")
}

func quoteExternalTerminalArg(value string) string {
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

func externalTerminalEnv(in []schema.EnvVariable) map[string]string {
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

func externalTerminalOutputLimit(limit *int) int {
	if limit == nil || *limit <= 0 {
		return 0
	}
	return *limit
}

func externalTerminalOutputText(stdout string, stderr string, limit int) (string, bool) {
	output := stdout
	if stderr != "" {
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		output += stderr
	}
	if limit <= 0 || len([]byte(output)) <= limit {
		return output, false
	}
	data := []byte(output)
	return string(data[len(data)-limit:]), true
}

func externalTerminalExitStatus(snapshot sandbox.SessionSnapshot) *schema.TerminalExitStatus {
	if snapshot.Running || snapshot.State == sandbox.SessionRunning || snapshot.State == "" {
		return nil
	}
	code := snapshot.ExitCode
	return &schema.TerminalExitStatus{ExitCode: &code}
}

var _ acpexternal.TerminalHandler = (*externalTerminalHandler)(nil)
