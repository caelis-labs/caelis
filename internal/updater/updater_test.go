package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		left  string
		right string
		want  int
	}{
		{left: "v1.2.3", right: "1.2.2", want: 1},
		{left: "1.2.3", right: "v1.2.3", want: 0},
		{left: "1.2.3", right: "1.2.3-beta.1", want: 1},
		{left: "1.2.3-beta.2", right: "1.2.3-beta.1", want: 1},
		{left: "v1.0.0-beta.10", right: "v1.0.0-beta.9", want: 1},
		{left: "v1.0.0+build.1", right: "v1.0.0+build.2", want: 0},
		{left: "1.2.3", right: "1.3.0", want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.left+"_"+tt.right, func(t *testing.T) {
			got := compareVersions(tt.left, tt.right)
			switch {
			case got > 0 && tt.want <= 0:
				t.Fatalf("compareVersions() = %d, want %d", got, tt.want)
			case got < 0 && tt.want >= 0:
				t.Fatalf("compareVersions() = %d, want %d", got, tt.want)
			case got == 0 && tt.want != 0:
				t.Fatalf("compareVersions() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCheckSkipsDevelopmentBuild(t *testing.T) {
	result, err := New(Config{CurrentVersion: "dev", StoreDir: t.TempDir()}).Check(context.Background(), CheckOptions{Force: true})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !result.Skipped || result.InstallMethod != MethodDev || result.Reason == "" {
		t.Fatalf("Check() = %#v, want skipped dev result", result)
	}
}

func TestAutoCheckUsesDailyCache(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	storeDir := t.TempDir()
	server := newUpdaterTestHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v1.1.0\n")
	}))
	defer server.Close()
	manager := New(Config{
		StoreDir:        storeDir,
		CurrentVersion:  "v1.0.0",
		ReleasesBaseURL: server.URL,
		HTTPClient:      server.Client(),
		Now:             func() time.Time { return now },
		Env:             emptyUpdaterEnv,
	})
	first, err := manager.Check(context.Background(), CheckOptions{Auto: true})
	if err != nil {
		t.Fatalf("first Check() error = %v", err)
	}
	if !first.Available || first.LatestVersion != "v1.1.0" {
		t.Fatalf("first Check() = %#v, want available v1.1.0", first)
	}
	cached := New(Config{
		StoreDir:        storeDir,
		CurrentVersion:  "v1.0.0",
		ReleasesBaseURL: "http://127.0.0.1:1/unreachable",
		Now:             func() time.Time { return now.Add(time.Hour) },
		Env:             emptyUpdaterEnv,
	})
	second, err := cached.Check(context.Background(), CheckOptions{Auto: true})
	if err != nil {
		t.Fatalf("cached Check() error = %v", err)
	}
	if second.Checked || !second.Available || second.LatestVersion != "v1.1.0" {
		t.Fatalf("cached Check() = %#v, want cached available result", second)
	}
}

func TestNPMGlobalUpdateRunsNPMInstall(t *testing.T) {
	globalRoot := t.TempDir()
	packageDir := filepath.Join(globalRoot, "@caelis", "caelis")
	platformDir := filepath.Join(globalRoot, "@caelis", "caelis-linux-x64")
	platformBinary := filepath.Join(platformDir, "runtime", "caelis")
	var ran []string
	var progress []ProgressEvent
	manager := New(Config{
		StoreDir:       t.TempDir(),
		CurrentVersion: "v1.0.0",
		GOOS:           "linux",
		Env: func(key string) string {
			switch key {
			case EnvInstallMethod:
				return MethodNPM
			case EnvNPMPackageDir:
				return packageDir
			case EnvNPMPlatformPackageDir:
				return platformDir
			default:
				return ""
			}
		},
		LookPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		CommandOutput: func(_ context.Context, name string, args []string) ([]byte, error) {
			if name == platformBinary {
				return []byte(`{"version":"v1.2.0","build_kind":"release","build_id":"release-build"}`), nil
			}
			switch strings.Join(args, " ") {
			case "root -g":
				return []byte(globalRoot + "\n"), nil
			case "view @caelis/caelis version --registry=https://registry.npmjs.org":
				return []byte("1.2.0\n"), nil
			default:
				t.Fatalf("unexpected CommandOutput %q %#v", name, args)
				return nil, nil
			}
		},
		CommandRun: func(_ context.Context, name string, args []string, env []string, _ io.Writer, _ io.Writer) error {
			if len(env) != 0 {
				t.Fatalf("npm install env = %#v, want inherited environment", env)
			}
			ran = append([]string{name}, args...)
			return nil
		},
	})
	result, err := manager.Update(context.Background(), UpdateOptions{
		Progress: func(event ProgressEvent) {
			progress = append(progress, event)
		},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	want := []string{"/usr/bin/npm", "install", "-g", "@caelis/caelis@1.2.0", "--registry=https://registry.npmjs.org"}
	if !result.Updated || !reflect.DeepEqual(ran, want) {
		t.Fatalf("Update() = %#v, command=%#v, want updated command %#v", result, ran, want)
	}
	wantProgress := []ProgressEvent{
		{Stage: ProgressChecking},
		{Stage: ProgressChecking, Done: true},
		{Stage: ProgressInstalling, Detail: MethodNPM},
		{Stage: ProgressInstalling, Detail: MethodNPM, Done: true},
	}
	if !reflect.DeepEqual(progress, wantProgress) {
		t.Fatalf("progress = %#v, want %#v", progress, wantProgress)
	}
}

func TestNPMGlobalUpdateRejectsUnverifiedInstalledArtifact(t *testing.T) {
	for _, tt := range []struct {
		name        string
		platformDir bool
		installed   string
		want        string
	}{
		{name: "stale artifact", platformDir: true, installed: `{"version":"v1.1.0","build_kind":"release","build_id":"b"}`, want: "expected v1.2.0"},
		{name: "unknown platform package", want: "is not set"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			globalRoot := t.TempDir()
			packageDir := filepath.Join(globalRoot, "@caelis", "caelis")
			platformDir := filepath.Join(globalRoot, "@caelis", "caelis-linux-x64")
			manager := New(Config{
				StoreDir:       t.TempDir(),
				CurrentVersion: "v1.0.0",
				GOOS:           "linux",
				Env: func(key string) string {
					switch key {
					case EnvInstallMethod:
						return MethodNPM
					case EnvNPMPackageDir:
						return packageDir
					case EnvNPMPlatformPackageDir:
						if tt.platformDir {
							return platformDir
						}
						return ""
					default:
						return ""
					}
				},
				LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
				CommandOutput: func(_ context.Context, _ string, args []string) ([]byte, error) {
					switch strings.Join(args, " ") {
					case "root -g":
						return []byte(globalRoot + "\n"), nil
					case "view @caelis/caelis version --registry=https://registry.npmjs.org":
						return []byte("1.2.0\n"), nil
					case "version --format json":
						return []byte(tt.installed), nil
					default:
						t.Fatalf("unexpected CommandOutput args: %#v", args)
						return nil, nil
					}
				},
				CommandRun: func(context.Context, string, []string, []string, io.Writer, io.Writer) error { return nil },
			})
			result, err := manager.Update(context.Background(), UpdateOptions{})
			if err == nil || result.Updated {
				t.Fatalf("Update() = %#v, %v, want npm artifact verification failure", result, err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Update() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestWindowsNPMGlobalUpdateHandsOffToForegroundLauncher(t *testing.T) {
	globalRoot := t.TempDir()
	packageDir := filepath.Join(globalRoot, "@caelis", "caelis")
	handoffDir := filepath.Join(t.TempDir(), "reserved-handoff")
	executable := filepath.Join(packageDir, "runtime", "caelis.exe")
	manager := New(Config{
		StoreDir:       t.TempDir(),
		CurrentVersion: "v1.0.0",
		Executable:     executable,
		GOOS:           "windows",
		Env: func(key string) string {
			switch key {
			case EnvInstallMethod:
				return MethodNPM
			case EnvNPMPackageDir:
				return packageDir
			case EnvNPMUpdateHandoffDir:
				return handoffDir
			default:
				return ""
			}
		},
		LookPath: func(name string) (string, error) {
			return "/usr/bin/" + name + ".cmd", nil
		},
		CommandOutput: func(_ context.Context, _ string, args []string) ([]byte, error) {
			switch strings.Join(args, " ") {
			case "root -g":
				return []byte(globalRoot + "\n"), nil
			case "view @caelis/caelis version --registry=https://registry.npmjs.org":
				return []byte("1.2.0\n"), nil
			default:
				t.Fatalf("unexpected CommandOutput args: %#v", args)
				return nil, nil
			}
		},
		CommandRun: func(context.Context, string, []string, []string, io.Writer, io.Writer) error {
			t.Fatal("Windows npm handoff must not run npm before the native process exits")
			return nil
		},
	})

	result, err := manager.Update(context.Background(), UpdateOptions{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !result.Deferred || !result.Handoff || result.Updated {
		t.Fatalf("Update() = %#v, want foreground handoff", result)
	}
	if info, err := os.Stat(handoffDir); err != nil || !info.IsDir() {
		t.Fatalf("handoff directory was not created lazily: info=%v err=%v", info, err)
	}
	data, err := os.ReadFile(filepath.Join(handoffDir, npmHandoffPlanName))
	if err != nil {
		t.Fatalf("read npm handoff plan: %v", err)
	}
	var plan npmHandoffPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("decode npm handoff plan: %v", err)
	}
	wantCommand := []string{"/usr/bin/npm.cmd", "install", "-g", "@caelis/caelis@1.2.0", "--registry=https://registry.npmjs.org"}
	wantPlan := npmHandoffPlan{
		Version:        2,
		Command:        wantCommand,
		CommandLine:    windowsNPMCommandLine(wantCommand),
		CurrentVersion: "v1.0.0",
		LatestVersion:  "v1.2.0",
		Executable:     executable,
	}
	lockData, err := os.ReadFile(manager.lockPath())
	if err != nil {
		t.Fatalf("read handoff update lock: %v", err)
	}
	if !reflect.DeepEqual(plan, wantPlan) {
		t.Fatalf("handoff plan = %#v, want %#v", plan, wantPlan)
	}
	ownershipData, err := os.ReadFile(filepath.Join(handoffDir, npmHandoffOwnershipName))
	if err != nil {
		t.Fatalf("read npm handoff ownership: %v", err)
	}
	var ownership npmHandoffOwnership
	if err := json.Unmarshal(ownershipData, &ownership); err != nil {
		t.Fatalf("decode npm handoff ownership: %v", err)
	}
	wantOwnership := npmHandoffOwnership{
		Version:   1,
		LockPath:  manager.lockPath(),
		LockToken: strings.TrimSpace(string(lockData)),
	}
	if !reflect.DeepEqual(ownership, wantOwnership) {
		t.Fatalf("handoff ownership = %#v, want %#v", ownership, wantOwnership)
	}
	if _, err := os.Stat(manager.lockPath()); err != nil {
		t.Fatalf("handoff update lock is not held: %v", err)
	}
}

func TestWindowsNPMUpdateWithoutLauncherHandoffIsRejected(t *testing.T) {
	globalRoot := t.TempDir()
	packageDir := filepath.Join(globalRoot, "@caelis", "caelis")
	manager := New(Config{
		StoreDir:       t.TempDir(),
		CurrentVersion: "v1.0.0",
		GOOS:           "windows",
		Env: func(key string) string {
			switch key {
			case EnvInstallMethod:
				return MethodNPM
			case EnvNPMPackageDir:
				return packageDir
			default:
				return ""
			}
		},
		LookPath: func(name string) (string, error) {
			return "/usr/bin/" + name + ".cmd", nil
		},
		CommandOutput: func(_ context.Context, _ string, args []string) ([]byte, error) {
			switch strings.Join(args, " ") {
			case "root -g":
				return []byte(globalRoot + "\n"), nil
			case "view @caelis/caelis version --registry=https://registry.npmjs.org":
				return []byte("1.2.0\n"), nil
			default:
				t.Fatalf("unexpected CommandOutput args: %#v", args)
				return nil, nil
			}
		},
		CommandRun: func(context.Context, string, []string, []string, io.Writer, io.Writer) error {
			t.Fatal("Windows npm update must not run npm in-process")
			return nil
		},
	})
	result, err := manager.Update(context.Background(), UpdateOptions{})
	if err == nil || result.Updated || result.Deferred {
		t.Fatalf("Update() = %#v, %v, want explicit handoff guidance error", result, err)
	}
	for _, want := range []string{"npm launcher handoff", "npm install -g @caelis/caelis@1.2.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Update() error = %v, want %q", err, want)
		}
	}
}

func TestNPMNonGlobalInstallIsSkipped(t *testing.T) {
	globalRoot := t.TempDir()
	localPackageDir := filepath.Join(t.TempDir(), "node_modules", "@caelis", "caelis")
	manager := New(Config{
		StoreDir:       t.TempDir(),
		CurrentVersion: "v1.0.0",
		Env: func(key string) string {
			switch key {
			case EnvInstallMethod:
				return MethodNPM
			case EnvNPMPackageDir:
				return localPackageDir
			default:
				return ""
			}
		},
		LookPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
		CommandOutput: func(_ context.Context, _ string, args []string) ([]byte, error) {
			if strings.Join(args, " ") != "root -g" {
				t.Fatalf("unexpected CommandOutput args: %#v", args)
			}
			return []byte(globalRoot + "\n"), nil
		},
	})
	result, err := manager.Check(context.Background(), CheckOptions{Force: true})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !result.Skipped || !strings.Contains(result.Reason, "not global") {
		t.Fatalf("Check() = %#v, want non-global skip", result)
	}
}

func TestHintEligibleRequiresAvailableUnskippedUnlockedResult(t *testing.T) {
	storeDir := t.TempDir()
	manager := New(Config{
		StoreDir:       storeDir,
		CurrentVersion: "v1.0.0",
		ProcessExists:  func(pid int) bool { return pid == 7 },
	})
	good := Result{LatestVersion: "v1.1.0", Available: true, InstallMethod: MethodRaw}
	if !manager.HintEligible(good) {
		t.Fatal("HintEligible() = false for eligible result, want true")
	}
	if manager.HintEligible(Result{Skipped: true, LatestVersion: "v1.1.0", Available: true}) {
		t.Fatal("HintEligible() = true for skipped result, want false")
	}
	if manager.HintEligible(Result{LatestVersion: "v1.1.0", Available: false}) {
		t.Fatal("HintEligible() = true for unavailable result, want false")
	}
	if manager.HintEligible(Result{Available: true}) {
		t.Fatal("HintEligible() = true for empty latest version, want false")
	}
	writeUpdateLockFile(t, storeDir, updateLockRecord{PID: 7, LockedAt: time.Now().UTC()})
	if manager.HintEligible(good) {
		t.Fatal("HintEligible() = true while update lock held, want false")
	}
}

func TestUpdateSkipsWhenAnotherUpdateIsRunning(t *testing.T) {
	storeDir := t.TempDir()
	writeUpdateLockFile(t, storeDir, updateLockRecord{PID: 7, LockedAt: time.Now().UTC()})
	manager := New(Config{
		StoreDir:       storeDir,
		CurrentVersion: "v1.0.0",
		ProcessExists:  func(pid int) bool { return pid == 7 },
	})
	result, err := manager.Update(context.Background(), UpdateOptions{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !result.Skipped || !strings.Contains(result.Reason, "already running") {
		t.Fatalf("Update() = %#v, want running update skip", result)
	}
}

func TestDeferredUpdateStateKeepsCurrentVersionAvailable(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	manager := New(Config{
		StoreDir:       t.TempDir(),
		CurrentVersion: "v1.0.0",
		Now:            func() time.Time { return now },
	})
	manager.saveUpdateResultState(Result{
		CurrentVersion: "v1.0.0",
		LatestVersion:  "v1.2.0",
		InstallMethod:  MethodRaw,
		Deferred:       true,
	})
	state, ok := manager.loadState()
	if !ok {
		t.Fatal("loadState() = false, want saved state")
	}
	if state.CurrentVersion != "v1.0.0" || state.LatestVersion != "v1.2.0" || !state.Available {
		t.Fatalf("deferred state = %#v, want current v1.0.0 latest v1.2.0 available", state)
	}
}

func emptyUpdaterEnv(string) string { return "" }

type updaterTestHTTPServer struct {
	URL     string
	handler http.Handler
}

func newUpdaterTestHTTPServer(handler http.Handler) *updaterTestHTTPServer {
	return &updaterTestHTTPServer{
		URL:     "http://updater.test",
		handler: handler,
	}
}

func (s *updaterTestHTTPServer) Client() *http.Client {
	return &http.Client{Transport: updaterRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		s.handler.ServeHTTP(recorder, req)
		response := recorder.Result()
		response.Request = req
		return response, nil
	})}
}

func (*updaterTestHTTPServer) Close() {}

type updaterRoundTripFunc func(*http.Request) (*http.Response, error)

func (f updaterRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
