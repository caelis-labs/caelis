package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

// Backend implements sandbox.Backend by running commands directly on
// the host with no OS-level sandboxing.
//
// Security model: workdir gate only. When CommandRequest.Constraints
// are set, the backend validates the working directory against allowed
// paths before execution. It does NOT enforce path isolation during
// execution — the command can still access any path via absolute paths,
// symlinks, or shell redirects. For full path isolation, use a platform
// sandbox backend (seatbelt/bwrap/restricted-token).
type Backend struct {
	mu       sync.RWMutex
	sessions map[string]*session
}

// New creates a new host backend.
func New() *Backend {
	return &Backend{sessions: make(map[string]*session)}
}

func (b *Backend) Name() string { return "host" }

func (b *Backend) Describe(_ context.Context) (sandbox.Descriptor, error) {
	return sandbox.Descriptor{
		Name:        "host",
		Description: "Direct host execution with no sandboxing",
		Platform:    "any",
	}, nil
}

func (b *Backend) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	if err := req.Validate(); err != nil {
		return sandbox.CommandResult{}, err
	}

	// Enforce command-level constraints on working directory.
	if err := sandbox.CheckCommandConstraints(req.Dir, req.Constraints); err != nil {
		return sandbox.CommandResult{}, err
	}

	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Second)
		defer cancel()
	}

	// Always use /bin/sh -c to support shell syntax (pipes, redirects, etc.).
	cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", req.Command)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	if req.Env != nil {
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	if len(req.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := sandbox.CommandResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: 0,
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return sandbox.CommandResult{}, fmt.Errorf("host run: %w", err)
		}
	}
	return result, nil
}

func (b *Backend) FileSystem(_ context.Context, c sandbox.Constraints) (sandbox.FileSystem, error) {
	fs := &hostFS{}
	if len(c.Paths) > 0 {
		return newConstrainedFS(fs, c), nil
	}
	return fs, nil
}

func (b *Backend) Status(_ context.Context) (sandbox.Status, error) {
	return sandbox.Status{Running: true}, nil
}

func (b *Backend) Close() error {
	b.mu.RLock()
	sessions := make([]*session, 0, len(b.sessions))
	for _, s := range b.sessions {
		sessions = append(sessions, s)
	}
	b.mu.RUnlock()
	for _, s := range sessions {
		_ = s.Terminate(context.Background())
	}
	return nil
}

// Compile-time interface check.
var _ sandbox.Backend = (*Backend)(nil)

// hostFS implements sandbox.FileSystem using os directly.
type hostFS struct{}

func (f *hostFS) Read(path string) ([]byte, error)     { return os.ReadFile(path) }
func (f *hostFS) Write(path string, data []byte) error { return os.WriteFile(path, data, 0644) }
func (f *hostFS) Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
func (f *hostFS) Delete(path string) error { return os.Remove(path) }
func (f *hostFS) Stat(path string) (sandbox.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return sandbox.FileInfo{}, err
	}
	return sandbox.FileInfo{
		Name:    info.Name(),
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}, nil
}
func (f *hostFS) List(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names, nil
}
