//go:build windows

package windows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/cmdsession"
	corepolicy "github.com/OnslaughtSnail/caelis/impl/sandbox/internal/policy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/internal/runnerruntime"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/capability"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/pathutil"
	winpolicy "github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/policy"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/runnerclient"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setup"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/setupstate"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func newRuntime(cfg Config) (sandbox.Runtime, error) {
	cfg = sandbox.NormalizeConfig(cfg)
	stateRoot, err := resolveStateRoot(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	executable := strings.TrimSpace(cfg.HelperPath)
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("impl/sandbox/windows: resolve helper executable: %w", err)
		}
	}
	hostRuntime, err := host.New(host.Config{CWD: cfg.CWD})
	if err != nil {
		return nil, err
	}
	runner := &setupRunner{
		cfg:        cfg,
		stateRoot:  stateRoot,
		executable: executable,
	}
	runner.client = runnerclient.New(runnerclient.Config{
		Executable:     executable,
		ExecutablePath: runner.helperExecutablePath,
		Args:           []string{runnerHelperCommand},
		StateRoot:      stateRoot,
		Policy:         runner.policyForRequest,
		Credentials:    runner.credentialsForRequest,
	})
	base := runnerruntime.New(runnerruntime.Config{
		Backend: sandbox.BackendWindowsElevated,
		Descriptor: sandbox.Descriptor{
			Backend:   sandbox.BackendWindowsElevated,
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
				Backend:    sandbox.BackendWindowsElevated,
				Permission: sandbox.PermissionWorkspaceWrite,
				Isolation:  sandbox.IsolationProcess,
				Network:    sandbox.NetworkDisabled,
			},
		},
		Status: runner.status(),
		BaseFS: hostRuntime.FileSystem(),
		Policy: func(constraints sandbox.Constraints) corepolicy.Policy {
			return corepolicy.Default(cfg, constraints)
		},
		Runner: runner,
	})
	return &runtime{Runtime: base, runner: runner}, nil
}

type runtime struct {
	*runnerruntime.Runtime
	runner *setupRunner
}

func (r *runtime) Status() sandbox.Status {
	if r == nil || r.runner == nil {
		return sandbox.Status{}
	}
	return sandbox.CloneStatus(r.runner.status())
}

func (r *runtime) Prepare(ctx context.Context) error {
	if r == nil || r.runner == nil {
		return fmt.Errorf("impl/sandbox/windows: runtime is unavailable")
	}
	return r.runner.Prepare(ctx)
}

func (r *runtime) Preflight(ctx context.Context, opts sandbox.PreflightOptions) error {
	if r == nil || r.runner == nil {
		return fmt.Errorf("impl/sandbox/windows: runtime is unavailable")
	}
	return r.runner.Preflight(ctx, opts)
}

func (r *runtime) Reset(ctx context.Context) error {
	if r == nil || r.runner == nil {
		return fmt.Errorf("impl/sandbox/windows: runtime is unavailable")
	}
	return r.runner.Reset(ctx)
}

type setupRunner struct {
	cfg        sandbox.Config
	stateRoot  string
	executable string
	client     *runnerclient.Client

	maintenanceMu       contextMutex
	usersReadyMu        sync.Mutex
	usersReadyCheckedAt time.Time
	usersReadyErr       string
	refreshMu           sync.Mutex
	refreshedPolicies   map[string]struct{}
	policyMu            sync.Mutex
	policyCache         map[string]cachedWindowsPolicy
	helperMu            sync.Mutex
	helperPathCache     map[string]cachedHelperPath
	executableHashMu    sync.Mutex
	executableHash      string
}

type contextMutex struct {
	once sync.Once
	ch   chan struct{}
}

func (m *contextMutex) Lock(ctx context.Context) error {
	m.init()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.ch:
		return nil
	}
}

func (m *contextMutex) Unlock() {
	m.init()
	select {
	case m.ch <- struct{}{}:
	default:
		panic("impl/sandbox/windows: unlock of unlocked context mutex")
	}
}

func (m *contextMutex) init() {
	m.once.Do(func() {
		m.ch = make(chan struct{}, 1)
		m.ch <- struct{}{}
	})
}

const usersReadyCacheTTL = time.Minute
const (
	defaultPrepareTimeout = 10 * time.Minute
	defaultResetTimeout   = 5 * time.Minute
)

var errSandboxUsersFileMissing = errors.New("sandbox users file missing")

type cachedWindowsPolicy struct {
	policy winpolicy.Policy
	hash   string
}

type cachedHelperPath struct {
	path string
	hash string
}

func (r *setupRunner) Run(ctx context.Context, req runnerruntime.Request) (sandbox.CommandResult, error) {
	if err := r.requireSetupReady(); err != nil {
		return sandbox.CommandResult{Route: sandbox.RouteSandbox, Backend: sandbox.BackendWindowsElevated}, err
	}
	if err := r.refreshRequestACLs(ctx, req); err != nil {
		return sandbox.CommandResult{Route: sandbox.RouteSandbox, Backend: sandbox.BackendWindowsElevated}, err
	}
	return r.client.Run(ctx, req)
}

func (r *setupRunner) StartAsync(ctx context.Context, req runnerruntime.Request) (string, error) {
	if err := r.requireSetupReady(); err != nil {
		return "", err
	}
	if err := r.refreshRequestACLs(ctx, req); err != nil {
		return "", err
	}
	return r.client.StartAsync(ctx, req)
}

func (r *setupRunner) WriteInput(sessionID string, input []byte) error {
	return r.client.WriteInput(sessionID, input)
}

func (r *setupRunner) ReadOutput(sessionID string, stdoutMarker, stderrMarker int64) ([]byte, []byte, int64, int64, error) {
	return r.client.ReadOutput(sessionID, stdoutMarker, stderrMarker)
}

func (r *setupRunner) GetSessionStatus(sessionID string) (cmdsession.SessionStatus, error) {
	return r.client.GetSessionStatus(sessionID)
}

func (r *setupRunner) WaitSession(ctx context.Context, sessionID string, timeout time.Duration) (sandbox.CommandResult, error) {
	return r.client.WaitSession(ctx, sessionID, timeout)
}

func (r *setupRunner) TerminateSession(sessionID string) error {
	return r.client.TerminateSession(sessionID)
}

func (r *setupRunner) Close() error {
	if r.client == nil {
		return nil
	}
	return r.client.Close()
}

func (r *setupRunner) Prepare(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	ctx, cancel = withDefaultOperationTimeout(ctx, defaultPrepareTimeout)
	defer cancel()
	if err := r.maintenanceMu.Lock(ctx); err != nil {
		return err
	}
	defer r.maintenanceMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{Phase: "inspect", Message: "checking Windows sandbox setup state"})
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{Phase: "payload", Message: "collecting global sandbox policy"})
	globalPayload, err := r.globalSetupPayload()
	if err != nil {
		return err
	}
	dirs := setupstate.NewDirs(r.stateRoot)
	_ = setupstate.ClearProgress(dirs.ProgressPath)
	globalFreshness := r.freshnessForPayload(globalPayload)
	if globalFreshness.Current {
		if usersReadyErr := r.usersFileReady(); usersReadyErr != nil {
			globalFreshness = setupstate.Freshness{Reason: usersReadyErr.Error()}
		}
	}
	workspace := workspaceSetupSnapshot{}
	if globalFreshness.Current {
		workspace = r.workspaceSetupSnapshot()
	}
	if globalFreshness.Current && workspace.Current {
		if err := r.prepareCommandRunnerHelper(ctx); err != nil {
			return err
		}
		sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{Phase: "complete", Message: "Windows sandbox setup is already current", Done: true})
		r.markBaseRefreshApplied()
		return nil
	}
	r.clearUsersReadyCache()
	r.clearRefreshCache()
	r.clearPolicyCache()
	kind := setup.SetupKindFull
	if globalFreshness.Current {
		kind = setup.SetupKindWorkspaceOnly
	}
	if kind == setup.SetupKindFull && r.client != nil {
		_ = r.client.Invalidate()
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{Phase: "payload", Message: "binding sandbox policy capabilities"})
	payload, _, err := r.setupPayload(r.baseSetupRequest(), kind)
	if err != nil {
		return err
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{Phase: "payload", Message: "setup payload is ready"})
	payload.OperationID = setupOperationID("setup")
	payload.StartedAt = time.Now().UTC()
	payload.ExpiresAt = time.Now().Add(defaultPrepareTimeout).UTC()
	payload.ProgressPath = dirs.ProgressPath
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{Phase: "payload", Message: "encoding setup payload"})
	encoded, err := setup.EncodePayload(payload)
	if err != nil {
		return err
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{Phase: "helper", Message: "materializing sandbox helper executable"})
	helperPath, _, err := r.materializeHelper()
	if err != nil {
		return err
	}
	var setupErr error
	if elevated, err := win32.IsElevated(); err == nil && elevated {
		sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{Phase: "elevation", Message: "current process is elevated; running setup without a UAC prompt"})
		setupErr = setup.ExecuteWithProgress(payload, func(progress setup.Progress) {
			sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
				Phase:   progress.Phase,
				Message: progress.Message,
				Step:    progress.Step,
				Total:   progress.Total,
				Done:    progress.Done,
				Debug:   progress.Debug,
			})
		})
	} else {
		sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{Phase: "elevation", Message: "waiting for elevated setup helper; approve the UAC prompt if Windows asks"})
		stopProgress := forwardSetupProgressFile(ctx, dirs.ProgressPath)
		setupErr = win32.RunElevatedAndWaitContext(ctx, helperPath, []string{setupHelperCommand, encoded}, r.cfg.CWD)
		stopProgress()
	}
	if err := setupErr; err != nil {
		if r.prepareTargetCurrent(kind) {
			return nil
		}
		code := "elevated_setup_failed"
		message := err.Error()
		dirs := setupstate.NewDirs(r.stateRoot)
		if report, readErr := setupstate.ReadError(dirs.ErrorPath); readErr == nil && strings.TrimSpace(report.Message) != "" {
			if strings.TrimSpace(report.Code) != "" {
				code = strings.TrimSpace(report.Code)
			}
			message = strings.TrimSpace(report.Message)
		}
		var canceled win32.ElevatedLaunchCanceledError
		if errors.As(err, &canceled) {
			code = "uac_canceled"
			message = "Windows sandbox setup was canceled. Approve the UAC prompt to finish Elevated sandbox setup, or run without sandbox isolation."
		}
		_ = setupstate.WriteError(dirs.ErrorPath, setupstate.ErrorReport{
			Phase:   "orchestrator",
			Code:    code,
			Message: message,
		})
		return fmt.Errorf("impl/sandbox/windows: %s: %w", message, err)
	}
	r.clearUsersReadyCache()
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{Phase: "verify", Message: "verifying setup marker"})
	if freshness := r.setupReadyFreshness(); !freshness.Current {
		return fmt.Errorf("impl/sandbox/windows: elevated setup did not become current: %s", freshness.Reason)
	}
	if freshness := r.workspaceSetupSnapshot(); !freshness.Current {
		return fmt.Errorf("impl/sandbox/windows: workspace setup did not become current: %s", freshness.Reason)
	}
	if err := r.prepareCommandRunnerHelper(ctx); err != nil {
		return err
	}
	r.clearRefreshCache()
	r.clearPolicyCache()
	r.markBaseRefreshApplied()
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{Phase: "complete", Message: "Windows sandbox setup is ready", Done: true})
	return nil
}

func (r *setupRunner) prepareTargetCurrent(kind setup.SetupKind) bool {
	switch kind {
	case setup.SetupKindWorkspaceOnly:
		return r.workspaceSetupSnapshot().Current
	case setup.SetupKindFull:
		return r.setupReadyFreshness().Current && r.workspaceSetupSnapshot().Current
	default:
		return false
	}
}

func (r *setupRunner) Preflight(ctx context.Context, opts sandbox.PreflightOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.maintenanceMu.Lock(ctx); err != nil {
		return err
	}
	defer r.maintenanceMu.Unlock()
	if freshness := r.setupReadyFreshness(); !freshness.Current {
		return nil
	}
	workspace := r.workspaceSetupSnapshot()
	if workspace.Current {
		r.markBaseRefreshApplied()
		return nil
	}
	if !opts.AllowNonElevatedRepair {
		return nil
	}
	payload, _, err := r.setupPayload(r.baseSetupRequest(), setup.SetupKindWorkspaceOnly)
	if err != nil {
		return err
	}
	if err := setup.ExecuteWithProgress(payload, func(progress setup.Progress) {
		sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
			Phase:   progress.Phase,
			Message: progress.Message,
			Step:    progress.Step,
			Total:   progress.Total,
			Done:    progress.Done,
			Debug:   progress.Debug,
		})
	}); err != nil {
		dirs := setupstate.NewDirs(r.stateRoot)
		_ = setupstate.WriteError(dirs.ErrorPath, setupstate.ErrorReport{
			Phase:   "workspace_preflight",
			Code:    "workspace_acl_refresh_failed",
			Message: err.Error(),
		})
		return err
	}
	r.clearRefreshCache()
	r.clearPolicyCache()
	r.markBaseRefreshApplied()
	return nil
}

func (r *setupRunner) Reset(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	ctx, cancel = withDefaultOperationTimeout(ctx, defaultResetTimeout)
	defer cancel()
	if err := r.maintenanceMu.Lock(ctx); err != nil {
		return err
	}
	defer r.maintenanceMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	r.clearUsersReadyCache()
	r.clearRefreshCache()
	r.clearPolicyCache()
	if r.client != nil {
		_ = r.client.Invalidate()
	}
	dirs := setupstate.NewDirs(r.stateRoot)
	now := time.Now().UTC()
	payload := setup.Payload{
		Version:         setup.PayloadVersion,
		Kind:            setup.SetupKindReset,
		OperationID:     setupOperationID("reset"),
		StartedAt:       now,
		ExpiresAt:       now.Add(defaultResetTimeout),
		StateRoot:       r.stateRoot,
		OfflineUsername: setupOfflineUser(r.stateRoot),
		OnlineUsername:  setupOnlineUser(r.stateRoot),
		OwnerUsername:   currentWindowsUser(),
		ProgressPath:    dirs.ResetProgressPath,
	}.Normalize()
	_ = setupstate.ClearProgress(dirs.ResetProgressPath)
	_ = setupstate.ClearError(dirs.ResetErrorPath)
	if elevated, err := win32.IsElevated(); err == nil && elevated {
		err := setup.ExecuteWithProgress(payload, func(progress setup.Progress) {
			sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
				Phase:   progress.Phase,
				Message: progress.Message,
				Step:    progress.Step,
				Total:   progress.Total,
				Done:    progress.Done,
				Debug:   progress.Debug,
			})
		})
		if err == nil {
			_ = os.RemoveAll(dirs.Reset)
		} else if _, readErr := setupstate.ReadError(dirs.ResetErrorPath); readErr != nil {
			_ = setupstate.WriteError(dirs.ResetErrorPath, setupstate.ErrorReport{
				Phase:   "orchestrator",
				Code:    "reset_failed",
				Message: err.Error(),
			})
		}
		return err
	}
	encoded, err := setup.EncodePayload(payload)
	if err != nil {
		return err
	}
	helperPath := r.resetHelperPath()
	if helperPath == "" {
		return fmt.Errorf("impl/sandbox/windows: reset helper executable path is required")
	}
	stopProgress := forwardSetupProgressFile(ctx, dirs.ResetProgressPath)
	err = win32.RunElevatedAndWaitContext(ctx, helperPath, []string{setupHelperCommand, encoded}, r.cfg.CWD)
	stopProgress()
	r.clearUsersReadyCache()
	r.clearRefreshCache()
	r.clearPolicyCache()
	if err != nil {
		_ = setupstate.WriteError(dirs.ResetErrorPath, setupstate.ErrorReport{
			Phase:   "orchestrator",
			Code:    "reset_failed",
			Message: err.Error(),
		})
		return err
	}
	_ = os.RemoveAll(dirs.Reset)
	return nil
}

func withDefaultOperationTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return ctx, func() {}
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func setupOperationID(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "op"
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func (r *setupRunner) resetHelperPath() string {
	source := strings.TrimSpace(r.executable)
	if dedicated := siblingSetupHelper(source); dedicated != "" {
		return dedicated
	}
	return source
}

func forwardSetupProgressFile(ctx context.Context, path string) func() {
	path = strings.TrimSpace(path)
	if path == "" {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	var last string
	emit := func() {
		report, err := setupstate.ReadProgress(path)
		if err != nil {
			return
		}
		key := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%t\x00%t", report.Phase, report.Message, report.Step, report.Total, report.Done, report.Debug)
		if key == last {
			return
		}
		last = key
		sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
			Phase:   report.Phase,
			Message: report.Message,
			Step:    report.Step,
			Total:   report.Total,
			Done:    report.Done,
			Debug:   report.Debug,
		})
	}
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(350 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				emit()
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				emit()
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func (r *setupRunner) requireSetupReady() error {
	if freshness := r.setupReadyFreshness(); !freshness.Current {
		reason := strings.TrimSpace(freshness.Reason)
		if reason == "" {
			reason = "setup marker missing"
		}
		return fmt.Errorf("impl/sandbox/windows: Windows Elevated sandbox global setup is required (%s)", reason)
	}
	return nil
}

func (r *setupRunner) refreshRequestACLs(ctx context.Context, req runnerruntime.Request) error {
	if ctx == nil {
		ctx = context.Background()
	}
	requestKey, keyErr := r.policyRequestKey(req)
	if keyErr == nil && r.refreshAlreadyApplied(requestKey) {
		return nil
	}
	if err := r.maintenanceMu.Lock(ctx); err != nil {
		return fmt.Errorf("impl/sandbox/windows: refresh sandbox ACLs without elevation: %w", err)
	}
	defer r.maintenanceMu.Unlock()
	if keyErr == nil && r.refreshAlreadyApplied(requestKey) {
		return nil
	}
	payload, _, err := r.setupPayload(req, setup.SetupKindRuntimeRefresh)
	if err != nil {
		return err
	}
	if payload.Policy.FullAccess || !runtimePolicyHasACLTargets(payload.Policy) {
		return nil
	}
	cacheKeys := refreshCacheKeys(requestKey, payload.WorkspacePolicyHash)
	if r.refreshAnyApplied(cacheKeys...) {
		r.markRefreshApplied(cacheKeys...)
		return nil
	}
	if err := setup.Execute(payload); err != nil {
		return fmt.Errorf("impl/sandbox/windows: refresh sandbox ACLs without elevation: %w", err)
	}
	r.markRefreshApplied(cacheKeys...)
	return nil
}

func (r *setupRunner) setupReadyFreshness() setupstate.Freshness {
	payload, err := r.globalSetupPayload()
	if err != nil {
		return setupstate.Freshness{Reason: err.Error()}
	}
	freshness := r.freshnessForPayload(payload)
	if !freshness.Current {
		return freshness
	}
	if err := r.usersFileReady(); err != nil {
		return setupstate.Freshness{Reason: err.Error()}
	}
	return freshness
}

func (r *setupRunner) usersFileReady() error {
	if err := r.cachedUsersFileReady(); err != nil {
		return err
	}
	return nil
}

func (r *setupRunner) cachedUsersFileReady() error {
	r.usersReadyMu.Lock()
	if !r.usersReadyCheckedAt.IsZero() && time.Since(r.usersReadyCheckedAt) < usersReadyCacheTTL {
		errText := r.usersReadyErr
		r.usersReadyMu.Unlock()
		if errText != "" {
			return errors.New(errText)
		}
		return nil
	}
	r.usersReadyMu.Unlock()

	err := r.checkUsersFileReady()
	if errors.Is(err, errSandboxUsersFileMissing) {
		return err
	}
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	r.usersReadyMu.Lock()
	r.usersReadyCheckedAt = time.Now()
	r.usersReadyErr = errText
	r.usersReadyMu.Unlock()
	return err
}

func (r *setupRunner) clearUsersReadyCache() {
	r.usersReadyMu.Lock()
	defer r.usersReadyMu.Unlock()
	r.usersReadyCheckedAt = time.Time{}
	r.usersReadyErr = ""
}

func (r *setupRunner) refreshAlreadyApplied(policyHash string) bool {
	policyHash = strings.TrimSpace(policyHash)
	if policyHash == "" {
		return false
	}
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	_, ok := r.refreshedPolicies[policyHash]
	return ok
}

func (r *setupRunner) refreshAnyApplied(policyHashes ...string) bool {
	for _, policyHash := range policyHashes {
		if r.refreshAlreadyApplied(policyHash) {
			return true
		}
	}
	return false
}

func (r *setupRunner) markRefreshApplied(policyHashes ...string) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if r.refreshedPolicies == nil {
		r.refreshedPolicies = map[string]struct{}{}
	}
	for _, policyHash := range policyHashes {
		policyHash = strings.TrimSpace(policyHash)
		if policyHash == "" {
			continue
		}
		r.refreshedPolicies[policyHash] = struct{}{}
	}
}

func (r *setupRunner) markBaseRefreshApplied() {
	if r == nil {
		return
	}
	r.markRefreshAppliedForRequest(r.baseSetupRequest())
}

func (r *setupRunner) markRefreshAppliedForRequest(req runnerruntime.Request) {
	requestKey, _ := r.policyRequestKey(req)
	payload, _, err := r.setupPayload(req, setup.SetupKindRuntimeRefresh)
	if err != nil {
		return
	}
	r.markRefreshApplied(refreshCacheKeys(requestKey, payload.WorkspacePolicyHash)...)
}

func (r *setupRunner) clearRefreshCache() {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	r.refreshedPolicies = nil
}

func refreshCacheKeys(keys ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func (r *setupRunner) cachedPolicy(cacheKey string) (winpolicy.Policy, string, bool) {
	cacheKey = strings.TrimSpace(cacheKey)
	if cacheKey == "" {
		return winpolicy.Policy{}, "", false
	}
	r.policyMu.Lock()
	defer r.policyMu.Unlock()
	cached, ok := r.policyCache[cacheKey]
	return cached.policy, cached.hash, ok
}

func (r *setupRunner) markPolicyCached(cacheKey string, policy winpolicy.Policy, policyHash string) {
	cacheKey = strings.TrimSpace(cacheKey)
	if cacheKey == "" {
		return
	}
	r.policyMu.Lock()
	defer r.policyMu.Unlock()
	if r.policyCache == nil {
		r.policyCache = map[string]cachedWindowsPolicy{}
	}
	r.policyCache[cacheKey] = cachedWindowsPolicy{policy: policy, hash: policyHash}
}

func (r *setupRunner) clearPolicyCache() {
	r.policyMu.Lock()
	defer r.policyMu.Unlock()
	r.policyCache = nil
}

func (r *setupRunner) checkUsersFileReady() error {
	dirs := setupstate.NewDirs(r.stateRoot)
	data, err := os.ReadFile(dirs.UsersPath)
	if err != nil {
		if os.IsNotExist(err) {
			return errSandboxUsersFileMissing
		}
		return fmt.Errorf("read sandbox users file: %w", err)
	}
	var users setup.UsersFile
	if err := json.Unmarshal(data, &users); err != nil {
		return fmt.Errorf("decode sandbox users file: %w", err)
	}
	if strings.TrimSpace(users.Offline.Username) == "" {
		return fmt.Errorf("sandbox users file is incomplete")
	}
	expectedOffline := setupOfflineUser(r.stateRoot)
	if !strings.EqualFold(strings.TrimSpace(users.Offline.Username), expectedOffline) {
		return fmt.Errorf("sandbox users file does not match expected sandbox account")
	}
	if err := validateUserSecret(users.Offline); err != nil {
		return fmt.Errorf("offline sandbox credentials are stale: %w", err)
	}
	return nil
}

func validateUserSecret(secret setup.UserSecret) error {
	password, err := win32.UnprotectString(secret.PasswordProtected)
	if err != nil {
		return fmt.Errorf("unprotect sandbox credentials: %w", err)
	}
	return win32.ValidateCredentials(win32.LogonCredentials{
		Username: secret.Username,
		Password: password,
	})
}

func (r *setupRunner) status() sandbox.Status {
	status := sandbox.Status{
		RequestedBackend: sandbox.BackendWindowsElevated,
		ResolvedBackend:  sandbox.BackendWindowsElevated,
	}
	globalCheck := sandbox.SetupCheck{
		Name:  "global",
		Scope: sandbox.SetupScopeGlobal,
	}
	payload, err := r.globalSetupPayload()
	if err == nil {
		globalCheck.Version = payload.Version
		globalCheck.Details = map[string]string{
			"runner_hash":  payload.RunnerHash,
			"policy_hash":  payload.GlobalPolicyHash,
			"offline_user": payload.OfflineUsername,
			"owner_user":   payload.OwnerUsername,
		}
	}
	freshness := setupstate.Freshness{Reason: strings.TrimSpace(errString(err))}
	if err == nil {
		freshness = r.freshnessForPayload(payload)
		if freshness.Current {
			if readyErr := r.usersFileReady(); readyErr != nil {
				freshness = setupstate.Freshness{Reason: readyErr.Error()}
			}
		}
	}
	globalCheck.Current = freshness.Current
	globalCheck.Required = !freshness.Current
	globalCheck.Reason = strings.TrimSpace(freshness.Reason)
	if !freshness.Current {
		status.Setup.Required = true
		status.FallbackReason = globalCheck.Reason
		status.FallbackInstallHint = "Run /sandbox setup in the TUI or `caelis sandbox setup` in a terminal to initialize Windows Elevated sandbox with one UAC prompt."
		if report, err := readLatestSetupError(setupstate.NewDirs(r.stateRoot)); err == nil {
			status.Setup.Error = strings.TrimSpace(report.Message)
			globalCheck.Error = status.Setup.Error
		}
		status.Setup.Checks = []sandbox.SetupCheck{globalCheck}
		return status
	}
	workspace := r.workspaceSetupSnapshot()
	workspaceCheck := sandbox.SetupCheck{
		Name:      "workspace",
		Scope:     sandbox.SetupScopeWorkspace,
		Current:   workspace.Current,
		Required:  !workspace.Current,
		Reason:    strings.TrimSpace(workspace.Reason),
		Root:      strings.TrimSpace(workspace.Root),
		UpdatedAt: workspace.UpdatedAt,
		Details: map[string]string{
			"policy_hash": strings.TrimSpace(workspace.PolicyHash),
		},
		Counts: map[string]int{
			"read_roots":  workspace.ReadRoots,
			"write_roots": workspace.WriteRoots,
			"deny_read":   workspace.DenyRead,
			"deny_write":  workspace.DenyWrite,
		},
	}
	if !workspace.Current {
		status.Setup.Required = true
		status.FallbackReason = workspace.Reason
		status.FallbackInstallHint = "Run /sandbox setup in the TUI or `caelis sandbox setup` in a terminal to authorize this Windows sandbox workspace."
		if report, err := readLatestSetupError(setupstate.NewDirs(r.stateRoot)); err == nil {
			status.Setup.Error = strings.TrimSpace(report.Message)
			workspaceCheck.Error = status.Setup.Error
		}
	}
	status.Setup.Checks = []sandbox.SetupCheck{globalCheck, workspaceCheck}
	return status
}

func readLatestSetupError(dirs setupstate.Dirs) (setupstate.ErrorReport, error) {
	setupReport, setupErr := setupstate.ReadError(dirs.ErrorPath)
	resetReport, resetErr := setupstate.ReadError(dirs.ResetErrorPath)
	if setupErr != nil && resetErr != nil {
		return setupstate.ErrorReport{}, setupErr
	}
	if setupErr != nil {
		return resetReport, nil
	}
	if resetErr != nil {
		return setupReport, nil
	}
	if resetReport.Time.After(setupReport.Time) {
		return resetReport, nil
	}
	return setupReport, nil
}

type workspaceSetupSnapshot struct {
	Current    bool
	Reason     string
	Root       string
	PolicyHash string
	UpdatedAt  time.Time
	ReadRoots  int
	WriteRoots int
	DenyRead   int
	DenyWrite  int
}

func (r *setupRunner) workspaceSetupSnapshot() workspaceSetupSnapshot {
	req := r.baseSetupRequest()
	root := firstNonEmpty(req.Dir, r.cfg.CWD)
	policy, policyHash, missingCaps, err := r.policyForRequestWithHashReadOnly(req)
	out := workspaceSetupSnapshot{
		Root:       root,
		PolicyHash: policyHash,
		ReadRoots:  len(policy.ReadRoots),
		WriteRoots: len(policy.WriteRoots),
		DenyRead:   len(policy.DenyReadPaths),
		DenyWrite:  len(policy.DenyWritePaths),
	}
	dirs := setupstate.NewDirs(r.stateRoot)
	if record, readErr := setupstate.ReadWorkspace(dirs.WorkspacePath); readErr == nil {
		out.UpdatedAt = record.UpdatedAt
	}
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	if policy.FullAccess || len(policy.WriteRoots) == 0 {
		out.Current = true
		return out
	}
	if len(missingCaps) > 0 {
		out.Reason = "workspace capability SIDs are missing"
		return out
	}
	record, readErr := setupstate.ReadWorkspace(dirs.WorkspacePath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			out.Reason = "workspace setup state missing"
		} else {
			out.Reason = readErr.Error()
		}
		return out
	}
	out.UpdatedAt = record.UpdatedAt
	if strings.TrimSpace(record.PolicyHash) != strings.TrimSpace(policyHash) {
		out.Reason = "workspace policy changed"
		return out
	}
	if pathutil.Key(record.WorkspaceRoot) != "" && pathutil.Key(root) != "" && pathutil.Key(record.WorkspaceRoot) != pathutil.Key(root) {
		out.Reason = "workspace root changed"
		return out
	}
	if record.SetupVersion != 0 && record.SetupVersion != setup.PayloadVersion {
		out.Reason = "workspace setup version changed"
		return out
	}
	if strings.TrimSpace(record.OfflineUsername) != "" && !strings.EqualFold(record.OfflineUsername, setupOfflineUser(r.stateRoot)) {
		out.Reason = "offline sandbox user changed"
		return out
	}
	results, err := setup.CheckPolicyACLs(policy, setupOfflineUser(r.stateRoot))
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	for _, result := range results {
		if result.Current {
			continue
		}
		reason := strings.TrimSpace(result.Reason)
		if reason == "" {
			reason = "ACL entries missing"
		}
		if path := strings.TrimSpace(result.Path); path != "" {
			reason = path + ": " + reason
		}
		out.Reason = reason
		return out
	}
	out.Current = true
	return out
}

func (r *setupRunner) freshnessForPayload(payload setup.Payload) setupstate.Freshness {
	marker, err := setupstate.ReadMarker(setupstate.NewDirs(r.stateRoot).MarkerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return setupstate.Freshness{Reason: "setup marker missing"}
		}
		return setupstate.Freshness{Reason: err.Error()}
	}
	freshness := setupstate.CheckFreshness(marker, setupstate.Expectation{
		Version:         payload.Version,
		PolicyHash:      payload.GlobalPolicyHash,
		OfflineUsername: payload.OfflineUsername,
		OwnerUsername:   payload.OwnerUsername,
	})
	if !freshness.Current {
		return freshness
	}
	if !policyWriteRootCapabilitiesCurrent(payload.GlobalPolicy) {
		return setupstate.Freshness{Reason: "global capability SIDs are missing"}
	}
	return freshness
}

func (r *setupRunner) globalSetupPayload() (setup.Payload, error) {
	policy, err := r.globalSetupPolicy(false)
	if err != nil {
		return setup.Payload{}, err
	}
	policyHash, err := stablePolicyHash(policy)
	if err != nil {
		return setup.Payload{}, err
	}
	runnerHash, err := r.cachedExecutableHash()
	if err != nil {
		return setup.Payload{}, err
	}
	payload := setup.Payload{
		Version:          setup.PayloadVersion,
		Kind:             setup.SetupKindFull,
		StateRoot:        r.stateRoot,
		RunnerHash:       runnerHash,
		PolicyHash:       policyHash,
		GlobalPolicyHash: policyHash,
		GlobalPolicy:     policy,
		Policy:           policy,
		OfflineUsername:  setupOfflineUser(r.stateRoot),
		OwnerUsername:    currentWindowsUser(),
	}.Normalize()
	return payload, nil
}

func (r *setupRunner) setupPayload(req runnerruntime.Request, kind setup.SetupKind) (setup.Payload, setupstate.Freshness, error) {
	if kind == "" {
		kind = setup.SetupKindFull
	}
	globalPayload, err := r.globalSetupPayload()
	if err != nil {
		return setup.Payload{}, setupstate.Freshness{}, err
	}
	globalPolicy := globalPayload.GlobalPolicy
	if kind == setup.SetupKindFull {
		globalPolicy, err = r.globalSetupPolicy(true)
		if err != nil {
			return setup.Payload{}, setupstate.Freshness{}, err
		}
	}
	policy, workspacePolicyHash, err := r.policyForRequestWithHash(req)
	if err != nil {
		return setup.Payload{}, setupstate.Freshness{}, err
	}
	if kind == setup.SetupKindRuntimeRefresh {
		policy = runtimeRefreshDeltaPolicy(policy, globalPolicy)
	}
	dirs := setupstate.NewDirs(r.stateRoot)
	payload := setup.Payload{
		Version:             setup.PayloadVersion,
		Kind:                kind,
		StateRoot:           r.stateRoot,
		RunnerHash:          globalPayload.RunnerHash,
		PolicyHash:          globalPayload.GlobalPolicyHash,
		GlobalPolicyHash:    globalPayload.GlobalPolicyHash,
		WorkspacePolicyHash: workspacePolicyHash,
		GlobalPolicy:        globalPolicy,
		Policy:              policy,
		OfflineUsername:     globalPayload.OfflineUsername,
		OwnerUsername:       globalPayload.OwnerUsername,
		WorkspaceRoot:       firstNonEmpty(req.Dir, r.cfg.CWD),
		WorkspaceStatePath:  dirs.WorkspacePath,
		RefreshOnly:         kind == setup.SetupKindRuntimeRefresh,
	}.Normalize()
	return payload, setupstate.Freshness{}, nil
}

func (r *setupRunner) globalSetupPolicy(bindCapabilities bool) (winpolicy.Policy, error) {
	policy := winpolicy.CommonGlobalPolicy(globalSetupWriteRoots())
	if len(policy.WriteRoots) == 0 {
		return policy, nil
	}
	dirs := setupstate.NewDirs(r.stateRoot)
	var (
		binding capability.Binding
		err     error
	)
	if bindCapabilities {
		binding, err = capability.BindWriteRoots(dirs.CapPath, "", policy.WriteRoots)
	} else {
		binding, err = capability.LookupWriteRoots(dirs.CapPath, "", policy.WriteRoots)
	}
	if err != nil {
		return winpolicy.Policy{}, fmt.Errorf("impl/sandbox/windows: bind global capability SIDs: %w", err)
	}
	policy.CapabilitySIDs = binding.AllSIDs
	policy.WriteRootCapabilitySIDs = binding.WriteRootTo
	return policy, nil
}

func globalSetupWriteRoots() []string {
	return nil
}

func runtimeRefreshDeltaPolicy(policy winpolicy.Policy, global winpolicy.Policy) winpolicy.Policy {
	out := policy
	globalReadCoverage := append([]string{}, global.ReadRoots...)
	globalReadCoverage = append(globalReadCoverage, global.WriteRoots...)
	out.ReadRoots = filterPolicyPathsOutsideRoots(policy.ReadRoots, globalReadCoverage)
	out.WriteRoots = filterPolicyPathsOutsideRoots(policy.WriteRoots, global.WriteRoots)
	out.DenyReadPaths = filterPolicyPathsOutsideRoots(policy.DenyReadPaths, global.DenyReadPaths)
	out.DenyWritePaths = filterPolicyPathsOutsideRoots(policy.DenyWritePaths, global.DenyWritePaths)
	out.MaterializeDenyWritePaths = filterPolicyPathsOutsideRoots(policy.MaterializeDenyWritePaths, global.DenyWritePaths)
	if len(policy.WriteRootCapabilitySIDs) > 0 {
		remaining := map[string]struct{}{}
		for _, root := range out.WriteRoots {
			if key := pathutil.Key(root); key != "" {
				remaining[key] = struct{}{}
			}
		}
		out.WriteRootCapabilitySIDs = map[string]string{}
		for root, sid := range policy.WriteRootCapabilitySIDs {
			if _, ok := remaining[pathutil.Key(root)]; ok {
				out.WriteRootCapabilitySIDs[root] = sid
			}
		}
		if len(out.WriteRootCapabilitySIDs) == 0 {
			out.WriteRootCapabilitySIDs = nil
		}
	}
	return out
}

func filterPolicyPathsOutsideRoots(paths []string, roots []string) []string {
	paths = pathutil.Dedupe(paths)
	if len(paths) == 0 || len(roots) == 0 {
		return paths
	}
	var out []string
	for _, path := range paths {
		if !pathCoveredByAnyRoot(path, roots) {
			out = append(out, path)
		}
	}
	return out
}

func pathCoveredByAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if pathutil.IsUnder(path, root) {
			return true
		}
	}
	return false
}

func runtimePolicyHasACLTargets(policy winpolicy.Policy) bool {
	return len(policy.ReadRoots) > 0 ||
		len(policy.WriteRoots) > 0 ||
		len(policy.DenyReadPaths) > 0 ||
		len(policy.DenyWritePaths) > 0
}

func policyWriteRootCapabilitiesCurrent(policy winpolicy.Policy) bool {
	for _, root := range policy.WriteRoots {
		if strings.TrimSpace(policyWriteRootCapabilitySID(policy, root)) == "" {
			return false
		}
	}
	return true
}

func policyWriteRootCapabilitySID(policy winpolicy.Policy, root string) string {
	if len(policy.WriteRootCapabilitySIDs) == 0 {
		return ""
	}
	if sid := strings.TrimSpace(policy.WriteRootCapabilitySIDs[pathutil.Normalize(root)]); sid != "" {
		return sid
	}
	rootKey := pathutil.Key(root)
	for candidate, sid := range policy.WriteRootCapabilitySIDs {
		if pathutil.Key(candidate) == rootKey {
			return strings.TrimSpace(sid)
		}
	}
	return ""
}

func setupOfflineUser(stateRoot string) string {
	return "CaelisSbxOff" + stateRootHash(stateRoot)
}

func setupOnlineUser(stateRoot string) string {
	return "CaelisSbxOn" + stateRootHash(stateRoot)
}

func stateRootHash(stateRoot string) string {
	normalized := strings.ToLower(strings.TrimSpace(filepath.Clean(stateRoot)))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])[:8]
}

func (r *setupRunner) baseSetupRequest() runnerruntime.Request {
	return runnerruntime.Request{
		Dir: r.cfg.CWD,
		Constraints: sandbox.Constraints{
			Route:      sandbox.RouteSandbox,
			Backend:    sandbox.BackendWindowsElevated,
			Permission: sandbox.PermissionWorkspaceWrite,
			Network:    sandbox.NetworkDisabled,
		},
	}
}

func (r *setupRunner) helperExecutablePath(runnerruntime.Request) (string, error) {
	path, _, err := r.materializeRunnerHelper()
	return path, err
}

func (r *setupRunner) prepareCommandRunnerHelper(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{Phase: "helper", Message: "materializing command runner helper executable"})
	_, _, err := r.materializeRunnerHelper()
	return err
}

func (r *setupRunner) materializeHelper() (string, string, error) {
	source := strings.TrimSpace(r.executable)
	if dedicated := siblingSetupHelper(source); dedicated != "" {
		source = dedicated
	}
	return r.materializeHelperFromSource(source, "caelis-windows-sandbox-")
}

func (r *setupRunner) materializeRunnerHelper() (string, string, error) {
	source := strings.TrimSpace(r.executable)
	if dedicated := siblingRunnerHelper(source); dedicated != "" {
		source = dedicated
	}
	return r.materializeHelperFromSource(source, "caelis-command-runner-")
}

func (r *setupRunner) materializeHelperFromSource(source string, prefix string) (string, string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", "", fmt.Errorf("impl/sandbox/windows: helper executable path is required")
	}
	cacheKey := helperPathCacheKey(source, prefix)
	if cached, ok := r.cachedHelperPath(cacheKey); ok {
		return cached.path, cached.hash, nil
	}
	hash, err := r.helperSourceHash(source)
	if err != nil {
		return "", "", err
	}
	shortHash := hash
	if len(shortHash) > 16 {
		shortHash = shortHash[:16]
	}
	dirs := setupstate.NewDirs(r.stateRoot)
	target := filepath.Join(dirs.Bin, prefix+shortHash+".exe")
	if strings.EqualFold(source, target) {
		r.markHelperPathCached(cacheKey, source, hash)
		return source, hash, nil
	}
	if helperFileFresh(source, target) {
		r.markHelperPathCached(cacheKey, target, hash)
		return target, hash, nil
	}
	r.helperMu.Lock()
	defer r.helperMu.Unlock()
	if cached, ok := r.cachedHelperPathLocked(cacheKey); ok {
		return cached.path, cached.hash, nil
	}
	if helperFileFresh(source, target) {
		r.markHelperPathCachedLocked(cacheKey, target, hash)
		return target, hash, nil
	}
	if err := os.MkdirAll(dirs.Bin, 0o700); err != nil {
		return "", "", err
	}
	tmp, err := os.CreateTemp(dirs.Bin, ".caelis-helper-*.tmp")
	if err != nil {
		return "", "", err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	src, err := os.Open(source)
	if err != nil {
		_ = tmp.Close()
		return "", "", err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		_ = src.Close()
		_ = tmp.Close()
		return "", "", err
	}
	if err := src.Close(); err != nil {
		_ = tmp.Close()
		return "", "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		return "", "", err
	}
	committedPath, err := commitHelperTemp(tmpPath, target, hash)
	if err != nil {
		return "", "", err
	}
	committed = true
	r.markHelperPathCachedLocked(cacheKey, committedPath, hash)
	return committedPath, hash, nil
}

func (r *setupRunner) helperSourceHash(source string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(source), strings.TrimSpace(r.executable)) {
		return r.cachedExecutableHash()
	}
	return fileHash(source)
}

func helperPathCacheKey(source string, prefix string) string {
	return strings.ToLower(filepath.Clean(strings.TrimSpace(source))) + "\x00" + strings.TrimSpace(prefix)
}

func (r *setupRunner) cachedHelperPath(key string) (cachedHelperPath, bool) {
	r.helperMu.Lock()
	defer r.helperMu.Unlock()
	if r.helperPathCache == nil {
		return cachedHelperPath{}, false
	}
	return r.cachedHelperPathLocked(key)
}

func (r *setupRunner) cachedHelperPathLocked(key string) (cachedHelperPath, bool) {
	if r.helperPathCache == nil {
		return cachedHelperPath{}, false
	}
	cached, ok := r.helperPathCache[key]
	return cached, ok && strings.TrimSpace(cached.path) != "" && strings.TrimSpace(cached.hash) != ""
}

func (r *setupRunner) markHelperPathCached(key string, path string, hash string) {
	r.helperMu.Lock()
	defer r.helperMu.Unlock()
	r.markHelperPathCachedLocked(key, path, hash)
}

func (r *setupRunner) markHelperPathCachedLocked(key string, path string, hash string) {
	key = strings.TrimSpace(key)
	path = strings.TrimSpace(path)
	hash = strings.TrimSpace(hash)
	if key == "" || path == "" || hash == "" {
		return
	}
	if r.helperPathCache == nil {
		r.helperPathCache = map[string]cachedHelperPath{}
	}
	r.helperPathCache[key] = cachedHelperPath{path: path, hash: hash}
}

func helperFileHashMatches(path string, hash string) bool {
	existingHash, err := fileHash(path)
	return err == nil && existingHash == hash
}

func helperFileFresh(source string, target string) bool {
	sourceInfo, err := os.Stat(source)
	if err != nil || sourceInfo.IsDir() {
		return false
	}
	targetInfo, err := os.Stat(target)
	if err != nil || targetInfo.IsDir() {
		return false
	}
	if sourceInfo.Size() != targetInfo.Size() {
		return false
	}
	sourceMod := sourceInfo.ModTime()
	targetMod := targetInfo.ModTime()
	return targetMod.Equal(sourceMod) || targetMod.After(sourceMod)
}

func commitHelperTemp(tmpPath string, target string, hash string) (string, error) {
	if helperFileHashMatches(target, hash) {
		_ = os.Remove(tmpPath)
		return target, nil
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		if helperFileHashMatches(target, hash) {
			_ = os.Remove(tmpPath)
			return target, nil
		}
		return "", fmt.Errorf("replace helper %s: %w", target, err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		if helperFileHashMatches(target, hash) {
			_ = os.Remove(tmpPath)
			return target, nil
		}
		return "", err
	}
	return target, nil
}

func siblingRunnerHelper(executable string) string {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return ""
	}
	dir := filepath.Dir(executable)
	for _, candidate := range []string{
		filepath.Join(dir, "caelis-command-runner.exe"),
		filepath.Join(dir, "caelis-resources", "caelis-command-runner.exe"),
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func siblingSetupHelper(executable string) string {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return ""
	}
	dir := filepath.Dir(executable)
	for _, candidate := range []string{
		filepath.Join(dir, "caelis-windows-sandbox-setup.exe"),
		filepath.Join(dir, "caelis-resources", "caelis-windows-sandbox-setup.exe"),
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func (r *setupRunner) credentialsForRequest(req runnerruntime.Request) (runnerclient.Credentials, error) {
	if _, err := r.policyForRequest(req); err != nil {
		return runnerclient.Credentials{}, err
	}
	dirs := setupstate.NewDirs(r.stateRoot)
	data, err := os.ReadFile(dirs.UsersPath)
	if err != nil {
		return runnerclient.Credentials{}, fmt.Errorf("impl/sandbox/windows: read sandbox credentials: %w", err)
	}
	var users setup.UsersFile
	if err := json.Unmarshal(data, &users); err != nil {
		return runnerclient.Credentials{}, fmt.Errorf("impl/sandbox/windows: decode sandbox credentials: %w", err)
	}
	secret := users.Offline
	password, err := win32.UnprotectString(secret.PasswordProtected)
	if err != nil {
		return runnerclient.Credentials{}, fmt.Errorf("impl/sandbox/windows: unprotect sandbox credentials: %w", err)
	}
	return runnerclient.Credentials{Username: secret.Username, Password: password}, nil
}

func (r *setupRunner) policyForRequest(req runnerruntime.Request) (winpolicy.Policy, error) {
	policy, _, err := r.policyForRequestWithHash(req)
	return policy, err
}

func (r *setupRunner) policyForRequestWithHash(req runnerruntime.Request) (winpolicy.Policy, string, error) {
	cacheKey, err := r.policyRequestKey(req)
	if err != nil {
		return winpolicy.Policy{}, "", err
	}
	if cached, hash, ok := r.cachedPolicy(cacheKey); ok {
		return cached, hash, nil
	}
	policy := windowsPolicyForRequest(r.cfg, req)
	policyHash, err := stablePolicyHash(policy)
	if err != nil {
		return winpolicy.Policy{}, "", err
	}
	if policy.FullAccess || len(policy.WriteRoots) == 0 {
		r.markPolicyCached(cacheKey, policy, policyHash)
		return policy, policyHash, nil
	}
	dirs := setupstate.NewDirs(r.stateRoot)
	binding, err := capability.BindWriteRoots(dirs.CapPath, firstNonEmpty(req.Dir, r.cfg.CWD), policy.WriteRoots)
	if err != nil {
		return winpolicy.Policy{}, "", fmt.Errorf("impl/sandbox/windows: bind capability SIDs: %w", err)
	}
	policy.CapabilitySIDs = binding.AllSIDs
	policy.WriteRootCapabilitySIDs = binding.WriteRootTo
	r.markPolicyCached(cacheKey, policy, policyHash)
	return policy, policyHash, nil
}

func (r *setupRunner) policyForRequestWithHashReadOnly(req runnerruntime.Request) (winpolicy.Policy, string, []string, error) {
	policy := windowsPolicyForRequest(r.cfg, req)
	policyHash, err := stablePolicyHash(policy)
	if err != nil {
		return winpolicy.Policy{}, "", nil, err
	}
	if policy.FullAccess || len(policy.WriteRoots) == 0 {
		return policy, policyHash, nil, nil
	}
	dirs := setupstate.NewDirs(r.stateRoot)
	binding, err := capability.LookupWriteRoots(dirs.CapPath, firstNonEmpty(req.Dir, r.cfg.CWD), policy.WriteRoots)
	if err != nil {
		return winpolicy.Policy{}, "", nil, fmt.Errorf("impl/sandbox/windows: inspect capability SIDs: %w", err)
	}
	policy.CapabilitySIDs = binding.AllSIDs
	policy.WriteRootCapabilitySIDs = binding.WriteRootTo
	return policy, policyHash, append([]string(nil), binding.Missing...), nil
}

func (r *setupRunner) policyRequestKey(req runnerruntime.Request) (string, error) {
	return setupstate.HashJSON(struct {
		CWD              string              `json:"cwd,omitempty"`
		ReadableRoots    []string            `json:"readable_roots,omitempty"`
		WritableRoots    []string            `json:"writable_roots,omitempty"`
		ReadOnlySubpaths []string            `json:"read_only_subpaths,omitempty"`
		Dir              string              `json:"dir,omitempty"`
		Constraints      sandbox.Constraints `json:"constraints,omitempty"`
	}{
		CWD:              strings.TrimSpace(r.cfg.CWD),
		ReadableRoots:    append([]string(nil), r.cfg.ReadableRoots...),
		WritableRoots:    append([]string(nil), r.cfg.WritableRoots...),
		ReadOnlySubpaths: append([]string(nil), r.cfg.ReadOnlySubpaths...),
		Dir:              strings.TrimSpace(req.Dir),
		Constraints:      sandbox.NormalizeConstraints(req.Constraints),
	})
}

func (r *setupRunner) cachedExecutableHash() (string, error) {
	r.executableHashMu.Lock()
	if r.executableHash != "" {
		hash := r.executableHash
		r.executableHashMu.Unlock()
		return hash, nil
	}
	r.executableHashMu.Unlock()

	hash, err := fileHash(r.executable)
	if err != nil {
		return "", err
	}
	r.executableHashMu.Lock()
	if r.executableHash == "" {
		r.executableHash = hash
	}
	hash = r.executableHash
	r.executableHashMu.Unlock()
	return hash, nil
}

func stablePolicyHash(policy winpolicy.Policy) (string, error) {
	policy.CapabilitySIDs = nil
	policy.WriteRootCapabilitySIDs = nil
	return setupstate.HashJSON(policy)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func windowsPolicyForRequest(cfg sandbox.Config, req runnerruntime.Request) winpolicy.Policy {
	return winpolicy.Build(winpolicy.Input{
		Config:      cfg,
		Constraints: req.Constraints,
		CommandDir:  req.Dir,
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func currentWindowsUser() string {
	if u, err := user.Current(); err == nil && strings.TrimSpace(u.Username) != "" {
		return strings.TrimSpace(u.Username)
	}
	domain := strings.TrimSpace(os.Getenv("USERDOMAIN"))
	name := strings.TrimSpace(os.Getenv("USERNAME"))
	if domain != "" && name != "" {
		return domain + `\` + name
	}
	return name
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

func fileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
