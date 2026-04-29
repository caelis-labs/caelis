package runnerruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/cmdsession"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/policy"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/policyfs"
)

type OutputChunk struct {
	Stream string
	Text   string
}

type Request struct {
	Command      string
	Dir          string
	Timeout      time.Duration
	IdleTimeout  time.Duration
	TTY          bool
	EnvOverrides map[string]string
	Constraints  sdksandbox.Constraints
	OnOutput     func(OutputChunk)
}

type Runner interface {
	Run(context.Context, Request) (sdksandbox.CommandResult, error)
	StartAsync(context.Context, Request) (string, error)
	WriteInput(sessionID string, input []byte) error
	ReadOutput(sessionID string, stdoutMarker, stderrMarker int64) (stdout, stderr []byte, newStdoutMarker, newStderrMarker int64, err error)
	GetSessionStatus(sessionID string) (cmdsession.SessionStatus, error)
	WaitSession(context.Context, string, time.Duration) (sdksandbox.CommandResult, error)
	TerminateSession(sessionID string) error
	Close() error
}

type Config struct {
	Backend    sdksandbox.Backend
	Descriptor sdksandbox.Descriptor
	Status     sdksandbox.Status
	BaseFS     sdksandbox.FileSystem
	Policy     func(sdksandbox.Constraints) policy.Policy
	Runner     Runner
}

type Runtime struct {
	backend    sdksandbox.Backend
	descriptor sdksandbox.Descriptor
	status     sdksandbox.Status
	baseFS     sdksandbox.FileSystem
	policy     func(sdksandbox.Constraints) policy.Policy
	runner     Runner
}

func New(cfg Config) *Runtime {
	return &Runtime{
		backend:    cfg.Backend,
		descriptor: sdksandbox.CloneDescriptor(cfg.Descriptor),
		status:     cfg.Status,
		baseFS:     cfg.BaseFS,
		policy:     cfg.Policy,
		runner:     cfg.Runner,
	}
}

func (r *Runtime) Describe() sdksandbox.Descriptor {
	return sdksandbox.CloneDescriptor(r.descriptor)
}

func (r *Runtime) FileSystem() sdksandbox.FileSystem {
	return r.FileSystemFor(sdksandbox.Constraints{})
}

func (r *Runtime) FileSystemFor(constraints sdksandbox.Constraints) sdksandbox.FileSystem {
	if r.baseFS == nil || r.policy == nil {
		return r.baseFS
	}
	return policyfs.New(r.baseFS, func() policy.Policy {
		return r.policy(sdksandbox.NormalizeConstraints(constraints))
	})
}

func (r *Runtime) Run(ctx context.Context, req sdksandbox.CommandRequest) (sdksandbox.CommandResult, error) {
	if r.runner == nil {
		return sdksandbox.CommandResult{}, fmt.Errorf("sdk/sandbox: backend %q runner is unavailable", r.backend)
	}
	result, err := r.runner.Run(ctx, translateRequest(req))
	if result.Route == "" {
		result.Route = sdksandbox.RouteSandbox
	}
	if result.Backend == "" {
		result.Backend = r.backend
	}
	return sdksandbox.NormalizeSandboxPermissionFailure(result, err)
}

func (r *Runtime) Start(ctx context.Context, req sdksandbox.CommandRequest) (sdksandbox.Session, error) {
	if r.runner == nil {
		return nil, fmt.Errorf("sdk/sandbox: backend %q runner is unavailable", r.backend)
	}
	sessionID, err := r.runner.StartAsync(ctx, translateRequest(req))
	if err != nil {
		return nil, err
	}
	return &session{backend: r.backend, runner: r.runner, sessionID: strings.TrimSpace(sessionID)}, nil
}

func (r *Runtime) OpenSession(id string) (sdksandbox.Session, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("sdk/sandbox: session id is required")
	}
	if _, err := r.runner.GetSessionStatus(id); err != nil {
		return nil, err
	}
	return &session{backend: r.backend, runner: r.runner, sessionID: id}, nil
}

func (r *Runtime) OpenSessionRef(ref sdksandbox.SessionRef) (sdksandbox.Session, error) {
	ref = sdksandbox.CloneSessionRef(ref)
	if ref.Backend != "" && ref.Backend != r.backend {
		return nil, fmt.Errorf("sdk/sandbox: backend %q is unsupported by %q runtime", ref.Backend, r.backend)
	}
	return r.OpenSession(ref.SessionID)
}

func (r *Runtime) SupportedBackends() []sdksandbox.Backend {
	return []sdksandbox.Backend{r.backend}
}

func (r *Runtime) Status() sdksandbox.Status {
	return r.status
}

func (r *Runtime) Close() error {
	if r.runner == nil {
		return nil
	}
	return r.runner.Close()
}

type session struct {
	backend   sdksandbox.Backend
	runner    Runner
	sessionID string
}

func (s *session) Ref() sdksandbox.SessionRef {
	return sdksandbox.SessionRef{Backend: s.backend, SessionID: s.sessionID}
}

func (s *session) Terminal() sdksandbox.TerminalRef {
	return sdksandbox.TerminalRef{Backend: s.backend, SessionID: s.sessionID, TerminalID: s.sessionID}
}

func (s *session) WriteInput(_ context.Context, input []byte) error {
	return s.runner.WriteInput(s.sessionID, input)
}

func (s *session) ReadOutput(_ context.Context, stdoutMarker, stderrMarker int64) ([]byte, []byte, int64, int64, error) {
	stdout, stderr, nextStdout, nextStderr, err := s.runner.ReadOutput(s.sessionID, stdoutMarker, stderrMarker)
	stderr = sdksandbox.NormalizeSandboxPermissionOutput("stderr", stderr)
	return stdout, stderr, nextStdout, nextStderr, err
}

func (s *session) Status(_ context.Context) (sdksandbox.SessionStatus, error) {
	status, err := s.runner.GetSessionStatus(s.sessionID)
	if err != nil {
		return sdksandbox.SessionStatus{}, err
	}
	return translateStatus(s.backend, s.sessionID, status), nil
}

func (s *session) Wait(ctx context.Context, timeout time.Duration) (sdksandbox.SessionStatus, error) {
	if timeout <= 0 {
		return s.Status(ctx)
	}
	inner, err := getSession(s.runner, s.sessionID)
	if err != nil {
		return sdksandbox.SessionStatus{}, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := inner.Wait(waitCtx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return s.Status(ctx)
		}
		return sdksandbox.SessionStatus{}, err
	}
	return s.Status(ctx)
}

func (s *session) Result(ctx context.Context) (sdksandbox.CommandResult, error) {
	result, err := s.runner.WaitSession(ctx, s.sessionID, 0)
	if result.Route == "" {
		result.Route = sdksandbox.RouteSandbox
	}
	if result.Backend == "" {
		result.Backend = s.backend
	}
	return sdksandbox.NormalizeSandboxPermissionFailure(result, err)
}

func (s *session) Terminate(_ context.Context) error {
	return s.runner.TerminateSession(s.sessionID)
}

func translateRequest(req sdksandbox.CommandRequest) Request {
	req = sdksandbox.CloneRequest(req)
	return Request{
		Command:      req.Command,
		Dir:          req.Dir,
		Timeout:      req.Timeout,
		IdleTimeout:  req.IdleTimeout,
		TTY:          req.TTY,
		EnvOverrides: req.Env,
		Constraints:  sdksandbox.EffectiveConstraints(req),
		OnOutput: func(chunk OutputChunk) {
			if req.OnOutput == nil {
				return
			}
			if strings.EqualFold(strings.TrimSpace(chunk.Stream), "stderr") {
				chunk.Text = string(sdksandbox.NormalizeSandboxPermissionOutput("stderr", []byte(chunk.Text)))
			}
			req.OnOutput(sdksandbox.OutputChunk{Stream: chunk.Stream, Text: chunk.Text})
		},
	}
}

func translateStatus(backend sdksandbox.Backend, sessionID string, status cmdsession.SessionStatus) sdksandbox.SessionStatus {
	return sdksandbox.SessionStatus{
		SessionRef:    sdksandbox.SessionRef{Backend: backend, SessionID: sessionID},
		Terminal:      sdksandbox.TerminalRef{Backend: backend, SessionID: sessionID, TerminalID: sessionID},
		Running:       status.State == cmdsession.SessionStateRunning,
		SupportsInput: true,
		ExitCode:      status.ExitCode,
		StartedAt:     status.StartTime,
		UpdatedAt:     status.LastActivity,
	}
}

type sessionLookup interface {
	GetSession(id string) (*cmdsession.AsyncSession, error)
}

func getSession(r Runner, sessionID string) (*cmdsession.AsyncSession, error) {
	lookup, ok := r.(sessionLookup)
	if !ok {
		return nil, fmt.Errorf("sdk/sandbox: backend session lookup is unavailable")
	}
	return lookup.GetSession(sessionID)
}
