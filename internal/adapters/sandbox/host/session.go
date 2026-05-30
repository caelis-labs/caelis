package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
)

const sessionOutputBufferCap = 1024 * 1024

var sessionSeq atomic.Uint64

type commandSession struct {
	ref      sandbox.SessionRef
	command  string
	dir      string
	terminal sandbox.TerminalRef

	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
	stdin  io.WriteCloser

	stdout *sessionOutputBuffer
	stderr *sessionOutputBuffer

	mu        sync.RWMutex
	readers   sync.WaitGroup
	running   bool
	state     sandbox.SessionState
	exitCode  int
	errText   string
	startedAt time.Time
	updatedAt time.Time
	done      chan struct{}
	closeOnce sync.Once

	req sandbox.CommandRequest
}

func newCommandSession(dir string, req sandbox.CommandRequest) *commandSession {
	id := fmt.Sprintf("host-%d-%d", time.Now().UnixNano(), sessionSeq.Add(1))
	now := time.Now().UTC()
	return &commandSession{
		ref:       sandbox.SessionRef{ID: id, Backend: sandbox.BackendHost},
		command:   strings.TrimSpace(req.Command),
		dir:       strings.TrimSpace(dir),
		terminal:  sandbox.TerminalRef{ID: id, SessionID: id},
		stdout:    newSessionOutputBuffer(sessionOutputBufferCap),
		stderr:    newSessionOutputBuffer(sessionOutputBufferCap),
		running:   true,
		state:     sandbox.SessionRunning,
		exitCode:  -1,
		startedAt: now,
		updatedAt: now,
		done:      make(chan struct{}),
		req:       req,
	}
}

func (s *commandSession) start() error {
	baseCtx := context.Background()
	if s.req.Timeout > 0 {
		s.ctx, s.cancel = context.WithTimeout(baseCtx, s.req.Timeout)
	} else {
		s.ctx, s.cancel = context.WithCancel(baseCtx)
	}
	cmd := exec.CommandContext(s.ctx, shellName(), shellArgs(s.command)...)
	cmd.Dir = s.dir
	if len(s.req.Env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range s.req.Env {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		s.cancel()
		return fmt.Errorf("sandbox/host: create stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		s.cancel()
		return fmt.Errorf("sandbox/host: create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		s.cancel()
		return fmt.Errorf("sandbox/host: create stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		s.cancel()
		return fmt.Errorf("sandbox/host: start async command: %w", err)
	}
	s.mu.Lock()
	s.cmd = cmd
	s.stdin = stdin
	s.touchLocked()
	s.mu.Unlock()

	s.readers.Add(2)
	go s.copyOutput(stdout, "stdout", s.stdout)
	go s.copyOutput(stderr, "stderr", s.stderr)
	go s.waitForExit()

	if len(s.req.Stdin) > 0 {
		if _, err := stdin.Write(s.req.Stdin); err != nil {
			_ = s.Close()
			return fmt.Errorf("sandbox/host: write initial stdin: %w", err)
		}
	}
	return nil
}

func (s *commandSession) Ref() sandbox.SessionRef {
	return s.ref
}

func (s *commandSession) Snapshot(ctx context.Context) (sandbox.SessionSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return sandbox.SessionSnapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked(), nil
}

func (s *commandSession) Read(ctx context.Context, cursor sandbox.OutputCursor) (sandbox.OutputSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return sandbox.OutputSnapshot{}, err
	}
	stdout, nextStdout, stdoutDropped := s.stdout.readSince(cursor.Stdout)
	stderr, nextStderr, stderrDropped := s.stderr.readSince(cursor.Stderr)
	return sandbox.OutputSnapshot{
		Stdout: string(stdout),
		Stderr: string(stderr),
		Cursor: sandbox.OutputCursor{
			Stdout: nextStdout,
			Stderr: nextStderr,
		},
		StdoutDroppedBytes: stdoutDropped,
		StderrDroppedBytes: stderrDropped,
	}, nil
}

func (s *commandSession) Write(ctx context.Context, input []byte) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.RLock()
	running := s.running
	stdin := s.stdin
	s.mu.RUnlock()
	if !running {
		return errors.New("sandbox/host: session is not running")
	}
	if stdin == nil {
		return errors.New("sandbox/host: session stdin is unavailable")
	}
	if _, err := stdin.Write(input); err != nil {
		return fmt.Errorf("sandbox/host: write stdin: %w", err)
	}
	s.mu.Lock()
	s.touchLocked()
	s.mu.Unlock()
	return nil
}

func (s *commandSession) Cancel(ctx context.Context) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.state = sandbox.SessionCancelled
	s.errText = "cancelled"
	s.touchLocked()
	cancel := s.cancel
	cmd := s.cmd
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	return nil
}

func (s *commandSession) Wait(ctx context.Context) (sandbox.CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.done:
		return s.result(), nil
	case <-ctx.Done():
		return sandbox.CommandResult{}, ctx.Err()
	}
}

func (s *commandSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.Cancel(context.Background())
	})
	return err
}

func (s *commandSession) copyOutput(reader io.Reader, stream string, buffer *sessionOutputBuffer) {
	defer s.readers.Done()
	buf := make([]byte, 8192)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			buffer.write(chunk)
			s.mu.Lock()
			s.touchLocked()
			s.mu.Unlock()
			if s.req.OnOutput != nil {
				s.req.OnOutput(sandbox.OutputChunk{Stream: stream, Text: string(chunk)})
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *commandSession) waitForExit() {
	err := s.cmd.Wait()
	s.readers.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.state == sandbox.SessionCancelled {
		s.exitCode = -1
		s.touchLocked()
		close(s.done)
		return
	}
	s.exitCode = 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			s.exitCode = exitErr.ExitCode()
			s.state = sandbox.SessionFailed
			s.errText = err.Error()
		} else if ctxErr := s.ctx.Err(); ctxErr != nil {
			s.exitCode = -1
			s.state = sandbox.SessionFailed
			s.errText = ctxErr.Error()
		} else {
			s.exitCode = -1
			s.state = sandbox.SessionFailed
			s.errText = err.Error()
		}
	} else {
		s.state = sandbox.SessionCompleted
	}
	s.touchLocked()
	close(s.done)
}

func (s *commandSession) result() sandbox.CommandResult {
	s.mu.RLock()
	exitCode := s.exitCode
	errText := s.errText
	s.mu.RUnlock()
	return sandbox.CommandResult{
		Stdout:   string(s.stdout.readAll()),
		Stderr:   string(s.stderr.readAll()),
		Error:    errText,
		ExitCode: exitCode,
		Route:    sandbox.RouteHost,
		Backend:  sandbox.BackendHost,
	}
}

func (s *commandSession) snapshotLocked() sandbox.SessionSnapshot {
	return sandbox.SessionSnapshot{
		Ref:           s.ref,
		Command:       s.command,
		Dir:           s.dir,
		State:         s.state,
		Running:       s.running,
		SupportsInput: s.running && s.stdin != nil,
		ExitCode:      s.exitCode,
		Error:         s.errText,
		StartedAt:     s.startedAt,
		UpdatedAt:     s.updatedAt,
		Terminal:      s.terminal,
	}
}

func (s *commandSession) touchLocked() {
	s.updatedAt = time.Now().UTC()
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

type sessionOutputBuffer struct {
	mu       sync.RWMutex
	data     []byte
	capacity int
	total    int64
	dropped  int64
}

func newSessionOutputBuffer(capacity int) *sessionOutputBuffer {
	if capacity <= 0 {
		capacity = sessionOutputBufferCap
	}
	return &sessionOutputBuffer{capacity: capacity}
}

func (b *sessionOutputBuffer) write(data []byte) {
	if len(data) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += int64(len(data))
	b.data = append(b.data, data...)
	if len(b.data) > b.capacity {
		drop := len(b.data) - b.capacity
		b.data = append([]byte(nil), b.data[drop:]...)
		b.dropped += int64(drop)
	}
}

func (b *sessionOutputBuffer) readSince(marker int64) ([]byte, int64, int64) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if marker < 0 {
		marker = 0
	}
	earliest := b.total - int64(len(b.data))
	dropped := int64(0)
	if marker < earliest {
		dropped = earliest - marker
		marker = earliest
	}
	if marker >= b.total {
		return nil, b.total, dropped
	}
	start := int(marker - earliest)
	out := append([]byte(nil), b.data[start:]...)
	return out, b.total, dropped
}

func (b *sessionOutputBuffer) readAll() []byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]byte(nil), b.data...)
}

var _ sandbox.Session = (*commandSession)(nil)
