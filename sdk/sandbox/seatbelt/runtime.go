package seatbelt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/host"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/cmdsession"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/policy"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/procutil"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/runnerruntime"
)

const seatbeltSandboxType = "seatbelt"

type Config = sdksandbox.Config

type backendFactory struct{}

func (backendFactory) Backend() sdksandbox.Backend { return sdksandbox.BackendSeatbelt }

func (backendFactory) Build(cfg sdksandbox.Config) (sdksandbox.Runtime, error) {
	return New(cfg)
}

type seatbeltRunner struct {
	execCommand    func(context.Context, string, ...string) *exec.Cmd
	lookPath       func(string) (string, error)
	goos           string
	cfg            Config
	sessionManager *cmdsession.SessionManager
	closed         atomic.Bool
}

func New(cfg Config) (sdksandbox.Runtime, error) {
	cfg = sdksandbox.NormalizeConfig(cfg)
	runner := &seatbeltRunner{
		execCommand:    exec.CommandContext,
		lookPath:       exec.LookPath,
		goos:           stdruntime.GOOS,
		cfg:            cfg,
		sessionManager: cmdsession.NewSessionManager(cmdsession.DefaultSessionManagerConfig()),
	}
	if err := runner.probe(context.Background()); err != nil {
		_ = runner.Close()
		return nil, err
	}
	hostRuntime, err := host.New(host.Config{CWD: cfg.CWD})
	if err != nil {
		_ = runner.Close()
		return nil, err
	}
	return runnerruntime.New(runnerruntime.Config{
		Backend: sdksandbox.BackendSeatbelt,
		Descriptor: sdksandbox.Descriptor{
			Backend:   sdksandbox.BackendSeatbelt,
			Isolation: sdksandbox.IsolationContainer,
			Capabilities: sdksandbox.CapabilitySet{
				FileSystem:     true,
				CommandExec:    true,
				AsyncSessions:  true,
				TTY:            false,
				NetworkControl: true,
				PathPolicy:     true,
				EnvPolicy:      true,
			},
			DefaultConstraints: sdksandbox.Constraints{
				Route:      sdksandbox.RouteSandbox,
				Backend:    sdksandbox.BackendSeatbelt,
				Permission: sdksandbox.PermissionWorkspaceWrite,
				Isolation:  sdksandbox.IsolationContainer,
				Network:    sdksandbox.NetworkInherit,
			},
		},
		Status: sdksandbox.Status{
			RequestedBackend: sdksandbox.BackendSeatbelt,
			ResolvedBackend:  sdksandbox.BackendSeatbelt,
		},
		BaseFS: hostRuntime.FileSystem(),
		Policy: func(constraints sdksandbox.Constraints) policy.Policy {
			return policy.Default(cfg, constraints)
		},
		Runner: runner,
	}), nil
}

func (s *seatbeltRunner) probe(ctx context.Context) error {
	if s.goos != "darwin" {
		return fmt.Errorf("seatbelt sandbox is only supported on darwin (current=%s)", s.goos)
	}
	if _, err := s.lookPath("sandbox-exec"); err != nil {
		return fmt.Errorf("seatbelt sandbox unavailable: sandbox-exec not found: %w", err)
	}
	cmd := s.execCommand(ctx, "sandbox-exec", "-p", "(version 1) (allow default)", "/bin/sh", "-lc", "echo seatbelt-probe")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return fmt.Errorf("seatbelt sandbox probe failed: %w", err)
		}
		return fmt.Errorf("seatbelt sandbox probe failed: %w; stderr=%s", err, msg)
	}
	return nil
}

func (s *seatbeltRunner) Run(ctx context.Context, req runnerruntime.Request) (sdksandbox.CommandResult, error) {
	runCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()

	workDir, err := procutil.ResolveHostWorkDir(req.Dir)
	if err != nil {
		return sdksandbox.CommandResult{}, fmt.Errorf("tool: resolve seatbelt workdir failed: %w", err)
	}
	effectivePolicy := policy.Default(s.cfg, req.Constraints)
	profile := buildSeatbeltProfile(effectivePolicy, workDir)

	args := []string{"-p", profile, "bash", "-lc", req.Command}
	cmd := s.execCommand(runCtx, "sandbox-exec", args...)
	procutil.ApplyNonInteractiveCommandDefaults(cmd)
	if strings.TrimSpace(req.Dir) != "" {
		cmd.Dir = req.Dir
	}
	cmd.Env = mergeCommandEnv(req.EnvOverrides)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	lastOutput := atomic.Int64{}
	lastOutput.Store(time.Now().UnixNano())
	cmd.Stdout = procutil.NewActivityWriter(&stdout, &lastOutput, "stdout", emitOutput(req.OnOutput))
	cmd.Stderr = procutil.NewActivityWriter(&stderr, &lastOutput, "stderr", emitOutput(req.OnOutput))

	if err := cmd.Start(); err != nil {
		return sdksandbox.CommandResult{}, fmt.Errorf("tool: seatbelt sandbox command start failed: %w", err)
	}
	waitErr := procutil.WaitWithIdleTimeout(runCtx, cmd, req.IdleTimeout, &lastOutput)

	result := sdksandbox.CommandResult{
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
		Route:   sdksandbox.RouteSandbox,
		Backend: sdksandbox.BackendSeatbelt,
	}
	if waitErr == nil {
		return result, nil
	}
	result.ExitCode = resolveExitCode(waitErr)
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) || errors.Is(waitErr, context.DeadlineExceeded) {
		label := "context deadline"
		if req.Timeout > 0 {
			label = req.Timeout.String()
		}
		return result, fmt.Errorf("tool: seatbelt sandbox command timed out after %s; %s", label, commandOutputSummary(result))
	}
	if errors.Is(waitErr, procutil.ErrIdleTimeout) {
		label := "idle limit"
		if req.IdleTimeout > 0 {
			label = req.IdleTimeout.String()
		}
		return result, fmt.Errorf("tool: seatbelt sandbox command produced no output for %s and was terminated; %s", label, commandOutputSummary(result))
	}
	return result, fmt.Errorf("tool: seatbelt sandbox command failed: %w; %s", waitErr, commandOutputSummary(result))
}

func (s *seatbeltRunner) StartAsync(_ context.Context, req runnerruntime.Request) (string, error) {
	manager, err := s.asyncSessionManager()
	if err != nil {
		return "", err
	}
	if req.TTY {
		return "", fmt.Errorf("tool: seatbelt async tty is not supported")
	}
	workDir, err := procutil.ResolveHostWorkDir(req.Dir)
	if err != nil {
		return "", fmt.Errorf("tool: resolve seatbelt workdir failed: %w", err)
	}
	effectivePolicy := policy.Default(s.cfg, req.Constraints)
	session, err := manager.StartSession(cmdsession.AsyncSessionConfig{
		Command:         req.Command,
		Dir:             req.Dir,
		Env:             mergeCommandEnv(req.EnvOverrides),
		OutputBufferCap: 256 * 1024,
		Timeout:         req.Timeout,
		IdleTimeout:     req.IdleTimeout,
		BuildCommand: func(ctx context.Context, cfg cmdsession.AsyncSessionConfig) (*exec.Cmd, error) {
			profile := buildSeatbeltProfile(effectivePolicy, workDir)
			cmd := s.execCommand(ctx, "sandbox-exec", "-p", profile, "bash", "-lc", cfg.Command)
			if strings.TrimSpace(cfg.Dir) != "" {
				cmd.Dir = cfg.Dir
			}
			cmd.Env = append([]string(nil), cfg.Env...)
			return cmd, nil
		},
	})
	if err != nil {
		return "", err
	}
	return session.ID, nil
}

func (s *seatbeltRunner) WriteInput(sessionID string, input []byte) error {
	manager, err := s.asyncSessionManager()
	if err != nil {
		return err
	}
	return manager.WriteInput(sessionID, input)
}

func (s *seatbeltRunner) ReadOutput(sessionID string, stdoutMarker, stderrMarker int64) ([]byte, []byte, int64, int64, error) {
	manager, err := s.asyncSessionManager()
	if err != nil {
		return nil, nil, 0, 0, err
	}
	return manager.ReadOutput(sessionID, stdoutMarker, stderrMarker)
}

func (s *seatbeltRunner) GetSessionStatus(sessionID string) (cmdsession.SessionStatus, error) {
	manager, err := s.asyncSessionManager()
	if err != nil {
		return cmdsession.SessionStatus{}, err
	}
	return manager.GetSessionStatus(sessionID)
}

func (s *seatbeltRunner) GetSession(sessionID string) (*cmdsession.AsyncSession, error) {
	manager, err := s.asyncSessionManager()
	if err != nil {
		return nil, err
	}
	return manager.GetSession(sessionID)
}

func (s *seatbeltRunner) WaitSession(ctx context.Context, sessionID string, timeout time.Duration) (sdksandbox.CommandResult, error) {
	manager, err := s.asyncSessionManager()
	if err != nil {
		return sdksandbox.CommandResult{}, err
	}
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		session, err := manager.GetSession(sessionID)
		if err != nil {
			return sdksandbox.CommandResult{}, err
		}
		select {
		case <-ctx.Done():
			return sdksandbox.CommandResult{}, ctx.Err()
		case <-session.ExitChannel():
		case <-timer.C:
			return sdksandbox.CommandResult{Route: sdksandbox.RouteSandbox, Backend: sdksandbox.BackendSeatbelt}, nil
		}
	} else if _, err := manager.WaitSession(ctx, sessionID); err != nil {
		return sdksandbox.CommandResult{}, err
	}
	result, err := manager.GetResult(sessionID)
	result.Route = sdksandbox.RouteSandbox
	result.Backend = sdksandbox.BackendSeatbelt
	return result, err
}

func (s *seatbeltRunner) TerminateSession(sessionID string) error {
	manager, err := s.asyncSessionManager()
	if err != nil {
		return err
	}
	return manager.TerminateSession(sessionID)
}

func (s *seatbeltRunner) Close() error {
	s.closed.Store(true)
	if s.sessionManager != nil {
		return s.sessionManager.Close()
	}
	return nil
}

func (s *seatbeltRunner) asyncSessionManager() (*cmdsession.SessionManager, error) {
	if s == nil || s.closed.Load() || s.sessionManager == nil {
		return nil, fmt.Errorf("sdk/sandbox/seatbelt: runner is closed")
	}
	return s.sessionManager, nil
}

func buildSeatbeltProfile(p policy.Policy, workDir string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n")
	b.WriteString("(import \"system.sb\")\n")
	b.WriteString("(allow process*)\n")
	b.WriteString("(allow signal (target same-sandbox))\n")
	b.WriteString("(allow sysctl-read)\n")
	if roots := policy.ShellReadableRoots(p, workDir); len(roots) > 0 {
		for _, root := range roots {
			fmt.Fprintf(&b, "(allow file-read* (subpath %s))\n", sbplString(root))
			fmt.Fprintf(&b, "(allow file-read-metadata file-test-existence (subpath %s))\n", sbplString(root))
		}
	} else {
		b.WriteString("(allow file-read*)\n")
	}
	b.WriteString(seatbeltCoreExtensions)
	b.WriteString(seatbeltMachServices)
	b.WriteString(seatbeltDeviceAndFramework)
	if p.NetworkAccess {
		b.WriteString("(allow network*)\n")
		b.WriteString(seatbeltNetworkExtensions)
	}
	for _, root := range seatbeltWritableRoots(p, workDir) {
		fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", sbplString(root))
	}
	for _, sub := range seatbeltReadOnlySubpaths(p, workDir) {
		fmt.Fprintf(&b, "(deny file-write* (subpath %s))\n", sbplString(sub))
	}
	return b.String()
}

func seatbeltWritableRoots(p policy.Policy, workDir string) []string {
	if p.Type == policy.TypeReadOnly {
		return nil
	}
	roots := make([]string, 0, len(p.WritableRoots)+8)
	for _, one := range p.WritableRoots {
		if resolved := policy.ResolveSandboxPath(workDir, one); resolved != "" {
			roots = append(roots, policy.SandboxPathVariants(resolved)...)
		}
	}
	if tmp := strings.TrimSpace(os.TempDir()); tmp != "" {
		roots = append(roots, policy.SandboxPathVariants(tmp)...)
	}
	roots = append(roots, policy.SandboxPathVariants("/tmp")...)
	roots = append(roots, policy.SandboxPathVariants("/var/tmp")...)
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		roots = append(roots, policy.SandboxPathVariants(filepath.Join(home, "Library", "Caches"))...)
		roots = append(roots, policy.SandboxPathVariants(filepath.Join(home, ".cache"))...)
	}
	if p.NetworkAccess {
		if cacheDir := darwinUserCacheDir(); cacheDir != "" {
			roots = append(roots, policy.SandboxPathVariants(cacheDir)...)
		}
	}
	return normalizeStringList(roots)
}

func darwinUserCacheDir() string {
	tmpDir := strings.TrimRight(os.TempDir(), string(filepath.Separator))
	parent := filepath.Dir(tmpDir)
	if parent == "" || !strings.Contains(parent, string(filepath.Separator)+"var"+string(filepath.Separator)+"folders") {
		return ""
	}
	cacheDir := filepath.Join(parent, "C")
	if info, err := os.Stat(cacheDir); err == nil && info.IsDir() {
		return cacheDir
	}
	return ""
}

func seatbeltReadOnlySubpaths(p policy.Policy, workDir string) []string {
	values := make([]string, 0, len(p.ReadOnlySubpaths))
	for _, one := range p.ReadOnlySubpaths {
		if resolved := policy.ResolveSandboxPath(workDir, one); resolved != "" {
			values = append(values, policy.SandboxPathVariants(resolved)...)
		}
	}
	return normalizeStringList(values)
}

func sbplString(v string) string { return strconv.Quote(v) }

func normalizeStringList(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func mergeCommandEnv(extra map[string]string) []string {
	env := os.Environ()
	for key, value := range extra {
		if key == "" {
			continue
		}
		env = append(env, key+"="+value)
	}
	return env
}

func resolveExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func commandOutputSummary(result sdksandbox.CommandResult) string {
	stdout := strings.TrimSpace(result.Stdout)
	stderr := strings.TrimSpace(result.Stderr)
	switch {
	case stdout != "" && stderr != "":
		return fmt.Sprintf("stdout=%q stderr=%q", stdout, stderr)
	case stdout != "":
		return fmt.Sprintf("stdout=%q", stdout)
	case stderr != "":
		return fmt.Sprintf("stderr=%q", stderr)
	default:
		return "no output"
	}
}

func emitOutput(fn func(runnerruntime.OutputChunk)) func(string, string) {
	if fn == nil {
		return nil
	}
	return func(stream string, text string) {
		fn(runnerruntime.OutputChunk{Stream: stream, Text: text})
	}
}

func init() {
	if err := sdksandbox.RegisterBackendFactory(backendFactory{}); err != nil {
		panic(err)
	}
}
