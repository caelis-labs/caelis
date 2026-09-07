package gatewayapp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func (s *Stack) runSandboxLifecycle(ctx context.Context, action sandboxLifecycleAction) (SandboxStatus, error) {
	target, err := s.commandBackend.selectSandboxLifecycleTarget(s.composition.sandbox)
	if err != nil {
		return SandboxStatus{}, err
	}
	if target.NoOp {
		return s.runtimeProjection().SandboxStatus(), nil
	}
	defer func() { _ = target.Close() }()
	return s.commandBackend.sandboxLifecycleStatus(target), action(ctx, target.Runtime)
}

func TestPrepareSandboxUsesCurrentLifecycleRuntime(t *testing.T) {
	runtime := &sandboxLifecyclePrepareRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
	}
	stack := sandboxLifecycleTestStack(runtime, "windows")

	status, err := stack.runSandboxLifecycle(context.Background(), prepareSandboxRuntime)
	if err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	if runtime.prepareCalls != 1 {
		t.Fatalf("Prepare() calls = %d, want 1", runtime.prepareCalls)
	}
	if runtime.closeCalls != 0 {
		t.Fatalf("Close() calls = %d, want 0 for current runtime", runtime.closeCalls)
	}
	if got := status.ResolvedBackend; got != "windows" {
		t.Fatalf("ResolvedBackend = %q, want windows", got)
	}
}

func TestSandboxStatusForWorkspaceDoesNotReuseStartupRuntimeSetup(t *testing.T) {
	runtime := newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows)
	runtime.status.Setup = sandbox.SetupStatus{Checks: []sandbox.SetupCheck{{
		Name: "workspace", Scope: sandbox.SetupScopeWorkspace, Current: true,
		Root: "/workspace", Counts: map[string]int{"write_roots": 2},
	}}}
	stack := sandboxLifecycleTestStack(runtime, "windows")
	stack.composition.sandbox = mergeSandboxConfig(stack.composition.process.sandboxPersisted, stack.composition.runtimeProcessSnapshot().sandboxOverride)

	startup := stack.ControlStatus().SandboxForWorkspace(session.WorkspaceRef{Key: "startup", CWD: "/workspace"})
	if startup.WorkspaceSetupRoot != "/workspace" || startup.WorkspaceSetupWriteRoots != 2 {
		t.Fatalf("startup workspace status = %#v, want Runtime setup projection", startup)
	}
	other := stack.ControlStatus().SandboxForWorkspace(session.WorkspaceRef{Key: "other", CWD: "/other-workspace"})
	if other.WorkspaceSetupRoot != "" || other.WorkspaceSetupWriteRoots != 0 || len(other.Setup.Checks) != 0 {
		t.Fatalf("other workspace status = %#v, leaked startup Runtime setup", other)
	}
}

func TestRepairSandboxFallsBackToPrepare(t *testing.T) {
	runtime := &sandboxLifecyclePrepareRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
	}
	stack := sandboxLifecycleTestStack(runtime, "windows")

	if _, err := stack.runSandboxLifecycle(context.Background(), repairSandboxRuntime); err != nil {
		t.Fatalf("RepairSandbox() error = %v", err)
	}
	if runtime.prepareCalls != 1 {
		t.Fatalf("Prepare() calls = %d, want 1", runtime.prepareCalls)
	}
}

func TestRepairSandboxPrefersRepair(t *testing.T) {
	runtime := &sandboxLifecycleRepairRuntime{
		sandboxLifecyclePrepareRuntime: &sandboxLifecyclePrepareRuntime{
			sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
		},
	}
	stack := sandboxLifecycleTestStack(runtime, "windows")

	if _, err := stack.runSandboxLifecycle(context.Background(), repairSandboxRuntime); err != nil {
		t.Fatalf("RepairSandbox() error = %v", err)
	}
	if runtime.repairCalls != 1 {
		t.Fatalf("Repair() calls = %d, want 1", runtime.repairCalls)
	}
	if runtime.prepareCalls != 0 {
		t.Fatalf("Prepare() calls = %d, want 0 when Repair is available", runtime.prepareCalls)
	}
}

func TestSandboxLifecycleUsesTemporaryRuntimeWhenCurrentCannotHandleLifecycle(t *testing.T) {
	current := newSandboxLifecycleTestRuntime("", sandbox.BackendHost)
	temp := &sandboxLifecyclePrepareRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
	}
	stack := sandboxLifecycleTestStack(current, "windows")
	stack.composition.workspace.CWD = "/workspace"
	stack.composition.authorities.storeDir = "/store"

	var factoryCalls int
	stack.commandBackend.sandboxLifecycleFactory = func(cfg sandbox.Config, current sandbox.Runtime) (sandbox.LifecycleTarget, error) {
		factoryCalls++
		if cfg.RequestedBackend != sandbox.BackendWindows {
			t.Fatalf("factory cfg.RequestedBackend = %q, want windows", cfg.RequestedBackend)
		}
		if cfg.CWD != "/workspace" {
			t.Fatalf("factory cfg.CWD = %q, want /workspace", cfg.CWD)
		}
		if cfg.StateDir != "/store" {
			t.Fatalf("factory cfg.StateDir = %q, want /store", cfg.StateDir)
		}
		if current != stack.composition.exec {
			t.Fatalf("factory current runtime = %#v, want stack runtime", current)
		}
		return sandbox.LifecycleTarget{Runtime: temp, Config: cfg}, nil
	}

	status, err := stack.runSandboxLifecycle(context.Background(), prepareSandboxRuntime)
	if err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	if factoryCalls != 1 {
		t.Fatalf("lifecycle factory calls = %d, want 1", factoryCalls)
	}
	if temp.prepareCalls != 1 {
		t.Fatalf("temporary Prepare() calls = %d, want 1", temp.prepareCalls)
	}
	if temp.closeCalls != 1 {
		t.Fatalf("temporary Close() calls = %d, want 1", temp.closeCalls)
	}
	if got := status.ResolvedBackend; got != "windows" {
		t.Fatalf("ResolvedBackend = %q, want temporary runtime status windows", got)
	}
}

func TestSandboxLifecycleSkipsHostBackend(t *testing.T) {
	runtime := &sandboxLifecycleResetRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendHost, sandbox.BackendHost),
	}
	stack := sandboxLifecycleTestStack(runtime, "host")

	var factoryCalls int
	stack.commandBackend.sandboxLifecycleFactory = func(sandbox.Config, sandbox.Runtime) (sandbox.LifecycleTarget, error) {
		factoryCalls++
		return sandbox.LifecycleTarget{NoOp: true}, nil
	}

	status, err := stack.runSandboxLifecycle(context.Background(), resetSandboxRuntime)
	if err != nil {
		t.Fatalf("ResetSandbox() error = %v", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("lifecycle factory calls = %d, want 0 for host backend", factoryCalls)
	}
	if runtime.resetCalls != 0 {
		t.Fatalf("Reset() calls = %d, want 0 for host backend", runtime.resetCalls)
	}
	if got := status.Route; got != "host" {
		t.Fatalf("Route = %q, want host", got)
	}
}

func TestSandboxLifecycleFactoryError(t *testing.T) {
	current := newSandboxLifecycleTestRuntime("", sandbox.BackendHost)
	stack := sandboxLifecycleTestStack(current, "windows")
	wantErr := errors.New("factory failed")
	stack.commandBackend.sandboxLifecycleFactory = func(sandbox.Config, sandbox.Runtime) (sandbox.LifecycleTarget, error) {
		return sandbox.LifecycleTarget{}, wantErr
	}

	_, err := stack.runSandboxLifecycle(context.Background(), resetSandboxRuntime)
	if !errors.Is(err, wantErr) {
		t.Fatalf("ResetSandbox() error = %v, want %v", err, wantErr)
	}
}

func TestSandboxLifecycleCurrentRuntimeWithoutCapabilityNoops(t *testing.T) {
	runtime := newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows)
	stack := sandboxLifecycleTestStack(runtime, "windows")
	stack.composition.workspace.CWD = t.TempDir()
	stack.composition.authorities.storeDir = t.TempDir()
	stack.composition.authorities.sandboxHostAuthorityDir = t.TempDir()

	status, err := stack.runSandboxLifecycle(context.Background(), prepareSandboxRuntime)
	if err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	if got := status.ResolvedBackend; got != "windows" {
		t.Fatalf("ResolvedBackend = %q, want stack runtime status windows", got)
	}

	status, err = stack.runSandboxLifecycle(context.Background(), resetSandboxRuntime)
	if err != nil {
		t.Fatalf("ResetSandbox() error = %v", err)
	}
	if got := status.ResolvedBackend; got != "windows" {
		t.Fatalf("ResolvedBackend after reset = %q, want stack runtime status windows", got)
	}
	if runtime.closeCalls != 0 {
		t.Fatalf("Close() calls = %d, want 0 for current runtime", runtime.closeCalls)
	}
}

func TestSandboxLifecycleTemporaryRuntimeWithoutCapabilityReturnsTemporaryStatus(t *testing.T) {
	current := newSandboxLifecycleTestRuntime("", sandbox.BackendHost)
	temp := newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendCustom)
	stack := sandboxLifecycleTestStack(current, "windows")
	stack.commandBackend.sandboxLifecycleFactory = func(cfg sandbox.Config, _ sandbox.Runtime) (sandbox.LifecycleTarget, error) {
		cfg.RequestedBackend = sandbox.BackendWindows
		return sandbox.LifecycleTarget{Runtime: temp, Config: cfg}, nil
	}

	status, err := stack.runSandboxLifecycle(context.Background(), prepareSandboxRuntime)
	if err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	if got := status.ResolvedBackend; got != string(sandbox.BackendCustom) {
		t.Fatalf("ResolvedBackend = %q, want temporary runtime status custom", got)
	}
	if temp.closeCalls != 1 {
		t.Fatalf("temporary Close() calls = %d, want 1", temp.closeCalls)
	}
}

func TestSandboxLifecyclePropagatesActionError(t *testing.T) {
	wantErr := errors.New("prepare failed")
	runtime := &sandboxLifecyclePrepareRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
		prepareErr:                  wantErr,
	}
	stack := sandboxLifecycleTestStack(runtime, "windows")

	status, err := stack.runSandboxLifecycle(context.Background(), prepareSandboxRuntime)
	if !errors.Is(err, wantErr) {
		t.Fatalf("PrepareSandbox() error = %v, want %v", err, wantErr)
	}
	if got := status.ResolvedBackend; got != "windows" {
		t.Fatalf("ResolvedBackend = %q, want windows even on action error", got)
	}
}

func TestSandboxRefreshFailureLogsRawErrorAndKeepsUnknownOutcome(t *testing.T) {
	refreshErr := errors.New("impl/sandbox/windows: refresh sandbox: Access is denied")
	closeErr := errors.New("impl/sandbox/windows: close sandbox: ACL handle still held")
	runtime := &sandboxLifecycleRefreshRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
		refreshErr:                  refreshErr,
	}
	runtime.closeErr = closeErr
	stack := sandboxLifecycleTestStack(newSandboxLifecycleTestRuntime("", sandbox.BackendHost), "windows")
	logger, logs := sandboxLifecycleTestDiagnostics()
	stack.composition.authorities.diagnostics = logger
	stack.commandBackend.sandboxLifecycleFactory = func(cfg sandbox.Config, _ sandbox.Runtime) (sandbox.LifecycleTarget, error) {
		return sandbox.LifecycleTarget{Runtime: runtime, Config: cfg}, nil
	}

	result, err := stack.commandBackend.executeConfigurationCommand(context.Background(), appserver.ActionSandboxRefresh, appserver.SandboxRequest{})
	if !errors.Is(err, refreshErr) || !errors.Is(err, closeErr) {
		t.Fatalf("RefreshSandbox() error = %v, want refresh and close errors", err)
	}
	var outcomeErr *appserver.OutcomeError
	if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != appserver.OutcomeUnknown || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("RefreshSandbox() = %#v, %v, want unknown effect outcome", result, err)
	}
	if runtime.refreshCalls != 1 {
		t.Fatalf("Refresh() calls = %d, want 1", runtime.refreshCalls)
	}
	if runtime.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1 for temporary runtime", runtime.closeCalls)
	}
	got := logs.String()
	for _, want := range []string{
		`"msg":"Windows sandbox refresh failed"`,
		`"component":"sandbox"`,
		`"operation":"refresh"`,
		refreshErr.Error(),
		closeErr.Error(),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostics = %q, want %s", got, want)
		}
	}
}

func TestSandboxRefreshCanceledDoesNotLogUnlessCloseFails(t *testing.T) {
	runtime := &sandboxLifecycleRefreshRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
		refreshErr:                  context.Canceled,
	}
	stack := sandboxLifecycleTestStack(runtime, "windows")
	logger, logs := sandboxLifecycleTestDiagnostics()
	stack.composition.authorities.diagnostics = logger

	_, _, _, err := stack.commandBackend.runSandboxLifecycleCommand(context.Background(), sandboxLifecycleCommand{refresh: true}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh canceled error = %v", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("canceled refresh diagnostics = %q, want empty", logs.String())
	}

	aclErr := errors.New("ACL update denied")
	runtime.refreshErr = errors.Join(aclErr, context.Canceled)
	_, _, _, err = stack.commandBackend.runSandboxLifecycleCommand(context.Background(), sandboxLifecycleCommand{refresh: true}, nil)
	if !errors.Is(err, aclErr) || !strings.Contains(logs.String(), aclErr.Error()) {
		t.Fatalf("joined cancellation lost ACL failure: err=%v, logs=%q", err, logs.String())
	}
	logs.Reset()

	closeErr := errors.New("impl/sandbox/windows: close sandbox: Access is denied")
	temp := &sandboxLifecycleRefreshRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
		refreshErr:                  context.Canceled,
	}
	temp.closeErr = closeErr
	stack.commandBackend.sandboxLifecycleFactory = func(cfg sandbox.Config, _ sandbox.Runtime) (sandbox.LifecycleTarget, error) {
		return sandbox.LifecycleTarget{Runtime: temp, Config: cfg}, nil
	}
	stack.composition.exec = newSandboxLifecycleTestRuntime("", sandbox.BackendHost)
	_, _, _, err = stack.commandBackend.runSandboxLifecycleCommand(context.Background(), sandboxLifecycleCommand{refresh: true}, nil)
	if !errors.Is(err, closeErr) {
		t.Fatalf("canceled refresh with close error = %v, want %v", err, closeErr)
	}
	if !strings.Contains(logs.String(), closeErr.Error()) || strings.Contains(logs.String(), "context canceled") {
		t.Fatalf("diagnostics = %q, want raw close error without canceled refresh", logs.String())
	}
}

func TestSandboxRefreshNilDiagnosticsDoesNotPanic(t *testing.T) {
	runtime := &sandboxLifecycleRefreshRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
		refreshErr:                  errors.New("refresh failed"),
	}
	stack := sandboxLifecycleTestStack(runtime, "windows")
	_, _, _, err := stack.commandBackend.runSandboxLifecycleCommand(context.Background(), sandboxLifecycleCommand{refresh: true}, nil)
	if err == nil {
		t.Fatal("Refresh() error = nil, want refresh failure")
	}
}

func TestSandboxExplicitLifecycleFailuresStayErrorsWithoutRefreshLog(t *testing.T) {
	prepareErr := errors.New("prepare failed")
	repairErr := errors.New("repair failed")
	resetErr := errors.New("reset failed")
	tests := []struct {
		name    string
		action  appserver.Action
		runtime sandbox.Runtime
		wantErr error
	}{
		{
			name:   "prepare",
			action: appserver.ActionSandboxPrepare,
			runtime: &sandboxLifecyclePrepareRuntime{
				sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
				prepareErr:                  prepareErr,
			},
			wantErr: prepareErr,
		},
		{
			name:   "repair",
			action: appserver.ActionSandboxRepair,
			runtime: &sandboxLifecycleRepairRuntime{
				sandboxLifecyclePrepareRuntime: &sandboxLifecyclePrepareRuntime{
					sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
				},
				repairErr: repairErr,
			},
			wantErr: repairErr,
		},
		{
			name:   "reset",
			action: appserver.ActionSandboxReset,
			runtime: &sandboxLifecycleResetRuntime{
				sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
				resetErr:                    resetErr,
			},
			wantErr: resetErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stack := sandboxLifecycleTestStack(tt.runtime, "windows")
			logger, logs := sandboxLifecycleTestDiagnostics()
			stack.composition.authorities.diagnostics = logger
			result, err := stack.commandBackend.executeConfigurationCommand(context.Background(), tt.action, appserver.SandboxRequest{})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("%s error = %v, want %v", tt.name, err, tt.wantErr)
			}
			var outcomeErr *appserver.OutcomeError
			if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != appserver.OutcomeUnknown || result.Outcome != appserver.OutcomeCommitted {
				t.Fatalf("%s = %#v, %v, want unknown effect outcome", tt.name, result, err)
			}
			if strings.Contains(logs.String(), "Windows sandbox refresh failed") {
				t.Fatalf("%s wrote refresh diagnostics = %q", tt.name, logs.String())
			}
		})
	}
}

func TestCloseWorkspaceResourcesRetainsFailedRuntimeForRetry(t *testing.T) {
	closeErr := errors.New("close failed")
	runtime := newSandboxLifecycleTestRuntime(sandbox.BackendHost, sandbox.BackendHost)
	runtime.closeErr = closeErr
	stack := sandboxLifecycleTestStack(runtime, "host")

	if err := stack.composition.closeWorkspaceResources(); !errors.Is(err, closeErr) {
		t.Fatalf("first closeWorkspaceResources() error = %v, want %v", err, closeErr)
	}
	if stack.composition.exec != runtime {
		t.Fatal("failed sandbox Runtime close discarded the retryable resource owner")
	}

	runtime.closeErr = nil
	if err := stack.composition.closeWorkspaceResources(); err != nil {
		t.Fatalf("retry closeWorkspaceResources() error = %v", err)
	}
	if stack.composition.exec != nil {
		t.Fatal("successful sandbox Runtime close retained the resource")
	}
	if runtime.closeCalls != 2 {
		t.Fatalf("Close() calls = %d, want 2", runtime.closeCalls)
	}
}

func sandboxLifecycleTestStack(runtime sandbox.Runtime, requestedBackend string) *Stack {
	configured := SandboxConfig{RequestedType: requestedBackend}
	stack := &Stack{
		composition: runtimeComposition{
			authorities: runtimeHostAuthorities{storeDir: "/store"},
			workspace:   session.WorkspaceRef{CWD: "/workspace"}, sandbox: configured,
			process: &runtimeProcessState{sandboxPersisted: cloneSandboxConfig(configured)}, exec: runtime,
		},
	}
	stack.commandBackend = &controlCommandBackend{composition: &stack.composition}
	return stack
}

type sandboxLifecycleTestRuntime struct {
	status     sandbox.Status
	selection  sandbox.Status
	closeCalls int
	closeErr   error
}

func newSandboxLifecycleTestRuntime(requested sandbox.Backend, resolved sandbox.Backend) *sandboxLifecycleTestRuntime {
	status := sandbox.Status{
		RequestedBackend: requested,
		ResolvedBackend:  resolved,
	}
	return &sandboxLifecycleTestRuntime{
		status:    status,
		selection: status,
	}
}

func (r *sandboxLifecycleTestRuntime) Describe() sandbox.Descriptor {
	return sandbox.Descriptor{}
}

func (r *sandboxLifecycleTestRuntime) FileSystem() sandbox.FileSystem {
	return nil
}

func (r *sandboxLifecycleTestRuntime) FileSystemFor(sandbox.Constraints) sandbox.FileSystem {
	return nil
}

func (r *sandboxLifecycleTestRuntime) Run(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
	return sandbox.CommandResult{}, nil
}

func (r *sandboxLifecycleTestRuntime) Start(context.Context, sandbox.CommandRequest) (sandbox.Session, error) {
	return nil, nil
}

func (r *sandboxLifecycleTestRuntime) OpenSession(string) (sandbox.Session, error) {
	return nil, nil
}

func (r *sandboxLifecycleTestRuntime) OpenSessionRef(sandbox.SessionRef) (sandbox.Session, error) {
	return nil, nil
}

func (r *sandboxLifecycleTestRuntime) SupportedBackends() []sandbox.Backend {
	return nil
}

func (r *sandboxLifecycleTestRuntime) Status() sandbox.Status {
	return sandbox.CloneStatus(r.status)
}

func (r *sandboxLifecycleTestRuntime) SelectionStatus() sandbox.Status {
	return sandbox.CloneStatus(r.selection)
}

func (r *sandboxLifecycleTestRuntime) Close() error {
	r.closeCalls++
	return r.closeErr
}

type sandboxLifecyclePrepareRuntime struct {
	*sandboxLifecycleTestRuntime
	prepareCalls int
	prepareErr   error
}

func (r *sandboxLifecyclePrepareRuntime) Prepare(context.Context) error {
	r.prepareCalls++
	return r.prepareErr
}

type sandboxLifecycleRepairRuntime struct {
	*sandboxLifecyclePrepareRuntime
	repairCalls int
	repairErr   error
}

func (r *sandboxLifecycleRepairRuntime) Repair(context.Context) error {
	r.repairCalls++
	return r.repairErr
}

type sandboxLifecycleResetRuntime struct {
	*sandboxLifecycleTestRuntime
	resetCalls int
	resetErr   error
}

func (r *sandboxLifecycleResetRuntime) Reset(context.Context) error {
	r.resetCalls++
	return r.resetErr
}

type sandboxLifecycleRefreshRuntime struct {
	*sandboxLifecycleTestRuntime
	refreshCalls int
	refreshErr   error
}

func (r *sandboxLifecycleRefreshRuntime) Refresh(context.Context) error {
	r.refreshCalls++
	return r.refreshErr
}

func sandboxLifecycleTestDiagnostics() (*slog.Logger, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(logs, nil)), logs
}
