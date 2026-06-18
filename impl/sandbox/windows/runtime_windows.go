//go:build windows

package windows

import (
	"bytes"
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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/policy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/policyfs"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/winps"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/acl"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/capability"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/job"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/pathutil"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
	"github.com/OnslaughtSnail/caelis/internal/winproc"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

const (
	workspaceManifestVersion = 1
	windowsOutputCap         = 1024 * 1024
	windowsTerminateDrain    = 500 * time.Millisecond
	windowsCacheMaxBytes     = 10 * 1024 * 1024 * 1024
	windowsCacheMaxAge       = 14 * 24 * time.Hour
	windowsPreflightTimeout  = 15 * time.Second
)

var (
	modifyFileDACL           = acl.ModifyFileDACL
	errBackgroundRefreshBusy = errors.New("windows sandbox background ACL refresh yielded to foreground work")
)

type ensureMode string

const (
	ensureModeForegroundCore    ensureMode = "foreground-core"
	ensureModeBackgroundRefresh ensureMode = "background-refresh"
)

type appliedWriteGrant struct {
	path string
	sid  string
}

func newRuntime(cfg Config) (sandbox.Runtime, error) {
	cfg = sandbox.NormalizeConfig(cfg)
	stateRoot, err := resolveStateRoot(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	hostRuntime, err := host.New(host.Config{CWD: cfg.CWD})
	if err != nil {
		return nil, err
	}
	return &runtime{
		cfg:       cfg,
		stateRoot: stateRoot,
		fs:        hostRuntime.FileSystem(),
		sessions:  map[string]*windowsSession{},
	}, nil
}

type runtime struct {
	cfg       sandbox.Config
	stateRoot string
	fs        sandbox.FileSystem

	ensureMu  sync.Mutex
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
			TTY:            false,
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
		p := policy.Default(r.cfg, sandbox.NormalizeConstraints(constraints))
		// Windows workspace-write intentionally does not enforce read or hidden
		// roots; only write roots and deny-write carveouts are security policy.
		p.ReadableRoots = nil
		p.HiddenRoots = nil
		return p
	})
}

func (r *runtime) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req = sandbox.CloneRequest(req)
	result := sandbox.CommandResult{Route: sandbox.RouteSandbox, Backend: sandbox.BackendWindows, ExitCode: -1}
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
	defer token.Close()
	if len(req.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}
	stdout := &cappedOutputBuffer{max: windowsOutputCap}
	stderr := &cappedOutputBuffer{max: windowsOutputCap}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = runCommandWithJob(runCtx, cmd)
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		result.Error = err.Error()
	}
	if req.OnOutput != nil {
		if result.Stdout != "" {
			req.OnOutput(sandbox.OutputChunk{Stream: "stdout", Text: result.Stdout})
		}
		if result.Stderr != "" {
			req.OnOutput(sandbox.OutputChunk{Stream: "stderr", Text: result.Stderr})
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
	cmd, token, err := r.restrictedShellCommand(cmdCtx, req, true, policy)
	if err != nil {
		cancel()
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = token.Close()
		cancel()
		return nil, fmt.Errorf("impl/sandbox/windows: create stdin pipe: %w", err)
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = token.Close()
		cancel()
		return nil, fmt.Errorf("impl/sandbox/windows: create stdout pipe: %w", err)
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = token.Close()
		cancel()
		return nil, fmt.Errorf("impl/sandbox/windows: create stderr pipe: %w", err)
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		_ = token.Close()
		cancel()
		return nil, err
	}
	_ = token.Close()
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	jobObject, _ := assignCommandJob(cmd)

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
		cmd:           cmd,
		job:           jobObject,
		stdin:         stdin,
		cancel:        cancel,
		running:       true,
		supportsInput: true,
		startedAt:     now,
		updatedAt:     now,
		done:          make(chan struct{}),
		onOutput:      req.OnOutput,
	}
	r.mu.Lock()
	r.sessions[sessionID] = session
	r.mu.Unlock()

	session.wg.Add(2)
	go session.readStream(stdout, "stdout")
	go session.readStream(stderr, "stderr")
	go session.waitForExit()
	return session, nil
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
	backend := normalizeBackendAlias(ref.Backend)
	if backend != "" && backend != sandbox.BackendWindows {
		return nil, fmt.Errorf("impl/sandbox/windows: backend %q is unsupported", ref.Backend)
	}
	return r.OpenSession(ref.SessionID)
}

func (r *runtime) SupportedBackends() []sandbox.Backend {
	return []sandbox.Backend{sandbox.BackendWindows}
}

func (r *runtime) Status() sandbox.Status {
	check := r.workspaceSetupCheck()
	status := sandbox.Status{
		RequestedBackend: sandbox.BackendWindows,
		ResolvedBackend:  sandbox.BackendWindows,
		Setup: sandbox.SetupStatus{
			Required: check.Required,
			Error:    strings.TrimSpace(check.Error),
			Checks:   []sandbox.SetupCheck{check},
		},
	}
	status.Setup.Details = map[string]string{
		"backend": "windows-restricted-token",
	}
	return sandbox.CloneStatus(status)
}

func (r *runtime) SelectionStatus() sandbox.Status {
	return sandbox.Status{
		RequestedBackend: sandbox.BackendWindows,
		ResolvedBackend:  sandbox.BackendWindows,
	}
}

func (r *runtime) Prepare(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "acl",
		Message: "preparing current workspace ACL policy",
		Step:    1,
		Total:   2,
	})
	_, err := r.ensureForRequestMode(ctx, sandbox.CommandRequest{
		Dir: r.cfg.CWD,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkEnabled,
		},
	}, ensureModeBackgroundRefresh)
	if err != nil {
		return err
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "complete",
		Message: "Windows workspace-write sandbox ACL policy is ready",
		Step:    2,
		Total:   2,
		Done:    true,
	})
	return nil
}

func (r *runtime) Repair(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.Prepare(ctx); err == nil {
		return nil
	} else if !requiresElevatedACLRepair(err) {
		return err
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "elevate",
		Message: "requesting explicit Windows sandbox ACL repair",
		Step:    1,
		Total:   2,
	})
	if err := r.runElevatedRepair(ctx); err != nil {
		r.recordWorkspaceSetupError(err)
		return err
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "verify",
		Message: "verifying Windows sandbox ACL repair",
		Step:    2,
		Total:   2,
	})
	if err := r.Prepare(ctx); err != nil {
		r.recordWorkspaceSetupError(err)
		return err
	}
	return nil
}

func requiresElevatedACLRepair(err error) bool {
	if err == nil {
		return false
	}
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "acl: write") && strings.Contains(lower, "dacl")
}

func (r *runtime) Preflight(ctx context.Context, opts sandbox.PreflightOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !opts.AllowNonElevatedRepair {
		return nil
	}
	refreshCtx, cancel := context.WithTimeout(ctx, windowsPreflightTimeout)
	defer cancel()
	return r.Refresh(refreshCtx)
}

func (r *runtime) Refresh(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !r.beginRefresh() {
		return nil
	}
	err := r.refreshForRequest(ctx, sandbox.CommandRequest{
		Dir: r.cfg.CWD,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindows,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkEnabled,
		},
	})
	r.finishRefresh(err)
	return err
}

func (r *runtime) startBackgroundRefresh(ctx context.Context, req sandbox.CommandRequest) {
	if r == nil || !r.beginRefresh() {
		return
	}
	go func() {
		refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		r.finishRefresh(r.refreshForRequest(refreshCtx, req))
	}()
}

func (r *runtime) beginRefresh() bool {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if r.refreshRunning {
		return false
	}
	r.refreshRunning = true
	return true
}

func (r *runtime) finishRefresh(err error) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	r.refreshRunning = false
	r.lastRefreshAt = time.Now().UTC()
	if err == nil || errors.Is(err, context.Canceled) {
		r.lastRefreshError = ""
		return
	}
	r.lastRefreshError = strings.TrimSpace(err.Error())
}

func (r *runtime) refreshSnapshot() (running bool, lastErr string, lastAt time.Time, lastCacheCleanup time.Time, lastCacheBytes int64) {
	r.refreshMu.RLock()
	defer r.refreshMu.RUnlock()
	return r.refreshRunning, strings.TrimSpace(r.lastRefreshError), r.lastRefreshAt, r.lastCacheCleanupAt, r.lastCacheBytes
}

func (r *runtime) refreshForRequest(ctx context.Context, req sandbox.CommandRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	policy, err := r.policyForRequest(req)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.sandboxStateDir(), 0o700); err != nil {
		return err
	}
	manifest, manifestErr := r.readManifest()
	if manifestErr == nil && manifestCoversPolicy(manifest, policy, true) {
		missing, err := r.missingACLEntries(policy)
		if err == nil && len(missing) == 0 {
			return r.cleanupSandboxCaches(ctx, policy.SandboxEnvRoot)
		}
	}
	if manifestErr == nil {
		if !r.tryCleanupStaleManifestACLs(ctx, manifest, policy) {
			return r.cleanupSandboxCaches(ctx, policy.SandboxEnvRoot)
		}
	}
	appliedPolicy, complete, aclErr := r.applyPolicyACLsInterruptible(ctx, policy)
	if complete && aclErr == nil {
		if !r.tryWriteManifest(ctx, appliedPolicy) {
			return r.cleanupSandboxCaches(ctx, policy.SandboxEnvRoot)
		}
	}
	cacheErr := r.cleanupSandboxCaches(ctx, policy.SandboxEnvRoot)
	return errors.Join(aclErr, cacheErr)
}

func (r *runtime) tryCleanupStaleManifestACLs(ctx context.Context, manifest workspaceManifest, policy workspacePolicy) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	if !r.ensureMu.TryLock() {
		return false
	}
	defer r.ensureMu.Unlock()
	r.cleanupStaleManifestACLs(manifest, policy)
	return true
}

func (r *runtime) tryWriteManifest(ctx context.Context, policy workspacePolicy) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	if !r.ensureMu.TryLock() {
		return false
	}
	defer r.ensureMu.Unlock()
	return r.writeManifest(policy) == nil
}

func (r *runtime) Reset(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	plan := r.cleanupPlan()
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "clean",
		Message: fmt.Sprintf("cleaning Windows sandbox state: %d ACL paths, %d legacy paths", len(plan.ACLPaths), len(plan.LegacyPaths)),
		Step:    1,
		Total:   3,
	})
	var errs []error
	for _, path := range plan.ACLPaths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := os.Stat(path); err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("inspect ACL cleanup path %s: %w", path, err))
			}
			continue
		}
		if err := acl.RemoveFileDACLPrincipals(path, plan.Principals...); err != nil {
			errs = append(errs, fmt.Errorf("remove sandbox ACL principals from %s: %w", path, err))
		}
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "state",
		Message: "removing Windows sandbox state files",
		Step:    2,
		Total:   3,
	})
	for _, path := range plan.LegacyPaths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("remove sandbox state path %s: %w", path, err))
		}
	}
	for _, leftover := range plan.LegacyProtected {
		sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
			Phase:   "legacy",
			Message: "legacy Windows sandbox artifact may require elevated cleanup: " + leftover,
			Step:    2,
			Total:   3,
		})
	}
	if len(errs) > 0 {
		joined := errors.Join(errs...)
		sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
			Phase:   "error",
			Message: "Windows sandbox cleanup finished with errors: " + joined.Error(),
			Step:    3,
			Total:   3,
		})
		return fmt.Errorf("impl/sandbox/windows: clean sandbox state: %w", joined)
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Phase:   "complete",
		Message: "Windows sandbox cleanup complete",
		Step:    3,
		Total:   3,
		Done:    true,
	})
	return nil
}

func (r *runtime) Close() error {
	r.mu.RLock()
	sessions := make([]*windowsSession, 0, len(r.sessions))
	for _, session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.mu.RUnlock()
	for _, session := range sessions {
		_ = session.Terminate(context.Background())
	}
	return nil
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
	winproc.ConfigureHiddenConsole(cmd)
	return cmd, token, nil
}

func runCommandWithJob(ctx context.Context, cmd *exec.Cmd) error {
	if cmd == nil {
		return fmt.Errorf("impl/sandbox/windows: command is required")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	jobObject, _ := assignCommandJob(cmd)
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	select {
	case err := <-waitCh:
		if jobObject != nil {
			_ = jobObject.Close()
		}
		return err
	case <-ctx.Done():
		terminateErr := terminateWindowsCommand(cmd, jobObject)
		select {
		case <-waitCh:
			return errors.Join(ctx.Err(), terminateErr)
		case <-time.After(windowsTerminateDrain):
			return errors.Join(
				ctx.Err(),
				fmt.Errorf("impl/sandbox/windows: command terminated before process wait completed"),
				terminateErr,
			)
		}
	}
}

func terminateWindowsCommand(cmd *exec.Cmd, jobObject *job.Object) error {
	var errs []error
	if jobObject != nil {
		if err := jobObject.Terminate(1); err != nil {
			errs = append(errs, fmt.Errorf("terminate job object: %w", err))
		}
		if err := jobObject.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close job object: %w", err))
		}
	}
	if cmd != nil && cmd.Process != nil {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			errs = append(errs, fmt.Errorf("kill process: %w", err))
		}
	}
	return errors.Join(errs...)
}

func assignCommandJob(cmd *exec.Cmd) (*job.Object, error) {
	if cmd == nil || cmd.Process == nil {
		return nil, nil
	}
	jobObject, err := job.New()
	if err != nil {
		return nil, err
	}
	if err := jobObject.AssignPID(cmd.Process.Pid); err != nil {
		_ = jobObject.Close()
		return nil, err
	}
	return jobObject, nil
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
	UpdatedAt               time.Time         `json:"updated_at,omitempty"`
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
	r.ensureMu.Lock()
	defer r.ensureMu.Unlock()
	if err := os.MkdirAll(r.sandboxStateDir(), 0o700); err != nil {
		r.recordWorkspaceSetupError(err)
		return workspacePolicy{}, err
	}
	manifest, manifestErr := r.readManifest()
	if manifestErr == nil && manifestCoversPolicy(manifest, policy, false) {
		if !manifestCoversPolicy(manifest, policy, true) {
			r.cleanupStaleManifestDenyACLs(manifest, policy)
		}
		missing, err := r.missingACLEntries(policy)
		if err == nil && len(missing) == 0 {
			r.clearWorkspaceSetupError()
			return policy, nil
		}
	}
	if manifestErr == nil && mode == ensureModeBackgroundRefresh {
		r.cleanupStaleManifestACLs(manifest, policy)
	}
	if err := r.applyPolicyACLs(policy); err != nil {
		r.recordWorkspaceSetupError(err)
		return policy, err
	}
	if err := r.writeManifest(policy); err != nil {
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
	return r.policyForRequestWithBinding(req, true, mode)
}

func (r *runtime) inspectPolicyForRequest(req sandbox.CommandRequest) (workspacePolicy, error) {
	return r.policyForRequestWithBinding(req, false, ensureModeBackgroundRefresh)
}

func (r *runtime) policyForRequestWithBinding(req sandbox.CommandRequest, createSIDs bool, mode ensureMode) (workspacePolicy, error) {
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
	envRoot, err := r.prepareSandboxEnvRoot(workspaceRoot, createSIDs)
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
	if createSIDs {
		binding, err = capability.BindWriteRoots(r.capabilityStorePath(), workspaceRoot, writeRoots)
	} else {
		binding, err = capability.LookupWriteRoots(r.capabilityStorePath(), workspaceRoot, writeRoots)
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

func sandboxEnvDirs(envRoot string) []string {
	envRoot = pathutil.Normalize(envRoot)
	if envRoot == "" {
		return nil
	}
	cacheRoot := sandboxCacheRoot(envRoot)
	return pathutil.Dedupe([]string{
		envRoot,
		sandboxTempRoot(envRoot),
		cacheRoot,
		filepath.Join(cacheRoot, "go-build"),
		filepath.Join(cacheRoot, "go-mod"),
		filepath.Join(cacheRoot, "npm"),
		filepath.Join(cacheRoot, "pip"),
		filepath.Join(envRoot, "powershell"),
		sandboxPowerShellCacheDir(envRoot),
		sandboxPythonSiteDir(envRoot),
	})
}

func sandboxTempRoot(envRoot string) string {
	envRoot = pathutil.Normalize(envRoot)
	if envRoot == "" {
		return ""
	}
	return filepath.Join(envRoot, "tmp")
}

func sandboxCacheRoot(envRoot string) string {
	envRoot = pathutil.Normalize(envRoot)
	if envRoot == "" {
		return ""
	}
	return filepath.Join(envRoot, "cache")
}

func sandboxPowerShellCacheDir(envRoot string) string {
	envRoot = pathutil.Normalize(envRoot)
	if envRoot == "" {
		return ""
	}
	return filepath.Join(envRoot, "powershell", "CommandAnalysis")
}

func sandboxPythonSiteDir(envRoot string) string {
	envRoot = pathutil.Normalize(envRoot)
	if envRoot == "" {
		return ""
	}
	return filepath.Join(envRoot, "python-site")
}

func (r *runtime) missingACLEntries(policy workspacePolicy) ([]acl.Entry, error) {
	var missing []acl.Entry
	for _, root := range policy.WriteRoots {
		entries := allowEntries(policy.sidForWriteRoot(root))
		info, err := os.Stat(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("impl/sandbox/windows: writable root %s is not a directory", root)
		}
		rootMissing, err := acl.MissingFileDACLEntries(root, entries...)
		if err != nil {
			return nil, err
		}
		missing = append(missing, rootMissing...)
	}
	envSID := policy.sidForWriteRoot(policy.SandboxEnvRoot)
	if envSID != "" {
		for _, path := range sandboxEnvDirs(policy.SandboxEnvRoot) {
			if pathListContains(policy.WriteRoots, path) {
				continue
			}
			entries := allowEntries(envSID)
			info, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, err
			}
			if !info.IsDir() {
				return nil, fmt.Errorf("impl/sandbox/windows: sandbox environment path %s is not a directory", path)
			}
			pathMissing, err := acl.MissingFileDACLEntries(path, entries...)
			if err != nil {
				return nil, err
			}
			missing = append(missing, pathMissing...)
		}
	}
	for _, path := range policy.DenyWritePaths {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		entries := denyEntries(policy.CapabilitySIDs)
		pathMissing, err := acl.MissingFileDACLEntries(path, entries...)
		if err != nil {
			return nil, err
		}
		missing = append(missing, pathMissing...)
	}
	return missing, nil
}

func (r *runtime) applyPolicyACLs(policy workspacePolicy) error {
	for _, root := range policy.WriteRoots {
		info, err := os.Stat(root)
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("impl/sandbox/windows: inspect writable root %s: %w", root, err)
			}
			continue
		}
		if !info.IsDir() {
			return fmt.Errorf("impl/sandbox/windows: writable root %s is not a directory", root)
		}
		if err := ensureFileDACLEntries(root, allowEntries(policy.sidForWriteRoot(root))...); err != nil {
			return fmt.Errorf("impl/sandbox/windows: apply writable root ACL %s: %w", root, diagnoseACLWriteFailure(root, err))
		}
	}
	envSID := policy.sidForWriteRoot(policy.SandboxEnvRoot)
	if envSID != "" {
		for _, path := range sandboxEnvDirs(policy.SandboxEnvRoot) {
			if pathListContains(policy.WriteRoots, path) {
				continue
			}
			info, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return fmt.Errorf("impl/sandbox/windows: inspect sandbox environment path %s: %w", path, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("impl/sandbox/windows: sandbox environment path %s is not a directory", path)
			}
			if err := ensureFileDACLEntries(path, allowEntries(envSID)...); err != nil {
				return fmt.Errorf("impl/sandbox/windows: apply sandbox environment ACL %s: %w", path, diagnoseACLWriteFailure(path, err))
			}
		}
	}
	for _, path := range policy.DenyWritePaths {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("impl/sandbox/windows: inspect deny-write path %s: %w", path, err)
		}
		if err := ensureFileDACLEntries(path, denyEntries(policy.CapabilitySIDs)...); err != nil {
			return fmt.Errorf("impl/sandbox/windows: apply deny-write ACL %s: %w", path, diagnoseACLWriteFailure(path, err))
		}
	}
	return nil
}

func (r *runtime) applyPolicyACLsInterruptible(ctx context.Context, policy workspacePolicy) (workspacePolicy, bool, error) {
	keptRoots := make([]string, 0, len(policy.WriteRoots))
	failedDenyPaths := make([]string, 0, len(policy.DenyWritePaths))
	appliedGrants := make([]appliedWriteGrant, 0, len(policy.WriteRoots)+len(sandboxEnvDirs(policy.SandboxEnvRoot)))
	var errs []error
	for _, root := range policy.WriteRoots {
		sid := policy.sidForWriteRoot(root)
		ok, err := r.tryApplyWritableRootACL(ctx, root, sid)
		if errors.Is(err, errBackgroundRefreshBusy) {
			return policy, false, errors.Join(errs...)
		}
		if err != nil {
			errs = append(errs, err)
		}
		if ok {
			keptRoots = append(keptRoots, root)
			appliedGrants = append(appliedGrants, appliedWriteGrant{path: root, sid: sid})
		} else if ctx.Err() != nil {
			return policy, false, errors.Join(append(errs, ctx.Err())...)
		}
	}
	for _, path := range sandboxEnvDirs(policy.SandboxEnvRoot) {
		if pathListContains(policy.WriteRoots, path) {
			continue
		}
		sid := policy.sidForWriteRoot(policy.SandboxEnvRoot)
		ok, err := r.tryApplyWritableRootACL(ctx, path, sid)
		if errors.Is(err, errBackgroundRefreshBusy) {
			return policy, false, errors.Join(errs...)
		}
		if err != nil {
			errs = append(errs, err)
		}
		if ok {
			appliedGrants = append(appliedGrants, appliedWriteGrant{path: path, sid: sid})
		} else if ctx.Err() != nil {
			return policy, false, errors.Join(append(errs, ctx.Err())...)
		}
	}
	for _, path := range policy.DenyWritePaths {
		ok, err := r.tryApplyDenyWriteACL(ctx, path, policy.CapabilitySIDs)
		if errors.Is(err, errBackgroundRefreshBusy) {
			return policy, false, errors.Join(errs...)
		}
		if err != nil {
			errs = append(errs, err)
		}
		if ok {
			continue
		}
		if ctx.Err() != nil {
			return policy, false, errors.Join(append(errs, ctx.Err())...)
		}
		failedDenyPaths = append(failedDenyPaths, path)
	}
	if len(failedDenyPaths) > 0 {
		if !r.tryRevokeWriteGrantsCovering(ctx, appliedGrants, failedDenyPaths) {
			return policy, false, errors.Join(errs...)
		}
		keptRoots = removeWriteRootsCovering(keptRoots, failedDenyPaths)
	}
	return policy.withWriteRoots(keptRoots), true, errors.Join(errs...)
}

func (r *runtime) tryApplyWritableRootACL(ctx context.Context, root string, sid string) (bool, error) {
	root = strings.TrimSpace(root)
	if root == "" || strings.TrimSpace(sid) == "" {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !r.ensureMu.TryLock() {
		return false, errBackgroundRefreshBusy
	}
	defer r.ensureMu.Unlock()
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("impl/sandbox/windows: inspect writable root %s: %w", root, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("impl/sandbox/windows: writable root %s is not a directory", root)
	}
	if err := ensureFileDACLEntries(root, allowEntries(sid)...); err != nil {
		return false, fmt.Errorf("impl/sandbox/windows: apply writable root ACL %s: %w", root, diagnoseACLWriteFailure(root, err))
	}
	return true, nil
}

func (r *runtime) tryApplyDenyWriteACL(ctx context.Context, path string, sids []string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !r.ensureMu.TryLock() {
		return false, errBackgroundRefreshBusy
	}
	defer r.ensureMu.Unlock()
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("impl/sandbox/windows: inspect deny-write path %s: %w", path, err)
	}
	if err := ensureFileDACLEntries(path, denyEntries(sids)...); err != nil {
		return false, fmt.Errorf("impl/sandbox/windows: apply deny-write ACL %s: %w", path, diagnoseACLWriteFailure(path, err))
	}
	return true, nil
}

func (r *runtime) tryRevokeWriteGrantsCovering(ctx context.Context, grants []appliedWriteGrant, blocked []string) bool {
	if len(grants) == 0 || len(blocked) == 0 {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	if !r.ensureMu.TryLock() {
		return false
	}
	defer r.ensureMu.Unlock()
	revokeWriteGrantsCovering(grants, blocked)
	return true
}

func ensureFileDACLEntries(path string, entries ...acl.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	missing, err := acl.MissingFileDACLEntries(path, entries...)
	if err == nil && len(missing) == 0 {
		return nil
	}
	return modifyFileDACL(path, entries...)
}

func revokeWriteGrantsCovering(grants []appliedWriteGrant, blocked []string) {
	if len(grants) == 0 || len(blocked) == 0 {
		return
	}
	for _, grant := range grants {
		if !pathOverlapsAny(grant.path, blocked) {
			continue
		}
		_ = modifyFileDACL(grant.path, revokeEntries(grant.sid)...)
	}
}

func removeWriteRootsCovering(roots []string, blocked []string) []string {
	if len(roots) == 0 || len(blocked) == 0 {
		return pathutil.Dedupe(roots)
	}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if !pathOverlapsAny(root, blocked) {
			out = append(out, root)
		}
	}
	return pathutil.Dedupe(out)
}

func pathOverlapsAny(root string, blocked []string) bool {
	for _, path := range blocked {
		if pathutil.IsUnder(path, root) || pathutil.IsUnder(root, path) {
			return true
		}
	}
	return false
}

func diagnoseACLWriteFailure(path string, err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		return err
	}
	detail := "current token cannot update the directory DACL; Windows workspace-write sandbox needs WRITE_DAC so it can add the Caelis synthetic write SID"
	if info, inspectErr := acl.InspectFileDACL(path); inspectErr == nil {
		parts := []string{detail}
		if owner := firstNonEmpty(info.Owner, info.OwnerSID); owner != "" {
			parts = append(parts, "owner="+owner)
		}
		parts = append(parts,
			fmt.Sprintf("protected_dacl=%t", info.Protected),
			fmt.Sprintf("has_inherited_ace=%t", info.HasInheritedACE),
			fmt.Sprintf("ace_count=%d", info.ACECount),
		)
		parts = append(parts, "file writes may still work through existing Modify rights, but sandbox preparation cannot proceed without DACL write access")
		parts = append(parts, "manual fix: run `/doctor fix` in TUI or `caelis sandbox fix`")
		detail = strings.Join(parts, "; ")
	} else {
		detail += "; DACL diagnosis failed: " + inspectErr.Error()
	}
	return fmt.Errorf("%w; %s", err, detail)
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

func (p workspacePolicy) withWriteRoots(roots []string) workspacePolicy {
	roots = pathutil.Dedupe(roots)
	out := p
	out.WriteRoots = roots
	out.WriteRootCapabilitySIDs = map[string]string{}
	var sids []string
	for _, root := range roots {
		if sid := p.sidForWriteRoot(root); sid != "" {
			normalized := pathutil.Normalize(root)
			out.WriteRootCapabilitySIDs[normalized] = sid
			sids = append(sids, sid)
		}
	}
	if len(sids) == 0 {
		if sid := p.sidForWriteRoot(p.SandboxEnvRoot); sid != "" {
			sids = append(sids, sid)
		}
	}
	out.CapabilitySIDs = dedupeStrings(sids)
	if len(out.WriteRootCapabilitySIDs) == 0 {
		out.WriteRootCapabilitySIDs = nil
	}
	if hash, err := hashWorkspacePolicyFields(out.WorkspaceRoot, out.CommandDir, out.SandboxEnvRoot, out.WriteRoots, out.DenyWritePaths); err == nil {
		out.PolicyHash = hash
	}
	return out
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

func revokeEntries(sid string) []acl.Entry {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return nil
	}
	return []acl.Entry{{
		Principal: sid,
		Mode:      acl.Revoke,
	}}
}

func (r *runtime) readManifest() (workspaceManifest, error) {
	data, err := os.ReadFile(r.manifestPath())
	if err != nil {
		return workspaceManifest{}, err
	}
	var manifest workspaceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return workspaceManifest{}, err
	}
	return normalizeManifest(manifest), nil
}

func (r *runtime) writeManifest(policy workspacePolicy) error {
	manifest := workspaceManifest{
		Version:                 workspaceManifestVersion,
		WorkspaceRoot:           policy.WorkspaceRoot,
		SandboxEnvRoot:          policy.SandboxEnvRoot,
		PolicyHash:              policy.PolicyHash,
		CapabilitySIDs:          append([]string(nil), policy.CapabilitySIDs...),
		WriteRoots:              append([]string(nil), policy.WriteRoots...),
		DenyWritePaths:          append([]string(nil), policy.DenyWritePaths...),
		WriteRootCapabilitySIDs: cloneStringMap(policy.WriteRootCapabilitySIDs),
		ACEs:                    manifestACEs(policy),
		UpdatedAt:               time.Now().UTC(),
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	path := r.manifestPath()
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
	in.WriteRoots = pathutil.Dedupe(in.WriteRoots)
	in.DenyWritePaths = pathutil.Dedupe(in.DenyWritePaths)
	in.CapabilitySIDs = dedupeStrings(in.CapabilitySIDs)
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

func manifestSatisfiesPolicy(manifest workspaceManifest, policy workspacePolicy) bool {
	return manifestCoversPolicy(manifest, policy, false)
}

func manifestCoversPolicy(manifest workspaceManifest, policy workspacePolicy, requireExact bool) bool {
	if manifestFresh(manifest, policy) {
		return true
	}
	if requireExact {
		return false
	}
	if manifest.Version != workspaceManifestVersion {
		return false
	}
	if pathutil.Key(manifest.WorkspaceRoot) != pathutil.Key(policy.WorkspaceRoot) {
		return false
	}
	if pathutil.Key(manifest.SandboxEnvRoot) != pathutil.Key(policy.SandboxEnvRoot) {
		return false
	}
	if !pathSetContainsAll(manifest.WriteRoots, policy.WriteRoots) ||
		!pathSetContainsAll(manifest.DenyWritePaths, policy.DenyWritePaths) {
		return false
	}
	for _, sid := range policy.CapabilitySIDs {
		if !stringSetContains(manifest.CapabilitySIDs, sid) {
			return false
		}
	}
	for root, sid := range policy.WriteRootCapabilitySIDs {
		if strings.TrimSpace(sid) == "" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(manifest.WriteRootCapabilitySIDs[pathutil.Normalize(root)]), strings.TrimSpace(sid)) {
			return false
		}
	}
	return true
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
		for _, sid := range policy.CapabilitySIDs {
			if strings.TrimSpace(sid) != "" {
				out = append(out, manifestACE{Path: path, Principal: sid, Mode: string(acl.Deny), Rights: string(acl.Write), Inherit: true})
			}
		}
	}
	return out
}

func (r *runtime) cleanupStaleManifestACLs(manifest workspaceManifest, policy workspacePolicy) {
	r.cleanupStaleManifestACLsMatching(manifest, policy, func(manifestACE) bool { return true })
}

func (r *runtime) cleanupStaleManifestDenyACLs(manifest workspaceManifest, policy workspacePolicy) {
	r.cleanupStaleManifestACLsMatching(manifest, policy, func(ace manifestACE) bool {
		return strings.EqualFold(strings.TrimSpace(ace.Mode), string(acl.Deny))
	})
}

func (r *runtime) cleanupStaleManifestACLsMatching(manifest workspaceManifest, policy workspacePolicy, include func(manifestACE) bool) {
	currentGrantPaths := map[string]struct{}{}
	grantPaths := append([]string{}, policy.WriteRoots...)
	grantPaths = append(grantPaths, sandboxEnvDirs(policy.SandboxEnvRoot)...)
	for _, path := range grantPaths {
		if key := pathutil.Key(path); key != "" {
			currentGrantPaths[key] = struct{}{}
		}
	}
	currentDenyPaths := map[string]struct{}{}
	for _, path := range policy.DenyWritePaths {
		if key := pathutil.Key(path); key != "" {
			currentDenyPaths[key] = struct{}{}
		}
	}
	currentSIDs := map[string]struct{}{}
	for _, sid := range policy.CapabilitySIDs {
		currentSIDs[strings.ToUpper(strings.TrimSpace(sid))] = struct{}{}
	}
	for _, ace := range manifest.ACEs {
		if include != nil && !include(ace) {
			continue
		}
		path := strings.TrimSpace(ace.Path)
		sid := strings.TrimSpace(ace.Principal)
		if path == "" || sid == "" {
			continue
		}
		if manifestACEStillCurrent(ace, currentGrantPaths, currentDenyPaths, currentSIDs) {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			_ = acl.RemoveFileDACLPrincipals(path, sid)
		}
	}
}

func manifestACEStillCurrent(ace manifestACE, currentGrantPaths map[string]struct{}, currentDenyPaths map[string]struct{}, currentSIDs map[string]struct{}) bool {
	sid := strings.ToUpper(strings.TrimSpace(ace.Principal))
	if sid == "" {
		return false
	}
	if _, ok := currentSIDs[sid]; !ok {
		return false
	}
	pathKey := pathutil.Key(ace.Path)
	if pathKey == "" {
		return false
	}
	switch {
	case strings.EqualFold(strings.TrimSpace(ace.Mode), string(acl.Grant)):
		_, ok := currentGrantPaths[pathKey]
		return ok
	case strings.EqualFold(strings.TrimSpace(ace.Mode), string(acl.Deny)):
		_, ok := currentDenyPaths[pathKey]
		return ok
	default:
		return false
	}
}

func (r *runtime) workspaceSetupCheck() (check sandbox.SetupCheck) {
	check = sandbox.SetupCheck{
		Name:     "workspace",
		Scope:    sandbox.SetupScopeWorkspace,
		Required: false,
	}
	lastErr := r.workspaceSetupError()
	defer func() {
		if lastErr == "" {
			return
		}
		check.Current = false
		check.Required = true
		check.Error = lastErr
		check.Reason = "workspace ACL repair failed; explicit user repair is required"
		if check.Details == nil {
			check.Details = map[string]string{}
		}
		check.Details["manual_fix_hint"] = "run `/doctor fix` in TUI or `caelis sandbox fix`"
	}()
	policy, err := r.inspectPolicyForRequest(sandbox.CommandRequest{Dir: r.cfg.CWD, Constraints: r.Describe().DefaultConstraints})
	if err != nil {
		check.Reason = err.Error()
		return check
	}
	check.Root = policy.WorkspaceRoot
	check.Details = map[string]string{"policy_hash": policy.PolicyHash}
	refreshRunning, refreshErr, refreshAt, cacheCleanupAt, cacheBytes := r.refreshSnapshot()
	check.Details["refresh_state"] = "idle"
	if refreshRunning {
		check.Details["refresh_state"] = "running"
	}
	if refreshErr != "" {
		check.Details["refresh_error"] = refreshErr
	}
	if !refreshAt.IsZero() {
		check.Details["last_refresh_at"] = refreshAt.Format(time.RFC3339)
	}
	if !cacheCleanupAt.IsZero() {
		check.Details["last_cache_cleanup_at"] = cacheCleanupAt.Format(time.RFC3339)
	}
	if cacheBytes > 0 {
		check.Details["sandbox_cache_bytes"] = fmt.Sprint(cacheBytes)
	}
	check.Counts = map[string]int{
		"write_roots": len(policy.WriteRoots),
		"deny_write":  len(policy.DenyWritePaths),
	}
	manifest, err := r.readManifest()
	if err != nil {
		check.Reason = "workspace ACL manifest will be prepared lazily"
		return check
	}
	check.Current = manifestFresh(manifest, policy)
	check.UpdatedAt = manifest.UpdatedAt
	if !check.Current {
		check.Reason = "workspace ACL manifest is stale and will be repaired lazily"
	}
	return check
}

func (r *runtime) recordWorkspaceSetupError(err error) {
	if r == nil || err == nil {
		return
	}
	r.setupMu.Lock()
	defer r.setupMu.Unlock()
	r.lastWorkspaceSetupError = strings.TrimSpace(err.Error())
}

func (r *runtime) clearWorkspaceSetupError() {
	if r == nil {
		return
	}
	r.setupMu.Lock()
	defer r.setupMu.Unlock()
	r.lastWorkspaceSetupError = ""
}

func (r *runtime) workspaceSetupError() string {
	if r == nil {
		return ""
	}
	r.setupMu.RLock()
	defer r.setupMu.RUnlock()
	return strings.TrimSpace(r.lastWorkspaceSetupError)
}

type cleanupPlan struct {
	ACLPaths        []string
	Principals      []string
	LegacyPaths     []string
	LegacyProtected []string
}

func (r *runtime) cleanupPlan() cleanupPlan {
	var plan cleanupPlan
	if manifest, err := r.readManifest(); err == nil {
		for _, ace := range manifest.ACEs {
			plan.ACLPaths = append(plan.ACLPaths, ace.Path)
			plan.Principals = append(plan.Principals, ace.Principal)
		}
	}
	legacyRoots, legacyPrincipals := r.legacyACLArtifacts()
	plan.ACLPaths = append(plan.ACLPaths, legacyRoots...)
	plan.Principals = append(plan.Principals, legacyPrincipals...)
	plan.ACLPaths = pathutil.Dedupe(plan.ACLPaths)
	plan.Principals = dedupeStrings(plan.Principals)
	plan.LegacyPaths = pathutil.Dedupe([]string{
		r.manifestPath(),
		r.capabilityStorePath(),
		r.sandboxEnvBase(),
		r.legacyWorkspaceSandboxEnvRoot(),
		filepath.Join(r.sandboxStateDir(), "workspace_setup.json"),
		filepath.Join(r.sandboxStateDir(), "setup_marker.json"),
		filepath.Join(r.sandboxStateDir(), "setup_error.json"),
		filepath.Join(r.sandboxStateDir(), "setup_progress.json"),
		filepath.Join(r.stateRoot, ".sandbox-bin"),
		filepath.Join(r.stateRoot, ".sandbox-secrets"),
		filepath.Join(r.stateRoot, ".sandbox-reset"),
	})
	hash := stateRootHash(r.stateRoot)
	plan.LegacyProtected = dedupeStrings(
		[]string{
			"local user CaelisSbxOff" + hash,
			"local user CaelisSbxOn" + hash,
			"local group CaelisSandboxUsers",
			"Windows Firewall rules CaelisSandbox-*",
		},
	)
	return plan
}

func (r *runtime) legacyACLArtifacts() ([]string, []string) {
	var roots []string
	var principals []string
	type oldWorkspace struct {
		WriteRoots              []string          `json:"write_roots"`
		DenyWritePaths          []string          `json:"deny_write_paths"`
		CapabilitySIDs          []string          `json:"capability_sids"`
		WriteRootCapabilitySIDs map[string]string `json:"write_root_capability_sids"`
		OfflineUsername         string            `json:"offline_username"`
		OnlineUsername          string            `json:"online_username"`
	}
	if data, err := os.ReadFile(filepath.Join(r.sandboxStateDir(), "workspace_setup.json")); err == nil {
		var record oldWorkspace
		if json.Unmarshal(data, &record) == nil {
			roots = append(roots, record.WriteRoots...)
			roots = append(roots, record.DenyWritePaths...)
			principals = append(principals, record.CapabilitySIDs...)
			for _, sid := range record.WriteRootCapabilitySIDs {
				principals = append(principals, sid)
			}
			principals = append(principals, record.OfflineUsername, record.OnlineUsername, "CaelisSandboxUsers")
		}
	}
	if data, err := os.ReadFile(r.capabilityStorePath()); err == nil {
		var store capability.Store
		if json.Unmarshal(data, &store) == nil {
			for root, sid := range store.WorkspaceByCWD {
				roots = append(roots, root)
				principals = append(principals, sid)
			}
			for root, sid := range store.WritableRootByPath {
				roots = append(roots, root)
				principals = append(principals, sid)
			}
		}
	}
	return pathutil.Dedupe(roots), dedupeStrings(principals)
}

func (r *runtime) sandboxStateDir() string {
	return filepath.Join(r.stateRoot, ".sandbox")
}

func (r *runtime) capabilityStorePath() string {
	return filepath.Join(r.sandboxStateDir(), "cap_sids.json")
}

func (r *runtime) manifestPath() string {
	return filepath.Join(r.sandboxStateDir(), "workspace_write_manifest.json")
}

func (r *runtime) sandboxEnvBase() string {
	return filepath.Join(r.sandboxStateDir(), "env")
}

func (r *runtime) sandboxEnvRoot(workspaceRoot string) string {
	workspace := pathutil.Normalize(workspaceRoot)
	if workspace == "" {
		workspace = pathutil.Normalize(r.cfg.CWD)
	}
	if workspace == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(pathutil.Key(workspace)))
	return filepath.Join(r.sandboxEnvBase(), hex.EncodeToString(sum[:])[:16])
}

type sandboxEnvCacheEntry struct {
	path    string
	modTime time.Time
	size    int64
}

func (r *runtime) cleanupSandboxCaches(ctx context.Context, activeEnvRoot string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	base := r.sandboxEnvBase()
	entries, total, err := sandboxEnvCacheEntries(ctx, base)
	if err != nil {
		if os.IsNotExist(err) {
			r.recordCacheCleanup(time.Now().UTC(), 0)
			return nil
		}
		return err
	}
	activeKey := pathutil.Key(activeEnvRoot)
	now := time.Now().UTC()
	var errs []error
	removed := map[string]struct{}{}
	for _, entry := range entries {
		if activeKey != "" && pathutil.Key(entry.path) == activeKey {
			continue
		}
		if now.Sub(entry.modTime) <= windowsCacheMaxAge {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if err := os.RemoveAll(entry.path); err != nil {
			errs = append(errs, fmt.Errorf("impl/sandbox/windows: clean sandbox cache %s: %w", entry.path, err))
			continue
		}
		total -= entry.size
		removed[pathutil.Key(entry.path)] = struct{}{}
	}
	if total > windowsCacheMaxBytes {
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].modTime.Before(entries[j].modTime)
		})
		for _, entry := range entries {
			if total <= windowsCacheMaxBytes {
				break
			}
			key := pathutil.Key(entry.path)
			if key == "" {
				continue
			}
			if activeKey != "" && key == activeKey {
				continue
			}
			if _, ok := removed[key]; ok {
				continue
			}
			if err := ctx.Err(); err != nil {
				return errors.Join(append(errs, err)...)
			}
			if err := os.RemoveAll(entry.path); err != nil {
				errs = append(errs, fmt.Errorf("impl/sandbox/windows: clean sandbox cache %s: %w", entry.path, err))
				continue
			}
			total -= entry.size
			removed[key] = struct{}{}
		}
	}
	if total < 0 {
		total = 0
	}
	r.recordCacheCleanup(now, total)
	return errors.Join(errs...)
}

func sandboxEnvCacheEntries(ctx context.Context, base string) ([]sandboxEnvCacheEntry, int64, error) {
	base = pathutil.Normalize(base)
	if base == "" {
		return nil, 0, nil
	}
	items, err := os.ReadDir(base)
	if err != nil {
		return nil, 0, err
	}
	entries := make([]sandboxEnvCacheEntry, 0, len(items))
	var total int64
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, total, err
		}
		if !item.IsDir() {
			continue
		}
		path := filepath.Join(base, item.Name())
		info, err := item.Info()
		if err != nil {
			return nil, total, err
		}
		size, err := directorySize(ctx, path)
		if err != nil {
			return nil, total, err
		}
		entries = append(entries, sandboxEnvCacheEntry{path: path, modTime: info.ModTime(), size: size})
		total += size
	}
	return entries, total, nil
}

func directorySize(ctx context.Context, root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func (r *runtime) recordCacheCleanup(at time.Time, bytes int64) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	r.lastCacheCleanupAt = at
	r.lastCacheBytes = bytes
}

func (r *runtime) legacyWorkspaceSandboxEnvRoot() string {
	workspace := pathutil.Normalize(r.cfg.CWD)
	if workspace == "" {
		return ""
	}
	return filepath.Join(workspace, ".caelis-sandbox")
}

type windowsSession struct {
	ref      sandbox.SessionRef
	terminal sandbox.TerminalRef

	cmd    *exec.Cmd
	job    *job.Object
	stdin  io.WriteCloser
	cancel context.CancelFunc
	done   chan struct{}
	wg     sync.WaitGroup

	onOutput func(sandbox.OutputChunk)

	mu            sync.RWMutex
	stdout        []byte
	stderr        []byte
	stdoutTotal   int64
	stderrTotal   int64
	stdoutText    win32.ConsoleOutputDecoder
	stderrText    win32.ConsoleOutputDecoder
	running       bool
	supportsInput bool
	exitCode      int
	waitErr       error
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
	stdout, newStdoutMarker = cappedOutputSince(s.stdout, s.stdoutTotal, stdoutMarker)
	stderr, newStderrMarker = cappedOutputSince(s.stderr, s.stderrTotal, stderrMarker)
	return stdout, stderr, newStdoutMarker, newStderrMarker, nil
}

func (s *windowsSession) Status(_ context.Context) (sandbox.SessionStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sandbox.CloneSessionStatus(sandbox.SessionStatus{
		SessionRef:    s.ref,
		Terminal:      s.terminal,
		Running:       s.running,
		SupportsInput: s.supportsInput,
		ExitCode:      s.exitCode,
		StartedAt:     s.startedAt,
		UpdatedAt:     s.updatedAt,
	}), nil
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
	running := s.running
	s.mu.RUnlock()
	if !running {
		return nil
	}
	s.cancel()
	terminateErr := terminateWindowsCommand(cmd, s.takeJob())
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
			var decoded []byte
			switch stream {
			case "stderr":
				decoded = s.stderrText.Decode(chunk)
				s.stderr = appendCappedBytes(s.stderr, decoded, windowsOutputCap)
				s.stderrTotal += int64(len(decoded))
			default:
				decoded = s.stdoutText.Decode(chunk)
				s.stdout = appendCappedBytes(s.stdout, decoded, windowsOutputCap)
				s.stdoutTotal += int64(len(decoded))
			}
			s.updatedAt = time.Now()
			s.mu.Unlock()
			if s.onOutput != nil && len(decoded) > 0 {
				s.onOutput(sandbox.OutputChunk{Stream: stream, Text: string(decoded)})
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *windowsSession) waitForExit() {
	err := s.cmd.Wait()
	if jobObject := s.takeJob(); jobObject != nil {
		_ = jobObject.Close()
	}
	s.wg.Wait()
	s.mu.Lock()
	stdoutTail := s.stdoutText.Flush()
	stderrTail := s.stderrText.Flush()
	if len(stdoutTail) > 0 {
		s.stdout = appendCappedBytes(s.stdout, stdoutTail, windowsOutputCap)
		s.stdoutTotal += int64(len(stdoutTail))
	}
	if len(stderrTail) > 0 {
		s.stderr = appendCappedBytes(s.stderr, stderrTail, windowsOutputCap)
		s.stderrTotal += int64(len(stderrTail))
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	if s.doneClosed {
		s.updatedAt = time.Now()
		s.mu.Unlock()
		if s.onOutput != nil {
			if len(stdoutTail) > 0 {
				s.onOutput(sandbox.OutputChunk{Stream: "stdout", Text: string(stdoutTail)})
			}
			if len(stderrTail) > 0 {
				s.onOutput(sandbox.OutputChunk{Stream: "stderr", Text: string(stderrTail)})
			}
		}
		return
	}
	if s.cmd.ProcessState != nil {
		s.exitCode = s.cmd.ProcessState.ExitCode()
	}
	s.running = false
	s.updatedAt = time.Now()
	s.waitErr = err
	s.doneClosed = true
	close(s.done)
	s.mu.Unlock()
	if s.onOutput != nil {
		if len(stdoutTail) > 0 {
			s.onOutput(sandbox.OutputChunk{Stream: "stdout", Text: string(stdoutTail)})
		}
		if len(stderrTail) > 0 {
			s.onOutput(sandbox.OutputChunk{Stream: "stderr", Text: string(stderrTail)})
		}
	}
}

func (s *windowsSession) takeJob() *job.Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobObject := s.job
	s.job = nil
	return jobObject
}

func (s *windowsSession) forceTerminated(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doneClosed {
		return
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	s.running = false
	s.exitCode = -1
	s.updatedAt = time.Now()
	s.waitErr = err
	s.doneClosed = true
	close(s.done)
}

type cappedOutputBuffer struct {
	mu      sync.Mutex
	max     int
	buf     []byte
	decoder win32.ConsoleOutputDecoder
	flushed bool
}

func (b *cappedOutputBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	decoded := b.decoder.Decode(p)
	if len(decoded) > 0 {
		b.buf = appendCappedBytes(b.buf, decoded, b.max)
	}
	return len(p), nil
}

func (b *cappedOutputBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.flushed {
		if tail := b.decoder.Flush(); len(tail) > 0 {
			b.buf = appendCappedBytes(b.buf, tail, b.max)
		}
		b.flushed = true
	}
	return string(b.buf)
}

func appendCappedBytes(dst []byte, src []byte, max int) []byte {
	if max <= 0 {
		return append(dst, src...)
	}
	if len(src) >= max {
		return append([]byte(nil), src[len(src)-max:]...)
	}
	keep := max - len(src)
	if len(dst) > keep {
		dst = dst[len(dst)-keep:]
	}
	out := append([]byte(nil), dst...)
	return append(out, src...)
}

func cappedOutputSince(buf []byte, total int64, marker int64) ([]byte, int64) {
	if total < 0 {
		total = 0
	}
	base := total - int64(len(buf))
	if base < 0 {
		base = 0
	}
	if marker < base {
		marker = base
	}
	if marker > total {
		marker = total
	}
	start := marker - base
	if start < 0 {
		start = 0
	}
	if start > int64(len(buf)) {
		start = int64(len(buf))
	}
	return append([]byte(nil), buf[start:]...), total
}

func sandboxEnvironment(policy workspacePolicy, extra map[string]string) ([]string, error) {
	envRoot := strings.TrimSpace(policy.SandboxEnvRoot)
	if envRoot == "" {
		return nil, fmt.Errorf("impl/sandbox/windows: sandbox environment root is required")
	}
	envRoot = pathutil.Normalize(envRoot)
	tempRoot := sandboxTempRoot(envRoot)
	cacheRoot := sandboxCacheRoot(envRoot)
	psCacheDir := sandboxPowerShellCacheDir(envRoot)
	pythonSiteDir := sandboxPythonSiteDir(envRoot)
	for _, dir := range sandboxEnvDirs(envRoot) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("impl/sandbox/windows: prepare sandbox environment directory %s: %w", dir, err)
		}
	}
	touchSandboxEnvRoot(envRoot)
	if err := writeSandboxPythonSiteCustomize(pythonSiteDir); err != nil {
		return nil, err
	}
	forced := map[string]string{
		"TEMP":                        tempRoot,
		"TMP":                         tempRoot,
		"GOTMPDIR":                    tempRoot,
		"CAELIS_SANDBOX_TEMP":         tempRoot,
		"GOCACHE":                     filepath.Join(cacheRoot, "go-build"),
		"GOMODCACHE":                  filepath.Join(cacheRoot, "go-mod"),
		"GOTELEMETRY":                 "off",
		"PIP_CACHE_DIR":               filepath.Join(cacheRoot, "pip"),
		"npm_config_cache":            filepath.Join(cacheRoot, "npm"),
		"PYTHONPATH":                  prependEnvPath(pythonSiteDir, commandEnvValue(extra, "PYTHONPATH")),
		"PSModuleAnalysisCachePath":   filepath.Join(psCacheDir, "PowerShell_AnalysisCache"),
		"POWERSHELL_TELEMETRY_OPTOUT": "1",
	}
	if skillsDir := hostUserSkillsDir(); skillsDir != "" {
		forced["CAELIS_SKILLS_DIR"] = skillsDir
	}
	if strings.TrimSpace(forced["SystemRoot"]) == "" {
		if systemRoot := strings.TrimSpace(os.Getenv("SystemRoot")); systemRoot != "" {
			forced["SystemRoot"] = systemRoot
		} else if windir := strings.TrimSpace(os.Getenv("WINDIR")); windir != "" {
			forced["SystemRoot"] = windir
		} else {
			forced["SystemRoot"] = `C:\Windows`
		}
	}
	if strings.TrimSpace(os.Getenv("WINDIR")) == "" {
		forced["WINDIR"] = forced["SystemRoot"]
	}
	if strings.TrimSpace(os.Getenv("ComSpec")) == "" {
		forced["ComSpec"] = filepath.Join(forced["SystemRoot"], "System32", "cmd.exe")
	}
	if strings.TrimSpace(os.Getenv("PATHEXT")) == "" {
		forced["PATHEXT"] = `.COM;.EXE;.BAT;.CMD;.VBS;.VBE;.JS;.JSE;.WSF;.WSH;.MSC`
	}
	return mergeEnv(extra, forced), nil
}

func touchSandboxEnvRoot(envRoot string) {
	envRoot = strings.TrimSpace(envRoot)
	if envRoot == "" {
		return
	}
	now := time.Now()
	_ = os.Chtimes(envRoot, now, now)
}

func hostUserSkillsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".caelis", "skills")
}

func mergeEnv(extra map[string]string, forced map[string]string) []string {
	values := map[string]string{}
	names := map[string]string{}
	set := func(key string, value string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		canonical := strings.ToUpper(key)
		if existing := names[canonical]; existing != "" && existing != key {
			delete(values, existing)
		}
		names[canonical] = key
		values[key] = value
	}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		set(key, value)
	}
	for key, value := range extra {
		set(key, value)
	}
	for key, value := range forced {
		set(key, value)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

func commandEnvValue(extra map[string]string, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	for name, value := range extra {
		if strings.EqualFold(strings.TrimSpace(name), key) {
			return value
		}
	}
	prefix := strings.ToUpper(key) + "="
	for _, item := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(item), prefix) {
			return item[len(prefix):]
		}
	}
	return ""
}

func prependEnvPath(first string, rest string) string {
	first = strings.TrimSpace(first)
	if first == "" {
		return rest
	}
	if strings.TrimSpace(rest) == "" {
		return first
	}
	return first + string(os.PathListSeparator) + rest
}

func writeSandboxPythonSiteCustomize(siteDir string) error {
	siteDir = strings.TrimSpace(siteDir)
	if siteDir == "" {
		return nil
	}
	if err := os.MkdirAll(siteDir, 0o700); err != nil {
		return fmt.Errorf("impl/sandbox/windows: prepare Python sandbox site directory %s: %w", siteDir, err)
	}
	path := filepath.Join(siteDir, "sitecustomize.py")
	if err := os.WriteFile(path, []byte(sandboxPythonSiteCustomize), 0o600); err != nil {
		return fmt.Errorf("impl/sandbox/windows: write Python sandbox sitecustomize %s: %w", path, err)
	}
	return nil
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

func normalizeBackendAlias(backend sandbox.Backend) sandbox.Backend {
	switch strings.ToLower(strings.TrimSpace(string(backend))) {
	case "", "windows", "windows-restricted-token", "windows_restricted_token", "windows-elevated", "windows_elevated", "elevated":
		if strings.TrimSpace(string(backend)) == "" {
			return ""
		}
		return sandbox.BackendWindows
	default:
		return backend
	}
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

func pathSetContainsAll(haystack, needles []string) bool {
	for _, needle := range pathutil.Dedupe(needles) {
		if !pathListContains(haystack, needle) {
			return false
		}
	}
	return true
}

func sameStringSet(a, b []string) bool {
	return slices.Equal(dedupeStrings(a), dedupeStrings(b))
}

func stringSetContains(values []string, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
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
