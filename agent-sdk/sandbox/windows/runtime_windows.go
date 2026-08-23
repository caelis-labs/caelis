//go:build windows

package windows

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/policyfs"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/consoleoutput"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/internal/conpty"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/internal/outputwait"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/internal/sessionrun"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/acl"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/capability"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/jobprocess"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/pathutil"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/internal/win32"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/windows/winps"
)

const (
	workspaceManifestVersion = 2
	windowsOutputCap         = 1024 * 1024
	windowsTerminateDrain    = 500 * time.Millisecond
	windowsCacheMaxBytes     = 10 * 1024 * 1024 * 1024
	windowsCacheMaxAge       = 14 * 24 * time.Hour
	windowsPreflightTimeout  = 15 * time.Second
)

var resolveHostReceiptAuthorityRoot = defaultHostReceiptAuthorityRoot

type ensureMode string

type capabilityBindingMode string

const (
	ensureModeForegroundCore    ensureMode            = "foreground-core"
	ensureModeBackgroundRefresh ensureMode            = "background-refresh"
	bindingModeCreate           capabilityBindingMode = "create"
	bindingModeLookup           capabilityBindingMode = "lookup"
	bindingModeDerive           capabilityBindingMode = "derive"
)

func newRuntime(cfg Config) (sandbox.Runtime, error) {
	cfg = sandbox.NormalizeConfig(cfg)
	hostUserSID, err := win32.CurrentProcessUserSID()
	if err != nil {
		return nil, fmt.Errorf("impl/sandbox/windows: resolve current Host user SID: %w", err)
	}
	hostUserSID, err = win32.NormalizeSID(hostUserSID)
	if err != nil {
		return nil, fmt.Errorf("impl/sandbox/windows: normalize current Host user SID: %w", err)
	}
	var authorityRoot string
	if strings.TrimSpace(cfg.HostAuthorityDir) != "" {
		authorityRoot, err = hostReceiptAuthorityRootAt(cfg.HostAuthorityDir, hostUserSID)
	} else {
		authorityRoot, err = resolveHostReceiptAuthorityRoot(hostUserSID)
	}
	if err != nil {
		return nil, fmt.Errorf("impl/sandbox/windows: resolve Host ACL receipt authority: %w", err)
	}
	return newRuntimeWithHostIdentity(cfg, hostUserSID, authorityRoot)
}

func newRuntimeWithHostIdentity(cfg Config, hostUserSID, authorityRoot string) (sandbox.Runtime, error) {
	cfg = sandbox.NormalizeConfig(cfg)
	stateRoot, err := resolveStateRoot(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	hostUserSID, err = win32.NormalizeSID(hostUserSID)
	if err != nil {
		return nil, fmt.Errorf("impl/sandbox/windows: normalize Host user SID: %w", err)
	}
	authorityRoot = pathutil.Normalize(authorityRoot)
	if authorityRoot == "" || !filepath.IsAbs(authorityRoot) {
		return nil, fmt.Errorf("impl/sandbox/windows: canonical absolute Host ACL receipt authority is required")
	}
	hostRuntime, err := host.New(host.Config{CWD: cfg.CWD})
	if err != nil {
		return nil, err
	}
	runtimeID, err := newID("runtime")
	if err != nil {
		return nil, err
	}
	processIdentity, err := win32.CurrentProcessIdentity()
	if err != nil {
		return nil, fmt.Errorf("impl/sandbox/windows: identify Host process: %w", err)
	}
	r := &runtime{
		cfg:                      cfg,
		stateRoot:                stateRoot,
		hostUserSID:              hostUserSID,
		hostReceiptAuthorityRoot: authorityRoot,
		hostProcessIdentity:      processIdentity,
		runtimeID:                runtimeID,
		fs:                       hostRuntime.FileSystem(),
		sessions:                 map[string]*windowsSession{},
	}
	r.stateCoordinator = sandboxCoordinatorFor(stateRoot)
	r.registeredEnvRoot = r.sandboxEnvRoot(cfg.CWD)
	if err := r.stateCoordinator.registerRuntime(r.registeredEnvRoot); err != nil {
		return nil, fmt.Errorf("impl/sandbox/windows: register runtime state: %w", err)
	}
	return r, nil
}

func defaultHostReceiptAuthorityRoot(hostUserSID string) (string, error) {
	localAppData, err := win32.CurrentUserLocalAppData()
	if err != nil {
		return "", err
	}
	return hostReceiptAuthorityRootAt(filepath.Join(localAppData, "Caelis", "sandbox", "windows", "hosts"), hostUserSID)
}

func hostReceiptAuthorityRootAt(base, hostUserSID string) (string, error) {
	base = pathutil.Normalize(base)
	if base == "" || !filepath.IsAbs(base) {
		return "", fmt.Errorf("canonical absolute Host authority base is required")
	}
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(hostUserSID))))
	return filepath.Join(base, hex.EncodeToString(sum[:])[:16]), nil
}

type runtime struct {
	cfg         sandbox.Config
	stateRoot   string
	hostUserSID string
	fs          sandbox.FileSystem

	hostReceiptAuthorityRoot string
	hostProcessIdentity      win32.ProcessIdentity
	runtimeID                string
	hostRegistrationMu       sync.Mutex
	hostRegistered           bool
	pendingHostUseReleases   int
	hostAuthorityMu          sync.Mutex
	hostAuthorityValidated   bool

	stateCoordinator  *sandboxStateCoordinator
	registeredEnvRoot string
	closeOnce         sync.Once
	closeErr          error

	setupMu   sync.RWMutex
	refreshMu sync.RWMutex
	mu        sync.RWMutex
	sessions  map[string]*windowsSession

	lastWorkspaceSetupError string
	refreshRunning          bool
	lastRefreshError        string
	lastRefreshAt           time.Time
	lastCacheCleanupAt      time.Time
	lastCacheBytes          int64
}

func (r *runtime) Describe() sandbox.Descriptor {
	return sandbox.Descriptor{
		Backend:   sandbox.BackendWindows,
		Isolation: sandbox.IsolationProcess,
		Capabilities: sandbox.CapabilitySet{
			FileSystem:     true,
			CommandExec:    true,
			AsyncSessions:  true,
			TTY:            true,
			NetworkControl: false,
			PathPolicy:     true,
			EnvPolicy:      true,
		},
		DefaultConstraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Isolation:  sandbox.IsolationProcess,
			Network:    sandbox.NetworkEnabled,
		},
	}
}

func (r *runtime) FileSystem() sandbox.FileSystem {
	return r.FileSystemFor(sandbox.Constraints{})
}

func (r *runtime) FileSystemFor(constraints sandbox.Constraints) sandbox.FileSystem {
	if r == nil || r.fs == nil {
		return nil
	}
	return policyfs.New(r.fs, func() policy.Policy {
		// Windows workspace-write intentionally does not enforce hidden paths;
		// only write roots and deny-write carveouts are security policy.
		p := policy.Default(r.cfg, sandbox.NormalizeConstraints(constraints))
		p.HiddenRoots = nil
		return p
	})
}

func (r *runtime) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req = sandbox.CloneRequest(req)
	if req.TTY {
		session, err := r.Start(ctx, req)
		if err != nil {
			return sandbox.CommandResult{Route: sandbox.RouteSandbox, Backend: sandbox.BackendWindows, ExitCode: -1}, err
		}
		return sessionrun.Wait(ctx, session, req.Stdin)
	}
	result := sandbox.CommandResult{Route: sandbox.RouteSandbox, Backend: sandbox.BackendWindows, ExitCode: -1}
	releaseUse, err := r.beginRuntimeUse()
	if err != nil {
		result.Error = err.Error()
		return result, fmt.Errorf("impl/sandbox/windows: start command: %w", err)
	}
	keepUse := false
	defer func() {
		if !keepUse {
			releaseUse()
		}
	}()
	policy, err := r.ensureForRequest(ctx, req)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	defer r.startBackgroundRefresh(context.WithoutCancel(ctx), req)
	runCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()
	cmd, token, err := r.restrictedShellCommand(runCtx, req, len(req.Stdin) > 0, policy)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	stdout := consoleoutput.NewCappedBuffer(windowsOutputCap)
	stderr := consoleoutput.NewCappedBuffer(windowsOutputCap)
	var exitCode int
	exitCode, err, keepUse = runAtomicJobProcess(runCtx, cmd, token, req.Stdin, stdout, stderr, releaseUse)
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	result.ExitCode = exitCode
	if err != nil {
		result.Error = err.Error()
	}
	if req.OnOutput != nil {
		if result.Stdout != "" {
			req.OnOutput(sandbox.OutputChunk{Stream: "stdout", Text: result.Stdout, Cursor: int64(len([]byte(result.Stdout)))})
		}
		if result.Stderr != "" {
			req.OnOutput(sandbox.OutputChunk{Stream: "stderr", Text: result.Stderr, Cursor: int64(len([]byte(result.Stderr)))})
		}
	}
	return result, err
}

func (r *runtime) Start(ctx context.Context, req sandbox.CommandRequest) (sandbox.Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req = sandbox.CloneRequest(req)
	releaseUse, err := r.beginRuntimeUse()
	if err != nil {
		return nil, fmt.Errorf("impl/sandbox/windows: start command: %w", err)
	}
	keepUse := false
	defer func() {
		if !keepUse {
			releaseUse()
		}
	}()
	policy, err := r.ensureForRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	defer r.startBackgroundRefresh(context.WithoutCancel(ctx), req)
	sessionID, err := newID("exec")
	if err != nil {
		return nil, err
	}
	terminalID, err := newID("term")
	if err != nil {
		return nil, err
	}
	cmdCtx := context.WithoutCancel(ctx)
	cancel := func() {}
	if req.Timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(cmdCtx, req.Timeout)
	}
	cmd, token, err := r.restrictedShellCommand(cmdCtx, req, req.TTY, policy)
	if err != nil {
		cancel()
		return nil, err
	}
	if req.TTY {
		process, startErr := conpty.Start(conpty.Config{
			Token:       uintptr(token),
			Application: cmd.Path,
			Args:        append([]string(nil), cmd.Args[1:]...),
			Dir:         cmd.Dir,
			Env:         append([]string(nil), cmd.Env...),
		})
		_ = token.Close()
		if startErr != nil {
			cancel()
			return nil, fmt.Errorf("impl/sandbox/windows: start ConPTY: %w", startErr)
		}
		now := time.Now()
		session := &windowsSession{
			ref: sandbox.SessionRef{Backend: sandbox.BackendWindows, SessionID: sessionID},
			terminal: sandbox.TerminalRef{
				Backend: sandbox.BackendWindows, SessionID: sessionID, TerminalID: terminalID,
			},
			cmd:           cmd,
			conpty:        process,
			stdin:         process.Input(),
			stdoutReader:  process.Output(),
			cancel:        cancel,
			running:       true,
			supportsInput: true,
			startedAt:     now,
			updatedAt:     now,
			done:          make(chan struct{}),
			releaseUse:    releaseUse,
			onOutput:      req.OnOutput,
			outputSignal:  make(chan struct{}),
		}
		r.mu.Lock()
		r.sessions[sessionID] = session
		r.mu.Unlock()
		session.wg.Add(1)
		go session.readStream(process.Output(), "stdout")
		go session.waitForExit()
		go watchSessionContext(cmdCtx, session.done, process.Terminate)
		keepUse = true
		return session, nil
	}
	process, err := jobprocess.Start(jobprocess.Config{Token: uintptr(token), Application: cmd.Path, Args: append([]string(nil), cmd.Args[1:]...), Dir: cmd.Dir, Env: append([]string(nil), cmd.Env...)})
	_ = token.Close()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("impl/sandbox/windows: start atomic Job process: %w", err)
	}

	now := time.Now()
	session := &windowsSession{
		ref: sandbox.SessionRef{
			Backend:   sandbox.BackendWindows,
			SessionID: sessionID,
		},
		terminal: sandbox.TerminalRef{
			Backend:    sandbox.BackendWindows,
			SessionID:  sessionID,
			TerminalID: terminalID,
		},
		cmd:          cmd,
		jobProcess:   process,
		stdoutReader: process.Stdout(),
		stderrReader: process.Stderr(),
		cancel:       cancel,
		running:      true,
		startedAt:    now,
		updatedAt:    now,
		done:         make(chan struct{}),
		releaseUse:   releaseUse,
		onOutput:     req.OnOutput,
		outputSignal: make(chan struct{}),
	}
	r.mu.Lock()
	r.sessions[sessionID] = session
	r.mu.Unlock()

	session.wg.Add(2)
	go session.readStream(process.Stdout(), "stdout")
	go session.readStream(process.Stderr(), "stderr")
	go session.waitForExit()
	go watchSessionContext(cmdCtx, session.done, process.Terminate)
	keepUse = true
	return session, nil
}

func watchSessionContext(ctx context.Context, done <-chan struct{}, terminate func() error) {
	select {
	case <-ctx.Done():
		if terminate != nil {
			_ = terminate()
		}
	case <-done:
	}
}

func (r *runtime) OpenSession(id string) (sandbox.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	session, ok := r.sessions[strings.TrimSpace(id)]
	if !ok {
		return nil, fmt.Errorf("impl/sandbox/windows: session %q not found", id)
	}
	return session, nil
}

func (r *runtime) OpenSessionRef(ref sandbox.SessionRef) (sandbox.Session, error) {
	ref = sandbox.CloneSessionRef(ref)
	backend := sandbox.CanonicalBackend(ref.Backend)
	if backend != "" && backend != sandbox.BackendWindows {
		return nil, fmt.Errorf("impl/sandbox/windows: backend %q is unsupported", ref.Backend)
	}
	return r.OpenSession(ref.SessionID)
}

func (r *runtime) SupportedBackends() []sandbox.Backend {
	return []sandbox.Backend{sandbox.BackendWindows}
}

func (r *runtime) restrictedShellCommand(ctx context.Context, req sandbox.CommandRequest, interactive bool, policy workspacePolicy) (*exec.Cmd, win32.Token, error) {
	token, err := win32.RestrictedCurrentProcessTokenWithSIDs(policy.CapabilitySIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("impl/sandbox/windows: create restricted token: %w", err)
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", winps.Args(req.Command, winps.Options{Interactive: interactive})...)
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		dir = r.cfg.CWD
	}
	cmd.Dir = dir
	env, err := sandboxEnvironment(policy, req.Env)
	if err != nil {
		_ = token.Close()
		return nil, 0, err
	}
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(token)}
	win32.ConfigureHiddenConsole(cmd)
	return cmd, token, nil
}

func runAtomicJobProcess(ctx context.Context, cmd *exec.Cmd, token win32.Token, stdin []byte, stdout, stderr io.Writer, releaseUse func()) (int, error, bool) {
	process, err := jobprocess.Start(jobprocess.Config{Token: uintptr(token), Application: cmd.Path, Args: append([]string(nil), cmd.Args[1:]...), Dir: cmd.Dir, Env: append([]string(nil), cmd.Env...), Stdin: len(stdin) > 0})
	_ = token.Close()
	if err != nil {
		return -1, err, false
	}
	defer process.ClosePipes()
	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() { defer copyWG.Done(); _, _ = io.Copy(stdout, process.Stdout()) }()
	go func() { defer copyWG.Done(); _, _ = io.Copy(stderr, process.Stderr()) }()
	if input := process.Input(); input != nil {
		_, writeErr := input.Write(stdin)
		closeErr := input.Close()
		if writeErr != nil || closeErr != nil {
			_ = process.Terminate()
			exitCode, waitErr := process.WaitRoot()
			drainErr, keepUse := drainAtomicJobProcess(process, releaseUse)
			copyWG.Wait()
			return exitCode, errors.Join(writeErr, closeErr, waitErr, drainErr), keepUse
		}
	}
	waitCh := make(chan struct {
		code int
		err  error
	}, 1)
	go func() {
		code, waitErr := process.WaitRoot()
		waitCh <- struct {
			code int
			err  error
		}{code, waitErr}
	}()
	var waited struct {
		code int
		err  error
	}
	var contextErr error
	select {
	case waited = <-waitCh:
	case <-ctx.Done():
		contextErr = ctx.Err()
		_ = process.Terminate()
		waited = <-waitCh
	}
	drainErr, keepUse := drainAtomicJobProcess(process, releaseUse)
	copyWG.Wait()
	return waited.code, errors.Join(contextErr, waited.err, drainErr), keepUse
}

func drainAtomicJobProcess(process *jobprocess.Process, releaseUse func()) (error, bool) {
	drainCtx, cancel := context.WithTimeout(context.Background(), windowsPreflightTimeout)
	drainErr := process.DrainAndClose(drainCtx)
	cancel()
	if drainErr == nil || !process.NeedsDrainRetry() {
		return drainErr, false
	}
	go retryAtomicJobProcessDrain(process, releaseUse)
	return drainErr, true
}

func retryAtomicJobProcessDrain(process *jobprocess.Process, releaseUse func()) {
	for {
		drainCtx, cancel := context.WithTimeout(context.Background(), windowsPreflightTimeout)
		err := process.DrainAndClose(drainCtx)
		cancel()
		if err == nil || !process.NeedsDrainRetry() {
			releaseUse()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type workspacePolicy struct {
	WorkspaceRoot           string            `json:"workspace_root,omitempty"`
	CommandDir              string            `json:"command_dir,omitempty"`
	SandboxEnvRoot          string            `json:"sandbox_env_root,omitempty"`
	WriteRoots              []string          `json:"write_roots,omitempty"`
	DenyWritePaths          []string          `json:"deny_write_paths,omitempty"`
	PolicyHash              string            `json:"policy_hash,omitempty"`
	CapabilitySIDs          []string          `json:"capability_sids,omitempty"`
	WriteRootCapabilitySIDs map[string]string `json:"write_root_capability_sids,omitempty"`
}

type workspaceManifest struct {
	Version                 int               `json:"version"`
	WorkspaceRoot           string            `json:"workspace_root,omitempty"`
	SandboxEnvRoot          string            `json:"sandbox_env_root,omitempty"`
	PolicyHash              string            `json:"policy_hash,omitempty"`
	CapabilitySIDs          []string          `json:"capability_sids,omitempty"`
	WriteRoots              []string          `json:"write_roots,omitempty"`
	DenyWritePaths          []string          `json:"deny_write_paths,omitempty"`
	WriteRootCapabilitySIDs map[string]string `json:"write_root_capability_sids,omitempty"`
	ACEs                    []manifestACE     `json:"aces,omitempty"`
	Phase                   string            `json:"phase,omitempty"`
	DeletingEnvRoot         string            `json:"deleting_env_root,omitempty"`
	ManagedReceipts         []manifestReceipt `json:"managed_receipts,omitempty"`
	RetiringReceipts        []manifestReceipt `json:"retiring_receipts,omitempty"`
	LegacyACEs              []manifestACE     `json:"legacy_aces,omitempty"`
	LegacyMigrationPrepared bool              `json:"legacy_migration_prepared,omitempty"`
	UpdatedAt               time.Time         `json:"updated_at,omitempty"`
}

type manifestReceipt struct {
	Path    string         `json:"path"`
	Entry   acl.Entry      `json:"entry"`
	Receipt acl.ACEReceipt `json:"receipt"`
	Applied bool           `json:"applied"`
}

type manifestACE struct {
	Path      string `json:"path,omitempty"`
	Principal string `json:"principal,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Rights    string `json:"rights,omitempty"`
	Inherit   bool   `json:"inherit,omitempty"`
}

func (r *runtime) ensureForRequest(ctx context.Context, req sandbox.CommandRequest) (workspacePolicy, error) {
	return r.ensureForRequestMode(ctx, req, ensureModeForegroundCore)
}

func (r *runtime) ensureForRequestMode(ctx context.Context, req sandbox.CommandRequest, mode ensureMode) (workspacePolicy, error) {
	if err := ctx.Err(); err != nil {
		return workspacePolicy{}, err
	}
	policy, err := r.policyForRequestMode(req, mode)
	if err != nil {
		r.recordWorkspaceSetupError(err)
		return workspacePolicy{}, err
	}
	r.stateCoordinator.aclMu.Lock()
	defer r.stateCoordinator.aclMu.Unlock()
	if err := os.MkdirAll(r.sandboxStateDir(), 0o700); err != nil {
		r.recordWorkspaceSetupError(err)
		return workspacePolicy{}, err
	}
	if err := r.migrateLegacyManifestLocked(); err != nil {
		r.recordWorkspaceSetupError(err)
		return workspacePolicy{}, err
	}
	if err := r.activateReceiptPolicyLocked(ctx, policy, mode == ensureModeBackgroundRefresh && r.stateCoordinator.canPruneACLs()); err != nil {
		r.recordWorkspaceSetupError(err)
		return policy, err
	}
	r.clearWorkspaceSetupError()
	return policy, nil
}

func (r *runtime) policyForRequest(req sandbox.CommandRequest) (workspacePolicy, error) {
	return r.policyForRequestMode(req, ensureModeBackgroundRefresh)
}

func (r *runtime) foregroundPolicyForRequest(req sandbox.CommandRequest) (workspacePolicy, error) {
	return r.policyForRequestMode(req, ensureModeForegroundCore)
}

func (r *runtime) policyForRequestMode(req sandbox.CommandRequest, mode ensureMode) (workspacePolicy, error) {
	return r.policyForRequestWithBinding(req, bindingModeCreate, mode)
}

func (r *runtime) inspectPolicyForRequest(req sandbox.CommandRequest) (workspacePolicy, error) {
	return r.policyForRequestWithBinding(req, bindingModeLookup, ensureModeBackgroundRefresh)
}

func (r *runtime) derivePolicyForRequest(req sandbox.CommandRequest) (workspacePolicy, error) {
	return r.policyForRequestWithBinding(req, bindingModeDerive, ensureModeBackgroundRefresh)
}

func (r *runtime) policyForRequestWithBinding(req sandbox.CommandRequest, bindingMode capabilityBindingMode, mode ensureMode) (workspacePolicy, error) {
	constraints := sandbox.EffectiveConstraints(req)
	constraints.Network = effectiveWindowsSandboxNetwork(constraints.Network)
	if constraints.Permission == "" || constraints.Permission == sandbox.PermissionDefault {
		constraints.Permission = sandbox.PermissionWorkspaceWrite
	}
	if constraints.Permission != sandbox.PermissionWorkspaceWrite {
		return workspacePolicy{}, fmt.Errorf("impl/sandbox/windows: permission mode %q is unsupported by the Windows workspace-write sandbox", constraints.Permission)
	}
	base := firstNonEmpty(req.Dir, r.cfg.CWD)
	workspaceRoot, err := pathutil.NormalizeWithBase("", r.cfg.CWD)
	if err != nil {
		return workspacePolicy{}, err
	}
	commandDir, err := pathutil.NormalizeWithBase(workspaceRoot, base)
	if err != nil {
		return workspacePolicy{}, err
	}
	coreUserWriteRoots := []string{workspaceRoot, commandDir}
	fullUserWriteRoots := append([]string(nil), coreUserWriteRoots...)
	commandSpecificWriteRoots := []string{}
	for _, root := range r.cfg.WritableRoots {
		if normalized, err := pathutil.NormalizeWithBase(workspaceRoot, root); err == nil && normalized != "" {
			fullUserWriteRoots = append(fullUserWriteRoots, normalized)
			if pathutil.IsUnder(commandDir, normalized) || pathutil.IsUnder(normalized, commandDir) {
				coreUserWriteRoots = append(coreUserWriteRoots, normalized)
			}
		}
	}
	for _, rule := range constraints.PathRules {
		if rule.Access != sandbox.PathAccessReadWrite {
			continue
		}
		if normalized, err := pathutil.NormalizeWithBase(commandDir, rule.Path); err == nil && normalized != "" {
			fullUserWriteRoots = append(fullUserWriteRoots, normalized)
			commandSpecificWriteRoots = append(commandSpecificWriteRoots, normalized)
		}
	}
	fullUserWriteRoots = pathutil.Dedupe(fullUserWriteRoots)
	coreUserWriteRoots = pathutil.Dedupe(append(coreUserWriteRoots, commandSpecificWriteRoots...))
	userWriteRoots := fullUserWriteRoots
	if mode == ensureModeForegroundCore {
		userWriteRoots = coreUserWriteRoots
	}
	envRoot, err := r.prepareSandboxEnvRoot(workspaceRoot, bindingMode == bindingModeCreate)
	if err != nil {
		return workspacePolicy{}, err
	}
	writeRoots := append([]string(nil), userWriteRoots...)
	if envRoot != "" {
		writeRoots = append(writeRoots, envRoot)
	}
	writeRoots = pathutil.Dedupe(writeRoots)
	writeRoots, err = existingWritableRoots(writeRoots)
	if err != nil {
		return workspacePolicy{}, err
	}
	writeRoots = pathutil.CompactCovered(writeRoots)
	if len(writeRoots) == 0 {
		return workspacePolicy{}, fmt.Errorf("impl/sandbox/windows: at least one writable root is required")
	}
	userWriteRoots, err = existingWritableRoots(userWriteRoots)
	if err != nil {
		return workspacePolicy{}, err
	}
	var denyWrite []string
	for _, root := range userWriteRoots {
		denyWrite = append(denyWrite, existingControlDirs(root)...)
		for _, subpath := range r.cfg.ReadOnlySubpaths {
			subpath = strings.TrimSpace(subpath)
			if subpath == "" {
				continue
			}
			path := filepath.Join(root, subpath)
			if _, err := os.Stat(path); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return workspacePolicy{}, fmt.Errorf("impl/sandbox/windows: inspect deny-write path %s: %w", path, err)
			}
			denyWrite = append(denyWrite, path)
		}
	}
	denyWrite = pathutil.Dedupe(denyWrite)
	hash, err := hashWorkspacePolicyFields(workspaceRoot, commandDir, envRoot, writeRoots, denyWrite)
	if err != nil {
		return workspacePolicy{}, err
	}
	var binding capability.Binding
	switch bindingMode {
	case bindingModeCreate:
		binding, err = capability.BindWriteRoots(r.capabilityStorePath(), capability.Scope{
			HostUserSID: r.hostUserSID, WorkspaceRoot: workspaceRoot, SandboxEnvRoot: envRoot, WriteRoots: writeRoots,
		})
	case bindingModeLookup:
		binding, err = capability.LookupWriteRoots(r.capabilityStorePath(), capability.Scope{
			HostUserSID: r.hostUserSID, WorkspaceRoot: workspaceRoot, SandboxEnvRoot: envRoot, WriteRoots: writeRoots,
		})
	case bindingModeDerive:
		binding, err = capability.DeriveWriteRoots(capability.Scope{
			HostUserSID: r.hostUserSID, AuthorityID: r.hostReceiptAuthorityRoot, WorkspaceRoot: workspaceRoot, SandboxEnvRoot: envRoot, WriteRoots: writeRoots,
		})
	default:
		err = fmt.Errorf("unsupported capability binding mode %q", bindingMode)
	}
	if err != nil {
		return workspacePolicy{}, fmt.Errorf("impl/sandbox/windows: bind write capability SIDs: %w", err)
	}
	return workspacePolicy{
		WorkspaceRoot:           workspaceRoot,
		CommandDir:              commandDir,
		SandboxEnvRoot:          envRoot,
		WriteRoots:              writeRoots,
		DenyWritePaths:          denyWrite,
		PolicyHash:              hash,
		CapabilitySIDs:          append([]string(nil), binding.AllSIDs...),
		WriteRootCapabilitySIDs: cloneStringMap(binding.WriteRootTo),
	}, nil
}

func effectiveWindowsSandboxNetwork(_ sandbox.Network) sandbox.Network {
	// The restricted-token backend currently has one network implementation.
	// Disabled/offline network intent is recorded by higher layers, but Windows
	// enforcement is not implemented yet, so execution stays on the online path.
	return sandbox.NetworkEnabled
}

func hashWorkspacePolicyFields(workspaceRoot, commandDir, envRoot string, writeRoots, denyWrite []string) (string, error) {
	return hashJSON(struct {
		WorkspaceRoot  string   `json:"workspace_root,omitempty"`
		CommandDir     string   `json:"command_dir,omitempty"`
		SandboxEnvRoot string   `json:"sandbox_env_root,omitempty"`
		WriteRoots     []string `json:"write_roots,omitempty"`
		DenyWritePaths []string `json:"deny_write_paths,omitempty"`
	}{
		WorkspaceRoot:  workspaceRoot,
		CommandDir:     commandDir,
		SandboxEnvRoot: envRoot,
		WriteRoots:     writeRoots,
		DenyWritePaths: denyWrite,
	})
}

func existingWritableRoots(roots []string) ([]string, error) {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if unsafeWritableRootReasonForCurrentUser(root) != "" {
			continue
		}
		info, err := os.Stat(root)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("impl/sandbox/windows: inspect writable root %s: %w", root, err)
			}
			continue
		}
		if !info.IsDir() {
			continue
		}
		out = append(out, root)
	}
	return pathutil.Dedupe(out), nil
}

func unsafeWritableRootReasonForCurrentUser(root string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return unsafeWritableRootReason(root, home)
}

func unsafeWritableRootReason(root string, userHome string) string {
	root = pathutil.Normalize(root)
	userHome = pathutil.Normalize(userHome)
	if root == "" || userHome == "" {
		return ""
	}
	switch {
	case isUNCPath(root):
		return "UNC roots are not supported by the Windows workspace-write sandbox"
	case isVolumeRoot(root):
		return "it is a volume root"
	case pathutil.IsUnder(userHome, root):
		return "it is the host user profile root or one of its ancestors"
	case isKnownSystemPath(root):
		return "it is under a Windows system or shared program data root"
	default:
		return ""
	}
}

func (r *runtime) prepareSandboxEnvRoot(workspaceRoot string, create bool) (string, error) {
	root := r.sandboxEnvRoot(workspaceRoot)
	if root == "" {
		return "", nil
	}
	if create {
		for _, dir := range sandboxEnvDirs(root) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return "", fmt.Errorf("impl/sandbox/windows: prepare sandbox environment directory %s: %w", dir, err)
			}
		}
	}
	return root, nil
}

func (p workspacePolicy) sidForWriteRoot(root string) string {
	key := pathutil.Key(root)
	for candidate, sid := range p.WriteRootCapabilitySIDs {
		if pathutil.Key(candidate) == key {
			return strings.TrimSpace(sid)
		}
	}
	return ""
}

func allowEntries(sid string) []acl.Entry {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return nil
	}
	return []acl.Entry{{
		Principal: sid,
		Rights:    acl.Modify,
		Mode:      acl.Grant,
		Inherit:   true,
	}}
}

func denyEntries(sids []string) []acl.Entry {
	entries := make([]acl.Entry, 0, len(sids))
	for _, sid := range sids {
		sid = strings.TrimSpace(sid)
		if sid == "" {
			continue
		}
		entries = append(entries, acl.Entry{
			Principal: sid,
			Rights:    acl.Write,
			Mode:      acl.Deny,
			Inherit:   true,
		})
	}
	return entries
}

func (r *runtime) readManifest() (workspaceManifest, error) {
	return readWorkspaceManifest(r.manifestPath())
}

func (r *runtime) migrateLegacyManifestLocked() error {
	legacyPath := r.legacyManifestPath()
	legacy, err := readWorkspaceManifest(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("impl/sandbox/windows: read legacy workspace manifest: %w", err)
	}
	if strings.TrimSpace(legacy.WorkspaceRoot) == "" {
		return fmt.Errorf("impl/sandbox/windows: legacy workspace manifest has no workspace root")
	}
	destination := r.manifestPathForWorkspace(legacy.WorkspaceRoot)
	if existing, readErr := readWorkspaceManifest(destination); readErr == nil {
		if pathutil.Key(existing.WorkspaceRoot) != pathutil.Key(legacy.WorkspaceRoot) {
			return fmt.Errorf("impl/sandbox/windows: legacy workspace manifest destination belongs to a different workspace")
		}
		legacy = mergeWorkspaceManifests(existing, legacy)
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("impl/sandbox/windows: read migrated workspace manifest: %w", readErr)
	}
	if err := persistWorkspaceManifest(destination, legacy); err != nil {
		return fmt.Errorf("impl/sandbox/windows: migrate legacy workspace manifest: %w", err)
	}
	if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("impl/sandbox/windows: remove legacy workspace manifest: %w", err)
	}
	return nil
}

func readWorkspaceManifest(path string) (workspaceManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return workspaceManifest{}, err
	}
	var manifest workspaceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return workspaceManifest{}, err
	}
	manifest = normalizeManifest(manifest)
	if err := validateWorkspaceManifestReceipts(manifest); err != nil {
		return workspaceManifest{}, err
	}
	return manifest, nil
}

func validateWorkspaceManifestReceipts(manifest workspaceManifest) error {
	for _, group := range [][]manifestReceipt{manifest.ManagedReceipts, manifest.RetiringReceipts} {
		for _, managed := range group {
			if pathutil.Key(managed.Path) != pathutil.Key(managed.Receipt.Path) {
				return fmt.Errorf("impl/sandbox/windows: receipt manifest path mismatch for %s", managed.Path)
			}
			if err := acl.ValidateACEReceiptEntry(managed.Receipt, managed.Entry); err != nil {
				return fmt.Errorf("impl/sandbox/windows: invalid exact receipt for %s: %w", managed.Path, err)
			}
		}
	}
	return nil
}

func workspaceManifestForPolicy(policy workspacePolicy) workspaceManifest {
	return workspaceManifest{
		Version:                 workspaceManifestVersion,
		WorkspaceRoot:           policy.WorkspaceRoot,
		SandboxEnvRoot:          policy.SandboxEnvRoot,
		PolicyHash:              policy.PolicyHash,
		CapabilitySIDs:          append([]string(nil), policy.CapabilitySIDs...),
		WriteRoots:              append([]string(nil), policy.WriteRoots...),
		DenyWritePaths:          append([]string(nil), policy.DenyWritePaths...),
		WriteRootCapabilitySIDs: cloneStringMap(policy.WriteRootCapabilitySIDs),
		ACEs:                    manifestACEs(policy),
		Phase:                   manifestPhaseActive,
		UpdatedAt:               time.Now().UTC(),
	}
}

func mergeWorkspaceManifests(existing workspaceManifest, current workspaceManifest) workspaceManifest {
	if pathutil.Key(existing.WorkspaceRoot) != pathutil.Key(current.WorkspaceRoot) ||
		pathutil.Key(existing.SandboxEnvRoot) != pathutil.Key(current.SandboxEnvRoot) {
		return current
	}
	if existing.Version == workspaceManifestVersion && current.Version != workspaceManifestVersion {
		existing.LegacyACEs = mergeManifestACEs(existing.LegacyACEs, current.ACEs)
		existing.UpdatedAt = time.Now().UTC()
		return existing
	}
	out := current
	out.CapabilitySIDs = dedupeStrings(append(append([]string(nil), existing.CapabilitySIDs...), current.CapabilitySIDs...))
	out.WriteRoots = pathutil.Dedupe(append(append([]string(nil), existing.WriteRoots...), current.WriteRoots...))
	out.DenyWritePaths = pathutil.Dedupe(append(append([]string(nil), existing.DenyWritePaths...), current.DenyWritePaths...))
	out.WriteRootCapabilitySIDs = cloneStringMap(existing.WriteRootCapabilitySIDs)
	if out.WriteRootCapabilitySIDs == nil {
		out.WriteRootCapabilitySIDs = map[string]string{}
	}
	for root, sid := range current.WriteRootCapabilitySIDs {
		out.WriteRootCapabilitySIDs[pathutil.Normalize(root)] = strings.TrimSpace(sid)
	}
	out.ACEs = mergeManifestACEs(existing.ACEs, current.ACEs)
	return out
}

func mergeManifestACEs(existing []manifestACE, current []manifestACE) []manifestACE {
	values := append(append([]manifestACE(nil), existing...), current...)
	out := make([]manifestACE, 0, len(values))
	seen := map[string]struct{}{}
	for _, ace := range values {
		key := strings.Join([]string{
			pathutil.Key(ace.Path),
			strings.ToUpper(strings.TrimSpace(ace.Principal)),
			strings.ToLower(strings.TrimSpace(ace.Mode)),
			strings.ToLower(strings.TrimSpace(ace.Rights)),
			fmt.Sprint(ace.Inherit),
		}, "\x00")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ace)
	}
	return out
}

func (r *runtime) persistManifest(manifest workspaceManifest) error {
	return persistWorkspaceManifest(r.manifestPath(), manifest)
}

func persistWorkspaceManifest(path string, manifest workspaceManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".workspace_write.*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}

func normalizeManifest(in workspaceManifest) workspaceManifest {
	in.WorkspaceRoot = pathutil.Normalize(in.WorkspaceRoot)
	in.SandboxEnvRoot = pathutil.Normalize(in.SandboxEnvRoot)
	in.DeletingEnvRoot = pathutil.Normalize(in.DeletingEnvRoot)
	in.WriteRoots = pathutil.Dedupe(in.WriteRoots)
	in.DenyWritePaths = pathutil.Dedupe(in.DenyWritePaths)
	in.CapabilitySIDs = dedupeStrings(in.CapabilitySIDs)
	in.LegacyACEs = mergeManifestACEs(nil, in.LegacyACEs)
	for i := range in.ManagedReceipts {
		in.ManagedReceipts[i].Path = pathutil.Normalize(in.ManagedReceipts[i].Path)
	}
	for i := range in.RetiringReceipts {
		in.RetiringReceipts[i].Path = pathutil.Normalize(in.RetiringReceipts[i].Path)
	}
	if len(in.WriteRootCapabilitySIDs) > 0 {
		out := map[string]string{}
		for root, sid := range in.WriteRootCapabilitySIDs {
			root = pathutil.Normalize(root)
			sid = strings.TrimSpace(sid)
			if root != "" && sid != "" {
				out[root] = sid
			}
		}
		in.WriteRootCapabilitySIDs = out
	}
	return in
}

func manifestFresh(manifest workspaceManifest, policy workspacePolicy) bool {
	if manifest.Version != workspaceManifestVersion {
		return false
	}
	if manifest.Phase != manifestPhaseActive || !receiptManifestCovers(manifest, receiptEffects(policy)) {
		return false
	}
	if manifest.PolicyHash != policy.PolicyHash {
		return false
	}
	if pathutil.Key(manifest.WorkspaceRoot) != pathutil.Key(policy.WorkspaceRoot) {
		return false
	}
	if pathutil.Key(manifest.SandboxEnvRoot) != pathutil.Key(policy.SandboxEnvRoot) {
		return false
	}
	if !sameStringSet(manifest.CapabilitySIDs, policy.CapabilitySIDs) {
		return false
	}
	if !samePathSet(manifest.WriteRoots, policy.WriteRoots) || !samePathSet(manifest.DenyWritePaths, policy.DenyWritePaths) {
		return false
	}
	return sameRootSIDMap(manifest.WriteRootCapabilitySIDs, policy.WriteRootCapabilitySIDs)
}

func manifestACEs(policy workspacePolicy) []manifestACE {
	var out []manifestACE
	for _, root := range policy.WriteRoots {
		if sid := policy.sidForWriteRoot(root); sid != "" {
			out = append(out, manifestACE{Path: root, Principal: sid, Mode: string(acl.Grant), Rights: string(acl.Modify), Inherit: true})
		}
	}
	if envSID := policy.sidForWriteRoot(policy.SandboxEnvRoot); envSID != "" {
		for _, path := range sandboxEnvDirs(policy.SandboxEnvRoot) {
			if pathListContains(policy.WriteRoots, path) {
				continue
			}
			out = append(out, manifestACE{Path: path, Principal: envSID, Mode: string(acl.Grant), Rights: string(acl.Modify), Inherit: true})
		}
	}
	for _, path := range policy.DenyWritePaths {
		if sid := policy.sidForCoveredPath(path); sid != "" {
			out = append(out, manifestACE{Path: path, Principal: sid, Mode: string(acl.Deny), Rights: string(acl.Write), Inherit: true})
		}
	}
	return out
}

type windowsSession struct {
	ref      sandbox.SessionRef
	terminal sandbox.TerminalRef

	cmd              *exec.Cmd
	conpty           *conpty.Process
	jobProcess       *jobprocess.Process
	stdin            io.WriteCloser
	stdoutReader     io.Closer
	stderrReader     io.Closer
	cancel           context.CancelFunc
	done             chan struct{}
	wg               sync.WaitGroup
	finalizeOnce     sync.Once
	closeReadersOnce sync.Once
	releaseUseOnce   sync.Once
	releaseUse       func()
	drainTree        func(context.Context) error
	closeTree        func()

	onOutput func(sandbox.OutputChunk)

	mu            sync.RWMutex
	stdout        []byte
	stderr        []byte
	stdoutTotal   int64
	stderrTotal   int64
	stdoutText    consoleoutput.ConsoleOutputDecoder
	stderrText    consoleoutput.ConsoleOutputDecoder
	outputSignal  chan struct{}
	stdoutCursor  int64
	stderrCursor  int64
	running       bool
	supportsInput bool
	exitCode      int
	waitErr       error
	finalizing    bool
	callbacks     int
	doneClosed    bool
	startedAt     time.Time
	updatedAt     time.Time
}

func (s *windowsSession) Ref() sandbox.SessionRef {
	return sandbox.CloneSessionRef(s.ref)
}

func (s *windowsSession) Terminal() sandbox.TerminalRef {
	return sandbox.CloneTerminalRef(s.terminal)
}

func (s *windowsSession) WriteInput(_ context.Context, input []byte) error {
	s.mu.RLock()
	writer := s.stdin
	running := s.running
	s.mu.RUnlock()
	if !running {
		return fmt.Errorf("impl/sandbox/windows: session %q is not running", s.ref.SessionID)
	}
	if writer == nil {
		return fmt.Errorf("impl/sandbox/windows: session %q does not accept stdin", s.ref.SessionID)
	}
	if len(input) == 0 {
		return nil
	}
	_, err := writer.Write(input)
	return err
}

func (s *windowsSession) ReadOutput(_ context.Context, stdoutMarker, stderrMarker int64) (stdout, stderr []byte, newStdoutMarker, newStderrMarker int64, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if stdoutMarker < 0 {
		stdoutMarker = 0
	}
	if stderrMarker < 0 {
		stderrMarker = 0
	}
	stdout, newStdoutMarker = consoleoutput.CappedOutputSince(s.stdout, s.stdoutTotal, stdoutMarker)
	stderr, newStderrMarker = consoleoutput.CappedOutputSince(s.stderr, s.stderrTotal, stderrMarker)
	return stdout, stderr, newStdoutMarker, newStderrMarker, nil
}

func (s *windowsSession) AwaitOutput(ctx context.Context, cursor sandbox.OutputCursor) (sandbox.OutputObservation, error) {
	observation, err := outputwait.Await(ctx, cursor, func() outputwait.Snapshot[sandbox.SessionStatus] {
		s.mu.RLock()
		defer s.mu.RUnlock()
		status := s.statusLocked()
		return outputwait.Snapshot[sandbox.SessionStatus]{
			Signal:    s.outputSignal,
			Published: sandbox.OutputCursor{Stdout: s.stdoutCursor, Stderr: s.stderrCursor},
			Available: sandbox.OutputCursor{Stdout: s.stdoutTotal, Stderr: s.stderrTotal},
			Terminal:  !status.Running,
			Status:    status,
		}
	})
	if err != nil {
		return sandbox.OutputObservation{}, err
	}
	return sandbox.CloneOutputObservation(sandbox.OutputObservation{
		Cursor: observation.Cursor,
		Status: observation.Status,
	}), nil
}

func (s *windowsSession) Status(_ context.Context) (sandbox.SessionStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sandbox.CloneSessionStatus(s.statusLocked()), nil
}

func (s *windowsSession) statusLocked() sandbox.SessionStatus {
	return sandbox.SessionStatus{
		SessionRef:    s.ref,
		Terminal:      s.terminal,
		Running:       s.running,
		SupportsInput: s.supportsInput && s.running,
		ExitCode:      s.exitCode,
		StartedAt:     s.startedAt,
		UpdatedAt:     s.updatedAt,
	}
}

func (s *windowsSession) Wait(ctx context.Context, timeout time.Duration) (sandbox.SessionStatus, error) {
	if timeout <= 0 {
		return s.Status(ctx)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return sandbox.SessionStatus{}, ctx.Err()
	case <-s.done:
		return s.Status(ctx)
	case <-timer.C:
		return s.Status(ctx)
	}
}

func (s *windowsSession) Result(_ context.Context) (sandbox.CommandResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := sandbox.CommandResult{
		Stdout:   string(s.stdout),
		Stderr:   string(s.stderr),
		ExitCode: s.exitCode,
		Route:    sandbox.RouteSandbox,
		Backend:  sandbox.BackendWindows,
	}
	if s.running {
		return result, fmt.Errorf("impl/sandbox/windows: session %q is still running", s.ref.SessionID)
	}
	if s.waitErr != nil {
		result.Error = s.waitErr.Error()
	}
	return result, s.waitErr
}

func (s *windowsSession) Terminate(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.RLock()
	cmd := s.cmd
	ptyProcess := s.conpty
	atomicProcess := s.jobProcess
	running := s.running
	finalizing := s.finalizing
	s.mu.RUnlock()
	if !running || finalizing {
		return nil
	}
	s.cancel()
	var terminateErr error
	if ptyProcess != nil {
		terminateErr = ptyProcess.Terminate()
	} else if atomicProcess != nil {
		terminateErr = atomicProcess.Terminate()
	} else {
		if cmd != nil && cmd.Process != nil {
			terminateErr = cmd.Process.Kill()
		}
	}
	if s.outputCallbackActive() {
		go s.forceTerminationAfterDrain(terminateErr)
		return terminateErr
	}
	timer := time.NewTimer(windowsTerminateDrain)
	defer timer.Stop()
	select {
	case <-s.done:
		return terminateErr
	case <-ctx.Done():
		s.forceTerminated(errors.Join(
			fmt.Errorf("impl/sandbox/windows: session %q terminated before process wait completed", s.ref.SessionID),
			ctx.Err(),
			terminateErr,
		))
		return terminateErr
	case <-timer.C:
		s.forceTerminated(errors.Join(
			fmt.Errorf("impl/sandbox/windows: session %q terminated before process wait completed", s.ref.SessionID),
			terminateErr,
		))
		return terminateErr
	}
}

func (s *windowsSession) readStream(reader io.Reader, stream string) {
	defer s.wg.Done()
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}
	buf := make([]byte, 8192)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			var decoded consoleoutput.StreamChunk
			switch stream {
			case "stderr":
				decoded = consoleoutput.DecodeStreamChunk(&s.stderrText, chunk, consoleoutput.StoreDecoded)
				s.stderr = consoleoutput.AppendCappedBytes(s.stderr, decoded.Stored, windowsOutputCap)
				s.stderrTotal += int64(len(decoded.Stored))
			default:
				decoded = consoleoutput.DecodeStreamChunk(&s.stdoutText, chunk, consoleoutput.StoreDecoded)
				s.stdout = consoleoutput.AppendCappedBytes(s.stdout, decoded.Stored, windowsOutputCap)
				s.stdoutTotal += int64(len(decoded.Stored))
			}
			s.updatedAt = time.Now()
			cursor := s.stdoutTotal
			if stream == "stderr" {
				cursor = s.stderrTotal
			}
			s.mu.Unlock()
			if s.onOutput != nil && len(decoded.Emit) > 0 {
				s.emitOutput(sandbox.OutputChunk{Stream: stream, Text: string(decoded.Emit), Cursor: cursor})
			}
			s.publishOutput(stream, cursor)
		}
		if err != nil {
			return
		}
	}
}

func (s *windowsSession) waitForExit() {
	var err error
	jobBacked := false
	if s.conpty != nil {
		jobBacked = true
		var exitCode int
		exitCode, err = s.conpty.Wait()
		s.mu.Lock()
		s.exitCode = exitCode
		s.mu.Unlock()
	} else if s.jobProcess != nil {
		jobBacked = true
		var exitCode int
		exitCode, err = s.jobProcess.WaitRoot()
		s.mu.Lock()
		s.exitCode = exitCode
		s.mu.Unlock()
		s.jobProcess.ClosePipes()
	} else {
		err = s.cmd.Wait()
	}
	var drainErr error
	if jobBacked {
		drainCtx, cancel := context.WithTimeout(context.Background(), windowsPreflightTimeout)
		drainErr = s.drainAndCloseProcessTree(drainCtx)
		cancel()
		err = errors.Join(err, drainErr)
	}
	s.finalize(err, false)
	if drainErr != nil && s.processTreeDrainNeedsRetry() {
		go s.retryProcessTreeDrain()
		return
	}
	s.releaseRuntimeUse()
}

func (s *windowsSession) processTreeDrainNeedsRetry() bool {
	if s == nil {
		return false
	}
	if s.drainTree != nil || s.conpty != nil {
		return true
	}
	return s.jobProcess != nil && s.jobProcess.NeedsDrainRetry()
}

func (s *windowsSession) drainAndCloseProcessTree(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.drainTree != nil {
		if err := s.drainTree(ctx); err != nil {
			return err
		}
		if s.closeTree != nil {
			s.closeTree()
		}
		return nil
	}
	if s.conpty != nil {
		if err := s.conpty.DrainJob(ctx); err != nil {
			return err
		}
		s.conpty.CloseAfterExit()
		return nil
	}
	if s.jobProcess != nil {
		return s.jobProcess.DrainAndClose(ctx)
	}
	return nil
}

func (s *windowsSession) retryProcessTreeDrain() {
	for {
		drainCtx, cancel := context.WithTimeout(context.Background(), windowsPreflightTimeout)
		err := s.drainAndCloseProcessTree(drainCtx)
		cancel()
		if err == nil || !s.processTreeDrainNeedsRetry() {
			s.releaseRuntimeUse()
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (s *windowsSession) finalize(err error, forced bool) {
	selected := false
	s.finalizeOnce.Do(func() {
		selected = true
	})
	if !selected {
		<-s.done
		return
	}

	s.mu.Lock()
	s.finalizing = true
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	stdoutTail := consoleoutput.FlushStreamChunk(&s.stdoutText, consoleoutput.StoreDecoded)
	stderrTail := consoleoutput.FlushStreamChunk(&s.stderrText, consoleoutput.StoreDecoded)
	if len(stdoutTail.Stored) > 0 {
		s.stdout = consoleoutput.AppendCappedBytes(s.stdout, stdoutTail.Stored, windowsOutputCap)
		s.stdoutTotal += int64(len(stdoutTail.Stored))
	}
	if len(stderrTail.Stored) > 0 {
		s.stderr = consoleoutput.AppendCappedBytes(s.stderr, stderrTail.Stored, windowsOutputCap)
		s.stderrTotal += int64(len(stderrTail.Stored))
	}
	stdoutCursor := s.stdoutTotal
	stderrCursor := s.stderrTotal
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	if !forced && s.cmd != nil && s.cmd.ProcessState != nil {
		s.exitCode = s.cmd.ProcessState.ExitCode()
	} else if forced {
		s.exitCode = -1
	}
	s.updatedAt = time.Now()
	s.waitErr = err
	s.finalizing = true
	s.mu.Unlock()
	if s.onOutput != nil {
		if len(stdoutTail.Emit) > 0 {
			s.emitOutput(sandbox.OutputChunk{Stream: "stdout", Text: string(stdoutTail.Emit), Cursor: stdoutCursor})
		}
		if len(stderrTail.Emit) > 0 {
			s.emitOutput(sandbox.OutputChunk{Stream: "stderr", Text: string(stderrTail.Emit), Cursor: stderrCursor})
		}
	}
	s.mu.Lock()
	if !s.doneClosed {
		s.stdoutCursor = stdoutCursor
		s.stderrCursor = stderrCursor
		s.running = false
		s.finalizing = false
		s.updatedAt = time.Now()
		s.doneClosed = true
		close(s.done)
		s.notifyOutputLocked()
	}
	s.mu.Unlock()
}

func (s *windowsSession) releaseRuntimeUse() {
	s.releaseUseOnce.Do(func() {
		if s.releaseUse != nil {
			s.releaseUse()
		}
	})
}

func (s *windowsSession) forceTerminated(err error) {
	s.closeOutputReaders()
	s.finalize(err, true)
}

func (s *windowsSession) forceTerminationAfterDrain(terminateErr error) {
	timer := time.NewTimer(windowsTerminateDrain)
	defer timer.Stop()
	select {
	case <-s.done:
		return
	case <-timer.C:
		s.forceTerminated(errors.Join(
			fmt.Errorf("impl/sandbox/windows: session %q terminated before process wait completed", s.ref.SessionID),
			terminateErr,
		))
	}
}

func (s *windowsSession) emitOutput(chunk sandbox.OutputChunk) {
	s.mu.Lock()
	s.callbacks++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.callbacks--
		s.mu.Unlock()
	}()
	s.onOutput(chunk)
}

func (s *windowsSession) outputCallbackActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.callbacks > 0
}

func (s *windowsSession) closeOutputReaders() {
	s.closeReadersOnce.Do(func() {
		s.mu.RLock()
		stdout := s.stdoutReader
		stderr := s.stderrReader
		s.mu.RUnlock()
		if stdout != nil {
			_ = stdout.Close()
		}
		if stderr != nil {
			_ = stderr.Close()
		}
	})
}

func (s *windowsSession) publishOutput(stream string, cursor int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doneClosed {
		return
	}
	switch stream {
	case "stderr":
		if cursor <= s.stderrCursor {
			return
		}
		s.stderrCursor = cursor
	default:
		if cursor <= s.stdoutCursor {
			return
		}
		s.stdoutCursor = cursor
	}
	s.notifyOutputLocked()
}

func (s *windowsSession) notifyOutputLocked() {
	if s.outputSignal != nil {
		close(s.outputSignal)
	}
	s.outputSignal = make(chan struct{})
}

func existingControlDirs(root string) []string {
	root = pathutil.Normalize(root)
	if root == "" {
		return nil
	}
	var paths []string
	for _, name := range []string{".git", ".codex", ".agents"} {
		path := filepath.Join(root, name)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			paths = append(paths, path)
		}
	}
	return paths
}

func resolveStateRoot(raw string) (string, error) {
	if strings.TrimSpace(raw) != "" {
		return filepath.Abs(strings.TrimSpace(raw))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("impl/sandbox/windows: resolve user home: %w", err)
	}
	return filepath.Join(home, ".caelis"), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			out[key] = value
		}
	}
	return out
}

func samePathSet(a, b []string) bool {
	return slices.Equal(pathutil.Dedupe(a), pathutil.Dedupe(b))
}

func pathListContains(paths []string, want string) bool {
	wantKey := pathutil.Key(want)
	if wantKey == "" {
		return false
	}
	for _, path := range paths {
		if pathutil.Key(path) == wantKey {
			return true
		}
	}
	return false
}

func sameStringSet(a, b []string) bool {
	return slices.Equal(dedupeStrings(a), dedupeStrings(b))
}

func sameRootSIDMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for root, sid := range a {
		found := false
		for candidate, candidateSID := range b {
			if pathutil.Key(root) == pathutil.Key(candidate) && strings.TrimSpace(sid) == strings.TrimSpace(candidateSID) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToUpper(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func hashJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func stateRootHash(stateRoot string) string {
	normalized := strings.ToLower(strings.TrimSpace(filepath.Clean(stateRoot)))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])[:8]
}

func newID(prefix string) (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(raw[:]), nil
}

var _ sandbox.Runtime = (*runtime)(nil)
var _ sandbox.Session = (*windowsSession)(nil)
