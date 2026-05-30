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
	"strings"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
)

type Runtime struct {
	cwd string
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
	return &Runtime{cwd: abs}, nil
}

func (r *Runtime) Descriptor() sandbox.Descriptor {
	return sandbox.Descriptor{
		Backend:   sandbox.BackendHost,
		Isolation: sandbox.IsolationHost,
		Capabilities: sandbox.CapabilitySet{
			FileSystem:  true,
			CommandExec: true,
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
	return fileSystem{cwd: r.cwd}
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

func (r *Runtime) Start(context.Context, sandbox.CommandRequest) (sandbox.Session, error) {
	return nil, errors.New("sandbox/host: async command sessions are not implemented")
}

func (r *Runtime) Close() error {
	return nil
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
