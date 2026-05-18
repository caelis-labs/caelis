package runnerclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/cmdsession"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/runnerruntime"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/textstream"
	winpolicy "github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/policy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/runnerproto"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

type Config struct {
	Executable     string
	ExecutablePath func(runnerruntime.Request) (string, error)
	Args           []string
	Dir            string
	Policy         func(runnerruntime.Request) (winpolicy.Policy, error)
	Credentials    func(runnerruntime.Request) (Credentials, error)
}

type Credentials struct {
	Username string
	Domain   string
	Password string
}

type Client struct {
	executable     string
	executablePath func(runnerruntime.Request) (string, error)
	args           []string
	dir            string
	policy         func(runnerruntime.Request) (winpolicy.Policy, error)
	credentials    func(runnerruntime.Request) (Credentials, error)

	mu       sync.RWMutex
	sessions map[string]*session
}

func New(cfg Config) *Client {
	return &Client{
		executable:     strings.TrimSpace(cfg.Executable),
		executablePath: cfg.ExecutablePath,
		args:           append([]string(nil), cfg.Args...),
		dir:            strings.TrimSpace(cfg.Dir),
		policy:         cfg.Policy,
		credentials:    cfg.Credentials,
		sessions:       map[string]*session{},
	}
}

func (c *Client) Run(ctx context.Context, req runnerruntime.Request) (sandbox.CommandResult, error) {
	s, err := c.start(ctx, req, false)
	if err != nil {
		return sandbox.CommandResult{}, err
	}
	if err := s.sendInitialStdin(req.Stdin); err != nil {
		_ = s.TerminateSession()
		return sandbox.CommandResult{}, err
	}
	result, err := s.WaitResult(ctx, 0)
	c.removeSession(s.id)
	return result, err
}

func (c *Client) StartAsync(ctx context.Context, req runnerruntime.Request) (string, error) {
	s, err := c.start(ctx, req, true)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.sessions[s.id] = s
	c.mu.Unlock()
	return s.id, nil
}

func (c *Client) WriteInput(sessionID string, input []byte) error {
	s, err := c.get(sessionID)
	if err != nil {
		return err
	}
	if !s.isRunning() {
		return fmt.Errorf("windows runner: session %q is not running", sessionID)
	}
	frame, err := runnerproto.NewFrame(runnerproto.TypeStdin, runnerproto.Bytes{Data: input})
	if err != nil {
		return err
	}
	if err := s.write(frame); err != nil {
		if !s.isRunning() {
			return fmt.Errorf("windows runner: session %q is not running", sessionID)
		}
		return err
	}
	return nil
}

func (s *session) isRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

func (c *Client) ReadOutput(sessionID string, stdoutMarker, stderrMarker int64) ([]byte, []byte, int64, int64, error) {
	s, err := c.get(sessionID)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	return s.readOutput(stdoutMarker, stderrMarker)
}

func (c *Client) GetSessionStatus(sessionID string) (cmdsession.SessionStatus, error) {
	s, err := c.get(sessionID)
	if err != nil {
		return cmdsession.SessionStatus{}, err
	}
	return s.status(), nil
}

func (c *Client) WaitSession(ctx context.Context, sessionID string, timeout time.Duration) (sandbox.CommandResult, error) {
	s, err := c.get(sessionID)
	if err != nil {
		return sandbox.CommandResult{}, err
	}
	return s.WaitResult(ctx, timeout)
}

func (c *Client) TerminateSession(sessionID string) error {
	s, err := c.get(sessionID)
	if err != nil {
		return err
	}
	return s.TerminateSession()
}

func (c *Client) Close() error {
	c.mu.RLock()
	sessions := make([]*session, 0, len(c.sessions))
	for _, s := range c.sessions {
		sessions = append(sessions, s)
	}
	c.mu.RUnlock()
	for _, s := range sessions {
		_ = s.TerminateSession()
	}
	return nil
}

func (c *Client) start(ctx context.Context, req runnerruntime.Request, stdinOpen bool) (*session, error) {
	id, err := newID("win")
	if err != nil {
		return nil, err
	}
	proc, err := c.launch(ctx, req)
	if err != nil {
		return nil, err
	}
	stdin := proc.Stdin()
	stdout := proc.Stdout()
	stderr := proc.Stderr()
	if stdin == nil || stdout == nil || stderr == nil {
		_ = proc.Kill()
		return nil, fmt.Errorf("windows runner: launcher returned incomplete pipes")
	}

	s := &session{
		id:        id,
		process:   proc,
		writer:    runnerproto.NewWriter(stdin),
		stdin:     stdin,
		reader:    runnerproto.NewReader(stdout),
		onOutput:  req.OnOutput,
		startedAt: time.Now(),
		updatedAt: time.Now(),
		running:   true,
		exitCode:  -1,
		done:      make(chan struct{}),
	}
	go s.captureRunnerStderr(stderr)

	hello, err := s.reader.ReadFrame()
	if err != nil {
		_ = s.TerminateSession()
		return nil, fmt.Errorf("windows runner: handshake failed: %w", err)
	}
	if hello.Type != runnerproto.TypeHello {
		_ = s.TerminateSession()
		return nil, fmt.Errorf("windows runner: expected hello frame, got %q", hello.Type)
	}

	p := winpolicy.Policy{}
	if c.policy != nil {
		p, err = c.policy(req)
		if err != nil {
			_ = s.TerminateSession()
			return nil, err
		}
	}
	attachCapabilitySIDs := p.CapabilitySIDs
	if os.Getenv("CAELIS_WINDOWS_SANDBOX_ATTACH_CAPS") == "0" {
		attachCapabilitySIDs = nil
	}
	spawn, err := runnerproto.NewFrame(runnerproto.TypeSpawn, runnerproto.Spawn{
		Command:       req.Command,
		CWD:           req.Dir,
		Env:           req.EnvOverrides,
		Timeout:       req.Timeout,
		TTY:           req.TTY,
		StdinOpen:     stdinOpen || len(req.Stdin) > 0,
		ReadRoots:     p.ReadRoots,
		WriteRoots:    p.WriteRoots,
		DenyRead:      p.DenyReadPaths,
		DenyWrite:     p.DenyWritePaths,
		Network:       string(p.Network),
		CapabilitySID: attachCapabilitySIDs,
	})
	if err != nil {
		_ = s.TerminateSession()
		return nil, err
	}
	if err := s.write(spawn); err != nil {
		_ = s.TerminateSession()
		return nil, err
	}
	go s.readLoop()
	return s, nil
}

func (c *Client) launch(ctx context.Context, req runnerruntime.Request) (process, error) {
	executable, err := c.resolveExecutable(req)
	if err != nil {
		return nil, err
	}
	creds := Credentials{}
	if c.credentials != nil {
		creds, err = c.credentials(req)
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(creds.Username) != "" {
		return win32.StartProcessWithLogon(win32.LogonCredentials{
			Username: creds.Username,
			Domain:   creds.Domain,
			Password: creds.Password,
		}, executable, c.args, c.dir)
	}
	return startExecProcess(context.WithoutCancel(ctx), executable, c.args, c.dir)
}

func (c *Client) resolveExecutable(req runnerruntime.Request) (string, error) {
	if c.executablePath != nil {
		executable, err := c.executablePath(req)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(executable) != "" {
			return strings.TrimSpace(executable), nil
		}
	}
	if strings.TrimSpace(c.executable) == "" {
		return "", fmt.Errorf("windows runner: executable path is required")
	}
	return strings.TrimSpace(c.executable), nil
}

func (c *Client) get(sessionID string) (*session, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.sessions[strings.TrimSpace(sessionID)]
	if !ok {
		return nil, cmdsession.ErrSessionNotFound
	}
	return s, nil
}

func (c *Client) removeSession(sessionID string) {
	c.mu.Lock()
	delete(c.sessions, strings.TrimSpace(sessionID))
	c.mu.Unlock()
}

type session struct {
	id      string
	process process
	writer  *runnerproto.Writer
	stdin   io.WriteCloser
	reader  *runnerproto.Reader

	onOutput func(runnerruntime.OutputChunk)

	mu           sync.RWMutex
	writeMu      sync.Mutex
	stdout       []byte
	stderr       []byte
	stdoutTotal  int64
	stderrTotal  int64
	runnerStderr bytes.Buffer
	stdoutText   textstream.UTF8Decoder
	stderrText   textstream.UTF8Decoder
	running      bool
	exitCode     int
	waitErr      error
	startedAt    time.Time
	updatedAt    time.Time
	done         chan struct{}
}

func (s *session) sendInitialStdin(stdin []byte) error {
	if len(stdin) > 0 {
		frame, err := runnerproto.NewFrame(runnerproto.TypeStdin, runnerproto.Bytes{Data: stdin})
		if err != nil {
			return err
		}
		if err := s.write(frame); err != nil {
			return err
		}
	}
	closeFrame, err := runnerproto.NewFrame(runnerproto.TypeStdinClose, nil)
	if err != nil {
		return err
	}
	return s.write(closeFrame)
}

func (s *session) write(frame runnerproto.Frame) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.writer.WriteFrame(frame)
}

func (s *session) readLoop() {
	defer close(s.done)
	defer func() { _ = s.process.Wait() }()
	for {
		frame, err := s.reader.ReadFrame()
		if err != nil {
			s.finish(-1, err)
			return
		}
		switch frame.Type {
		case runnerproto.TypeStdout, runnerproto.TypeStderr:
			var payload runnerproto.Bytes
			if err := frame.DecodePayload(&payload); err != nil {
				s.finish(-1, err)
				return
			}
			s.appendOutput(frame.Type, payload.Data)
		case runnerproto.TypeExit:
			var payload runnerproto.Exit
			if err := frame.DecodePayload(&payload); err != nil {
				s.finish(-1, err)
				return
			}
			var waitErr error
			if strings.TrimSpace(payload.Reason) != "" && payload.ExitCode != 0 {
				waitErr = errors.New(payload.Reason)
			}
			s.flushOutputText()
			s.finish(payload.ExitCode, waitErr)
			return
		case runnerproto.TypeError:
			var payload runnerproto.Error
			_ = frame.DecodePayload(&payload)
			message := strings.TrimSpace(payload.Message)
			if message == "" {
				message = "runner error"
			}
			s.flushOutputText()
			s.finish(-1, errors.New(message))
			return
		}
	}
}

func (s *session) captureRunnerStderr(reader io.Reader) {
	_, _ = io.Copy(&s.runnerStderr, reader)
}

func (s *session) appendOutput(typ string, data []byte) {
	if len(data) == 0 {
		return
	}
	stream := "stdout"
	s.mu.Lock()
	switch typ {
	case runnerproto.TypeStderr:
		stream = "stderr"
		s.stderr = append(s.stderr, data...)
		s.stderrTotal += int64(len(data))
	default:
		s.stdout = append(s.stdout, data...)
		s.stdoutTotal += int64(len(data))
	}
	s.updatedAt = time.Now()
	s.mu.Unlock()
	if s.onOutput != nil {
		text := ""
		if typ == runnerproto.TypeStderr {
			text = s.stderrText.Decode(data)
		} else {
			text = s.stdoutText.Decode(data)
		}
		if text != "" {
			s.onOutput(runnerruntime.OutputChunk{Stream: stream, Text: text})
		}
	}
}

func (s *session) flushOutputText() {
	if s.onOutput == nil {
		return
	}
	s.mu.Lock()
	stdout := s.stdoutText.Flush()
	stderr := s.stderrText.Flush()
	s.mu.Unlock()
	if stdout != "" {
		s.onOutput(runnerruntime.OutputChunk{Stream: "stdout", Text: stdout})
	}
	if stderr != "" {
		s.onOutput(runnerruntime.OutputChunk{Stream: "stderr", Text: stderr})
	}
}

func (s *session) finish(exitCode int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false
	s.exitCode = exitCode
	s.waitErr = err
	s.updatedAt = time.Now()
	_ = s.stdin.Close()
}

func (s *session) readOutput(stdoutMarker, stderrMarker int64) ([]byte, []byte, int64, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stdout, nextStdout := bytesSince(s.stdout, s.stdoutTotal, stdoutMarker)
	stderr, nextStderr := bytesSince(s.stderr, s.stderrTotal, stderrMarker)
	return stdout, stderr, nextStdout, nextStderr, nil
}

func (s *session) status() cmdsession.SessionStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := cmdsession.SessionStateRunning
	if !s.running {
		state = cmdsession.SessionStateCompleted
		if s.waitErr != nil {
			state = cmdsession.SessionStateError
		}
	}
	return cmdsession.SessionStatus{
		ID:           s.id,
		State:        state,
		ExitCode:     s.exitCode,
		StartTime:    s.startedAt,
		LastActivity: s.updatedAt,
		StdoutBytes:  s.stdoutTotal,
		StderrBytes:  s.stderrTotal,
	}
}

func (s *session) WaitResult(ctx context.Context, timeout time.Duration) (sandbox.CommandResult, error) {
	wait := s.done
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return sandbox.CommandResult{}, ctx.Err()
		case <-wait:
		case <-timer.C:
			return s.result(), nil
		}
	} else {
		select {
		case <-ctx.Done():
			return sandbox.CommandResult{}, ctx.Err()
		case <-wait:
		}
	}
	return s.resultAndErr()
}

func (s *session) result() sandbox.CommandResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resultLocked()
}

func (s *session) resultAndErr() (sandbox.CommandResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resultLocked(), s.waitErr
}

func (s *session) resultLocked() sandbox.CommandResult {
	return sandbox.CommandResult{
		Stdout:   string(s.stdout),
		Stderr:   string(s.stderr),
		ExitCode: s.exitCode,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendWindowsElevated,
	}
}

func (s *session) TerminateSession() error {
	killFrame, _ := runnerproto.NewFrame(runnerproto.TypeKill, nil)
	_ = s.write(killFrame)
	if s.process != nil {
		_ = s.process.Kill()
	}
	return nil
}

type process interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Wait() error
	Kill() error
}

type execProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader
}

func startExecProcess(ctx context.Context, executable string, args []string, dir string) (*execProcess, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	return &execProcess{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

func (p *execProcess) Stdin() io.WriteCloser {
	return p.stdin
}

func (p *execProcess) Stdout() io.Reader {
	return p.stdout
}

func (p *execProcess) Stderr() io.Reader {
	return p.stderr
}

func (p *execProcess) Wait() error {
	return p.cmd.Wait()
}

func (p *execProcess) Kill() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func bytesSince(buf []byte, total int64, marker int64) ([]byte, int64) {
	if marker < 0 {
		marker = 0
	}
	base := total - int64(len(buf))
	if base < 0 {
		base = 0
	}
	if marker < base {
		marker = base
	}
	if marker > total {
		marker = total
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
