package updater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestRawReleaseSourceConfiguration(t *testing.T) {
	for _, tt := range []struct{ name, config, env, want string }{
		{name: "default", want: "https://releases.caelis.dev"},
		{name: "environment", env: " https://mirror.example/channel/ ", want: "https://mirror.example/channel"},
		{name: "explicit", config: " https://config.example/raw/ ", env: "https://env.example", want: "https://config.example/raw"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manager := New(Config{ReleasesBaseURL: tt.config, Env: func(key string) string {
				if key == EnvReleasesBaseURL {
					return tt.env
				}
				return ""
			}})
			if got := manager.cfg.ReleasesBaseURL; got != tt.want {
				t.Fatalf("base = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInstallerScriptSelection(t *testing.T) {
	for _, tt := range []struct{ goos, script, command string }{
		{goos: "linux", script: "install.sh", command: "bash"},
		{goos: "darwin", script: "install.sh", command: "bash"},
		{goos: "windows", script: "install.ps1", command: "powershell"},
	} {
		t.Run(tt.goos, func(t *testing.T) {
			if got := installerScriptName(tt.goos); got != tt.script {
				t.Fatalf("installerScriptName(%q) = %q, want %q", tt.goos, got, tt.script)
			}
			name, args := installerCommand(tt.goos, "/tmp/script")
			if name != tt.command {
				t.Fatalf("installerCommand(%q) = %q, want %q", tt.goos, name, tt.command)
			}
			if len(args) == 0 || args[len(args)-1] != "/tmp/script" {
				t.Fatalf("installerCommand(%q) args = %#v, want script path last", tt.goos, args)
			}
		})
	}
}

func TestRawCheckValidatesLatestChannel(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
		want       string
	}{
		{name: "release", body: "v1.2.3\r\n", want: "v1.2.3"},
		{name: "prerelease", body: "v1.2.3-rc.1\n", want: "v1.2.3-rc.1"},
		{name: "empty"}, {name: "html", body: "<html>Not Found</html>"},
		{name: "github-json", body: `{"tag_name":"v1.2.3"}`},
		{name: "short", body: "v1.2"}, {name: "no-prefix", body: "1.2.3"},
		{name: "path", body: "v1.2.3/../../other"}, {name: "multiple", body: "v1.2.3\nv1.2.4"},
		{name: "oversize", body: "v1.2.3" + strings.Repeat(" ", 1024)},
		{name: "unavailable", body: "v1.2.3", status: http.StatusServiceUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newUpdaterTestHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.String() != "http://updater.test/channel/latest.txt" {
					t.Errorf("unexpected request: %s", r.URL)
				}
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				fmt.Fprint(w, tt.body)
			}))
			manager := New(Config{CurrentVersion: "v1.0.0", StoreDir: t.TempDir(), HTTPClient: server.Client(), Env: func(key string) string {
				if key == EnvReleasesBaseURL {
					return server.URL + "/channel/"
				}
				return ""
			}})
			result, err := manager.Check(context.Background(), CheckOptions{Force: true})
			if tt.want == "" {
				if err == nil || result.Checked || result.Available {
					t.Fatalf("invalid channel accepted: %#v, %v", result, err)
				}
				return
			}
			if err != nil || result.LatestVersion != tt.want || !result.Available {
				t.Fatalf("Check = %#v, %v", result, err)
			}
		})
	}
}

// rawUpdateFixture wires a manager whose raw install reaches only injected
// collaborators: a fake release channel, a captured process runner, and a
// scripted version probe. No real installer, download, or process runs.
type rawUpdateFixture struct {
	manager        *Manager
	exe            string
	installDir     string
	latestBody     string
	scriptBody     string
	installedBody  string
	installErr     error
	lastScript     string
	lastCommand    string
	lastArgs       []string
	lastEnv        []string
	versionQueries []string
}

func newRawUpdateFixture(t *testing.T, latest, installed string) *rawUpdateFixture {
	t.Helper()
	installDir := t.TempDir()
	exe := filepath.Join(installDir, "caelis")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	// installTargetDir resolves symlinked prefixes (for example /var on macOS),
	// so compare against the same resolved directory.
	if resolved, err := filepath.EvalSymlinks(installDir); err == nil {
		installDir = resolved
		exe = filepath.Join(installDir, "caelis")
	}
	fixture := &rawUpdateFixture{
		exe:           exe,
		installDir:    installDir,
		latestBody:    latest,
		scriptBody:    "#!/bin/bash\necho official-installer\n",
		installedBody: fmt.Sprintf(`{"version":%q,"build_kind":"release","build_id":"release-build"}`, installed),
	}
	releasesBaseURL := "http://updater.test/channel"
	server := newUpdaterTestHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.String() {
		case releasesBaseURL + "/latest.txt":
			fmt.Fprint(w, fixture.latestBody)
		case "https://caelis.dev/install.sh":
			fmt.Fprint(w, fixture.scriptBody)
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	fixture.manager = New(Config{
		StoreDir:        t.TempDir(),
		CurrentVersion:  "v1.0.0",
		Executable:      exe,
		GOOS:            "linux",
		ReleasesBaseURL: releasesBaseURL,
		HTTPClient:      server.Client(),
		Env:             emptyUpdaterEnv,
		LookPath:        func(name string) (string, error) { return "/usr/bin/" + name, nil },
		CommandRun: func(_ context.Context, name string, args []string, env []string, _ io.Writer, _ io.Writer) error {
			fixture.lastCommand = name
			fixture.lastArgs = append([]string(nil), args...)
			fixture.lastEnv = append([]string(nil), env...)
			if len(args) > 0 {
				data, err := os.ReadFile(args[len(args)-1])
				if err != nil {
					t.Fatalf("read installer script: %v", err)
				}
				fixture.lastScript = string(data)
			}
			return fixture.installErr
		},
		CommandOutput: func(_ context.Context, name string, args []string) ([]byte, error) {
			fixture.versionQueries = append(fixture.versionQueries, name)
			return []byte(fixture.installedBody), nil
		},
	})
	return fixture
}

func TestRawUpdateRunsOfficialInstallerAndVerifiesArtifact(t *testing.T) {
	fixture := newRawUpdateFixture(t, "v1.2.0\n", "v1.2.0")
	var progress []ProgressEvent
	result, err := fixture.manager.Update(context.Background(), UpdateOptions{
		Progress: func(event ProgressEvent) { progress = append(progress, event) },
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !result.Updated || result.LatestVersion != "v1.2.0" {
		t.Fatalf("Update() = %#v, want updated v1.2.0", result)
	}
	if fixture.lastCommand != "/usr/bin/bash" || len(fixture.lastArgs) != 1 {
		t.Fatalf("installer command = %q %#v, want bash <script>", fixture.lastCommand, fixture.lastArgs)
	}
	if fixture.lastScript != fixture.scriptBody {
		t.Fatalf("ran script = %q, want official installer body", fixture.lastScript)
	}
	for _, want := range []string{"CAELIS_INSTALL_DIR=" + fixture.installDir, "CAELIS_RELEASES_BASE_URL=http://updater.test/channel"} {
		if !slices.Contains(fixture.lastEnv, want) {
			t.Fatalf("installer env %#v missing %q", fixture.lastEnv, want)
		}
	}
	if len(fixture.versionQueries) != 1 || fixture.versionQueries[0] != filepath.Join(fixture.installDir, "caelis") {
		t.Fatalf("version probes = %#v, want installed binary", fixture.versionQueries)
	}
	want := []ProgressEvent{
		{Stage: ProgressChecking},
		{Stage: ProgressChecking, Done: true},
		{Stage: ProgressInstalling},
		{Stage: ProgressInstalling, Done: true},
	}
	if !slices.Equal(progress, want) {
		t.Fatalf("progress = %#v, want %#v", progress, want)
	}
}

func TestRawUpdateAcceptsNewerReleaseThanChecked(t *testing.T) {
	// The installer is latest-only, so a release can land between the check and
	// the install. The verified artifact at or above the target must succeed.
	fixture := newRawUpdateFixture(t, "v1.2.0\n", "v1.2.1")
	result, err := fixture.manager.Update(context.Background(), UpdateOptions{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !result.Updated || result.LatestVersion != "v1.2.1" {
		t.Fatalf("Update() = %#v, want updated v1.2.1", result)
	}
}

func TestRawUpdateRejectsStaleInstalledArtifact(t *testing.T) {
	fixture := newRawUpdateFixture(t, "v1.2.0\n", "v1.1.0")
	result, err := fixture.manager.Update(context.Background(), UpdateOptions{})
	if err == nil || result.Updated {
		t.Fatalf("Update() = %#v, %v, want stale artifact failure", result, err)
	}
	if !strings.Contains(err.Error(), "older than the released v1.2.0") {
		t.Fatalf("Update() error = %v, want stale artifact detail", err)
	}
}

func TestRawUpdateRejectsNonReleaseInstalledArtifact(t *testing.T) {
	for _, tt := range []struct{ name, body, want string }{
		{name: "loose version", body: `{"version":"1.2.0","build_kind":"release","build_id":"b"}`, want: "invalid release version"},
		{name: "dev version", body: `{"version":"dev","build_kind":"dev","build_id":"b"}`, want: "invalid release version"},
		{name: "dev build kind", body: `{"version":"v1.2.0","build_kind":"dev","build_id":"b"}`, want: "is not a release"},
		{name: "missing build kind", body: `{"version":"v1.2.0","build_id":"b"}`, want: "is not a release"},
		{name: "missing build identity", body: `{"version":"v1.2.0","build_kind":"release"}`, want: "missing build identity"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRawUpdateFixture(t, "v1.2.0\n", "v1.2.0")
			fixture.installedBody = tt.body
			result, err := fixture.manager.Update(context.Background(), UpdateOptions{})
			if err == nil || result.Updated {
				t.Fatalf("Update() = %#v, %v, want artifact verification failure", result, err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Update() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRawUpdateRequiresBash(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want string
	}{
		{name: "not found", err: exec.ErrNotFound, want: "official installer requires bash on PATH: " + exec.ErrNotFound.Error()},
		{name: "empty path", want: "official installer requires bash on PATH"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRawUpdateFixture(t, "v1.2.0\n", "v1.2.0")
			fixture.manager.cfg.LookPath = func(string) (string, error) { return "", tt.err }
			result, err := fixture.manager.Update(context.Background(), UpdateOptions{})
			if err == nil || result.Updated {
				t.Fatalf("Update() = %#v, %v, want missing bash failure", result, err)
			}
			if err.Error() != tt.want {
				t.Fatalf("Update() error = %q, want %q", err, tt.want)
			}
			if fixture.lastCommand != "" || len(fixture.versionQueries) != 0 {
				t.Fatalf("missing bash dispatched command %q or version queries %v", fixture.lastCommand, fixture.versionQueries)
			}
		})
	}
}

func TestRawUpdateSurfacesInstallerFailure(t *testing.T) {
	fixture := newRawUpdateFixture(t, "v1.2.0\n", "v1.2.0")
	fixture.installErr = fmt.Errorf("exit status 1")
	result, err := fixture.manager.Update(context.Background(), UpdateOptions{})
	if err == nil || result.Updated {
		t.Fatalf("Update() = %#v, %v, want installer failure", result, err)
	}
	if !strings.Contains(err.Error(), "official installer https://caelis.dev/install.sh failed") {
		t.Fatalf("Update() error = %v, want installer URL and cause", err)
	}
}

func TestInstallTargetDirResolvesSymlink(t *testing.T) {
	binaryName := rawBinaryName(runtime.GOOS)
	realDir := t.TempDir()
	realExe := filepath.Join(realDir, binaryName)
	if err := os.WriteFile(realExe, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, binaryName)
	if err := os.Symlink(realExe, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	manager := New(Config{Executable: link})
	dir, err := manager.installTargetDir()
	if err != nil {
		t.Fatalf("installTargetDir() error = %v", err)
	}
	// Replacing the linked file must target the real directory so the symlink
	// keeps pointing at the replaced binary.
	want, evalErr := filepath.EvalSymlinks(realDir)
	if evalErr != nil {
		t.Fatalf("resolve real dir: %v", evalErr)
	}
	if dir != want {
		t.Fatalf("installTargetDir() = %q, want resolved %q", dir, want)
	}
}

func TestInstallTargetDirRejectsNonStandardTarget(t *testing.T) {
	realDir := t.TempDir()
	// A symlink that resolves to a renamed binary would make the installer write
	// `caelis` while the command still runs `caelis-v1`.
	renamed := filepath.Join(realDir, "caelis-v1")
	if err := os.WriteFile(renamed, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, "caelis")
	if err := os.Symlink(renamed, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := New(Config{Executable: link}).installTargetDir(); err == nil {
		t.Fatal("installTargetDir() = nil, want rejection of renamed resolved target")
	}
	if _, err := New(Config{Executable: filepath.Join(realDir, "missing")}).installTargetDir(); err == nil {
		t.Fatal("installTargetDir() = nil, want rejection of unresolvable executable")
	}
}
