package gatewayapp

import (
	"context"
	"errors"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestPrepareSandboxUsesCurrentLifecycleRuntime(t *testing.T) {
	runtime := &sandboxLifecyclePrepareRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
	}
	stack := sandboxLifecycleTestStack(runtime, "windows")

	status, err := stack.PrepareSandbox(context.Background())
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

func TestRepairSandboxFallsBackToPrepare(t *testing.T) {
	runtime := &sandboxLifecyclePrepareRuntime{
		sandboxLifecycleTestRuntime: newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows),
	}
	stack := sandboxLifecycleTestStack(runtime, "windows")

	if _, err := stack.RepairSandbox(context.Background()); err != nil {
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

	if _, err := stack.RepairSandbox(context.Background()); err != nil {
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
	stack.Workspace.CWD = "/workspace"
	stack.storeDir = "/store"

	var factoryCalls int
	stack.sandboxLifecycleFactory = func(cfg sandbox.Config, current sandbox.Runtime) (sandbox.LifecycleTarget, error) {
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
		if current != stack.exec {
			t.Fatalf("factory current runtime = %#v, want stack runtime", current)
		}
		return sandbox.LifecycleTarget{Runtime: temp, Config: cfg}, nil
	}

	status, err := stack.PrepareSandbox(context.Background())
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
	stack.sandboxLifecycleFactory = func(sandbox.Config, sandbox.Runtime) (sandbox.LifecycleTarget, error) {
		factoryCalls++
		return sandbox.LifecycleTarget{NoOp: true}, nil
	}

	status, err := stack.ResetSandbox(context.Background())
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
	stack.sandboxLifecycleFactory = func(sandbox.Config, sandbox.Runtime) (sandbox.LifecycleTarget, error) {
		return sandbox.LifecycleTarget{}, wantErr
	}

	_, err := stack.ResetSandbox(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("ResetSandbox() error = %v, want %v", err, wantErr)
	}
}

func TestSandboxLifecycleCurrentRuntimeWithoutCapabilityNoops(t *testing.T) {
	runtime := newSandboxLifecycleTestRuntime(sandbox.BackendWindows, sandbox.BackendWindows)
	stack := sandboxLifecycleTestStack(runtime, "windows")

	status, err := stack.PrepareSandbox(context.Background())
	if err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	if got := status.ResolvedBackend; got != "windows" {
		t.Fatalf("ResolvedBackend = %q, want stack runtime status windows", got)
	}

	status, err = stack.ResetSandbox(context.Background())
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
	stack.sandboxLifecycleFactory = func(cfg sandbox.Config, _ sandbox.Runtime) (sandbox.LifecycleTarget, error) {
		cfg.RequestedBackend = sandbox.BackendWindows
		return sandbox.LifecycleTarget{Runtime: temp, Config: cfg}, nil
	}

	status, err := stack.PrepareSandbox(context.Background())
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

	status, err := stack.PrepareSandbox(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("PrepareSandbox() error = %v, want %v", err, wantErr)
	}
	if got := status.ResolvedBackend; got != "windows" {
		t.Fatalf("ResolvedBackend = %q, want windows even on action error", got)
	}
}

func sandboxLifecycleTestStack(runtime sandbox.Runtime, requestedBackend string) *Stack {
	return &Stack{
		Workspace: session.WorkspaceRef{CWD: "/workspace"},
		sandbox:   SandboxConfig{RequestedType: requestedBackend},
		exec:      runtime,
		storeDir:  "/store",
	}
}

type sandboxLifecycleTestRuntime struct {
	status     sandbox.Status
	selection  sandbox.Status
	closeCalls int
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
	return nil
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
