package portadapter

import (
	"context"
	"errors"
	"testing"

	coresandbox "github.com/OnslaughtSnail/caelis/core/sandbox"
	portsandbox "github.com/OnslaughtSnail/caelis/ports/sandbox"
)

func TestWrapProjectsStatusAndLifecycleProgress(t *testing.T) {
	fake := &fakePortRuntime{
		descriptor: portsandbox.Descriptor{
			Backend:   portsandbox.BackendWindows,
			Isolation: portsandbox.IsolationProcess,
			DefaultConstraints: portsandbox.Constraints{
				Route:   portsandbox.RouteSandbox,
				Backend: portsandbox.BackendWindows,
			},
		},
		status: portsandbox.Status{
			RequestedBackend: portsandbox.BackendWindows,
			ResolvedBackend:  portsandbox.BackendWindows,
			Setup: portsandbox.SetupStatus{
				Required: true,
				Error:    "workspace ACL setup required",
				Checks: []portsandbox.SetupCheck{{
					Scope:    portsandbox.SetupScopeWorkspace,
					Required: true,
					Reason:   "policy changed",
					Counts:   map[string]int{"write_roots": 2},
				}},
			},
		},
	}
	rt := Wrap(fake)
	status := rt.Status()
	if status.ResolvedBackend != coresandbox.BackendWindows || !status.Setup.Required || len(status.Setup.Checks) != 1 {
		t.Fatalf("Status() = %#v, want windows setup projection", status)
	}
	if status.Setup.Checks[0].Scope != coresandbox.SetupWorkspace || status.Setup.Checks[0].Counts["write_roots"] != 2 {
		t.Fatalf("setup checks = %#v, want workspace check conversion", status.Setup.Checks)
	}

	var progress []coresandbox.PrepareProgress
	ctx := coresandbox.ContextWithPrepareProgress(context.Background(), func(update coresandbox.PrepareProgress) {
		progress = append(progress, update)
	})
	preparer := rt.(coresandbox.PreparableRuntime)
	if err := preparer.Prepare(ctx); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if fake.prepareCalls != 1 {
		t.Fatalf("prepareCalls = %d, want 1", fake.prepareCalls)
	}
	if len(progress) != 1 || progress[0].Message != "repairing workspace policy" || progress[0].Step != 1 {
		t.Fatalf("progress = %#v, want forwarded update", progress)
	}

	preflight := rt.(coresandbox.PreflightRuntime)
	if err := preflight.Preflight(ctx, coresandbox.PreflightOptions{AllowNonElevatedRepair: true}); err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if !fake.preflightAllow {
		t.Fatal("preflight allow flag = false, want true")
	}
}

func TestWrapRepairFallsBackToPrepare(t *testing.T) {
	fake := &fakePrepareOnlyPortRuntime{}
	rt := Wrap(fake)
	repairer := rt.(coresandbox.RepairableRuntime)
	if err := repairer.Repair(context.Background()); err != nil {
		t.Fatalf("Repair() error = %v", err)
	}
	if fake.prepareCalls != 1 {
		t.Fatalf("prepareCalls = %d, want repair fallback to prepare", fake.prepareCalls)
	}
}

type fakePortRuntime struct {
	descriptor     portsandbox.Descriptor
	status         portsandbox.Status
	prepareCalls   int
	repairCalls    int
	preflightAllow bool
	resetCalls     int
}

func (f *fakePortRuntime) Describe() portsandbox.Descriptor {
	return portsandbox.CloneDescriptor(f.descriptor)
}

func (f *fakePortRuntime) FileSystem() portsandbox.FileSystem {
	return nil
}

func (f *fakePortRuntime) FileSystemFor(portsandbox.Constraints) portsandbox.FileSystem {
	return nil
}

func (f *fakePortRuntime) Run(context.Context, portsandbox.CommandRequest) (portsandbox.CommandResult, error) {
	return portsandbox.CommandResult{}, errors.New("not implemented")
}

func (f *fakePortRuntime) Start(context.Context, portsandbox.CommandRequest) (portsandbox.Session, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePortRuntime) OpenSession(string) (portsandbox.Session, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePortRuntime) OpenSessionRef(portsandbox.SessionRef) (portsandbox.Session, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePortRuntime) SupportedBackends() []portsandbox.Backend {
	return []portsandbox.Backend{f.descriptor.Backend}
}

func (f *fakePortRuntime) Status() portsandbox.Status {
	return portsandbox.CloneStatus(f.status)
}

func (f *fakePortRuntime) Close() error {
	return nil
}

func (f *fakePortRuntime) Prepare(ctx context.Context) error {
	f.prepareCalls++
	portsandbox.ReportPrepareProgress(ctx, portsandbox.PrepareProgress{
		Message: "repairing workspace policy",
		Step:    1,
		Total:   2,
	})
	return nil
}

func (f *fakePortRuntime) Repair(context.Context) error {
	f.repairCalls++
	return nil
}

func (f *fakePortRuntime) Preflight(_ context.Context, opts portsandbox.PreflightOptions) error {
	f.preflightAllow = opts.AllowNonElevatedRepair
	return nil
}

func (f *fakePortRuntime) Reset(context.Context) error {
	f.resetCalls++
	return nil
}

type fakePrepareOnlyPortRuntime struct {
	prepareCalls int
}

func (f *fakePrepareOnlyPortRuntime) Describe() portsandbox.Descriptor {
	return portsandbox.Descriptor{Backend: portsandbox.BackendWindows}
}

func (f *fakePrepareOnlyPortRuntime) FileSystem() portsandbox.FileSystem {
	return nil
}

func (f *fakePrepareOnlyPortRuntime) FileSystemFor(portsandbox.Constraints) portsandbox.FileSystem {
	return nil
}

func (f *fakePrepareOnlyPortRuntime) Run(context.Context, portsandbox.CommandRequest) (portsandbox.CommandResult, error) {
	return portsandbox.CommandResult{}, errors.New("not implemented")
}

func (f *fakePrepareOnlyPortRuntime) Start(context.Context, portsandbox.CommandRequest) (portsandbox.Session, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePrepareOnlyPortRuntime) OpenSession(string) (portsandbox.Session, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePrepareOnlyPortRuntime) OpenSessionRef(portsandbox.SessionRef) (portsandbox.Session, error) {
	return nil, errors.New("not implemented")
}

func (f *fakePrepareOnlyPortRuntime) SupportedBackends() []portsandbox.Backend {
	return []portsandbox.Backend{portsandbox.BackendWindows}
}

func (f *fakePrepareOnlyPortRuntime) Status() portsandbox.Status {
	return portsandbox.Status{RequestedBackend: portsandbox.BackendWindows, ResolvedBackend: portsandbox.BackendWindows}
}

func (f *fakePrepareOnlyPortRuntime) Close() error {
	return nil
}

func (f *fakePrepareOnlyPortRuntime) Prepare(context.Context) error {
	f.prepareCalls++
	return nil
}
