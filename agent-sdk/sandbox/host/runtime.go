package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/consoleoutput"
)

// Config defines one host-backed sandbox runtime.
type Config struct {
	CWD string
}

// Runtime is the minimal host-backed sandbox runtime implementation.
type Runtime struct {
	fs hostFS

	mu       sync.RWMutex
	sessions map[string]*hostSession
	status   sandbox.Status
}

// New returns one host-backed sandbox runtime.
func New(cfg Config) (*Runtime, error) {
	cwd := cfg.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		fs:       hostFS{cwd: cwd},
		sessions: map[string]*hostSession{},
		status: sandbox.Status{
			RequestedBackend: sandbox.BackendHost,
			ResolvedBackend:  sandbox.BackendHost,
		},
	}, nil
}

func (r *Runtime) FileSystem() sandbox.FileSystem {
	return r.fs
}

func (r *Runtime) FileSystemFor(_ sandbox.Constraints) sandbox.FileSystem {
	return r.fs
}

func (r *Runtime) Describe() sandbox.Descriptor {
	return sandbox.Descriptor{
		Backend:   sandbox.BackendHost,
		Isolation: sandbox.IsolationHost,
		Capabilities: sandbox.CapabilitySet{
			FileSystem:     true,
			CommandExec:    true,
			AsyncSessions:  true,
			TTY:            true,
			NetworkControl: false,
			PathPolicy:     false,
			EnvPolicy:      true,
		},
		DefaultConstraints: sandbox.Constraints{
			Route:      sandbox.RouteHost,
			Backend:    sandbox.BackendHost,
			Permission: sandbox.PermissionFullAccess,
			Isolation:  sandbox.IsolationHost,
			Network:    sandbox.NetworkInherit,
		},
	}
}

func (r *Runtime) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	req = sandbox.CloneRequest(req)
	dir := req.Dir
	if dir == "" {
		dir = r.fs.cwd
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	cmd := newShellCommand(runCtx, req.Command, len(req.Stdin) > 0)
	cmd.Dir = dir
	cmd.Env = mergeEnv(req.Env)
	if len(req.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}
	stdout := newCappedOutputBuffer(hostOutputCap)
	stderr := newCappedOutputBuffer(hostOutputCap)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()

	result := sandbox.CommandResult{
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
		Route:   sandbox.RouteHost,
		Backend: sandbox.BackendHost,
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if req.OnOutput != nil {
		if result.Stdout != "" {
			req.OnOutput(sandbox.OutputChunk{Stream: "stdout", Text: result.Stdout})
		}
		if result.Stderr != "" {
			req.OnOutput(sandbox.OutputChunk{Stream: "stderr", Text: result.Stderr})
		}
	}
	return result, err
}

func (r *Runtime) Start(ctx context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req = sandbox.CloneRequest(req)
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		dir = r.fs.cwd
	}
	sessionID, err := newID("exec")
	if err != nil {
		return nil, err
	}
	terminalID, err := newID("term")
	if err != nil {
		return nil, err
	}
	cmdCtx := context.WithoutCancel(ctx)
	cancel := func() {}
	if req.Timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(cmdCtx, req.Timeout)
	}
	cmd := newShellCommand(cmdCtx, req.Command, true)
	cmd.Dir = dir
	cmd.Env = mergeEnv(req.Env)
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("impl/sandbox/host: create stdin pipe: %w", err)
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		return nil, fmt.Errorf("impl/sandbox/host: create stdout pipe: %w", err)
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		cancel()
		return nil, fmt.Errorf("impl/sandbox/host: create stderr pipe: %w", err)
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		cancel()
		return nil, err
	}
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	now := time.Now()
	session := &hostSession{
		ref: sandbox.SessionRef{
			Backend:   sandbox.BackendHost,
			SessionID: sessionID,
		},
		terminal: sandbox.TerminalRef{
			Backend:    sandbox.BackendHost,
			SessionID:  sessionID,
			TerminalID: terminalID,
		},
		cmd:           cmd,
		stdin:         stdin,
		cancel:        cancel,
		running:       true,
		supportsInput: true,
		startedAt:     now,
		updatedAt:     now,
		done:          make(chan struct{}),
		onOutput:      req.OnOutput,
	}
	r.mu.Lock()
	r.sessions[sessionID] = session
	r.mu.Unlock()

	session.wg.Add(2)
	go session.readStream(stdout, "stdout")
	go session.readStream(stderr, "stderr")
	go session.waitForExit()

	return session, nil
}

func (r *Runtime) OpenSession(id string) (sandbox.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	session, ok := r.sessions[strings.TrimSpace(id)]
	if !ok {
		return nil, fmt.Errorf("impl/sandbox/host: session %q not found", id)
	}
	return session, nil
}

func (r *Runtime) OpenSessionRef(ref sandbox.SessionRef) (sandbox.Session, error) {
	ref = sandbox.CloneSessionRef(ref)
	if ref.Backend != "" && ref.Backend != sandbox.BackendHost {
		return nil, fmt.Errorf("impl/sandbox/host: backend %q is unsupported", ref.Backend)
	}
	return r.OpenSession(ref.SessionID)
}

func (r *Runtime) SupportedBackends() []sandbox.Backend {
	return []sandbox.Backend{sandbox.BackendHost}
}

func (r *Runtime) Status() sandbox.Status {
	return sandbox.CloneStatus(r.status)
}

func (r *Runtime) Close() error {
	r.mu.RLock()
	sessions := make([]*hostSession, 0, len(r.sessions))
	for _, session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.RUnlock()
	for _, session := range sessions {
		_ = session.Terminate(context.Background())
	}
	return nil
}

type hostSession struct {
	ref      sandbox.SessionRef
	terminal sandbox.TerminalRef

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc
	done   chan struct{}
	wg     sync.WaitGroup

	onOutput func(sandbox.OutputChunk)

	mu            sync.RWMutex
	stdout        []byte
	stderr        []byte
	stdoutTotal   int64
	stderrTotal   int64
	stdoutText    hostOutputDecoder
	stderrText    hostOutputDecoder
	running       bool
	supportsInput bool
	exitCode      int
	waitErr       error
	startedAt     time.Time
	updatedAt     time.Time
}

func (s *hostSession) Ref() sandbox.SessionRef {
	return sandbox.CloneSessionRef(s.ref)
}

func (s *hostSession) Terminal() sandbox.TerminalRef {
	return sandbox.CloneTerminalRef(s.terminal)
}

func (s *hostSession) WriteInput(_ context.Context, input []byte) error {
	s.mu.RLock()
	writer := s.stdin
	running := s.running
	s.mu.RUnlock()
	if !running {
		return fmt.Errorf("impl/sandbox/host: session %q is not running", s.ref.SessionID)
	}
	if writer == nil {
		return fmt.Errorf("impl/sandbox/host: session %q does not accept stdin", s.ref.SessionID)
	}
	if len(input) == 0 {
		return nil
	}
	_, err := writer.Write(input)
	return err
}

func (s *hostSession) ReadOutput(_ context.Context, stdoutMarker, stderrMarker int64) (stdout, stderr []byte, newStdoutMarker, newStderrMarker int64, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if stdoutMarker < 0 {
		stdoutMarker = 0
	}
	if stderrMarker < 0 {
		stderrMarker = 0
	}
	stdout, newStdoutMarker = consoleoutput.CappedOutputSince(s.stdout, s.stdoutTotal, stdoutMarker)
	stderr, newStderrMarker = consoleoutput.CappedOutputSince(s.stderr, s.stderrTotal, stderrMarker)
	return stdout, stderr, newStdoutMarker, newStderrMarker, nil
}

func (s *hostSession) Status(_ context.Context) (sandbox.SessionStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sandbox.CloneSessionStatus(sandbox.SessionStatus{
		SessionRef:    s.ref,
		Terminal:      s.terminal,
		Running:       s.running,
		SupportsInput: s.supportsInput,
		ExitCode:      s.exitCode,
		StartedAt:     s.startedAt,
		UpdatedAt:     s.updatedAt,
	}), nil
}

func (s *hostSession) Wait(ctx context.Context, timeout time.Duration) (sandbox.SessionStatus, error) {
	if timeout <= 0 {
		return s.Status(ctx)
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

func (s *hostSession) Result(_ context.Context) (sandbox.CommandResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := sandbox.CommandResult{
		Stdout:   string(s.stdout),
		Stderr:   string(s.stderr),
		ExitCode: s.exitCode,
		Route:    sandbox.RouteHost,
		Backend:  s.ref.Backend,
	}
	if s.running {
		return result, fmt.Errorf("impl/sandbox/host: session %q is still running", s.ref.SessionID)
	}
	return result, s.waitErr
}

func (s *hostSession) Terminate(_ context.Context) error {
	s.mu.RLock()
	cmd := s.cmd
	running := s.running
	s.mu.RUnlock()
	if !running || cmd == nil || cmd.Process == nil {
		return nil
	}
	s.cancel()
	return killProcessTree(cmd.Process)
}

func (s *hostSession) readStream(reader io.Reader, stream string) {
	defer s.wg.Done()
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	buf := make([]byte, 8192)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			var decoded consoleoutput.StreamChunk
			switch stream {
			case "stderr":
				decoded = s.stderrText.Decode(chunk)
				s.stderr = consoleoutput.AppendCappedBytes(s.stderr, decoded.Stored, hostOutputCap)
				s.stderrTotal += int64(len(decoded.Stored))
			default:
				decoded = s.stdoutText.Decode(chunk)
				s.stdout = consoleoutput.AppendCappedBytes(s.stdout, decoded.Stored, hostOutputCap)
				s.stdoutTotal += int64(len(decoded.Stored))
			}
			s.updatedAt = time.Now()
			s.mu.Unlock()
			if s.onOutput != nil && len(decoded.Emit) > 0 {
				s.onOutput(sandbox.OutputChunk{Stream: stream, Text: string(decoded.Emit)})
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *hostSession) waitForExit() {
	err := s.cmd.Wait()
	s.cleanupProcessGroupAfterExit()
	s.wg.Wait()

	s.mu.Lock()
	stdoutTail := s.stdoutText.Flush()
	stderrTail := s.stderrText.Flush()
	if len(stdoutTail.Stored) > 0 {
		s.stdout = consoleoutput.AppendCappedBytes(s.stdout, stdoutTail.Stored, hostOutputCap)
		s.stdoutTotal += int64(len(stdoutTail.Stored))
	}
	if len(stderrTail.Stored) > 0 {
		s.stderr = consoleoutput.AppendCappedBytes(s.stderr, stderrTail.Stored, hostOutputCap)
		s.stderrTotal += int64(len(stderrTail.Stored))
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	if s.cmd.ProcessState != nil {
		s.exitCode = s.cmd.ProcessState.ExitCode()
	}
	s.running = false
	s.updatedAt = time.Now()
	s.waitErr = err
	close(s.done)
	s.mu.Unlock()
	if s.onOutput != nil {
		if len(stdoutTail.Emit) > 0 {
			s.onOutput(sandbox.OutputChunk{Stream: "stdout", Text: string(stdoutTail.Emit)})
		}
		if len(stderrTail.Emit) > 0 {
			s.onOutput(sandbox.OutputChunk{Stream: "stderr", Text: string(stderrTail.Emit)})
		}
	}
}

func (s *hostSession) cleanupProcessGroupAfterExit() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = killProcessTree(s.cmd.Process)
}

type hostFS struct {
	cwd string
}

func (h hostFS) Getwd() (string, error) { return h.cwd, nil }

func (h hostFS) UserHomeDir() (string, error) { return os.UserHomeDir() }

func (h hostFS) Open(path string) (*os.File, error) { return os.Open(path) }

func (h hostFS) ReadDir(path string) ([]os.DirEntry, error) { return os.ReadDir(path) }

func (h hostFS) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }

func (h hostFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (h hostFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o644
	}
	return os.WriteFile(path, data, perm)
}

func (h hostFS) MkdirAll(path string, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o755
	}
	return os.MkdirAll(path, perm)
}

func (h hostFS) Glob(pattern string) ([]string, error) { return filepath.Glob(pattern) }

func (h hostFS) WalkDir(root string, fn fs.WalkDirFunc) error { return filepath.WalkDir(root, fn) }

func mergeEnv(extra map[string]string) []string {
	env := os.Environ()
	for key, value := range extra {
		if key == "" {
			continue
		}
		env = append(env, key+"="+value)
	}
	return env
}

const hostOutputCap = 1024 * 1024

var _ sandbox.Runtime = (*Runtime)(nil)
var _ sandbox.Session = (*hostSession)(nil)

type factory struct{}

func (factory) Backend() sandbox.Backend { return sandbox.BackendHost }

func (factory) Build(cfg sandbox.Config) (sandbox.Runtime, error) {
	return New(Config{CWD: cfg.CWD})
}

func init() {
	sandbox.RegisterBuiltInBackendFactory(factory{})
}

func newID(prefix string) (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(buf), nil
}
