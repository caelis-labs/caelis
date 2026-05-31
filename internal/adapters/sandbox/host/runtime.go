// Package host provides a core-native host sandbox adapter.
package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/internal/policy"
	"github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/internal/policyfs"
)

type Runtime struct {
	cwd      string
	cfg      sandbox.Config
	journal  *sessionJournal
	mu       sync.RWMutex
	sessions map[string]*commandSession
	closed   bool
}

type Factory struct{}

func (Factory) NewRuntime(ctx context.Context, cfg sandbox.Config) (sandbox.Runtime, error) {
	return New(ctx, cfg)
}

func New(ctx context.Context, cfg sandbox.Config) (*Runtime, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg = sandbox.NormalizeConfig(cfg)
	cwd := strings.TrimSpace(cfg.CWD)
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	cfg.CWD = abs
	return &Runtime{cwd: abs, cfg: cfg, journal: newSessionJournal(cfg.StateDir), sessions: map[string]*commandSession{}}, nil
}

func (r *Runtime) Descriptor() sandbox.Descriptor {
	return sandbox.Descriptor{
		Backend:   sandbox.BackendHost,
		Isolation: sandbox.IsolationHost,
		Capabilities: sandbox.CapabilitySet{
			FileSystem:    true,
			CommandExec:   true,
			AsyncSessions: true,
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

func (r *Runtime) Status() sandbox.Status {
	return sandbox.Status{
		RequestedBackend: sandbox.BackendHost,
		ResolvedBackend:  sandbox.BackendHost,
	}
}

func (r *Runtime) FileSystem() sandbox.FileSystem {
	base := fileSystem{cwd: r.cwd}
	return policyfs.New(base, func() policy.Policy {
		return policy.Default(r.cfg, sandbox.Constraints{Permission: sandbox.PermissionWorkspaceWrite})
	})
}

func (r *Runtime) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return sandbox.CommandResult{}, err
	}
	command := strings.TrimSpace(req.Command)
	if command == "" {
		return sandbox.CommandResult{}, errors.New("sandbox/host: command is required")
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, shellName(), shellArgs(command)...)
	cmd.Dir = r.resolveDir(req.Dir)
	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range req.Env {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	if len(req.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := sandbox.CommandResult{
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
		Route:   sandbox.RouteHost,
		Backend: sandbox.BackendHost,
	}
	if req.OnOutput != nil {
		if text := result.Stdout; text != "" {
			req.OnOutput(sandbox.OutputChunk{Stream: "stdout", Text: text})
		}
		if text := result.Stderr; text != "" {
			req.OnOutput(sandbox.OutputChunk{Stream: "stderr", Text: text})
		}
	}
	if err == nil {
		return result, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		result.Error = ctxErr.Error()
		result.ExitCode = -1
		return result, ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		result.Error = err.Error()
		return result, nil
	}
	result.ExitCode = -1
	result.Error = err.Error()
	return result, fmt.Errorf("sandbox/host: run command: %w", err)
}

func (r *Runtime) Start(ctx context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := strings.TrimSpace(req.Command)
	if command == "" {
		return nil, errors.New("sandbox/host: command is required")
	}
	session := newCommandSession(r.resolveDir(req.Dir), req, r.journal)
	if err := session.start(); err != nil {
		return nil, err
	}
	if err := session.persist(ctx); err != nil {
		_ = session.Close()
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		_ = session.Close()
		return nil, errors.New("sandbox/host: runtime is closed")
	}
	r.sessions[session.ref.ID] = session
	return session, nil
}

func (r *Runtime) Open(ctx context.Context, ref sandbox.SessionRef) (sandbox.Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(ref.ID)
	if id == "" {
		return nil, errors.New("sandbox/host: session id is required")
	}
	if ref.Backend != "" && ref.Backend != sandbox.BackendHost {
		return nil, fmt.Errorf("sandbox/host: unsupported session backend %q", ref.Backend)
	}
	r.mu.RLock()
	session, ok := r.sessions[id]
	r.mu.RUnlock()
	if !ok {
		session, err := r.journal.open(ctx, ref)
		if err == nil {
			return session, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("sandbox/host: session not found: %s", id)
	}
	return session, nil
}

func (r *Runtime) ListSessions(ctx context.Context, query sandbox.SessionListQuery) ([]sandbox.SessionSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	sessions := make([]*commandSession, 0, len(r.sessions))
	for _, session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.RUnlock()
	out := make([]sandbox.SessionSnapshot, 0, len(sessions))
	seen := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		snapshot, err := session.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, snapshot)
		seen[snapshot.Ref.ID] = struct{}{}
	}
	archived, err := r.journal.list(ctx)
	if err != nil {
		return nil, err
	}
	for _, snapshot := range archived {
		if _, ok := seen[snapshot.Ref.ID]; ok {
			continue
		}
		out = append(out, snapshot)
	}
	sort.SliceStable(out, func(i int, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

func (r *Runtime) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	sessions := make([]*commandSession, 0, len(r.sessions))
	for id, session := range r.sessions {
		sessions = append(sessions, session)
		delete(r.sessions, id)
	}
	r.mu.Unlock()
	var firstErr error
	for _, session := range sessions {
		if err := session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (r *Runtime) resolveDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return r.cwd
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(r.cwd, dir)
}

func shellName() string {
	if runtime.GOOS == "windows" {
		return "cmd.exe"
	}
	return "sh"
}

func shellArgs(command string) []string {
	if runtime.GOOS == "windows" {
		return []string{"/C", command}
	}
	return []string{"-lc", command}
}

type fileSystem struct {
	cwd string
}

func (f fileSystem) Getwd() (string, error) {
	if strings.TrimSpace(f.cwd) != "" {
		return f.cwd, nil
	}
	return os.Getwd()
}

func (fileSystem) UserHomeDir() (string, error) {
	return os.UserHomeDir()
}

func (f fileSystem) Open(path string) (*os.File, error) {
	return os.Open(f.resolve(path))
}

func (f fileSystem) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(f.resolve(path))
}

func (f fileSystem) Stat(path string) (os.FileInfo, error) {
	return os.Stat(f.resolve(path))
}

func (f fileSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(f.resolve(path))
}

func (f fileSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(f.resolve(path), data, perm)
}

func (f fileSystem) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(f.resolve(path), perm)
}

func (f fileSystem) Glob(pattern string) ([]string, error) {
	return filepath.Glob(f.resolve(pattern))
}

func (f fileSystem) WalkDir(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(f.resolve(root), fn)
}

func (f fileSystem) resolve(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	if strings.TrimSpace(f.cwd) == "" {
		return path
	}
	return filepath.Join(f.cwd, path)
}

var _ sandbox.Runtime = (*Runtime)(nil)
var _ sandbox.BackendFactory = Factory{}
