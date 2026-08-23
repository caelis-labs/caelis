package gatewayapp

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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
