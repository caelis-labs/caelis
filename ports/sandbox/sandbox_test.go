package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
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
			backend: BackendWindowsElevated,
			status: Status{
				ResolvedBackend: BackendWindowsElevated,
				Setup: SetupStatus{
					Required: true,
					Checks: []SetupCheck{
						{
							Name:     "global",
							Scope:    SetupScopeGlobal,
							Required: true,
							Reason:   "setup marker missing",
							Details: map[string]string{
								"offline_user": "CaelisSandboxOffline",
							},
						},
						{
							Name:  "workspace",
							Scope: SetupScopeWorkspace,
							Counts: map[string]int{
								"read_roots": 5,
							},
						},
					},
				},
			},
		},
		status: Status{
			RequestedBackend: BackendWindowsElevated,
			ResolvedBackend:  BackendWindowsElevated,
		},
	}
	status := rt.Status()
	if status.RequestedBackend != BackendWindowsElevated || status.ResolvedBackend != BackendWindowsElevated {
		t.Fatalf("Status backend = %q/%q, want windows-elevated", status.RequestedBackend, status.ResolvedBackend)
	}
	global, globalOK := status.Setup.Check("global")
	workspace, workspaceOK := status.Setup.Check("workspace")
	if !status.Setup.Required || !globalOK || global.Reason != "setup marker missing" || global.Details["offline_user"] != "CaelisSandboxOffline" || !workspaceOK || workspace.Counts["read_roots"] != 5 {
		t.Fatalf("Status() = %+v, want forwarded setup diagnostics", status)
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
