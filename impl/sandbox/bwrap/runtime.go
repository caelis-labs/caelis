//go:build linux

package bwrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	stdruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caelis-labs/caelis/impl/sandbox/host"
	"github.com/caelis-labs/caelis/impl/sandbox/internal/cmdsession"
	"github.com/caelis-labs/caelis/impl/sandbox/internal/policy"
	"github.com/caelis-labs/caelis/impl/sandbox/internal/procutil"
	"github.com/caelis-labs/caelis/impl/sandbox/internal/runnerruntime"
	"github.com/caelis-labs/caelis/ports/sandbox"
)

const (
	bwrapSandboxType  = "bwrap"
	bubblewrapDocsURL = "https://github.com/containers/bubblewrap"
)

type Config = sandbox.Config

type backendFactory struct{}

func (backendFactory) Backend() sandbox.Backend { return sandbox.BackendBwrap }

func (backendFactory) Build(cfg sandbox.Config) (sandbox.Runtime, error) {
	return New(cfg)
}

type bwrapRunner struct {
	execCommand    func(context.Context, string, ...string) *exec.Cmd
	lookPath       func(string) (string, error)
	readFile       func(string) ([]byte, error)
	stat           func(string) (os.FileInfo, error)
	goos           string
	cfg            Config
	sessionManager *cmdsession.SessionManager
	closed         atomic.Bool
}

func New(cfg Config) (sandbox.Runtime, error) {
	cfg = sandbox.NormalizeConfig(cfg)
	runner := &bwrapRunner{
		execCommand:    exec.CommandContext,
		lookPath:       exec.LookPath,
		readFile:       os.ReadFile,
		stat:           os.Stat,
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
		Backend: sandbox.BackendBwrap,
		Descriptor: sandbox.Descriptor{
			Backend:   sandbox.BackendBwrap,
			Isolation: sandbox.IsolationContainer,
			Capabilities: sandbox.CapabilitySet{
				FileSystem:     true,
				CommandExec:    true,
				AsyncSessions:  true,
				TTY:            false,
				NetworkControl: true,
				PathPolicy:     true,
				EnvPolicy:      true,
			},
			DefaultConstraints: sandbox.Constraints{
				Route:      sandbox.RouteSandbox,
				Backend:    sandbox.BackendBwrap,
				Permission: sandbox.PermissionWorkspaceWrite,
				Isolation:  sandbox.IsolationContainer,
				Network:    sandbox.NetworkInherit,
			},
		},
		Status: sandbox.Status{
			RequestedBackend: sandbox.BackendBwrap,
			ResolvedBackend:  sandbox.BackendBwrap,
		},
		BaseFS: hostRuntime.FileSystem(),
		Policy: func(constraints sandbox.Constraints) policy.Policy {
			return policy.Default(cfg, constraints)
		},
		Runner: runner,
	}), nil
}

func (b *bwrapRunner) probe(ctx context.Context) error {
	if b.goos != "linux" {
		return fmt.Errorf("bwrap sandbox is only supported on linux (current=%s)", b.goos)
	}
	bwrapPath, err := b.lookPath("bwrap")
	if err != nil {
		return fmt.Errorf("bwrap sandbox unavailable: bwrap not found: %w; %s", err, bubblewrapInstallHint(b.readFile))
	}
	if _, err := b.lookPath("bash"); err != nil {
		return fmt.Errorf("bwrap sandbox unavailable: bash not found: %w", err)
	}
	probeArgs := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--new-session",
		"--die-with-parent",
		"--unshare-user",
		"--unshare-pid",
	}
	if !policy.Default(b.cfg, sandbox.Constraints{}).NetworkAccess {
		probeArgs = append(probeArgs, "--unshare-net")
	}
	probeArgs = append(probeArgs, "--", "bash", "-lc", "echo bwrap-probe")
	cmd := b.execCommand(ctx, "bwrap", probeArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return fmt.Errorf("bwrap sandbox probe failed: %w", err)
		}
		if detail := bwrapProbeFailureDetail(bwrapPath, msg, b.stat, b.readFile); detail != "" {
			return fmt.Errorf("bwrap sandbox probe failed: %w; stderr=%s; %s", err, msg, detail)
		}
		return fmt.Errorf("bwrap sandbox probe failed: %w; stderr=%s", err, msg)
	}
	return nil
}

func (b *bwrapRunner) Run(ctx context.Context, req runnerruntime.Request) (sandbox.CommandResult, error) {
	runCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()

	workDir, err := procutil.ResolveHostWorkDir(req.Dir)
	if err != nil {
		return sandbox.CommandResult{}, fmt.Errorf("tool: resolve bwrap workdir failed: %w", err)
	}
	effectivePolicy := policy.Default(b.cfg, req.Constraints)
	bwrapArgs, err := buildBwrapArgs(effectivePolicy, workDir)
	if err != nil {
		return sandbox.CommandResult{}, fmt.Errorf("tool: prepare bwrap sandbox policy failed: %w", err)
	}
	bwrapArgs = append(bwrapArgs, "--", "bash", "-lc", req.Command)
	cmd := b.execCommand(runCtx, "bwrap", bwrapArgs...)
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
		return sandbox.CommandResult{}, fmt.Errorf("tool: bwrap sandbox command start failed: %w", err)
	}
	waitErr := procutil.WaitWithIdleTimeout(runCtx, cmd, req.IdleTimeout, &lastOutput)
	result := sandbox.CommandResult{
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
		Route:   sandbox.RouteSandbox,
		Backend: sandbox.BackendBwrap,
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
		return result, fmt.Errorf("tool: bwrap sandbox command timed out after %s; %s", label, commandOutputSummary(result))
	}
	if errors.Is(waitErr, procutil.ErrIdleTimeout) {
		label := "idle limit"
		if req.IdleTimeout > 0 {
			label = req.IdleTimeout.String()
		}
		return result, fmt.Errorf("tool: bwrap sandbox command produced no output for %s and was terminated; %s", label, commandOutputSummary(result))
	}
	if detail := bwrapMissingMountDiagnostic(result, effectivePolicy, workDir); detail != "" {
		result.Error = detail
	}
	return result, fmt.Errorf("tool: bwrap sandbox command failed: %w; %s", waitErr, commandOutputSummary(result))
}

func (b *bwrapRunner) StartAsync(_ context.Context, req runnerruntime.Request) (string, error) {
	manager, err := b.asyncSessionManager()
	if err != nil {
		return "", err
	}
	if req.TTY {
		return "", fmt.Errorf("tool: bwrap async tty is not supported")
	}
	workDir, err := procutil.ResolveHostWorkDir(req.Dir)
	if err != nil {
		return "", fmt.Errorf("tool: resolve bwrap workdir failed: %w", err)
	}
	effectivePolicy := policy.Default(b.cfg, req.Constraints)
	session, err := manager.StartSession(cmdsession.AsyncSessionConfig{
		Command:         req.Command,
		Dir:             req.Dir,
		Env:             mergeCommandEnv(req.EnvOverrides),
		OutputBufferCap: 256 * 1024,
		Timeout:         req.Timeout,
		IdleTimeout:     req.IdleTimeout,
		OnOutput:        asyncOutputForwarder(req.OnOutput),
		BuildCommand: func(ctx context.Context, cfg cmdsession.AsyncSessionConfig) (*exec.Cmd, error) {
			args, err := buildBwrapArgs(effectivePolicy, workDir)
			if err != nil {
				return nil, fmt.Errorf("tool: prepare bwrap sandbox policy failed: %w", err)
			}
			args = append(args, "--", "bash", "-lc", cfg.Command)
			cmd := b.execCommand(ctx, "bwrap", args...)
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

func (b *bwrapRunner) WriteInput(sessionID string, input []byte) error {
	manager, err := b.asyncSessionManager()
	if err != nil {
		return err
	}
	return manager.WriteInput(sessionID, input)
}

func (b *bwrapRunner) ReadOutput(sessionID string, stdoutMarker, stderrMarker int64) ([]byte, []byte, int64, int64, error) {
	manager, err := b.asyncSessionManager()
	if err != nil {
		return nil, nil, 0, 0, err
	}
	return manager.ReadOutput(sessionID, stdoutMarker, stderrMarker)
}

func (b *bwrapRunner) GetSessionStatus(sessionID string) (cmdsession.SessionStatus, error) {
	manager, err := b.asyncSessionManager()
	if err != nil {
		return cmdsession.SessionStatus{}, err
	}
	return manager.GetSessionStatus(sessionID)
}

func (b *bwrapRunner) GetSession(sessionID string) (*cmdsession.AsyncSession, error) {
	manager, err := b.asyncSessionManager()
	if err != nil {
		return nil, err
	}
	return manager.GetSession(sessionID)
}

func (b *bwrapRunner) WaitSession(ctx context.Context, sessionID string, timeout time.Duration) (sandbox.CommandResult, error) {
	manager, err := b.asyncSessionManager()
	if err != nil {
		return sandbox.CommandResult{}, err
	}
	if timeout > 0 {
		if _, err := manager.WaitSessionWithContextTimeout(ctx, sessionID, timeout); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return sandbox.CommandResult{Route: sandbox.RouteSandbox, Backend: sandbox.BackendBwrap}, nil
			}
			return sandbox.CommandResult{}, err
		}
	} else if _, err := manager.WaitSession(ctx, sessionID); err != nil {
		return sandbox.CommandResult{}, err
	}
	result, err := manager.GetResult(sessionID)
	result.Route = sandbox.RouteSandbox
	result.Backend = sandbox.BackendBwrap
	return result, err
}

func (b *bwrapRunner) TerminateSession(sessionID string) error {
	manager, err := b.asyncSessionManager()
	if err != nil {
		return err
	}
	return manager.TerminateSession(sessionID)
}

func (b *bwrapRunner) Close() error {
	b.closed.Store(true)
	if b.sessionManager != nil {
		return b.sessionManager.Close()
	}
	return nil
}

func (b *bwrapRunner) asyncSessionManager() (*cmdsession.SessionManager, error) {
	if b == nil || b.closed.Load() || b.sessionManager == nil {
		return nil, fmt.Errorf("impl/sandbox/bwrap: runner is closed")
	}
	return b.sessionManager, nil
}

func buildBwrapArgs(p policy.Policy, workDir string) ([]string, error) {
	args := []string{"--new-session", "--die-with-parent", "--unshare-user", "--unshare-pid"}
	if !p.NetworkAccess {
		args = append(args, "--unshare-net")
	}
	if policy.HasExplicitReadableRoots(p) {
		rootArgs, err := buildScopedBwrapRootArgs(p, workDir)
		if err != nil {
			return nil, err
		}
		args = append(args, rootArgs...)
	} else {
		args = append(args, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc")
	}
	if p.Type != policy.TypeReadOnly {
		writableRoots, err := bwrapWritableRoots(p, workDir)
		if err != nil {
			return nil, err
		}
		for _, root := range writableRoots {
			args = append(args, "--bind", root, root)
		}
	}
	for _, sub := range bwrapReadOnlySubpaths(p, workDir) {
		args = append(args, "--ro-bind", sub, sub)
	}
	return args, nil
}

func buildScopedBwrapRootArgs(p policy.Policy, workDir string) ([]string, error) {
	readableRoots := bwrapRegularReadableRoots(policy.ShellReadableRoots(p, workDir))
	writableRoots, err := bwrapWritableRoots(p, workDir)
	if err != nil {
		return nil, err
	}
	readOnlySubpaths := bwrapReadOnlySubpaths(p, workDir)
	destParents := bwrapMountParentDirs(readableRoots, writableRoots, readOnlySubpaths)
	args := []string{"--tmpfs", "/"}
	for _, dir := range destParents {
		args = append(args, "--dir", dir)
	}
	args = append(args, "--dev", "/dev", "--proc", "/proc")
	for _, root := range readableRoots {
		args = append(args, "--ro-bind", root, root)
	}
	return args, nil
}

var quotedAbsolutePathPattern = regexp.MustCompile(`['"](/[^'"\n]+)['"]`)
var bwrapStat = os.Stat

func bwrapMissingMountDiagnostic(result sandbox.CommandResult, p policy.Policy, workDir string) string {
	if !policy.HasExplicitReadableRoots(p) {
		return ""
	}
	mountedRoots, err := bwrapMountedRoots(p, workDir)
	if err != nil {
		return ""
	}
	if detail := bwrapMissingMountStreamDiagnostic(result.Stderr, mountedRoots); detail != "" {
		return detail
	}
	return bwrapMissingMountStreamDiagnostic(result.Stdout, mountedRoots)
}

func bwrapMissingMountStreamDiagnostic(text string, mountedRoots []string) string {
	path := bwrapHostExistingUnmountedPath(text, mountedRoots)
	if path == "" {
		return ""
	}
	return sandbox.SandboxPermissionDeniedMessage + " Host path exists but is not mounted in the sandbox: " + path
}

func bwrapHostExistingUnmountedPath(text string, mountedRoots []string) string {
	for _, line := range strings.Split(text, "\n") {
		if !bwrapLineLooksLikeMissingPath(line) {
			continue
		}
		for _, match := range quotedAbsolutePathPattern.FindAllStringSubmatch(line, -1) {
			if len(match) < 2 {
				continue
			}
			path := filepath.Clean(strings.TrimSpace(match[1]))
			if path == "." || path == string(filepath.Separator) {
				continue
			}
			if _, err := bwrapStat(path); err != nil {
				continue
			}
			if bwrapPathWithinAnyRoot(path, mountedRoots) {
				continue
			}
			return path
		}
	}
	return ""
}

func bwrapLineLooksLikeMissingPath(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "no such file or directory") ||
		strings.Contains(line, "没有那个文件或目录")
}

func bwrapMountedRoots(p policy.Policy, workDir string) ([]string, error) {
	roots := make([]string, 0, len(p.ReadableRoots)+len(p.WritableRoots)+len(p.ReadOnlySubpaths)+16)
	if policy.HasExplicitReadableRoots(p) {
		roots = append(roots, bwrapRegularReadableRoots(policy.ShellReadableRoots(p, workDir))...)
	}
	writableRoots, err := bwrapWritableRoots(p, workDir)
	if err != nil {
		return nil, err
	}
	roots = append(roots, writableRoots...)
	roots = append(roots, bwrapReadOnlySubpaths(p, workDir)...)
	return normalizeStringList(roots), nil
}

func bwrapPathWithinAnyRoot(path string, roots []string) bool {
	targets := []string{filepath.Clean(strings.TrimSpace(path))}
	if resolved, err := filepath.EvalSymlinks(targets[0]); err == nil && strings.TrimSpace(resolved) != "" {
		targets = append(targets, filepath.Clean(resolved))
	}
	for _, target := range normalizeStringList(targets) {
		for _, root := range roots {
			root = filepath.Clean(strings.TrimSpace(root))
			if root == "" || root == "." {
				continue
			}
			if target == root || strings.HasPrefix(target, root+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

func bwrapRegularReadableRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if bwrapManagedMountPath(root) {
			continue
		}
		out = append(out, root)
	}
	return out
}

func bwrapManagedMountPath(path string) bool {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	return cleaned == "/dev" ||
		strings.HasPrefix(cleaned, "/dev/") ||
		cleaned == "/proc" ||
		strings.HasPrefix(cleaned, "/proc/")
}

func bwrapWritableRoots(p policy.Policy, workDir string) ([]string, error) {
	if p.Type == policy.TypeReadOnly {
		return nil, nil
	}
	explicit := make([]string, 0, len(p.WritableRoots))
	for _, one := range p.WritableRoots {
		if resolved := resolveBwrapPath(workDir, one); resolved != "" {
			explicit = append(explicit, policy.WritableRootPath(resolved))
		}
	}
	if err := policy.EnsureExplicitWritableRoots(explicit); err != nil {
		return nil, err
	}

	roots := make([]string, 0, len(explicit)+8)
	roots = append(roots, explicit...)
	roots = append(roots, "/tmp", "/var/tmp")
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		roots = append(roots, filepath.Join(home, ".cache"))
	}
	return filterExistingPaths(normalizeStringList(roots)), nil
}

func bwrapReadOnlySubpaths(p policy.Policy, workDir string) []string {
	values := make([]string, 0, len(p.ReadOnlySubpaths))
	for _, one := range p.ReadOnlySubpaths {
		if resolved := resolveBwrapPath(workDir, one); resolved != "" {
			values = append(values, resolved)
		}
	}
	return filterExistingPaths(normalizeStringList(values))
}

func bwrapMountParentDirs(pathGroups ...[]string) []string {
	dirs := make([]string, 0, 32)
	seen := map[string]struct{}{}
	for _, paths := range pathGroups {
		for _, target := range paths {
			current := filepath.Dir(filepath.Clean(strings.TrimSpace(target)))
			for current != "" && current != "." && current != string(filepath.Separator) {
				if _, ok := seen[current]; !ok {
					seen[current] = struct{}{}
					dirs = append(dirs, current)
				}
				parent := filepath.Dir(current)
				if parent == current {
					break
				}
				current = parent
			}
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		leftDepth := strings.Count(dirs[i], string(filepath.Separator))
		rightDepth := strings.Count(dirs[j], string(filepath.Separator))
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return dirs[i] < dirs[j]
	})
	return dirs
}

func filterExistingPaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			result = append(result, p)
		}
	}
	return result
}

func resolveBwrapPath(baseDir, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}
	if strings.TrimSpace(baseDir) == "" {
		return ""
	}
	return filepath.Clean(filepath.Join(baseDir, trimmed))
}

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

func asyncOutputForwarder(fn func(runnerruntime.OutputChunk)) func(cmdsession.AsyncOutputChunk) {
	if fn == nil {
		return nil
	}
	return func(chunk cmdsession.AsyncOutputChunk) {
		fn(runnerruntime.OutputChunk{Stream: chunk.Stream, Text: string(chunk.Data)})
	}
}

func bwrapProbeFailureDetail(
	bwrapPath string,
	stderr string,
	statFn func(string) (os.FileInfo, error),
	readFileFn func(string) ([]byte, error),
) string {
	lower := strings.ToLower(strings.TrimSpace(stderr))
	if lower == "" {
		return ""
	}
	if !strings.Contains(lower, "uid map") &&
		!strings.Contains(lower, "new namespace") &&
		!strings.Contains(lower, "namespace failed") &&
		!strings.Contains(lower, "operation not permitted") &&
		!strings.Contains(lower, "permission denied") {
		return ""
	}
	parts := []string{"bubblewrap needs a working unprivileged user-namespace setup or a setuid-root bwrap binary on linux"}
	if statFn != nil && strings.TrimSpace(bwrapPath) != "" {
		if info, err := statFn(bwrapPath); err == nil && info.Mode()&os.ModeSetuid == 0 {
			parts = append(parts, fmt.Sprintf("%s is not setuid", bwrapPath))
		}
	}
	if readFileFn != nil {
		if value, ok := readFirstLineInt(readFileFn, "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"); ok && value == 1 {
			parts = append(parts, "kernel.apparmor_restrict_unprivileged_userns=1")
			if !apparmorBwrapProfileLoaded(readFileFn, bwrapPath) {
				parts = append(parts,
					"AppArmor bwrap profile not detected",
					"fix: create /etc/apparmor.d/bwrap with a userns rule and reload it with sudo apparmor_parser -r /etc/apparmor.d/bwrap, or use sandbox.requested_type=landlock",
				)
			}
		}
		if value, ok := readFirstLineInt(readFileFn, "/proc/sys/kernel/unprivileged_userns_clone"); ok && value == 0 {
			parts = append(parts, "kernel.unprivileged_userns_clone=0")
		}
		if value, ok := readFirstLineInt(readFileFn, "/proc/sys/user/max_user_namespaces"); ok && value == 0 {
			parts = append(parts, "user.max_user_namespaces=0")
		}
	}
	parts = append(parts, "docs="+bubblewrapDocsURL)
	return strings.Join(parts, "; ")
}

func apparmorBwrapProfileLoaded(readFileFn func(string) ([]byte, error), bwrapPath string) bool {
	if readFileFn == nil {
		return false
	}
	data, err := readFileFn("/sys/kernel/security/apparmor/profiles")
	if err != nil {
		return false
	}
	candidates := map[string]struct{}{
		"bwrap":          {},
		"/usr/bin/bwrap": {},
	}
	if trimmed := strings.TrimSpace(bwrapPath); trimmed != "" {
		candidates[trimmed] = struct{}{}
		if base := filepath.Base(trimmed); base != "" && base != "." {
			candidates[base] = struct{}{}
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if before, _, ok := strings.Cut(name, " ("); ok {
			name = strings.TrimSpace(before)
		}
		if _, ok := candidates[name]; ok {
			return true
		}
		if strings.HasSuffix(name, "/bwrap") {
			return true
		}
	}
	return false
}

func bubblewrapInstallHint(readFileFn func(string) ([]byte, error)) string {
	if cmd := bubblewrapInstallCommand(readFileFn); cmd != "" {
		return fmt.Sprintf("install bubblewrap (for example: %s); docs=%s", cmd, bubblewrapDocsURL)
	}
	return fmt.Sprintf("install bubblewrap from your distro packages; docs=%s", bubblewrapDocsURL)
}

func bubblewrapInstallCommand(readFileFn func(string) ([]byte, error)) string {
	ids := linuxDistributionIDs(readFileFn)
	switch {
	case containsAnyString(ids, "debian", "ubuntu", "linuxmint", "pop", "elementary", "neon", "raspbian", "kali"):
		return "sudo apt install bubblewrap"
	case containsAnyString(ids, "fedora", "rhel", "centos", "rocky", "almalinux", "ol"):
		return "sudo dnf install bubblewrap"
	case containsAnyString(ids, "arch", "manjaro", "endeavouros", "artix"):
		return "sudo pacman -S bubblewrap"
	case containsAnyString(ids, "opensuse", "opensuse-leap", "opensuse-tumbleweed", "suse", "sles"):
		return "sudo zypper install bubblewrap"
	case containsAnyString(ids, "alpine"):
		return "sudo apk add bubblewrap"
	case containsAnyString(ids, "void"):
		return "sudo xbps-install -S bubblewrap"
	case containsAnyString(ids, "gentoo"):
		return "sudo emerge bubblewrap"
	default:
		return ""
	}
}

func linuxDistributionIDs(readFileFn func(string) ([]byte, error)) []string {
	if readFileFn == nil {
		return nil
	}
	data, err := readFileFn("/etc/os-release")
	if err != nil {
		return nil
	}
	values := make([]string, 0, 4)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.ToUpper(key))
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "ID", "ID_LIKE":
			values = append(values, strings.Fields(strings.ToLower(value))...)
		}
	}
	return normalizeStringList(values)
}

func containsAnyString(values []string, needles ...string) bool {
	for _, value := range values {
		for _, needle := range needles {
			if value == needle {
				return true
			}
		}
	}
	return false
}

func readFirstLineInt(readFileFn func(string) ([]byte, error), path string) (int, bool) {
	if readFileFn == nil {
		return 0, false
	}
	data, err := readFileFn(path)
	if err != nil {
		return 0, false
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func init() {
	sandbox.RegisterBuiltInBackendFactory(backendFactory{})
}
