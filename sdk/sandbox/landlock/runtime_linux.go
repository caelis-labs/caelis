//go:build linux

package landlock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	sdksandbox "github.com/OnslaughtSnail/caelis/sdk/sandbox"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/host"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/cmdsession"
	sandboxpolicy "github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/policy"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/procutil"
	"github.com/OnslaughtSnail/caelis/sdk/sandbox/internal/runnerruntime"
	"golang.org/x/sys/unix"
)

const internalHelperCommand = "__caelis_execenv_helper__"

type landlockRunner struct {
	execCommand    func(context.Context, string, ...string) *exec.Cmd
	executablePath func() (string, error)
	helperPath     string
	probe          func() error
	goos           string
	cfg            Config
	sessionManager *cmdsession.SessionManager
	closed         atomic.Bool
}

func newRuntime(cfg Config) (sdksandbox.Runtime, error) {
	cfg = sdksandbox.NormalizeConfig(cfg)
	runner := &landlockRunner{
		execCommand:    exec.CommandContext,
		executablePath: os.Executable,
		helperPath:     strings.TrimSpace(cfg.HelperPath),
		probe:          probeLandlockSupport,
		goos:           stdruntime.GOOS,
		cfg:            cfg,
		sessionManager: cmdsession.NewSessionManager(cmdsession.DefaultSessionManagerConfig()),
	}
	if err := runner.probeRuntime(context.Background()); err != nil {
		_ = runner.Close()
		return nil, err
	}
	hostRuntime, err := host.New(host.Config{CWD: cfg.CWD})
	if err != nil {
		_ = runner.Close()
		return nil, err
	}
	return runnerruntime.New(runnerruntime.Config{
		Backend: sdksandbox.BackendLandlock,
		Descriptor: sdksandbox.Descriptor{
			Backend:   sdksandbox.BackendLandlock,
			Isolation: sdksandbox.IsolationProcess,
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
				Backend:    sdksandbox.BackendLandlock,
				Permission: sdksandbox.PermissionWorkspaceWrite,
				Isolation:  sdksandbox.IsolationProcess,
				Network:    sdksandbox.NetworkInherit,
			},
		},
		Status: sdksandbox.Status{
			RequestedBackend: sdksandbox.BackendLandlock,
			ResolvedBackend:  sdksandbox.BackendLandlock,
		},
		BaseFS: hostRuntime.FileSystem(),
		Policy: func(constraints sdksandbox.Constraints) sandboxpolicy.Policy {
			return sandboxpolicy.Default(cfg, constraints)
		},
		Runner: runner,
	}), nil
}

func (l *landlockRunner) probeRuntime(ctx context.Context) error {
	if l.goos != "linux" {
		return fmt.Errorf("landlock sandbox is only supported on linux (current=%s)", l.goos)
	}
	if l.probe != nil {
		if err := l.probe(); err != nil {
			return fmt.Errorf("landlock sandbox unavailable: %w", err)
		}
	}
	if err := l.probeHelper(ctx); err != nil {
		return fmt.Errorf("landlock sandbox unavailable: %w", err)
	}
	return nil
}

func (l *landlockRunner) Run(ctx context.Context, req runnerruntime.Request) (sdksandbox.CommandResult, error) {
	runCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()

	policyCWD, err := procutil.ResolveHostWorkDir(req.Dir)
	if err != nil {
		return sdksandbox.CommandResult{}, fmt.Errorf("tool: resolve landlock workdir failed: %w", err)
	}
	effectivePolicy := sandboxpolicy.Default(l.cfg, req.Constraints)
	exePath, err := l.resolveHelperPath()
	if err != nil {
		return sdksandbox.CommandResult{}, fmt.Errorf("tool: resolve landlock helper path failed: %w", err)
	}
	helperArgs, err := buildLandlockHelperArgs(effectivePolicy, policyCWD, policyCWD, req.Command)
	if err != nil {
		return sdksandbox.CommandResult{}, fmt.Errorf("tool: build landlock helper args failed: %w", err)
	}

	cmd := l.execCommand(runCtx, exePath, helperArgs...)
	procutil.ApplyNonInteractiveCommandDefaults(cmd)
	cmd.Env = mergeCommandEnv(req.EnvOverrides)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	lastOutput := atomic.Int64{}
	lastOutput.Store(time.Now().UnixNano())
	cmd.Stdout = procutil.NewActivityWriter(&stdout, &lastOutput, "stdout", emitOutput(req.OnOutput))
	cmd.Stderr = procutil.NewActivityWriter(&stderr, &lastOutput, "stderr", emitOutput(req.OnOutput))

	if err := cmd.Start(); err != nil {
		return sdksandbox.CommandResult{}, fmt.Errorf("tool: landlock sandbox command start failed: %w", err)
	}
	waitErr := procutil.WaitWithIdleTimeout(runCtx, cmd, req.IdleTimeout, &lastOutput)

	result := sdksandbox.CommandResult{
		Stdout:  stdout.String(),
		Stderr:  stderr.String(),
		Route:   sdksandbox.RouteSandbox,
		Backend: sdksandbox.BackendLandlock,
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
		return result, fmt.Errorf("tool: landlock sandbox command timed out after %s; %s", label, commandOutputSummary(result))
	}
	if errors.Is(waitErr, procutil.ErrIdleTimeout) {
		label := "idle limit"
		if req.IdleTimeout > 0 {
			label = req.IdleTimeout.String()
		}
		return result, fmt.Errorf("tool: landlock sandbox command produced no output for %s and was terminated; %s", label, commandOutputSummary(result))
	}
	return result, fmt.Errorf("tool: landlock sandbox command failed: %w; %s", waitErr, commandOutputSummary(result))
}

func (l *landlockRunner) StartAsync(_ context.Context, req runnerruntime.Request) (string, error) {
	manager, err := l.asyncSessionManager()
	if err != nil {
		return "", err
	}
	if req.TTY {
		return "", fmt.Errorf("tool: landlock async tty is not supported")
	}
	policyCWD, err := procutil.ResolveHostWorkDir(req.Dir)
	if err != nil {
		return "", fmt.Errorf("tool: resolve landlock workdir failed: %w", err)
	}
	effectivePolicy := sandboxpolicy.Default(l.cfg, req.Constraints)
	exePath, err := l.resolveHelperPath()
	if err != nil {
		return "", fmt.Errorf("tool: resolve landlock helper path failed: %w", err)
	}
	session, err := manager.StartSession(cmdsession.AsyncSessionConfig{
		Command:         req.Command,
		Dir:             req.Dir,
		Env:             mergeCommandEnv(req.EnvOverrides),
		OutputBufferCap: 256 * 1024,
		Timeout:         req.Timeout,
		IdleTimeout:     req.IdleTimeout,
		BuildCommand: func(ctx context.Context, cfg cmdsession.AsyncSessionConfig) (*exec.Cmd, error) {
			helperArgs, err := buildLandlockHelperArgs(effectivePolicy, policyCWD, policyCWD, cfg.Command)
			if err != nil {
				return nil, err
			}
			cmd := l.execCommand(ctx, exePath, helperArgs...)
			cmd.Env = append([]string(nil), cfg.Env...)
			return cmd, nil
		},
	})
	if err != nil {
		return "", err
	}
	return session.ID, nil
}

func (l *landlockRunner) WriteInput(sessionID string, input []byte) error {
	manager, err := l.asyncSessionManager()
	if err != nil {
		return err
	}
	return manager.WriteInput(sessionID, input)
}

func (l *landlockRunner) ReadOutput(sessionID string, stdoutMarker, stderrMarker int64) ([]byte, []byte, int64, int64, error) {
	manager, err := l.asyncSessionManager()
	if err != nil {
		return nil, nil, 0, 0, err
	}
	return manager.ReadOutput(sessionID, stdoutMarker, stderrMarker)
}

func (l *landlockRunner) GetSessionStatus(sessionID string) (cmdsession.SessionStatus, error) {
	manager, err := l.asyncSessionManager()
	if err != nil {
		return cmdsession.SessionStatus{}, err
	}
	return manager.GetSessionStatus(sessionID)
}

func (l *landlockRunner) GetSession(sessionID string) (*cmdsession.AsyncSession, error) {
	manager, err := l.asyncSessionManager()
	if err != nil {
		return nil, err
	}
	return manager.GetSession(sessionID)
}

func (l *landlockRunner) WaitSession(ctx context.Context, sessionID string, timeout time.Duration) (sdksandbox.CommandResult, error) {
	manager, err := l.asyncSessionManager()
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
			return sdksandbox.CommandResult{Route: sdksandbox.RouteSandbox, Backend: sdksandbox.BackendLandlock}, nil
		}
	} else if _, err := manager.WaitSession(ctx, sessionID); err != nil {
		return sdksandbox.CommandResult{}, err
	}
	result, err := manager.GetResult(sessionID)
	result.Route = sdksandbox.RouteSandbox
	result.Backend = sdksandbox.BackendLandlock
	return result, err
}

func (l *landlockRunner) TerminateSession(sessionID string) error {
	manager, err := l.asyncSessionManager()
	if err != nil {
		return err
	}
	return manager.TerminateSession(sessionID)
}

func (l *landlockRunner) Close() error {
	l.closed.Store(true)
	if l.sessionManager != nil {
		return l.sessionManager.Close()
	}
	return nil
}

func (l *landlockRunner) asyncSessionManager() (*cmdsession.SessionManager, error) {
	if l == nil || l.closed.Load() || l.sessionManager == nil {
		return nil, fmt.Errorf("sdk/sandbox/landlock: runner is closed")
	}
	return l.sessionManager, nil
}

func buildLandlockHelperArgs(p sandboxpolicy.Policy, policyCWD, commandCWD, command string) ([]string, error) {
	policyJSON, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return []string{
		internalHelperCommand,
		"--policy-json", string(policyJSON),
		"--policy-cwd", policyCWD,
		"--command-cwd", commandCWD,
		"--command", command,
	}, nil
}

func (l *landlockRunner) resolveHelperPath() (string, error) {
	exePath := strings.TrimSpace(l.helperPath)
	if exePath != "" {
		return exePath, nil
	}
	return l.executablePath()
}

func (l *landlockRunner) probeHelper(ctx context.Context) error {
	helperPath, err := l.resolveHelperPath()
	if err != nil {
		return fmt.Errorf("resolve landlock helper path: %w", err)
	}
	if ctx == nil {
		return fmt.Errorf("landlock helper probe requires context")
	}
	cmd := l.execCommand(ctx, helperPath, internalHelperCommand, "--probe")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return fmt.Errorf("landlock helper probe failed via %s: %w", helperPath, err)
		}
		return fmt.Errorf("landlock helper probe failed via %s: %w; stderr=%s", helperPath, err, message)
	}
	return nil
}

func probeLandlockSupport() error {
	abi, err := landlockABI()
	if err == nil {
		if abi <= 0 {
			return errors.New("landlock returned invalid ABI version")
		}
		return nil
	}
	if errors.Is(err, unix.ENOSYS) {
		return errors.New("landlock syscalls are unavailable on this kernel")
	}
	if errors.Is(err, unix.EOPNOTSUPP) {
		return errors.New("landlock is disabled or unsupported by this kernel")
	}
	return err
}

func MaybeRunInternalHelper(args []string) bool {
	if len(args) == 0 || args[0] != internalHelperCommand {
		return false
	}
	if err := runInternalHelper(args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "internal sandbox helper failed: %v\n", err)
		os.Exit(1)
	}
	return true
}

type internalHelperConfig struct {
	Probe      bool
	PolicyJSON string
	PolicyCWD  string
	CommandCWD string
	Command    string
}

func runInternalHelper(args []string) error {
	stdruntime.LockOSThread()

	fs := flag.NewFlagSet(internalHelperCommand, flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var cfg internalHelperConfig
	fs.BoolVar(&cfg.Probe, "probe", false, "helper availability probe")
	fs.StringVar(&cfg.PolicyJSON, "policy-json", "", "sandbox policy json")
	fs.StringVar(&cfg.PolicyCWD, "policy-cwd", "", "sandbox policy cwd")
	fs.StringVar(&cfg.CommandCWD, "command-cwd", "", "command cwd")
	fs.StringVar(&cfg.Command, "command", "", "command to execute")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.Probe {
		return nil
	}
	if strings.TrimSpace(cfg.PolicyJSON) == "" {
		return errors.New("missing --policy-json")
	}
	if strings.TrimSpace(cfg.Command) == "" {
		return errors.New("missing --command")
	}

	var p sandboxpolicy.Policy
	if err := json.Unmarshal([]byte(cfg.PolicyJSON), &p); err != nil {
		return fmt.Errorf("decode policy: %w", err)
	}

	needFSRestrictions := p.Type != sandboxpolicy.TypeDangerFull && p.Type != sandboxpolicy.TypeExternal
	if needFSRestrictions || !p.NetworkAccess {
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			return fmt.Errorf("set no_new_privs: %w", err)
		}
	}
	if !p.NetworkAccess {
		if err := installRestrictedNetworkSeccomp(); err != nil {
			return fmt.Errorf("install seccomp: %w", err)
		}
	}
	if needFSRestrictions {
		if err := applyLandlockFilesystemPolicy(p, cfg.PolicyCWD); err != nil {
			return err
		}
	}
	if strings.TrimSpace(cfg.CommandCWD) != "" {
		if err := os.Chdir(cfg.CommandCWD); err != nil {
			return fmt.Errorf("chdir: %w", err)
		}
	}

	shellPath, err := exec.LookPath("bash")
	if err != nil {
		return fmt.Errorf("resolve bash: %w", err)
	}
	return unix.Exec(shellPath, []string{"bash", "-lc", cfg.Command}, os.Environ())
}

func applyLandlockFilesystemPolicy(p sandboxpolicy.Policy, policyCWD string) error {
	abi, err := landlockABI()
	if err != nil {
		return err
	}
	attr := unix.LandlockRulesetAttr{
		Access_fs: landlockReadWriteMaskForABI(abi),
	}
	rulesetFD, err := landlockCreateRuleset(&attr, 0)
	if err != nil {
		return fmt.Errorf("create landlock ruleset: %w", err)
	}
	defer unix.Close(rulesetFD)

	if readableRoots := sandboxpolicy.ShellReadableRoots(p, policyCWD); len(readableRoots) > 0 {
		for _, root := range readableRoots {
			if err := landlockAddPathRule(rulesetFD, root, landlockReadOnlyMaskForABI(abi)); err != nil {
				return fmt.Errorf("allow readable root %s: %w", root, err)
			}
		}
	} else if err := landlockAddPathRule(rulesetFD, "/", landlockReadOnlyMaskForABI(abi)); err != nil {
		return fmt.Errorf("allow read-only root: %w", err)
	}
	if err := landlockAddPathRule(rulesetFD, "/dev/null", landlockFileReadWriteMaskForABI(abi)); err != nil {
		return fmt.Errorf("allow /dev/null writes: %w", err)
	}
	for _, root := range landlockWritableRoots(p, policyCWD) {
		if err := landlockAddPathRule(rulesetFD, root, landlockReadWriteMaskForABI(abi)); err != nil {
			return fmt.Errorf("allow writable root %s: %w", root, err)
		}
	}
	if err := landlockRestrictSelf(rulesetFD); err != nil {
		return fmt.Errorf("restrict self with landlock: %w", err)
	}
	return nil
}

func landlockWritableRoots(p sandboxpolicy.Policy, workDir string) []string {
	if p.Type == sandboxpolicy.TypeReadOnly {
		return nil
	}
	roots := make([]string, 0, len(p.WritableRoots)+8)
	for _, one := range p.WritableRoots {
		resolved := sandboxpolicy.ResolveSandboxPath(workDir, one)
		if resolved != "" {
			roots = append(roots, resolved)
		}
	}
	roots = append(roots, "/tmp", "/var/tmp")
	home, err := os.UserHomeDir()
	if err == nil && strings.TrimSpace(home) != "" {
		roots = append(roots, filepath.Join(home, ".cache"))
	}
	return sandboxpolicy.FilterExistingPaths(roots)
}

func landlockABI() (int, error) {
	r1, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0,
		0,
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION),
	)
	if errno != 0 {
		return 0, errno
	}
	return int(r1), nil
}

func landlockCreateRuleset(attr *unix.LandlockRulesetAttr, flags uintptr) (int, error) {
	var ptr uintptr
	var size uintptr
	if attr != nil {
		ptr = uintptr(unsafe.Pointer(attr))
		size = unsafe.Sizeof(*attr)
	}
	r1, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, ptr, size, flags)
	if errno != 0 {
		return 0, errno
	}
	return int(r1), nil
}

func landlockAddPathRule(rulesetFD int, path string, access uint64) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	attr := unix.LandlockPathBeneathAttr{
		Allowed_access: access,
		Parent_fd:      int32(fd),
	}
	_, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFD),
		uintptr(unix.LANDLOCK_RULE_PATH_BENEATH),
		uintptr(unsafe.Pointer(&attr)),
		0,
		0,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func landlockRestrictSelf(rulesetFD int) error {
	_, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFD), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func landlockReadOnlyMaskForABI(abi int) uint64 {
	mask := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_EXECUTE)
	if abi >= 5 {
		mask |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return mask
}

func landlockReadWriteMaskForABI(abi int) uint64 {
	mask := landlockReadOnlyMaskForABI(abi) |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM
	if abi >= 2 {
		mask |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		mask |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	return mask
}

func landlockFileReadWriteMaskForABI(abi int) uint64 {
	mask := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE)
	if abi >= 3 {
		mask |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		mask |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return mask
}

func installRestrictedNetworkSeccomp() error {
	prog, err := buildRestrictedNetworkSeccompProgram()
	if err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
		return err
	}
	return nil
}

func buildRestrictedNetworkSeccompProgram() (unix.SockFprog, error) {
	deny := uint32(unix.SECCOMP_RET_ERRNO | (unix.EPERM & unix.SECCOMP_RET_DATA))
	allow := uint32(unix.SECCOMP_RET_ALLOW)
	kill := uint32(unix.SECCOMP_RET_KILL_PROCESS)

	filters := []unix.SockFilter{
		bpfStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataOffsetArch),
		bpfJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, seccompAuditArch(), 1, 0),
		bpfStmt(unix.BPF_RET|unix.BPF_K, kill),
		bpfStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataOffsetNR),
	}

	for _, nr := range []int{
		unix.SYS_CONNECT,
		unix.SYS_ACCEPT,
		unix.SYS_ACCEPT4,
		unix.SYS_BIND,
		unix.SYS_LISTEN,
		unix.SYS_GETPEERNAME,
		unix.SYS_GETSOCKNAME,
		unix.SYS_SHUTDOWN,
		unix.SYS_SENDTO,
		unix.SYS_SENDMMSG,
		unix.SYS_RECVMMSG,
		unix.SYS_GETSOCKOPT,
		unix.SYS_SETSOCKOPT,
	} {
		filters = append(filters,
			bpfJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, uint32(nr), 0, 1),
			bpfStmt(unix.BPF_RET|unix.BPF_K, deny),
		)
	}

	filters = append(filters,
		bpfJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, uint32(unix.SYS_SOCKET), 0, 4),
		bpfStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataOffsetArg0),
		bpfJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.AF_UNIX, 1, 0),
		bpfStmt(unix.BPF_RET|unix.BPF_K, deny),
		bpfStmt(unix.BPF_RET|unix.BPF_K, allow),
		bpfJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, uint32(unix.SYS_SOCKETPAIR), 0, 4),
		bpfStmt(unix.BPF_LD|unix.BPF_W|unix.BPF_ABS, seccompDataOffsetArg0),
		bpfJump(unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K, unix.AF_UNIX, 1, 0),
		bpfStmt(unix.BPF_RET|unix.BPF_K, deny),
		bpfStmt(unix.BPF_RET|unix.BPF_K, allow),
		bpfStmt(unix.BPF_RET|unix.BPF_K, allow),
	)

	return unix.SockFprog{
		Len:    uint16(len(filters)),
		Filter: &filters[0],
	}, nil
}

func seccompAuditArch() uint32 {
	switch stdruntime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64
	default:
		panic(fmt.Sprintf("unsupported architecture for seccomp filter: %s", stdruntime.GOARCH))
	}
}

func bpfStmt(code uint16, k uint32) unix.SockFilter {
	return unix.SockFilter{Code: code, K: k}
}

func bpfJump(code uint16, k uint32, jt, jf uint8) unix.SockFilter {
	return unix.SockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

const (
	seccompDataOffsetNR   = 0
	seccompDataOffsetArch = 4
	seccompDataOffsetArg0 = 16
)

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
