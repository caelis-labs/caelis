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
	"syscall"
	"time"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
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
	status   sdksandbox.Status
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
		status: sdksandbox.Status{
			RequestedBackend: sdksandbox.BackendHost,
			ResolvedBackend:  sdksandbox.BackendHost,
		},
	}, nil
}

func (r *Runtime) FileSystem() sdksandbox.FileSystem {
	return r.fs
}

func (r *Runtime) FileSystemFor(_ sdksandbox.Constraints) sdksandbox.FileSystem {
	return r.fs
}

func (r *Runtime) Describe() sdksandbox.Descriptor {
	return sdksandbox.Descriptor{
		Backend:   sdksandbox.BackendHost,
		Isolation: sdksandbox.IsolationHost,
		Capabilities: sdksandbox.CapabilitySet{
			FileSystem:     true,
			CommandExec:    true,
			AsyncSessions:  true,
			TTY:            true,
			NetworkControl: false,
			PathPolicy:     false,
			EnvPolicy:      true,
		},
		DefaultConstraints: sdksandbox.Constraints{
			Route:      sdksandbox.RouteHost,
			Backend:    sdksandbox.BackendHost,
			Permission: sdksandbox.PermissionFullAccess,
			Isolation:  sdksandbox.IsolationHost,
			Network:    sdksandbox.NetworkInherit,
		},
	}
}

func (r *Runtime) Run(ctx context.Context, req sdksandbox.CommandRequest) (sdksandbox.CommandResult, error) {
	req = sdksandbox.CloneRequest(req)
	constraints := sdksandbox.EffectiveConstraints(req)
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

	cmd := exec.CommandContext(runCtx, "/bin/sh", "-lc", req.Command)
	cmd.Dir = dir
	cmd.Env = mergeEnv(req.Env)
	if len(req.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	result := sdksandbox.CommandResult{
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
		Route:   firstNonEmptyRoute(constraints.Route, sdksandbox.RouteHost),
		Backend: firstNonEmptyBackend(constraints.Backend, sdksandbox.BackendHost),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if req.OnOutput != nil {
		if result.Stdout != "" {
			req.OnOutput(sdksandbox.OutputChunk{Stream: "stdout", Text: result.Stdout})
		}
		if result.Stderr != "" {
			req.OnOutput(sdksandbox.OutputChunk{Stream: "stderr", Text: result.Stderr})
		}
	}
	return result, err
}

func (r *Runtime) Start(ctx context.Context, req sdksandbox.CommandRequest) (sdksandbox.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req = sdksandbox.CloneRequest(req)
	constraints := sdksandbox.EffectiveConstraints(req)
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
	cmd := exec.CommandContext(cmdCtx, "/bin/sh", "-lc", req.Command)
	cmd.Dir = dir
	cmd.Env = mergeEnv(req.Env)
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("sdk/sandbox/host: create stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("sdk/sandbox/host: create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("sdk/sandbox/host: create stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	now := time.Now()
	session := &hostSession{
		ref: sdksandbox.SessionRef{
			Backend:   firstNonEmptyBackend(constraints.Backend, sdksandbox.BackendHost),
			SessionID: sessionID,
		},
		terminal: sdksandbox.TerminalRef{
			Backend:    firstNonEmptyBackend(constraints.Backend, sdksandbox.BackendHost),
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

func (r *Runtime) OpenSession(id string) (sdksandbox.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	session, ok := r.sessions[strings.TrimSpace(id)]
	if !ok {
		return nil, fmt.Errorf("sdk/sandbox/host: session %q not found", id)
	}
	return session, nil
}

func (r *Runtime) OpenSessionRef(ref sdksandbox.SessionRef) (sdksandbox.Session, error) {
	ref = sdksandbox.CloneSessionRef(ref)
	if ref.Backend != "" && ref.Backend != sdksandbox.BackendHost {
		return nil, fmt.Errorf("sdk/sandbox/host: backend %q is unsupported", ref.Backend)
	}
	return r.OpenSession(ref.SessionID)
}

func (r *Runtime) SupportedBackends() []sdksandbox.Backend {
	return []sdksandbox.Backend{sdksandbox.BackendHost}
}

func (r *Runtime) Status() sdksandbox.Status {
	return r.status
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
	ref      sdksandbox.SessionRef
	terminal sdksandbox.TerminalRef

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc
	done   chan struct{}
	wg     sync.WaitGroup

	onOutput func(sdksandbox.OutputChunk)

	mu            sync.RWMutex
	stdout        []byte
	stderr        []byte
	running       bool
	supportsInput bool
	exitCode      int
	waitErr       error
	startedAt     time.Time
	updatedAt     time.Time
}

func (s *hostSession) Ref() sdksandbox.SessionRef {
	return sdksandbox.CloneSessionRef(s.ref)
}

func (s *hostSession) Terminal() sdksandbox.TerminalRef {
	return sdksandbox.CloneTerminalRef(s.terminal)
}

func (s *hostSession) WriteInput(_ context.Context, input []byte) error {
	s.mu.RLock()
	writer := s.stdin
	running := s.running
	s.mu.RUnlock()
	if !running {
		return fmt.Errorf("sdk/sandbox/host: session %q is not running", s.ref.SessionID)
	}
	if writer == nil {
		return fmt.Errorf("sdk/sandbox/host: session %q does not accept stdin", s.ref.SessionID)
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
	if stdoutMarker > int64(len(s.stdout)) {
		stdoutMarker = int64(len(s.stdout))
	}
	if stderrMarker > int64(len(s.stderr)) {
		stderrMarker = int64(len(s.stderr))
	}
	stdout = append([]byte(nil), s.stdout[stdoutMarker:]...)
	stderr = append([]byte(nil), s.stderr[stderrMarker:]...)
	return stdout, stderr, int64(len(s.stdout)), int64(len(s.stderr)), nil
}

func (s *hostSession) Status(_ context.Context) (sdksandbox.SessionStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sdksandbox.CloneSessionStatus(sdksandbox.SessionStatus{
		SessionRef:    s.ref,
		Terminal:      s.terminal,
		Running:       s.running,
		SupportsInput: s.supportsInput,
		ExitCode:      s.exitCode,
		StartedAt:     s.startedAt,
		UpdatedAt:     s.updatedAt,
	}), nil
}

func (s *hostSession) Wait(ctx context.Context, timeout time.Duration) (sdksandbox.SessionStatus, error) {
	if timeout <= 0 {
		return s.Status(ctx)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return sdksandbox.SessionStatus{}, ctx.Err()
	case <-s.done:
		return s.Status(ctx)
	case <-timer.C:
		return s.Status(ctx)
	}
}

func (s *hostSession) Result(_ context.Context) (sdksandbox.CommandResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := sdksandbox.CommandResult{
		Stdout:   string(s.stdout),
		Stderr:   string(s.stderr),
		ExitCode: s.exitCode,
		Route:    sdksandbox.RouteHost,
		Backend:  s.ref.Backend,
	}
	if s.running {
		return result, fmt.Errorf("sdk/sandbox/host: session %q is still running", s.ref.SessionID)
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
	buf := make([]byte, 8192)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			switch stream {
			case "stderr":
				s.stderr = append(s.stderr, chunk...)
			default:
				s.stdout = append(s.stdout, chunk...)
			}
			s.updatedAt = time.Now()
			s.mu.Unlock()
			if s.onOutput != nil {
				s.onOutput(sdksandbox.OutputChunk{Stream: stream, Text: string(chunk)})
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *hostSession) waitForExit() {
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
	s.running = false
	s.updatedAt = time.Now()
	s.waitErr = err
	close(s.done)
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

var _ sdksandbox.Runtime = (*Runtime)(nil)
var _ sdksandbox.Session = (*hostSession)(nil)

type factory struct{}

func (factory) Backend() sdksandbox.Backend { return sdksandbox.BackendHost }

func (factory) Build(cfg sdksandbox.Config) (sdksandbox.Runtime, error) {
	return New(Config{CWD: cfg.CWD})
}

func init() {
	if err := sdksandbox.RegisterBackendFactory(factory{}); err != nil {
		panic(err)
	}
}

func firstNonEmptyRoute(values ...sdksandbox.Route) sdksandbox.Route {
	for _, value := range values {
		if strings.TrimSpace(string(value)) != "" {
			return value
		}
	}
	return ""
}

func firstNonEmptyBackend(values ...sdksandbox.Backend) sdksandbox.Backend {
	for _, value := range values {
		if strings.TrimSpace(string(value)) != "" {
			return value
		}
	}
	return ""
}

func newID(prefix string) (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(buf), nil
}

func setProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessTree(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return proc.Kill()
}
