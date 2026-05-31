package runnerruntime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/internal/cmdsession"
	"github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/internal/policy"
	"github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/internal/policyfs"
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
	Stdin        []byte
	Constraints  sandbox.Constraints
	OnOutput     func(OutputChunk)
}

type Runner interface {
	Run(context.Context, Request) (sandbox.CommandResult, error)
	StartAsync(context.Context, Request) (string, error)
	WriteInput(sessionID string, input []byte) error
	ReadOutput(sessionID string, stdoutMarker, stderrMarker int64) (stdout, stderr []byte, newStdoutMarker, newStderrMarker int64, err error)
	GetSessionStatus(sessionID string) (cmdsession.SessionStatus, error)
	WaitSession(context.Context, string, time.Duration) (sandbox.CommandResult, error)
	TerminateSession(sessionID string) error
	Close() error
}

type Config struct {
	Backend    sandbox.Backend
	Descriptor sandbox.Descriptor
	Status     sandbox.Status
	BaseFS     sandbox.FileSystem
	Policy     func(sandbox.Constraints) policy.Policy
	Runner     Runner
}

type Runtime struct {
	backend    sandbox.Backend
	descriptor sandbox.Descriptor
	status     sandbox.Status
	baseFS     sandbox.FileSystem
	policy     func(sandbox.Constraints) policy.Policy
	runner     Runner
}

func New(cfg Config) *Runtime {
	return &Runtime{
		backend:    cfg.Backend,
		descriptor: sandbox.CloneDescriptor(cfg.Descriptor),
		status:     sandbox.CloneStatus(cfg.Status),
		baseFS:     cfg.BaseFS,
		policy:     cfg.Policy,
		runner:     cfg.Runner,
	}
}

func (r *Runtime) Descriptor() sandbox.Descriptor {
	return sandbox.CloneDescriptor(r.descriptor)
}

func (r *Runtime) FileSystem() sandbox.FileSystem {
	return r.FileSystemFor(sandbox.Constraints{})
}

func (r *Runtime) FileSystemFor(constraints sandbox.Constraints) sandbox.FileSystem {
	if r.baseFS == nil || r.policy == nil {
		return r.baseFS
	}
	return policyfs.New(r.baseFS, func() policy.Policy {
		return r.policy(sandbox.NormalizeConstraints(constraints))
	})
}

func (r *Runtime) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	if r.runner == nil {
		return sandbox.CommandResult{}, fmt.Errorf("sandbox/runtime: backend %q runner is unavailable", r.backend)
	}
	result, err := r.runner.Run(ctx, translateRequest(req))
	if result.Route == "" {
		result.Route = sandbox.RouteSandbox
	}
	if result.Backend == "" {
		result.Backend = r.backend
	}
	return sandbox.NormalizeSandboxPermissionFailure(result, err)
}

func (r *Runtime) Start(ctx context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
	if r.runner == nil {
		return nil, fmt.Errorf("sandbox/runtime: backend %q runner is unavailable", r.backend)
	}
	sessionID, err := r.runner.StartAsync(ctx, translateRequest(req))
	if err != nil {
		return nil, err
	}
	return &session{backend: r.backend, runner: r.runner, sessionID: strings.TrimSpace(sessionID)}, nil
}

func (r *Runtime) Open(ctx context.Context, ref sandbox.SessionRef) (sandbox.Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ref = sandbox.CloneSessionRef(ref)
	if ref.Backend != "" && ref.Backend != r.backend {
		return nil, fmt.Errorf("sandbox/runtime: backend %q is unsupported by %q runtime", ref.Backend, r.backend)
	}
	id := strings.TrimSpace(ref.ID)
	if id == "" {
		return nil, fmt.Errorf("sandbox/runtime: session id is required")
	}
	if _, err := r.runner.GetSessionStatus(id); err != nil {
		return nil, err
	}
	return &session{backend: r.backend, runner: r.runner, sessionID: id}, nil
}

func (r *Runtime) Status() sandbox.Status {
	return sandbox.CloneStatus(r.status)
}

func (r *Runtime) Close() error {
	if r.runner == nil {
		return nil
	}
	return r.runner.Close()
}

type session struct {
	backend   sandbox.Backend
	runner    Runner
	sessionID string
}

func (s *session) Ref() sandbox.SessionRef {
	return sandbox.SessionRef{Backend: s.backend, ID: s.sessionID}
}

func (s *session) Write(_ context.Context, input []byte) error {
	return s.runner.WriteInput(s.sessionID, input)
}

func (s *session) Read(_ context.Context, cursor sandbox.OutputCursor) (sandbox.OutputSnapshot, error) {
	stdout, stderr, nextStdout, nextStderr, err := s.runner.ReadOutput(s.sessionID, cursor.Stdout, cursor.Stderr)
	stderr = sandbox.NormalizeSandboxPermissionOutput("stderr", stderr)
	return sandbox.OutputSnapshot{
		Stdout: string(stdout),
		Stderr: string(stderr),
		Cursor: sandbox.OutputCursor{Stdout: nextStdout, Stderr: nextStderr},
	}, err
}

func (s *session) Snapshot(_ context.Context) (sandbox.SessionSnapshot, error) {
	status, err := s.runner.GetSessionStatus(s.sessionID)
	if err != nil {
		return sandbox.SessionSnapshot{}, err
	}
	return translateStatus(s.backend, s.sessionID, status), nil
}

func (s *session) Wait(ctx context.Context) (sandbox.CommandResult, error) {
	result, err := s.runner.WaitSession(ctx, s.sessionID, 0)
	if result.Route == "" {
		result.Route = sandbox.RouteSandbox
	}
	if result.Backend == "" {
		result.Backend = s.backend
	}
	return sandbox.NormalizeSandboxPermissionFailure(result, err)
}

func (s *session) Cancel(_ context.Context) error {
	return s.runner.TerminateSession(s.sessionID)
}

func (s *session) Close() error {
	return s.Cancel(context.Background())
}

func translateRequest(req sandbox.CommandRequest) Request {
	req = sandbox.CloneRequest(req)
	return Request{
		Command:      req.Command,
		Dir:          req.Dir,
		Timeout:      req.Timeout,
		IdleTimeout:  req.IdleTimeout,
		TTY:          req.TTY,
		EnvOverrides: req.Env,
		Stdin:        append([]byte(nil), req.Stdin...),
		Constraints:  sandbox.EffectiveConstraints(req),
		OnOutput: func(chunk OutputChunk) {
			if req.OnOutput == nil {
				return
			}
			if strings.EqualFold(strings.TrimSpace(chunk.Stream), "stderr") {
				chunk.Text = string(sandbox.NormalizeSandboxPermissionOutput("stderr", []byte(chunk.Text)))
			}
			req.OnOutput(sandbox.OutputChunk{Stream: chunk.Stream, Text: chunk.Text})
		},
	}
}

func translateStatus(backend sandbox.Backend, sessionID string, status cmdsession.SessionStatus) sandbox.SessionSnapshot {
	state := sandbox.SessionCompleted
	if status.State == cmdsession.SessionStateRunning {
		state = sandbox.SessionRunning
	} else if status.ExitCode != 0 || strings.TrimSpace(status.Error) != "" {
		state = sandbox.SessionFailed
	}
	return sandbox.SessionSnapshot{
		Ref:           sandbox.SessionRef{Backend: backend, ID: sessionID},
		Terminal:      sandbox.TerminalRef{ID: sessionID, SessionID: sessionID},
		State:         state,
		Running:       status.State == cmdsession.SessionStateRunning,
		SupportsInput: true,
		ExitCode:      status.ExitCode,
		Error:         strings.TrimSpace(status.Error),
		StartedAt:     status.StartTime,
		UpdatedAt:     status.LastActivity,
	}
}
