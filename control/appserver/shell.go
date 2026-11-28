package appserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// CapabilityShellExecution identifies the optional synchronous, Session-free Shell API.
const CapabilityShellExecution = "shell-execution-v1"

// RoleLocalShell is attached only by the managed loopback Host authenticator
// to the ordinary local credential. A principal ID alone conveys no grant.
const RoleLocalShell = "local-shell"

const maxShellExecutions = 4

// ShellLocation selects an execution boundary already granted by the Host.
type ShellLocation string

const (
	ShellSandbox ShellLocation = "sandbox"
	ShellHost    ShellLocation = "host"
)

// UnmarshalJSON distinguishes an omitted location from an invalid explicit value.
func (l *ShellLocation) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	location := ShellLocation(value)
	if location != ShellSandbox && location != ShellHost {
		return errorcode.New(errorcode.InvalidArgument, "location must be sandbox or host")
	}
	*l = location
	return nil
}

// ShellRequest executes one non-interactive command in a caller-selected workspace.
// An omitted location selects sandbox.
type ShellRequest struct {
	Workspace string        `json:"workspace"`
	Command   string        `json:"command"`
	Location  ShellLocation `json:"location,omitempty"`
}

// ShellResult describes a completed process, including ordinary nonzero exits.
type ShellResult struct {
	Location ShellLocation `json:"location"`
	Backend  string        `json:"backend"`
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
}

// ShellClient executes a command with the caller's authenticated Shell grant.
type ShellClient interface {
	ExecuteShell(context.Context, ShellRequest) (ShellResult, error)
}

// ShellService owns admission and drain for the Host's synchronous Shell grant.
type ShellService struct {
	principalID string
	run         func(context.Context, ShellRequest) (ShellResult, error)
	ctx         context.Context
	cancel      context.CancelFunc

	mu     sync.Mutex
	active int
	done   chan struct{}
}

// NewShellService binds the grant and its platform execution backend.
func NewShellService(principalID string, run func(context.Context, ShellRequest) (ShellResult, error)) *ShellService {
	ctx, cancel := context.WithCancel(context.Background())
	return &ShellService{principalID: principalID, run: run, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

// ExecuteShell admits one process only while the grant is open.
func (s *ShellService) ExecuteShell(ctx context.Context, p Principal, req ShellRequest) (ShellResult, error) {
	if p.ID == "" || p.ID != s.principalID || !p.HasRole(RoleLocalShell) || p.HasRole(RoleACPIngress) || p.HasRole(RoleSystemSessionRuntime) {
		return ShellResult{}, ErrUnauthorized
	}
	if req.Location == "" {
		req.Location = ShellSandbox
	}
	if req.Location != ShellSandbox && req.Location != ShellHost {
		return ShellResult{}, errorcode.New(errorcode.InvalidArgument, "location must be sandbox or host")
	}
	if !filepath.IsAbs(req.Workspace) || len(req.Workspace) > 4096 || strings.ContainsRune(req.Workspace, 0) {
		return ShellResult{}, errorcode.New(errorcode.InvalidArgument, "workspace must be an absolute path of at most 4096 bytes")
	}
	if strings.TrimSpace(req.Command) == "" || len(req.Command) > 32<<10 || strings.ContainsRune(req.Command, 0) {
		return ShellResult{}, errorcode.New(errorcode.InvalidArgument, "command must contain 1 to 32768 bytes without NUL")
	}

	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return ShellResult{}, errorcode.New(errorcode.Unavailable, "Shell admission is closed")
	}
	if s.active >= maxShellExecutions {
		s.mu.Unlock()
		return ShellResult{}, errorcode.New(errorcode.Overloaded, "Shell executions are busy")
	}
	s.active++
	s.mu.Unlock()

	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()

	type outcome struct {
		result ShellResult
		err    error
	}
	completed := make(chan outcome, 1)
	go func() {
		var out outcome
		defer func() {
			if recover() != nil {
				out.err = errorcode.New(errorcode.UnknownOutcome, "Shell execution stopped without a result")
			}
			s.mu.Lock()
			s.active--
			if s.active == 0 && s.ctx.Err() != nil {
				close(s.done)
			}
			s.mu.Unlock()
			completed <- out
		}()
		// The result deadline bounds the HTTP request even if a platform
		// setup call is stuck in the OS. Its slot stays owned until cleanup.
		if err := runCtx.Err(); err != nil {
			out.err = err
			return
		}
		out.result, out.err = s.run(runCtx, req)
	}()
	select {
	case out := <-completed:
		return out.result, out.err
	case <-runCtx.Done():
		return ShellResult{}, runCtx.Err()
	}
}

// PrepareIdleShutdown closes admission only if no Shell execution is in flight.
func (s *ShellService) PrepareIdleShutdown() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != 0 {
		return errorcode.New(errorcode.Conflict, "Host is busy; retry replacement after work completes")
	}
	s.stopLocked()
	return nil
}

func (s *ShellService) stopLocked() {
	if s.ctx.Err() == nil {
		s.cancel()
		if s.active == 0 {
			close(s.done)
		}
	}
}

// Quiesce cancels active calls and joins them before Host resources close.
func (s *ShellService) Quiesce(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.stopLocked()
	s.mu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type boundShellClient struct {
	service   *ShellService
	principal Principal
}

func (c *boundShellClient) ExecuteShell(ctx context.Context, req ShellRequest) (ShellResult, error) {
	return c.service.ExecuteShell(ctx, c.principal, req)
}
