//go:build darwin

package seatbelt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/cmdsession"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/procutil"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/runnerruntime"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/internal/envfd"
)

const (
	seatbeltSandboxType      = "seatbelt"
	seatbeltProbeExecutable  = "/usr/bin/true"
	seatbeltProbeTimeout     = 5 * time.Second
	seatbeltProbeWaitDelay   = time.Second
	seatbeltProbeStderrLimit = 64 * 1024
)

type Config = sandbox.Config

type backendFactory struct{}

func (backendFactory) Backend() sandbox.Backend { return sandbox.BackendSeatbelt }

func (backendFactory) Build(cfg sandbox.Config) (sandbox.Runtime, error) {
	return New(cfg)
}

type seatbeltRunner struct {
	execCommand    func(context.Context, string, ...string) *exec.Cmd
	lookPath       func(string) (string, error)
	goos           string
	helperPath     string
	cfg            Config
	env            sandbox.EnvironmentSnapshot
	sessionManager *cmdsession.SessionManager
	closed         atomic.Bool
}

func New(cfg Config) (sandbox.Runtime, error) {
	cfg = sandbox.NormalizeConfig(cfg)
	if err := sandbox.ValidateConfig(cfg); err != nil {
		return nil, err
	}
	helperPath := cfg.HelperPath
	if helperPath == "" {
		var err error
		helperPath, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("seatbelt helper executable: %w", err)
		}
	}
	if cfg.ResourceLimits != nil && cfg.ResourceLimits.ReadPaths != nil && !helperAllowedByReadCeiling(helperPath, cfg.ResourceLimits.ReadPaths) {
		return nil, fmt.Errorf("seatbelt helper %q must be inside mandatory read ceiling", helperPath)
	}
	runner := &seatbeltRunner{
		execCommand:    exec.CommandContext,
		lookPath:       exec.LookPath,
		goos:           stdruntime.GOOS,
		helperPath:     helperPath,
		cfg:            cfg,
		env:            sandbox.NewEnvironmentSnapshot(cfg.Execution, cfg.BaseEnv),
		sessionManager: cmdsession.NewSessionManager(cmdsession.DefaultSessionManagerConfig()),
	}
	if err := runner.probeHelper(context.Background()); err != nil {
		_ = runner.Close()
		return nil, err
	}
	if err := runner.probe(context.Background()); err != nil {
		_ = runner.Close()
		return nil, err
	}
	hostRuntime, err := host.New(host.Config{CWD: cfg.CWD, Execution: cfg.Execution, BaseEnv: cfg.BaseEnv})
	if err != nil {
		_ = runner.Close()
		return nil, err
	}
	return runnerruntime.New(runnerruntime.Config{
		CWD:     cfg.CWD,
		Backend: sandbox.BackendSeatbelt,
		Descriptor: sandbox.Descriptor{
			Backend:   sandbox.BackendSeatbelt,
			Isolation: sandbox.IsolationContainer,
			Capabilities: sandbox.CapabilitySet{
				FileSystem:     true,
				CommandExec:    true,
				AsyncSessions:  true,
				TTY:            true,
				NetworkControl: true,
				PathPolicy:     true,
				EnvPolicy:      true,
			},
			DefaultConstraints: sandbox.Constraints{
				Route:      sandbox.RouteSandbox,
				Backend:    sandbox.BackendSeatbelt,
				Permission: sandbox.PermissionWorkspaceWrite,
				Isolation:  sandbox.IsolationContainer,
				Network:    sandbox.NetworkInherit,
			},
		},
		Status: sandbox.Status{
			RequestedBackend: sandbox.BackendSeatbelt,
			ResolvedBackend:  sandbox.BackendSeatbelt,
		},
		BaseFS: hostRuntime.FileSystem(),
		Policy: func(constraints sandbox.Constraints) policy.Policy {
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
	profile := "(version 1) (allow default)"
	if s.cfg.ResourceLimits != nil {
		workDir, err := procutil.ResolveHostWorkDir(s.cfg.CWD)
		if err != nil {
			return fmt.Errorf("seatbelt sandbox probe failed: %w", err)
		}
		profile, err = buildSeatbeltProfile(policy.Default(s.cfg, sandbox.Constraints{}), workDir)
		if err != nil {
			return fmt.Errorf("seatbelt sandbox probe failed: %w", err)
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, seatbeltProbeTimeout)
	defer cancel()
	cmd := s.execCommand(probeCtx, "sandbox-exec", "-p", profile, seatbeltProbeExecutable)
	cmd.Env = []string{}
	procutil.ApplyNonInteractiveCommandDefaults(cmd)
	cmd.WaitDelay = seatbeltProbeWaitDelay
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	bounded := &procutil.BoundedWriter{Writer: &stderr, Remaining: seatbeltProbeStderrLimit}
	cmd.Stderr = bounded
	err := cmd.Run()
	msg := strings.TrimSpace(stderr.String())
	if bounded.Exceeded {
		if err != nil {
			return fmt.Errorf("seatbelt sandbox probe failed: %w; stderr budget exceeded", err)
		}
		return fmt.Errorf("seatbelt sandbox probe failed: stderr budget exceeded")
	}
	if err != nil {
		if msg == "" {
			return fmt.Errorf("seatbelt sandbox probe failed: %w", err)
		}
		return fmt.Errorf("seatbelt sandbox probe failed: %w; stderr=%s", err, msg)
	}
	return nil
}

func (s *seatbeltRunner) Run(ctx context.Context, req runnerruntime.Request) (sandbox.CommandResult, error) {
	runCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()

	workDir, err := procutil.ResolveHostWorkDir(req.Dir)
	if err != nil {
		return sandbox.CommandResult{}, fmt.Errorf("tool: resolve seatbelt workdir failed: %w", err)
	}
	effectivePolicy := policy.Default(s.cfg, req.Constraints)
	profile, err := buildSeatbeltProfile(effectivePolicy, workDir)
	if err != nil {
		return sandbox.CommandResult{}, fmt.Errorf("tool: prepare seatbelt sandbox policy failed: %w", err)
	}

	shell, shellArgs := sandbox.ShellArgs(s.cfg.Execution, req.Command)
	cmd, payload, err := s.seatbeltCommand(runCtx, profile, shell, shellArgs, s.env.Build(req.UnsetEnv, req.EnvOverrides))
	if err != nil {
		return sandbox.CommandResult{}, err
	}
	defer payload.Close()
	procutil.ApplyNonInteractiveCommandDefaults(cmd)
	if strings.TrimSpace(req.Dir) != "" {
		cmd.Dir = req.Dir
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	lastOutput := atomic.Int64{}
	lastOutput.Store(time.Now().UnixNano())
	cmd.Stdout = procutil.NewActivityWriter(&stdout, &lastOutput, "stdout", emitOutput(req.OnOutput))
	cmd.Stderr = procutil.NewActivityWriter(&stderr, &lastOutput, "stderr", emitOutput(req.OnOutput))

	if s.cfg.ResourceLimits != nil {
		cmd.Stdout = &procutil.BoundedWriter{Writer: cmd.Stdout, Remaining: 64 * 1024}
		cmd.Stderr = &procutil.BoundedWriter{Writer: cmd.Stderr, Remaining: 64 * 1024}
	}
	if err := cmd.Start(); err != nil {
		return sandbox.CommandResult{}, fmt.Errorf("tool: seatbelt sandbox command start failed: %w", err)
	}
	waitErr := procutil.WaitWithIdleTimeout(runCtx, cmd, req.IdleTimeout, &lastOutput)
	if s.cfg.ResourceLimits != nil {
		_ = procutil.KillProcess(cmd)
	}

	if s.cfg.ResourceLimits != nil && (cmd.Stdout.(*procutil.BoundedWriter).Exceeded || cmd.Stderr.(*procutil.BoundedWriter).Exceeded) {
		return sandbox.CommandResult{}, fmt.Errorf("sandbox command output budget exceeded")
	}
	result := sandbox.CommandResult{
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
		Route:   sandbox.RouteSandbox,
		Backend: sandbox.BackendSeatbelt,
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
	workDir, err := procutil.ResolveHostWorkDir(req.Dir)
	if err != nil {
		return "", fmt.Errorf("tool: resolve seatbelt workdir failed: %w", err)
	}
	effectivePolicy := policy.Default(s.cfg, req.Constraints)
	var payload *os.File
	defer func() {
		if payload != nil {
			_ = payload.Close()
		}
	}()
	session, err := manager.StartSession(cmdsession.AsyncSessionConfig{
		Command:         req.Command,
		Dir:             req.Dir,
		Env:             s.env.Build(req.UnsetEnv, req.EnvOverrides),
		OutputBufferCap: 256 * 1024,
		Timeout:         req.Timeout,
		IdleTimeout:     req.IdleTimeout,
		TTY:             req.TTY,
		OnOutput:        runnerruntime.UTF8OutputForwarder(req.OnOutput),
		BuildCommand: func(ctx context.Context, cfg cmdsession.AsyncSessionConfig) (*exec.Cmd, error) {
			profile, err := buildSeatbeltProfile(effectivePolicy, workDir)
			if err != nil {
				return nil, fmt.Errorf("tool: prepare seatbelt sandbox policy failed: %w", err)
			}
			shell, shellArgs := sandbox.ShellArgs(s.cfg.Execution, cfg.Command)
			cmd, file, err := s.seatbeltCommand(ctx, profile, shell, shellArgs, cfg.Env)
			if err != nil {
				return nil, err
			}
			payload = file
			if strings.TrimSpace(cfg.Dir) != "" {
				cmd.Dir = cfg.Dir
			}
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

func (s *seatbeltRunner) AwaitOutput(ctx context.Context, sessionID string, cursor sandbox.OutputCursor) (cmdsession.OutputObservation, error) {
	manager, err := s.asyncSessionManager()
	if err != nil {
		return cmdsession.OutputObservation{}, err
	}
	return manager.AwaitOutput(ctx, sessionID, cursor)
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

func (s *seatbeltRunner) WaitSession(ctx context.Context, sessionID string, timeout time.Duration) (sandbox.CommandResult, error) {
	manager, err := s.asyncSessionManager()
	if err != nil {
		return sandbox.CommandResult{}, err
	}
	if timeout > 0 {
		if _, err := manager.WaitSessionWithContextTimeout(ctx, sessionID, timeout); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return sandbox.CommandResult{Route: sandbox.RouteSandbox, Backend: sandbox.BackendSeatbelt}, nil
			}
			return sandbox.CommandResult{}, err
		}
	} else if _, err := manager.WaitSession(ctx, sessionID); err != nil {
		return sandbox.CommandResult{}, err
	}
	result, err := manager.GetResult(sessionID)
	result.Route = sandbox.RouteSandbox
	result.Backend = sandbox.BackendSeatbelt
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
		return nil, fmt.Errorf("impl/sandbox/seatbelt: runner is closed")
	}
	return s.sessionManager, nil
}

func buildSeatbeltProfile(p policy.Policy, workDir string) (string, error) {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n")
	restrictedReads := p.ResourceLimits != nil && p.ResourceLimits.ReadPaths != nil
	if !restrictedReads {
		// system.sb and the development compatibility sections grant ambient
		// filesystem and local-network access. Explicit read ceilings must not
		// import them, including when the ceiling is an empty set.
		b.WriteString("(import \"system.sb\")\n")
	}
	if restrictedReads {
		// Do not grant process inspection or unrestricted sysctl reads here.
		// These restrictions still do not isolate same-user environments:
		// numeric KERN_PROCARGS2 can expose them despite a filesystem ceiling.
		b.WriteString("(allow process-fork process-exec)\n")
		// Go allocator startup uses numeric HW_PAGESIZE, whose XNU name is
		// hw.pagesize_compat rather than the newer size_t-valued hw.pagesize.
		b.WriteString("(allow sysctl-read (sysctl-name \"hw.pagesize_compat\"))\n")
	} else {
		b.WriteString("(allow process*)\n")
		b.WriteString("(allow sysctl-read)\n")
	}
	b.WriteString("(allow signal (target same-sandbox))\n")
	if !restrictedReads {
		b.WriteString("(allow file-read*)\n")
	} else {
		for _, root := range p.ResourceLimits.ReadPaths {
			if !filepath.IsAbs(root) || filepath.Clean(root) == "/" {
				return "", fmt.Errorf("seatbelt: read ceiling requires absolute non-root paths")
			}
			fmt.Fprintf(&b, "(allow file-read* file-map-executable (subpath %s))\n", sbplString(root))
		}
	}
	if restrictedReads {
		// Loader bootstrap needs a root directory descriptor, not a root
		// subtree grant. File contents, including dyld cryptex data, remain
		// explicit above; no network or write capability is added.
		b.WriteString(`; dyld libignition opens the filesystem root as a directory descriptor.
; This literal does not grant any descendant file or writable path.
(allow file-read-data file-read-metadata (require-all (literal "/") (vnode-type DIRECTORY)))
(allow system-fcntl (fcntl-command F_ADDFILESIGS_RETURN F_CHECK_LV F_GETPATH))
(allow system-mac-syscall (require-all (mac-policy-name "Sandbox") (mac-syscall-number 2)))
(allow system-mac-syscall (require-all (mac-policy-name "Sandbox") (mac-syscall-number 67)))
(allow system-mac-syscall (mac-policy-name "vnguard"))
`)
	} else {
		b.WriteString(seatbeltCoreExtensions)
		b.WriteString(seatbeltMachServices)
		b.WriteString(seatbeltDeviceAndFramework)
	}
	if p.NetworkAccess {
		b.WriteString("(allow network*)\n")
		b.WriteString(seatbeltNetworkExtensions)
	}
	writableRoots, err := seatbeltWritableRoots(p, workDir)
	if err != nil {
		return "", err
	}
	for _, root := range writableRoots {
		fmt.Fprintf(&b, "(allow file-write* (subpath %s))\n", sbplString(root))
	}
	readOnlyPaths, err := policy.ReadOnlyPaths(p, workDir)
	if err != nil {
		return "", err
	}
	for _, sub := range readOnlyPaths {
		fmt.Fprintf(&b, "(deny file-write* (subpath %s))\n", sbplString(sub))
	}

	return b.String(), nil
}

func seatbeltWritableRoots(p policy.Policy, workDir string) ([]string, error) {
	if p.ResourceLimits != nil {
		return append([]string(nil), p.ResourceLimits.WritePaths...), nil
	}

	if p.Type == policy.TypeReadOnly {
		return nil, nil
	}
	explicit := make([]string, 0, len(p.WritableRoots))
	for _, one := range p.WritableRoots {
		if resolved := policy.ResolveSandboxPath(workDir, one); resolved != "" {
			explicit = append(explicit, policy.WritableRootPath(resolved))
		}
	}
	explicit = policy.FilterExistingPaths(explicit)

	roots := make([]string, 0, len(explicit)+8)
	for _, one := range explicit {
		roots = append(roots, policy.SandboxPathVariants(one)...)
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
	return normalizeStringList(roots), nil
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

func commandOutputSummary(result sandbox.CommandResult) string {
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
	sandbox.RegisterBuiltInBackendFactory(backendFactory{})
}

// seatbeltCommand keeps command environment off the sandbox-exec launcher and
// argv. The self-exec helper reads FD3 inside Seatbelt and execs the shell with
// the assembled environment without narrowing allowed variable names.
func (s *seatbeltRunner) seatbeltCommand(ctx context.Context, profile, shell string, shellArgs, env []string) (*exec.Cmd, *os.File, error) {
	payload, err := json.Marshal(env)
	if err != nil {
		return nil, nil, err
	}
	file, err := envfd.Open(payload)
	if err != nil {
		return nil, nil, err
	}
	args := []string{"-p", profile, s.helperPath, seatbeltHelperCommand, "--shell", shell, "--shell-flag", shellArgs[0], "--command", shellArgs[1], "--env-fd", "3"}
	cmd := s.execCommand(ctx, "sandbox-exec", args...)
	cmd.Env = []string{}
	cmd.ExtraFiles = append(cmd.ExtraFiles, file)
	return cmd, file, nil
}

func helperAllowedByReadCeiling(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
