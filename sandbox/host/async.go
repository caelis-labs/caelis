package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

func (b *Backend) Start(ctx context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if err := sandbox.CheckCommandConstraints(req.Dir, req.Constraints); err != nil {
		return nil, err
	}
	sessionID, err := newID("exec")
	if err != nil {
		return nil, err
	}
	terminalID, err := newID("term")
	if err != nil {
		return nil, err
	}

	runCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Second)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", req.Command)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	if req.Env != nil {
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("host start: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		return nil, fmt.Errorf("host start: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		return nil, fmt.Errorf("host start: stderr pipe: %w", err)
	}
	if len(req.Stdin) > 0 {
		go func() {
			_, _ = stdin.Write(req.Stdin)
			_ = stdin.Close()
		}()
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		cancel()
		return nil, err
	}

	now := time.Now()
	s := &session{
		ref: sandbox.SessionRef{
			Backend:   b.Name(),
			SessionID: sessionID,
		},
		terminal: sandbox.TerminalRef{
			Backend:    b.Name(),
			SessionID:  sessionID,
			TerminalID: terminalID,
		},
		cmd:           cmd,
		stdin:         stdin,
		cancel:        cancel,
		done:          make(chan struct{}),
		running:       true,
		supportsInput: true,
		startedAt:     now,
		updatedAt:     now,
	}

	b.mu.Lock()
	b.sessions[sessionID] = s
	b.mu.Unlock()

	s.wg.Add(2)
	go s.readStream(stdout, "stdout")
	go s.readStream(stderr, "stderr")
	go s.waitForExit(runCtx)
	return s, nil
}

func (b *Backend) OpenSessionRef(ref sandbox.SessionRef) (sandbox.Session, error) {
	if ref.Backend != "" && ref.Backend != b.Name() {
		return nil, fmt.Errorf("host session backend %q is unsupported", ref.Backend)
	}
	id := strings.TrimSpace(ref.SessionID)
	if id == "" {
		return nil, fmt.Errorf("host session id is required")
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	session := b.sessions[id]
	if session == nil {
		return nil, fmt.Errorf("host session %q not found", id)
	}
	return session, nil
}

type session struct {
	ref      sandbox.SessionRef
	terminal sandbox.TerminalRef

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc
	done   chan struct{}
	wg     sync.WaitGroup

	mu            sync.RWMutex
	stdout        []byte
	stderr        []byte
	stdoutTotal   int64
	stderrTotal   int64
	running       bool
	supportsInput bool
	exitCode      int
	err           error
	startedAt     time.Time
	updatedAt     time.Time
	endedAt       time.Time
}

func (s *session) Ref() sandbox.SessionRef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ref
}

func (s *session) Terminal() sandbox.TerminalRef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.terminal
}

func (s *session) WriteInput(_ context.Context, input []byte) error {
	s.mu.RLock()
	stdin := s.stdin
	running := s.running
	s.mu.RUnlock()
	if !running {
		return fmt.Errorf("host session %q is not running", s.ref.SessionID)
	}
	if stdin == nil {
		return fmt.Errorf("host session %q does not accept stdin", s.ref.SessionID)
	}
	if len(input) == 0 {
		return nil
	}
	_, err := stdin.Write(input)
	return err
}

func (s *session) ReadOutput(_ context.Context, stdoutMarker, stderrMarker int64) ([]byte, []byte, int64, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stdout, stdoutNext := outputSince(s.stdout, s.stdoutTotal, stdoutMarker)
	stderr, stderrNext := outputSince(s.stderr, s.stderrTotal, stderrMarker)
	return stdout, stderr, stdoutNext, stderrNext, nil
}

func (s *session) Status(_ context.Context) (sandbox.SessionStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sandbox.SessionStatus{
		SessionRef:    s.ref,
		Terminal:      s.terminal,
		Running:       s.running,
		SupportsInput: s.supportsInput,
		ExitCode:      s.exitCode,
		StartedAt:     s.startedAt,
		UpdatedAt:     s.updatedAt,
		EndedAt:       s.endedAt,
	}, nil
}

func (s *session) Wait(ctx context.Context, timeout time.Duration) (sandbox.SessionStatus, error) {
	if timeout <= 0 {
		select {
		case <-ctx.Done():
			return sandbox.SessionStatus{}, ctx.Err()
		case <-s.done:
			return s.Status(ctx)
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return sandbox.SessionStatus{}, ctx.Err()
	case <-s.done:
		return s.Status(ctx)
	case <-timer.C:
		return s.Status(ctx)
	}
}

func (s *session) Result(ctx context.Context) (sandbox.CommandResult, error) {
	select {
	case <-ctx.Done():
		return sandbox.CommandResult{}, ctx.Err()
	case <-s.done:
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sandbox.CommandResult{
		Stdout:   append([]byte(nil), s.stdout...),
		Stderr:   append([]byte(nil), s.stderr...),
		ExitCode: s.exitCode,
	}, s.err
}

func (s *session) Terminate(_ context.Context) error {
	s.mu.RLock()
	cmd := s.cmd
	running := s.running
	s.mu.RUnlock()
	if !running {
		return nil
	}
	s.cancel()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return nil
}

func (s *session) readStream(reader io.Reader, stream string) {
	defer s.wg.Done()
	buf := make([]byte, 8192)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			switch stream {
			case "stderr":
				s.stderr = append(s.stderr, chunk...)
				s.stderrTotal += int64(len(chunk))
			default:
				s.stdout = append(s.stdout, chunk...)
				s.stdoutTotal += int64(len(chunk))
			}
			s.updatedAt = time.Now()
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *session) waitForExit(ctx context.Context) {
	err := s.cmd.Wait()
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	if s.cmd.ProcessState != nil {
		s.exitCode = s.cmd.ProcessState.ExitCode()
	}
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	s.err = err
	s.running = false
	s.supportsInput = false
	s.updatedAt = time.Now()
	s.endedAt = s.updatedAt
	close(s.done)
}

func outputSince(buf []byte, total int64, marker int64) ([]byte, int64) {
	if marker < 0 {
		marker = 0
	}
	if marker > total {
		marker = total
	}
	base := total - int64(len(buf))
	if base < 0 {
		base = 0
	}
	if marker < base {
		marker = base
	}
	start := marker - base
	if start < 0 {
		start = 0
	}
	if start > int64(len(buf)) {
		start = int64(len(buf))
	}
	return append([]byte(nil), buf[start:]...), total
}

func newID(prefix string) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(buf[:]), nil
}

var _ sandbox.AsyncBackend = (*Backend)(nil)
var _ sandbox.Session = (*session)(nil)
