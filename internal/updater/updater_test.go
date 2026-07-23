package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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
		fmt.Fprint(w, `{"tag_name":"v1.1.0"}`)
	}))
	defer server.Close()
	manager := New(Config{
		StoreDir:       storeDir,
		CurrentVersion: "v1.0.0",
		GitHubAPIURL:   server.URL,
		HTTPClient:     server.Client(),
		Now:            func() time.Time { return now },
	})
	first, err := manager.Check(context.Background(), CheckOptions{Auto: true})
	if err != nil {
		t.Fatalf("first Check() error = %v", err)
	}
	if !first.Available || first.LatestVersion != "v1.1.0" {
		t.Fatalf("first Check() = %#v, want available v1.1.0", first)
	}
	cached := New(Config{
		StoreDir:       storeDir,
		CurrentVersion: "v1.0.0",
		GitHubAPIURL:   "http://127.0.0.1:1/unreachable",
		Now:            func() time.Time { return now.Add(time.Hour) },
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
			default:
				return ""
			}
		},
		LookPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
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
		CommandRun: func(_ context.Context, name string, args []string, _ io.Writer, _ io.Writer) error {
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

func TestWindowsNPMGlobalUpdateDefersInstall(t *testing.T) {
	globalRoot := t.TempDir()
	packageDir := filepath.Join(globalRoot, "@caelis", "caelis")
	var startName string
	var startArgs []string
	var progress []ProgressEvent
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
		CommandRun: func(context.Context, string, []string, io.Writer, io.Writer) error {
			t.Fatal("Windows npm update must be deferred instead of running npm in-process")
			return nil
		},
		CommandStart: func(name string, args []string) error {
			startName = name
			startArgs = append([]string(nil), args...)
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
	wantCommand := []string{"/usr/bin/npm.cmd", "install", "-g", "@caelis/caelis@1.2.0", "--registry=https://registry.npmjs.org"}
	if !result.Deferred || result.Updated || !reflect.DeepEqual(result.Command, wantCommand) {
		t.Fatalf("Update() = %#v, want deferred npm command %#v", result, wantCommand)
	}
	if result.Reason != windowsNPMDetachedReason {
		t.Fatalf("Update().Reason = %q, want %q", result.Reason, windowsNPMDetachedReason)
	}
	wantProgress := []ProgressEvent{
		{Stage: ProgressChecking},
		{Stage: ProgressChecking, Done: true},
		{Stage: ProgressInstalling, Detail: MethodNPM, Deferred: true},
		{Stage: ProgressInstalling, Detail: MethodNPM, Done: true, Deferred: true},
	}
	if !reflect.DeepEqual(progress, wantProgress) {
		t.Fatalf("progress = %#v, want %#v", progress, wantProgress)
	}
	if startName != "cmd.exe" || len(startArgs) != 5 || startArgs[4] == "" {
		t.Fatalf("CommandStart(%q, %#v), want cmd.exe start script", startName, startArgs)
	}
	scriptPath := startArgs[4]
	t.Cleanup(func() { _ = os.Remove(scriptPath) })
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read deferred npm script: %v", err)
	}
	text := string(script)
	for _, want := range []string{
		`tasklist /FI "PID eq `,
		`call "/usr/bin/npm.cmd" "install" "-g" "@caelis/caelis@1.2.0" "--registry=https://registry.npmjs.org"`,
		`del "%~f0"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("deferred npm script = %q, want fragment %q", text, want)
		}
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
		CommandRun: func(context.Context, string, []string, io.Writer, io.Writer) error {
			t.Fatal("Windows npm handoff must not run npm before the native process exits")
			return nil
		},
		CommandStart: func(string, []string) error {
			t.Fatal("foreground launcher handoff must not schedule a detached update")
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
		Version:        1,
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
	lockDir := filepath.Join(storeDir, "updates")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "update.lock"), []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if manager.HintEligible(good) {
		t.Fatal("HintEligible() = true while update lock held, want false")
	}
}

func TestUpdateSkipsWhenAnotherUpdateIsRunning(t *testing.T) {
	storeDir := t.TempDir()
	lockDir := filepath.Join(storeDir, "updates")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "update.lock"), []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	manager := New(Config{
		StoreDir:       storeDir,
		CurrentVersion: "v1.0.0",
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

func TestRawUpdateReportsStructuredInstallProgress(t *testing.T) {
	archive := releaseArchive(t, "caelis", []byte("new-binary"))
	sum := sha256.Sum256(archive)
	server := newUpdaterTestHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprint(w, `{"tag_name":"v1.2.0"}`)
		case "/release/v1.2.0/caelis_1.2.0_linux_amd64.tar.gz":
			w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
			_, _ = w.Write(archive)
		case "/release/v1.2.0/checksums.txt":
			fmt.Fprintf(w, "%s  caelis_1.2.0_linux_amd64.tar.gz\n", hex.EncodeToString(sum[:]))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	exe := filepath.Join(t.TempDir(), "caelis")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	var progress []ProgressEvent
	manager := New(Config{
		StoreDir:          t.TempDir(),
		CurrentVersion:    "v1.0.0",
		Executable:        exe,
		GOOS:              "linux",
		GOARCH:            "amd64",
		GitHubAPIURL:      server.URL + "/latest",
		GitHubReleaseBase: server.URL + "/release",
		HTTPClient:        server.Client(),
	})
	_, err := manager.Update(context.Background(), UpdateOptions{
		Progress: func(event ProgressEvent) {
			progress = append(progress, event)
		},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	archiveName := "caelis_1.2.0_linux_amd64.tar.gz"
	want := []ProgressEvent{
		{Stage: ProgressChecking},
		{Stage: ProgressChecking, Done: true},
		{Stage: ProgressDownloading, Detail: archiveName},
		{Stage: ProgressDownloading, Detail: archiveName, Current: int64(len(archive)), Total: int64(len(archive))},
		{Stage: ProgressDownloading, Detail: archiveName, Current: int64(len(archive)), Total: int64(len(archive)), Done: true},
		{Stage: ProgressVerifying},
		{Stage: ProgressVerifying, Done: true},
		{Stage: ProgressExtracting, Detail: "caelis"},
		{Stage: ProgressExtracting, Detail: "caelis", Done: true},
		{Stage: ProgressInstalling, Detail: "caelis"},
		{Stage: ProgressInstalling, Detail: "caelis", Done: true},
	}
	if !reflect.DeepEqual(progress, want) {
		t.Fatalf("progress = %#v, want %#v", progress, want)
	}
}

func TestRawUpdateDownloadsVerifiesAndReplacesExecutable(t *testing.T) {
	archive := releaseArchive(t, "caelis", []byte("new-binary"))
	sum := sha256.Sum256(archive)
	server := newUpdaterTestHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprint(w, `{"tag_name":"v1.2.0"}`)
		case "/release/v1.2.0/caelis_1.2.0_linux_amd64.tar.gz":
			_, _ = w.Write(archive)
		case "/release/v1.2.0/checksums.txt":
			fmt.Fprintf(w, "%s  caelis_1.2.0_linux_amd64.tar.gz\n", hex.EncodeToString(sum[:]))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	exe := filepath.Join(t.TempDir(), "caelis")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	manager := New(Config{
		StoreDir:          t.TempDir(),
		CurrentVersion:    "v1.0.0",
		Executable:        exe,
		GOOS:              "linux",
		GOARCH:            "amd64",
		GitHubAPIURL:      server.URL + "/latest",
		GitHubReleaseBase: server.URL + "/release",
		HTTPClient:        server.Client(),
	})
	result, err := manager.Update(context.Background(), UpdateOptions{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read executable: %v", err)
	}
	if !result.Updated || string(got) != "new-binary" {
		t.Fatalf("Update() = %#v, executable=%q, want updated binary", result, got)
	}
}

func releaseArchive(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("tar Write() error = %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close() error = %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	return buf.Bytes()
}

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
