package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCloneRequestIsolatesMutableFields(t *testing.T) {
	t.Parallel()

	req := CommandRequest{
		Command: "  echo ok  ",
		Dir:     " /tmp/project ",
		Env: map[string]string{
			"FOO": "bar",
		},
		Stdin:      []byte("hello"),
		Permission: PermissionWorkspaceWrite,
		RouteHint:  RouteSandbox,
	}

	cloned := CloneRequest(req)
	cloned.Env["FOO"] = "mutated"
	cloned.Stdin[0] = 'H'

	if got := cloned.Command; got != "echo ok" {
		t.Fatalf("cloned.Command = %q, want %q", got, "echo ok")
	}
	if got := cloned.Dir; got != "/tmp/project" {
		t.Fatalf("cloned.Dir = %q, want %q", got, "/tmp/project")
	}
	if got := req.Env["FOO"]; got != "bar" {
		t.Fatalf("req.Env[FOO] = %q, want %q", got, "bar")
	}
	if got := string(req.Stdin); got != "hello" {
		t.Fatalf("req.Stdin = %q, want %q", got, "hello")
	}
}

func TestSelectionStatusUsesLightweightProvider(t *testing.T) {
	runtime := &countingSelectionRuntime{
		fakeRuntime: fakeRuntime{
			backend: BackendCustom,
			status: Status{
				RequestedBackend: BackendCustom,
				ResolvedBackend:  BackendCustom,
			},
		},
		selection: Status{
			RequestedBackend: "",
			ResolvedBackend:  BackendWindows,
		},
	}

	got := SelectionStatus(runtime)
	if runtime.statusCalls != 0 {
		t.Fatalf("Status() calls = %d, want 0", runtime.statusCalls)
	}
	if got.ResolvedBackend != BackendWindows {
		t.Fatalf("SelectionStatus().ResolvedBackend = %q, want %q", got.ResolvedBackend, BackendWindows)
	}
}

func TestLifecycleTargetForUsesCurrentMatchingRuntime(t *testing.T) {
	current := &refreshRuntime{
		fakeRuntime: fakeRuntime{
			backend: BackendCustom,
			status: Status{
				RequestedBackend: "lifecycle-current",
				ResolvedBackend:  "lifecycle-current",
			},
		},
	}

	target, err := LifecycleTargetFor(Config{RequestedBackend: "lifecycle-current"}, current)
	if err != nil {
		t.Fatalf("LifecycleTargetFor() error = %v", err)
	}
	if !target.Current || target.Runtime != current || target.NoOp {
		t.Fatalf("target = %#v, want current runtime", target)
	}
}

func TestLifecycleTargetForBuildsFactoryWhenCurrentLacksLifecycle(t *testing.T) {
	backend := Backend("lifecycle-current-no-capability")
	current := &fakeRuntime{
		backend: backend,
		status: Status{
			RequestedBackend: backend,
			ResolvedBackend:  backend,
		},
	}
	runtime := &refreshRuntime{fakeRuntime: fakeRuntime{backend: backend}}
	factory := &fakeLifecycleFactory{backend: backend, runtime: runtime}
	lifecycleFactoriesMu.Lock()
	original := maps.Clone(lifecycleFactories)
	delete(lifecycleFactories, backend)
	lifecycleFactoriesMu.Unlock()
	t.Cleanup(func() {
		lifecycleFactoriesMu.Lock()
		lifecycleFactories = original
		lifecycleFactoriesMu.Unlock()
	})

	if err := RegisterLifecycleFactory(factory); err != nil {
		t.Fatalf("RegisterLifecycleFactory() error = %v", err)
	}

	target, err := LifecycleTargetFor(Config{RequestedBackend: backend}, current)
	if err != nil {
		t.Fatalf("LifecycleTargetFor() error = %v", err)
	}
	if target.Current || target.NoOp || target.Runtime != runtime {
		t.Fatalf("target = %#v, want registered lifecycle runtime", target)
	}
}

func TestLifecycleTargetForBuildsRegisteredFactory(t *testing.T) {
	backend := Backend("lifecycle-factory-test")
	runtime := &fakeRuntime{backend: backend}
	factory := &fakeLifecycleFactory{backend: backend, runtime: runtime}
	lifecycleFactoriesMu.Lock()
	original := maps.Clone(lifecycleFactories)
	delete(lifecycleFactories, backend)
	lifecycleFactoriesMu.Unlock()
	t.Cleanup(func() {
		lifecycleFactoriesMu.Lock()
		lifecycleFactories = original
		lifecycleFactoriesMu.Unlock()
	})

	if err := RegisterLifecycleFactory(factory); err != nil {
		t.Fatalf("RegisterLifecycleFactory() error = %v", err)
	}

	target, err := LifecycleTargetFor(Config{RequestedBackend: backend, CWD: " /tmp/work "}, &fakeRuntime{backend: BackendHost})
	if err != nil {
		t.Fatalf("LifecycleTargetFor() error = %v", err)
	}
	if target.Current || target.NoOp || target.Runtime != runtime {
		t.Fatalf("target = %#v, want registered temporary runtime", target)
	}
	if factory.last.RequestedBackend != backend || !strings.HasSuffix(filepath.ToSlash(factory.last.CWD), "/tmp/work") {
		t.Fatalf("factory config = %#v", factory.last)
	}
}

func TestLifecycleTargetForHostNoops(t *testing.T) {
	target, err := LifecycleTargetFor(Config{RequestedBackend: BackendHost}, &fakeRuntime{backend: BackendHost})
	if err != nil {
		t.Fatalf("LifecycleTargetFor() error = %v", err)
	}
	if !target.NoOp || target.Runtime != nil {
		t.Fatalf("target = %#v, want host noop", target)
	}
}

func TestFuncRunnerClonesRequestBeforeInvoke(t *testing.T) {
	t.Parallel()

	runner := FuncRunner(func(_ context.Context, req CommandRequest) (CommandResult, error) {
		req.Env["FOO"] = "mutated"
		req.Stdin[0] = 'H'
		return CommandResult{
			Stdout:   "ok\n",
			ExitCode: 0,
			Route:    RouteSandbox,
			Backend:  "seatbelt",
		}, nil
	})

	req := CommandRequest{
		Command: "echo ok",
		Env: map[string]string{
			"FOO": "bar",
		},
		Stdin: []byte("hello"),
	}

	result, err := runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := result.Backend; got != "seatbelt" {
		t.Fatalf("result.Backend = %q, want %q", got, "seatbelt")
	}
	if got := req.Env["FOO"]; got != "bar" {
		t.Fatalf("req.Env[FOO] = %q, want %q", got, "bar")
	}
	if got := string(req.Stdin); got != "hello" {
		t.Fatalf("req.Stdin = %q, want %q", got, "hello")
	}
}

func TestNormalizeSandboxPermissionFailurePreservesCommandDiagnostics(t *testing.T) {
	t.Parallel()

	deniedPath := "/sandbox-denied-home/.gitconfig"
	raw := "错误：无法锁定配置文件 " + deniedPath + ": 只读文件系统"
	result, err := NormalizeSandboxPermissionFailure(CommandResult{
		Stderr:   raw,
		ExitCode: 1,
		Route:    RouteSandbox,
		Backend:  BackendBwrap,
	}, errors.New("tool: bwrap sandbox command failed: exit status 255; stderr="+raw))
	if err == nil {
		t.Fatal("NormalizeSandboxPermissionFailure() error = nil, want command error")
	}
	if !strings.Contains(result.Stderr, deniedPath) || !strings.Contains(err.Error(), deniedPath) {
		t.Fatalf("normalized output lost command diagnostics: stderr=%q err=%q", result.Stderr, err.Error())
	}
	if result.Stderr != raw {
		t.Fatalf("stderr = %q, want raw command stderr %q", result.Stderr, raw)
	}
}

func TestCloneSessionStatusNormalizesSessionRef(t *testing.T) {
	t.Parallel()

	status := CloneSessionStatus(SessionStatus{
		SessionRef: SessionRef{
			Backend:   " sandbox ",
			SessionID: " sess-1 ",
		},
		Running:   true,
		ExitCode:  -1,
		StartedAt: time.Unix(10, 0),
		UpdatedAt: time.Unix(20, 0),
	})

	if got := status.Backend; got != "sandbox" {
		t.Fatalf("status.Backend = %q, want %q", got, "sandbox")
	}
	if got := status.SessionID; got != "sess-1" {
		t.Fatalf("status.SessionID = %q, want %q", got, "sess-1")
	}
	if !status.Running {
		t.Fatal("status.Running = false, want true")
	}
}

func TestValidateOutputCursorNormalizesNegativeAndRejectsAhead(t *testing.T) {
	if err := ValidateOutputCursor(
		OutputCursor{Stdout: -1, Stderr: -2},
		OutputCursor{},
	); err != nil {
		t.Fatalf("ValidateOutputCursor(negative) error = %v", err)
	}

	err := ValidateOutputCursor(
		OutputCursor{Stdout: 4, Stderr: 8},
		OutputCursor{Stdout: 4, Stderr: 7},
	)
	var ahead *OutputCursorAheadError
	if !errors.As(err, &ahead) {
		t.Fatalf("ValidateOutputCursor() error = %v, want OutputCursorAheadError", err)
	}
	if ahead.Stream != "stderr" || ahead.Requested != 8 || ahead.Available != 7 {
		t.Fatalf("cursor error = %+v", ahead)
	}
}

func TestOutputReadWindowDetectsRetainedGap(t *testing.T) {
	t.Parallel()

	start, gap := OutputReadWindow(10, []byte("tail"), 30)
	if start != 26 || !gap {
		t.Fatalf("OutputReadWindow(gap) = %d/%v, want 26/true", start, gap)
	}
	start, gap = OutputReadWindow(26, []byte("tail"), 30)
	if start != 26 || gap {
		t.Fatalf("OutputReadWindow(contiguous) = %d/%v, want 26/false", start, gap)
	}
}

func TestEffectiveConstraintsMergesLegacyFields(t *testing.T) {
	t.Parallel()

	req := CommandRequest{
		Permission: PermissionWorkspaceWrite,
		RouteHint:  RouteSandbox,
		Backend:    BackendSeatbelt,
		Constraints: Constraints{
			Network: NetworkDisabled,
			PathRules: []PathRule{
				{Path: " /workspace ", Access: PathAccessReadWrite},
			},
		},
	}

	got := EffectiveConstraints(req)
	if got.Permission != PermissionWorkspaceWrite {
		t.Fatalf("Permission = %q, want %q", got.Permission, PermissionWorkspaceWrite)
	}
	if got.Route != RouteSandbox {
		t.Fatalf("Route = %q, want %q", got.Route, RouteSandbox)
	}
	if got.Backend != BackendSeatbelt {
		t.Fatalf("Backend = %q, want %q", got.Backend, BackendSeatbelt)
	}
	if got.Network != NetworkDisabled {
		t.Fatalf("Network = %q, want %q", got.Network, NetworkDisabled)
	}
	if len(got.PathRules) != 1 || got.PathRules[0].Path != "/workspace" {
		t.Fatalf("PathRules = %+v, want normalized workspace rule", got.PathRules)
	}
}

func TestCompositeRuntimeFileSystemForPreservesConstraints(t *testing.T) {
	t.Parallel()

	hostFS := sentinelFileSystem{name: "host"}
	defaultSandboxFS := sentinelFileSystem{name: "sandbox-default"}
	constrainedSandboxFS := sentinelFileSystem{name: "sandbox-constrained"}
	hostRuntime := fakeRuntime{backend: BackendHost, fs: hostFS}
	sandboxRuntime := fakeRuntime{
		backend: BackendBwrap,
		fs:      defaultSandboxFS,
		fsFor: func(constraints Constraints) FileSystem {
			if len(constraints.PathRules) > 0 {
				return constrainedSandboxFS
			}
			return defaultSandboxFS
		},
	}
	rt := &compositeRuntime{
		host:    hostRuntime,
		sandbox: sandboxRuntime,
		backends: map[Backend]Runtime{
			BackendHost:  hostRuntime,
			BackendBwrap: sandboxRuntime,
		},
	}

	got := rt.FileSystemFor(Constraints{
		Route: RouteSandbox,
		PathRules: []PathRule{
			{Path: "/workspace", Access: PathAccessReadWrite},
		},
	})
	if got != constrainedSandboxFS {
		t.Fatalf("FileSystemFor() = %#v, want constrained sandbox filesystem", got)
	}
}

func TestCompositeRuntimeRejectsImplicitHostFallbackForSandboxRun(t *testing.T) {
	t.Parallel()

	hostRuntime := &fakeRuntime{backend: BackendHost}
	rt := &compositeRuntime{
		host: hostRuntime,
		backends: map[Backend]Runtime{
			BackendHost: hostRuntime,
		},
		status: Status{
			RequestedBackend: "",
			ResolvedBackend:  BackendHost,
			FallbackToHost:   true,
		},
	}

	result, err := rt.Run(context.Background(), CommandRequest{
		Command: "echo ok",
		Constraints: Constraints{
			Route:      RouteSandbox,
			Permission: PermissionWorkspaceWrite,
		},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want host approval error")
	}
	if !strings.Contains(err.Error(), HostExecutionRequiresApprovalMessage) {
		t.Fatalf("Run() error = %v, want host approval message", err)
	}
	if result.Route != RouteHost || result.Backend != BackendHost || result.Error != HostExecutionRequiresApprovalMessage {
		t.Fatalf("Run() result = %+v, want host approval result", result)
	}
}

func TestCompositeRuntimeRejectsImplicitHostFallbackForSandboxStart(t *testing.T) {
	t.Parallel()

	hostRuntime := &fakeRuntime{backend: BackendHost}
	rt := &compositeRuntime{
		host: hostRuntime,
		backends: map[Backend]Runtime{
			BackendHost: hostRuntime,
		},
		status: Status{
			RequestedBackend: "",
			ResolvedBackend:  BackendHost,
			FallbackToHost:   true,
		},
	}

	if _, err := rt.Start(context.Background(), CommandRequest{
		Command: "sleep 1",
		Constraints: Constraints{
			Route:      RouteSandbox,
			Permission: PermissionWorkspaceWrite,
		},
	}); err == nil || !strings.Contains(err.Error(), HostExecutionRequiresApprovalMessage) {
		t.Fatalf("Start() error = %v, want host approval error", err)
	}
}

func TestCompositeRuntimeRejectsImplicitHostFallbackWithNonComparableRuntime(t *testing.T) {
	t.Parallel()

	hostRuntime := fakeRuntime{backend: BackendHost}
	rt := &compositeRuntime{
		host: hostRuntime,
		backends: map[Backend]Runtime{
			BackendHost: hostRuntime,
		},
		status: Status{
			ResolvedBackend: BackendHost,
			FallbackToHost:  true,
		},
	}
	req := CommandRequest{
		Command: "echo ok",
		Constraints: Constraints{
			Route:      RouteSandbox,
			Permission: PermissionWorkspaceWrite,
		},
	}

	if _, err := rt.Run(context.Background(), req); err == nil || !strings.Contains(err.Error(), HostExecutionRequiresApprovalMessage) {
		t.Fatalf("Run() error = %v, want host approval error", err)
	}
	if _, err := rt.Start(context.Background(), req); err == nil || !strings.Contains(err.Error(), HostExecutionRequiresApprovalMessage) {
		t.Fatalf("Start() error = %v, want host approval error", err)
	}
}

func TestCompositeRuntimeAllowsApprovedHostRun(t *testing.T) {
	t.Parallel()

	hostRuntime := &fakeRuntime{backend: BackendHost}
	rt := &compositeRuntime{
		host: hostRuntime,
		backends: map[Backend]Runtime{
			BackendHost: hostRuntime,
		},
		status: Status{
			RequestedBackend: "",
			ResolvedBackend:  BackendHost,
			FallbackToHost:   true,
		},
	}

	result, err := rt.Run(context.Background(), CommandRequest{
		Command: "echo ok",
		Constraints: Constraints{
			Route:      RouteHost,
			Backend:    BackendHost,
			Permission: PermissionFullAccess,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Backend != BackendHost {
		t.Fatalf("Run() backend = %q, want host", result.Backend)
	}
}

func TestCompositeRuntimeAllowsSandboxFallbackForUnavailableRequestedBackend(t *testing.T) {
	t.Parallel()

	hostRuntime := &fakeRuntime{backend: BackendHost}
	sandboxRuntime := &fakeRuntime{backend: BackendBwrap}
	rt := &compositeRuntime{
		host:    hostRuntime,
		sandbox: sandboxRuntime,
		backends: map[Backend]Runtime{
			BackendHost:  hostRuntime,
			BackendBwrap: sandboxRuntime,
		},
		status: Status{
			RequestedBackend: BackendBwrap,
			ResolvedBackend:  BackendBwrap,
		},
	}

	result, err := rt.Run(context.Background(), CommandRequest{
		Command: "echo ok",
		Constraints: Constraints{
			Backend:    Backend("missing-test-backend"),
			Route:      RouteSandbox,
			Permission: PermissionWorkspaceWrite,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Backend != BackendBwrap {
		t.Fatalf("Run() backend = %q, want sandbox fallback backend %q", result.Backend, BackendBwrap)
	}
}

func TestCompositeRuntimeAllowsSandboxStartFallbackForUnavailableRequestedBackend(t *testing.T) {
	t.Parallel()

	hostRuntime := &fakeRuntime{backend: BackendHost}
	sandboxRuntime := &fakeRuntime{backend: BackendBwrap}
	rt := &compositeRuntime{
		host:    hostRuntime,
		sandbox: sandboxRuntime,
		backends: map[Backend]Runtime{
			BackendHost:  hostRuntime,
			BackendBwrap: sandboxRuntime,
		},
		status: Status{
			RequestedBackend: BackendBwrap,
			ResolvedBackend:  BackendBwrap,
		},
	}

	if _, err := rt.Start(context.Background(), CommandRequest{
		Command: "sleep 1",
		Constraints: Constraints{
			Backend:    Backend("missing-test-backend"),
			Route:      RouteSandbox,
			Permission: PermissionWorkspaceWrite,
		},
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestNormalizeConfigTreatsAutoBackendAsUnset(t *testing.T) {
	t.Parallel()

	for _, raw := range []Backend{"", "auto", "default"} {
		cfg := NormalizeConfig(Config{
			RequestedBackend: raw,
		})
		if cfg.RequestedBackend != "" {
			t.Fatalf("NormalizeConfig(%q).RequestedBackend = %q, want empty", raw, cfg.RequestedBackend)
		}
	}

	cfg := NormalizeConfig(Config{
		RequestedBackend: BackendSeatbelt,
	})
	if cfg.RequestedBackend != BackendSeatbelt {
		t.Fatalf("NormalizeConfig(seatbelt).RequestedBackend = %q, want %q", cfg.RequestedBackend, BackendSeatbelt)
	}
}

func TestCanonicalBackendNormalizesAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  Backend
		want Backend
	}{
		{name: "empty", raw: "", want: ""},
		{name: "auto", raw: " auto ", want: ""},
		{name: "default", raw: "DEFAULT", want: ""},
		{name: "host", raw: " HOST ", want: BackendHost},
		{name: "seatbelt", raw: "Seatbelt", want: BackendSeatbelt},
		{name: "bwrap", raw: "BWRAP", want: BackendBwrap},
		{name: "landlock", raw: "landlock", want: BackendLandlock},
		{name: "windows", raw: "windows", want: BackendWindows},
		{name: "windows restricted token dash", raw: "windows-restricted-token", want: BackendWindows},
		{name: "windows restricted token underscore", raw: "windows_restricted_token", want: BackendWindows},
		{name: "windows elevated dash", raw: BackendWindowsElevated, want: BackendWindows},
		{name: "windows elevated underscore", raw: "windows_elevated", want: BackendWindows},
		{name: "windows elevated space", raw: "windows elevated", want: BackendWindows},
		{name: "elevated", raw: "elevated", want: BackendWindows},
		{name: "custom", raw: "Custom", want: BackendCustom},
		{name: "unknown preserved", raw: " VendorBackend ", want: "VendorBackend"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CanonicalBackend(tt.raw); got != tt.want {
				t.Fatalf("CanonicalBackend(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestConstraintsRequestExplicitHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		constraints Constraints
		want        bool
	}{
		{name: "empty", constraints: Constraints{}, want: false},
		{name: "sandbox route", constraints: Constraints{Route: RouteSandbox}, want: false},
		{name: "host route", constraints: Constraints{Route: RouteHost}, want: true},
		{name: "host backend", constraints: Constraints{Backend: BackendHost}, want: true},
		{name: "full access", constraints: Constraints{Permission: PermissionFullAccess}, want: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ConstraintsRequestExplicitHost(tt.constraints); got != tt.want {
				t.Fatalf("ConstraintsRequestExplicitHost() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDescriptorImpliesHostExecution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		desc Descriptor
		want bool
	}{
		{name: "empty", desc: Descriptor{}, want: false},
		{name: "sandbox backend", desc: Descriptor{Backend: BackendBwrap}, want: false},
		{name: "host backend", desc: Descriptor{Backend: BackendHost}, want: true},
		{name: "host route", desc: Descriptor{DefaultConstraints: Constraints{Route: RouteHost}}, want: true},
		{name: "full access", desc: Descriptor{DefaultConstraints: Constraints{Permission: PermissionFullAccess}}, want: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := DescriptorImpliesHostExecution(tt.desc); got != tt.want {
				t.Fatalf("DescriptorImpliesHostExecution() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewPreservesCustomBackendRegistrationKey(t *testing.T) {
	want := Backend("VendorBackend")

	backendFactoriesMu.Lock()
	original := maps.Clone(backendFactories)
	originalOrder := append([]Backend(nil), backendFactoryOrder...)
	backendFactories = map[Backend]BackendFactory{
		BackendHost: fakeBackendFactory{backend: BackendHost},
		want:        fakeBackendFactory{backend: want},
	}
	backendFactoryOrder = []Backend{BackendHost, want}
	backendFactoriesMu.Unlock()
	t.Cleanup(func() {
		backendFactoriesMu.Lock()
		backendFactories = original
		backendFactoryOrder = originalOrder
		backendFactoriesMu.Unlock()
	})

	rt, err := New(Config{RequestedBackend: " VendorBackend "})
	if err != nil {
		t.Fatalf("New(custom mixed-case backend) error = %v", err)
	}
	t.Cleanup(func() {
		_ = rt.Close()
	})

	status := rt.Status()
	if status.ResolvedBackend != want {
		t.Fatalf("Status().ResolvedBackend = %q, want %q", status.ResolvedBackend, want)
	}
}

func TestRegisterBuiltInBackendFactoryRecordsDuplicateWithoutPanic(t *testing.T) {
	backend := Backend("test-duplicate-backend")
	backendFactoriesMu.Lock()
	oldFactory, hadFactory := backendFactories[backend]
	oldOrder := append([]Backend(nil), backendFactoryOrder...)
	oldErrs := append([]error(nil), backendRegistrationErrs...)
	delete(backendFactories, backend)
	backendFactoryOrder = removeBackendForTest(backendFactoryOrder, backend)
	backendRegistrationErrs = nil
	backendFactoriesMu.Unlock()
	t.Cleanup(func() {
		backendFactoriesMu.Lock()
		if hadFactory {
			backendFactories[backend] = oldFactory
		} else {
			delete(backendFactories, backend)
		}
		backendFactoryOrder = oldOrder
		backendRegistrationErrs = oldErrs
		backendFactoriesMu.Unlock()
	})

	if err := RegisterBackendFactory(fakeBackendFactory{backend: backend}); err != nil {
		t.Fatalf("RegisterBackendFactory() error = %v", err)
	}
	RegisterBuiltInBackendFactory(fakeBackendFactory{backend: backend})

	err := backendRegistrationError()
	if err == nil {
		t.Fatal("backendRegistrationError() = nil, want duplicate registration error")
	}
	if !strings.Contains(err.Error(), "duplicated backend factory") {
		t.Fatalf("backendRegistrationError() = %v, want duplicate backend factory", err)
	}
}

func TestNewAutoBackendPrefersSandboxCandidate(t *testing.T) {
	want := Backend("test-auto-backend")

	backendFactoriesMu.Lock()
	original := maps.Clone(backendFactories)
	originalOrder := append([]Backend(nil), backendFactoryOrder...)
	backendFactories = map[Backend]BackendFactory{
		BackendHost: fakeBackendFactory{backend: BackendHost},
		want:        fakeBackendFactory{backend: want},
	}
	backendFactoryOrder = []Backend{BackendHost, want}
	backendFactoriesMu.Unlock()
	t.Cleanup(func() {
		backendFactoriesMu.Lock()
		backendFactories = original
		backendFactoryOrder = originalOrder
		backendFactoriesMu.Unlock()
	})

	rt, err := New(Config{RequestedBackend: "auto", BackendCandidates: []Backend{want}})
	if err != nil {
		t.Fatalf("New(auto) error = %v", err)
	}
	t.Cleanup(func() {
		_ = rt.Close()
	})

	status := rt.Status()
	if status.ResolvedBackend != want {
		t.Fatalf("Status().ResolvedBackend = %q, want %q", status.ResolvedBackend, want)
	}
	if status.FallbackToHost {
		t.Fatalf("Status().FallbackToHost = true, want false for auto backend")
	}
}

func TestNewAutoBackendReportsSkippedSandboxCandidate(t *testing.T) {
	failed := Backend("test-failed-backend")
	resolved := Backend("test-resolved-backend")

	backendFactoriesMu.Lock()
	original := maps.Clone(backendFactories)
	originalOrder := append([]Backend(nil), backendFactoryOrder...)
	backendFactories = map[Backend]BackendFactory{
		BackendHost: fakeBackendFactory{backend: BackendHost},
		failed:      fakeBackendFactory{backend: failed, err: errors.New("probe blocked by AppArmor")},
		resolved:    fakeBackendFactory{backend: resolved},
	}
	backendFactoryOrder = []Backend{BackendHost, failed, resolved}
	backendFactoriesMu.Unlock()
	t.Cleanup(func() {
		backendFactoriesMu.Lock()
		backendFactories = original
		backendFactoryOrder = originalOrder
		backendFactoriesMu.Unlock()
	})

	rt, err := New(Config{RequestedBackend: "auto", BackendCandidates: []Backend{failed, resolved}})
	if err != nil {
		t.Fatalf("New(auto) error = %v", err)
	}
	t.Cleanup(func() {
		_ = rt.Close()
	})

	status := rt.Status()
	if status.ResolvedBackend != resolved {
		t.Fatalf("Status().ResolvedBackend = %q, want %q", status.ResolvedBackend, resolved)
	}
	if status.FallbackToHost {
		t.Fatalf("Status().FallbackToHost = true, want false for sandbox backend fallback")
	}
	for _, want := range []string{string(failed), "probe blocked by AppArmor"} {
		if !strings.Contains(status.FallbackReason, want) {
			t.Fatalf("FallbackReason = %q, want to contain %q", status.FallbackReason, want)
		}
	}
}

func TestNewAutoBackendFallsBackToHostWithInstallHint(t *testing.T) {
	candidates := []Backend{Backend("test-failed-backend")}

	backendFactoriesMu.Lock()
	original := maps.Clone(backendFactories)
	originalOrder := append([]Backend(nil), backendFactoryOrder...)
	backendFactories = map[Backend]BackendFactory{
		BackendHost: fakeBackendFactory{backend: BackendHost},
	}
	for _, candidate := range candidates {
		backendFactories[candidate] = fakeBackendFactory{backend: candidate, err: errors.New("sandbox backend unavailable")}
	}
	backendFactoryOrder = append([]Backend{BackendHost}, candidates...)
	backendFactoriesMu.Unlock()
	t.Cleanup(func() {
		backendFactoriesMu.Lock()
		backendFactories = original
		backendFactoryOrder = originalOrder
		backendFactoriesMu.Unlock()
	})

	rt, err := New(Config{
		RequestedBackend:    "auto",
		BackendCandidates:   candidates,
		FallbackInstallHint: "install test sandbox backend",
	})
	if err != nil {
		t.Fatalf("New(auto) error = %v", err)
	}
	t.Cleanup(func() {
		_ = rt.Close()
	})

	status := rt.Status()
	if !status.FallbackToHost {
		t.Fatal("Status().FallbackToHost = false, want host fallback for unavailable auto backend")
	}
	if status.ResolvedBackend != BackendHost {
		t.Fatalf("Status().ResolvedBackend = %q, want host", status.ResolvedBackend)
	}
	if strings.TrimSpace(status.FallbackInstallHint) == "" {
		t.Fatal("Status().FallbackInstallHint is empty, want install guidance")
	}
	if status.FallbackInstallHint != "install test sandbox backend" {
		t.Fatalf("FallbackInstallHint = %q, want configured hint", status.FallbackInstallHint)
	}
}

func TestCompositeRuntimeStatusForwardsBackendSetupDetails(t *testing.T) {
	rt := &compositeRuntime{
		host: fakeRuntime{backend: BackendHost},
		sandbox: fakeRuntime{
			backend: BackendWindows,
			status: Status{
				ResolvedBackend: BackendWindows,
				Setup: SetupStatus{
					Required: true,
					Checks: []SetupCheck{
						{
							Name:     "workspace",
							Scope:    SetupScopeWorkspace,
							Required: true,
							Reason:   "acl manifest stale",
							Details: map[string]string{
								"workspace": "C:\\ws",
							},
							Counts: map[string]int{
								"write_roots": 2,
							},
						},
					},
				},
			},
		},
		status: Status{
			RequestedBackend: BackendWindows,
			ResolvedBackend:  BackendWindows,
		},
	}
	status := rt.Status()
	if status.RequestedBackend != BackendWindows || status.ResolvedBackend != BackendWindows {
		t.Fatalf("Status backend = %q/%q, want windows", status.RequestedBackend, status.ResolvedBackend)
	}
	workspace, workspaceOK := status.Setup.Check("workspace")
	if !status.Setup.Required || !workspaceOK || workspace.Reason != "acl manifest stale" || workspace.Details["workspace"] != "C:\\ws" || workspace.Counts["write_roots"] != 2 {
		t.Fatalf("Status() = %+v, want forwarded setup diagnostics", status)
	}
}

func TestCompositeRuntimeRefreshForwardsSandbox(t *testing.T) {
	sandboxRuntime := &refreshRuntime{fakeRuntime: fakeRuntime{backend: BackendWindows}}
	rt := &compositeRuntime{
		host:    fakeRuntime{backend: BackendHost},
		sandbox: sandboxRuntime,
		backends: map[Backend]Runtime{
			BackendHost:    fakeRuntime{backend: BackendHost},
			BackendWindows: sandboxRuntime,
		},
	}

	if err := rt.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if sandboxRuntime.calls != 1 {
		t.Fatalf("sandbox Refresh calls = %d, want 1", sandboxRuntime.calls)
	}
}

type fakeBackendFactory struct {
	backend Backend
	err     error
}

func (f fakeBackendFactory) Backend() Backend { return f.backend }

func (f fakeBackendFactory) Build(Config) (Runtime, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &fakeRuntime{backend: f.backend}, nil
}

type fakeLifecycleFactory struct {
	backend Backend
	runtime Runtime
	err     error
	last    Config
}

func (f *fakeLifecycleFactory) Backend() Backend { return f.backend }

func (f *fakeLifecycleFactory) BuildLifecycle(cfg Config) (Runtime, error) {
	f.last = cfg
	if f.err != nil {
		return nil, f.err
	}
	return f.runtime, nil
}

func removeBackendForTest(values []Backend, backend Backend) []Backend {
	out := values[:0]
	for _, value := range values {
		if value != backend {
			out = append(out, value)
		}
	}
	return out
}

type fakeRuntime struct {
	backend Backend
	fs      FileSystem
	fsFor   func(Constraints) FileSystem
	status  Status
}

func (r fakeRuntime) Describe() Descriptor   { return Descriptor{Backend: r.backend} }
func (r fakeRuntime) FileSystem() FileSystem { return r.fs }
func (r fakeRuntime) FileSystemFor(constraints Constraints) FileSystem {
	if r.fsFor != nil {
		return r.fsFor(constraints)
	}
	return r.fs
}
func (r fakeRuntime) Run(context.Context, CommandRequest) (CommandResult, error) {
	return CommandResult{Backend: r.backend}, nil
}
func (r fakeRuntime) Start(context.Context, CommandRequest) (Session, error) { return nil, nil }
func (r fakeRuntime) OpenSession(string) (Session, error)                    { return nil, nil }
func (r fakeRuntime) OpenSessionRef(SessionRef) (Session, error)             { return nil, nil }
func (r fakeRuntime) SupportedBackends() []Backend                           { return []Backend{r.backend} }
func (r fakeRuntime) Status() Status {
	if r.status.RequestedBackend != "" || r.status.ResolvedBackend != "" {
		return r.status
	}
	return Status{RequestedBackend: r.backend, ResolvedBackend: r.backend}
}
func (r fakeRuntime) Close() error { return nil }

type countingSelectionRuntime struct {
	fakeRuntime
	selection   Status
	statusCalls int
}

func (r *countingSelectionRuntime) Status() Status {
	r.statusCalls++
	return r.fakeRuntime.Status()
}

func (r *countingSelectionRuntime) SelectionStatus() Status {
	return r.selection
}

type refreshRuntime struct {
	fakeRuntime
	calls int
}

func (r *refreshRuntime) Refresh(context.Context) error {
	r.calls++
	return nil
}

type sentinelFileSystem struct {
	name string
}

func (f sentinelFileSystem) Getwd() (string, error)        { return f.name, nil }
func (f sentinelFileSystem) UserHomeDir() (string, error)  { return f.name, nil }
func (f sentinelFileSystem) Open(string) (*os.File, error) { return nil, errors.New("not implemented") }
func (f sentinelFileSystem) ReadDir(string) ([]os.DirEntry, error) {
	return nil, errors.New("not implemented")
}
func (f sentinelFileSystem) Stat(string) (os.FileInfo, error) {
	return nil, errors.New("not implemented")
}
func (f sentinelFileSystem) ReadFile(string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (f sentinelFileSystem) WriteFile(string, []byte, os.FileMode) error {
	return errors.New("not implemented")
}
func (f sentinelFileSystem) Glob(string) ([]string, error) { return nil, errors.New("not implemented") }
func (f sentinelFileSystem) WalkDir(string, fs.WalkDirFunc) error {
	return errors.New("not implemented")
}
