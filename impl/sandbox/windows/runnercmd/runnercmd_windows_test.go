//go:build windows

package runnercmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/runnerproto"
	"github.com/OnslaughtSnail/caelis/impl/sandbox/windows/internal/win32"
)

func TestRunnerCommandEmitsOutputAndExit(t *testing.T) {
	requireRunnerCommandE2E(t)
	t.Parallel()

	var input bytes.Buffer
	spawn, err := runnerproto.NewFrame(runnerproto.TypeSpawn, runnerproto.Spawn{
		Command:       "Write-Output runner-ok",
		CapabilitySID: testCapabilitySIDs(t),
	})
	if err != nil {
		t.Fatalf("NewFrame() error = %v", err)
	}
	if err := runnerproto.NewWriter(&input).WriteFrame(spawn); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}

	var output bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(&input, &output, &stderr); code != 0 {
		t.Fatalf("Run() code = %d stderr=%s", code, stderr.String())
	}

	reader := runnerproto.NewReader(&output)
	hello, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(hello) error = %v", err)
	}
	if hello.Type != runnerproto.TypeHello {
		t.Fatalf("hello type = %q", hello.Type)
	}
	var helloPayload runnerproto.Hello
	if err := hello.DecodePayload(&helloPayload); err != nil {
		t.Fatalf("DecodePayload(hello) error = %v", err)
	}
	if !containsString(helloPayload.Capabilities, "capability_restricted_sid") {
		t.Fatalf("hello capabilities = %#v, want capability restricted SID support", helloPayload.Capabilities)
	}
	var sawOutput bool
	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}
		switch frame.Type {
		case runnerproto.TypeStdout:
			var payload runnerproto.Bytes
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(stdout) error = %v", err)
			}
			if bytes.Contains(payload.Data, []byte("runner-ok")) {
				sawOutput = true
			}
		case runnerproto.TypeExit:
			var payload runnerproto.Exit
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(exit) error = %v", err)
			}
			if payload.ExitCode != 0 {
				t.Fatalf("exit = %+v", payload)
			}
			if !sawOutput {
				t.Fatal("runner exited before stdout was observed")
			}
			return
		}
	}
}

func TestRunnerCommandUsesUTF8AndReadHostStdin(t *testing.T) {
	requireRunnerCommandE2E(t)
	t.Parallel()

	var input bytes.Buffer
	writer := runnerproto.NewWriter(&input)
	spawn, err := runnerproto.NewFrame(runnerproto.TypeSpawn, runnerproto.Spawn{
		Command:       "$name = Read-Host '请输入你的名字'; Write-Host ('你好，' + $name + '！')",
		StdinOpen:     true,
		Timeout:       10 * time.Second,
		CapabilitySID: testCapabilitySIDs(t),
	})
	if err != nil {
		t.Fatalf("NewFrame(spawn) error = %v", err)
	}
	if err := writer.WriteFrame(spawn); err != nil {
		t.Fatalf("WriteFrame(spawn) error = %v", err)
	}
	stdin, err := runnerproto.NewFrame(runnerproto.TypeStdin, runnerproto.Bytes{Data: []byte("世界\n")})
	if err != nil {
		t.Fatalf("NewFrame(stdin) error = %v", err)
	}
	if err := writer.WriteFrame(stdin); err != nil {
		t.Fatalf("WriteFrame(stdin) error = %v", err)
	}
	closeStdin, err := runnerproto.NewFrame(runnerproto.TypeStdinClose, nil)
	if err != nil {
		t.Fatalf("NewFrame(stdin close) error = %v", err)
	}
	if err := writer.WriteFrame(closeStdin); err != nil {
		t.Fatalf("WriteFrame(stdin close) error = %v", err)
	}

	var output bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(&input, &output, &stderr); code != 0 {
		t.Fatalf("Run() code = %d stderr=%s", code, stderr.String())
	}

	stdout, stderrText, exit := readRunnerOutputForTest(t, &output)
	if exit.ExitCode != 0 {
		t.Fatalf("exit = %+v stdout=%q stderr=%q", exit, stdout, stderrText)
	}
	if !strings.Contains(stdout, "你好，世界！") {
		t.Fatalf("stdout = %q, want UTF-8 greeting", stdout)
	}
	if strings.Contains(stdout, "\ufffd") || strings.Contains(stderrText, "\ufffd") {
		t.Fatalf("runner output contains replacement characters: stdout=%q stderr=%q", stdout, stderrText)
	}
}

func TestRunnerCommandCapturesPowerShellErrorStream(t *testing.T) {
	requireRunnerCommandE2E(t)
	t.Parallel()

	var input bytes.Buffer
	spawn, err := runnerproto.NewFrame(runnerproto.TypeSpawn, runnerproto.Spawn{
		Command:       "Write-Error 'raw-powershell-error'; exit 17",
		Timeout:       10 * time.Second,
		CapabilitySID: testCapabilitySIDs(t),
	})
	if err != nil {
		t.Fatalf("NewFrame() error = %v", err)
	}
	if err := runnerproto.NewWriter(&input).WriteFrame(spawn); err != nil {
		t.Fatalf("WriteFrame(spawn) error = %v", err)
	}

	var output bytes.Buffer
	var runnerStderr bytes.Buffer
	if code := Run(&input, &output, &runnerStderr); code != 0 {
		t.Fatalf("Run() code = %d stderr=%s", code, runnerStderr.String())
	}
	_, stderrText, exit := readRunnerOutputForTest(t, &output)
	if exit.ExitCode != 17 {
		t.Fatalf("exit = %+v stderr=%q, want exit 17", exit, stderrText)
	}
	if !strings.Contains(stderrText, "raw-powershell-error") {
		t.Fatalf("stderr = %q, want raw PowerShell error", stderrText)
	}
}

func TestRunnerCommandCapturesUnicodePowerShellErrorStream(t *testing.T) {
	requireRunnerCommandE2E(t)
	t.Parallel()

	message := "\u5b57\u7b26\u4e32\u7f3a\u5c11\u7ec8\u6b62\u7b26"
	var input bytes.Buffer
	spawn, err := runnerproto.NewFrame(runnerproto.TypeSpawn, runnerproto.Spawn{
		Command:       "Write-Error '" + message + "'; exit 17",
		Timeout:       10 * time.Second,
		CapabilitySID: testCapabilitySIDs(t),
	})
	if err != nil {
		t.Fatalf("NewFrame() error = %v", err)
	}
	if err := runnerproto.NewWriter(&input).WriteFrame(spawn); err != nil {
		t.Fatalf("WriteFrame(spawn) error = %v", err)
	}

	var output bytes.Buffer
	var runnerStderr bytes.Buffer
	if code := Run(&input, &output, &runnerStderr); code != 0 {
		t.Fatalf("Run() code = %d stderr=%s", code, runnerStderr.String())
	}
	_, stderrText, exit := readRunnerOutputForTest(t, &output)
	if exit.ExitCode != 17 {
		t.Fatalf("exit = %+v stderr=%q, want exit 17", exit, stderrText)
	}
	if !strings.Contains(stderrText, message) {
		t.Fatalf("stderr = %q, want Unicode PowerShell error %q", stderrText, message)
	}
	if strings.Contains(stderrText, "\ufffd") {
		t.Fatalf("stderr = %q, want no replacement characters", stderrText)
	}
}

func TestRunnerCommandPreservesObjectOutputLineBreaks(t *testing.T) {
	requireRunnerCommandE2E(t)
	t.Parallel()

	want := []string{"calculator", "demo-caelis.exe", "go.mod", "main.go"}
	var input bytes.Buffer
	spawn, err := runnerproto.NewFrame(runnerproto.TypeSpawn, runnerproto.Spawn{
		Command:       "@('calculator','demo-caelis.exe','go.mod','main.go')",
		Timeout:       10 * time.Second,
		CapabilitySID: testCapabilitySIDs(t),
	})
	if err != nil {
		t.Fatalf("NewFrame() error = %v", err)
	}
	if err := runnerproto.NewWriter(&input).WriteFrame(spawn); err != nil {
		t.Fatalf("WriteFrame(spawn) error = %v", err)
	}

	var output bytes.Buffer
	var runnerStderr bytes.Buffer
	if code := Run(&input, &output, &runnerStderr); code != 0 {
		t.Fatalf("Run() code = %d stderr=%s", code, runnerStderr.String())
	}
	stdout, stderrText, exit := readRunnerOutputForTest(t, &output)
	if exit.ExitCode != 0 {
		t.Fatalf("exit = %+v stdout=%q stderr=%q", exit, stdout, stderrText)
	}
	got := strings.Split(strings.ReplaceAll(strings.TrimSpace(stdout), "\r\n", "\n"), "\n")
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("stdout lines = %#v, want %#v; raw stdout=%q", got, want, stdout)
	}
	if strings.Contains(stdout, "calculatordemo-caelis.exego.modmain.go") {
		t.Fatalf("stdout lost line breaks: %q", stdout)
	}
}

func TestMergeEnvNetworkModes(t *testing.T) {
	hostProfile := filepath.Join(t.TempDir(), "host-profile")
	hostCache := filepath.Join(t.TempDir(), "host-go-cache")
	t.Setenv("USERPROFILE", hostProfile)
	t.Setenv("GOCACHE", hostCache)
	t.Setenv("GOMODCACHE", filepath.Join(t.TempDir(), "host-go-mod-cache"))
	t.Setenv("npm_config_cache", filepath.Join(t.TempDir(), "host-npm-cache"))
	t.Setenv("YARN_CACHE_FOLDER", filepath.Join(t.TempDir(), "host-yarn-cache"))
	for _, key := range []string{"CAELIS_SANDBOX_NETWORK", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"} {
		t.Setenv(key, "")
	}

	cwd := t.TempDir()
	env, err := mergeEnv(map[string]string{"EXTRA": "1"}, "offline", cwd)
	if err != nil {
		t.Fatalf("mergeEnv(offline) error = %v", err)
	}
	offline := strings.Join(env, "\n")
	for _, want := range []string{
		"CAELIS_SANDBOX_HOME=",
		"USERPROFILE=",
		"TEMP=",
		"EXTRA=1",
	} {
		if !strings.Contains(offline, want) {
			t.Fatalf("offline env missing %q in %q", want, offline)
		}
	}
	for _, key := range []string{"CAELIS_SANDBOX_NETWORK", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"} {
		if got := envValue(env, key); got != "" {
			t.Fatalf("%s = %q, want no offline network env override", key, got)
		}
	}

	home := envValue(env, "CAELIS_SANDBOX_HOME")
	if home == "" || !testPathIsUnder(home, cwd) {
		t.Fatalf("CAELIS_SANDBOX_HOME = %q, want under cwd %q", home, cwd)
	}
	if got := envValue(env, "USERPROFILE"); !strings.EqualFold(got, hostProfile) {
		t.Fatalf("USERPROFILE = %q, want inherited host profile %q", got, hostProfile)
	}
	if got := envValue(env, "GOCACHE"); !strings.EqualFold(got, hostCache) {
		t.Fatalf("GOCACHE = %q, want inherited host value %q", got, hostCache)
	}
	if got := envValue(env, "GOMODCACHE"); got == "" || testPathIsUnder(got, home) {
		t.Fatalf("GOMODCACHE = %q, want inherited host value outside sandbox home %q", got, home)
	}
	if got := envValue(env, "npm_config_cache"); got == "" || testPathIsUnder(got, home) {
		t.Fatalf("npm_config_cache = %q, want inherited host value outside sandbox home %q", got, home)
	}
	for _, key := range []string{
		"GOPATH",
		"YARN_CACHE_FOLDER",
		"PIP_CACHE_DIR",
		"UV_CACHE_DIR",
		"CARGO_HOME",
		"GRADLE_USER_HOME",
		"NUGET_PACKAGES",
		"npm_config_store_dir",
		"PNPM_HOME",
		"BUN_INSTALL",
		"BUN_INSTALL_CACHE_DIR",
	} {
		if got := envValue(env, key); got != "" && testPathIsUnder(got, home) {
			t.Fatalf("%s = %q, did not expect sandbox-local cache redirect under %q", key, got, home)
		}
	}

	override := filepath.Join(t.TempDir(), "override-go-cache")
	env, err = mergeEnv(map[string]string{"GOCACHE": override}, "offline", cwd)
	if err != nil {
		t.Fatalf("mergeEnv(override) error = %v", err)
	}
	if got := envValue(env, "GOCACHE"); !strings.EqualFold(got, override) {
		t.Fatalf("GOCACHE override = %q, want %q", got, override)
	}

	env, err = mergeEnv(nil, "online", "")
	if err != nil {
		t.Fatalf("mergeEnv(online) error = %v", err)
	}
	online := strings.Join(env, "\n")
	if strings.Contains(online, "http://127.0.0.1:9") {
		t.Fatalf("online env = %q, want no blackhole proxy hardening", online)
	}
}

func TestShouldHideCurrentUserProfileDirOnlySandboxProfiles(t *testing.T) {
	for _, profile := range []string{
		`C:\Users\CaelisSbxOffabcd1234`,
		`C:\Users\CaelisSbxOnabcd1234.DESKTOP`,
		`C:\Users\CaelisSandboxOffline`,
		`C:\Users\CaelisSandboxOnline.DESKTOP`,
	} {
		if !shouldHideCurrentUserProfileDir(profile) {
			t.Fatalf("shouldHideCurrentUserProfileDir(%q) = false, want true", profile)
		}
	}
	for _, profile := range []string{
		`C:\Users\15528`,
		`C:\Users\Administrator`,
		`C:\Users\Default`,
		``,
	} {
		if shouldHideCurrentUserProfileDir(profile) {
			t.Fatalf("shouldHideCurrentUserProfileDir(%q) = true, want false", profile)
		}
	}
}

func TestEffectiveWorkingDirectoryUsesSandboxHomeJunction(t *testing.T) {
	home := t.TempDir()
	hostProfile := t.TempDir()
	cwd := t.TempDir()
	env := []string{"CAELIS_SANDBOX_HOME=" + home, "USERPROFILE=" + hostProfile}

	got := effectiveWorkingDirectory(cwd, env)
	if strings.EqualFold(got, cwd) {
		t.Skip("directory junction creation unavailable in this Windows environment")
	}
	t.Cleanup(func() {
		_ = os.Remove(got)
	})
	if !isReparsePoint(got) {
		t.Fatalf("effectiveWorkingDirectory() = %q, want reparse-point junction", got)
	}
	if !testPathIsUnder(got, filepath.Join(home, ".caelis", ".sandbox", "cwd")) {
		t.Fatalf("effectiveWorkingDirectory() = %q, want under sandbox home %q", got, home)
	}
	again := effectiveWorkingDirectory(cwd, env)
	if !strings.EqualFold(got, again) {
		t.Fatalf("effectiveWorkingDirectory() reuse = %q, want %q", again, got)
	}
}

func TestCWDJunctionNameIsStableForCleanedCaseInsensitivePath(t *testing.T) {
	left := cwdJunctionName(`C:\Users\Admin\WorkDir\Repo\.`)
	right := cwdJunctionName(`c:\users\admin\workdir\repo`)
	if left == "" || left != right {
		t.Fatalf("cwdJunctionName() = %q and %q, want same non-empty name", left, right)
	}
}

func readRunnerOutputForTest(t *testing.T, output *bytes.Buffer) (string, string, runnerproto.Exit) {
	t.Helper()
	reader := runnerproto.NewReader(output)
	hello, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(hello) error = %v", err)
	}
	if hello.Type != runnerproto.TypeHello {
		t.Fatalf("hello type = %q", hello.Type)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}
		switch frame.Type {
		case runnerproto.TypeStdout:
			var payload runnerproto.Bytes
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(stdout) error = %v", err)
			}
			stdout.Write(payload.Data)
		case runnerproto.TypeStderr:
			var payload runnerproto.Bytes
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(stderr) error = %v", err)
			}
			stderr.Write(payload.Data)
		case runnerproto.TypeExit:
			var payload runnerproto.Exit
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(exit) error = %v", err)
			}
			return stdout.String(), stderr.String(), payload
		}
	}
}

func testPathIsUnder(path string, root string) bool {
	path = strings.ToLower(filepath.Clean(path))
	root = strings.ToLower(filepath.Clean(root))
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func TestRunnerTTYCommandUsesConPTY(t *testing.T) {
	requireRunnerCommandE2E(t)
	t.Parallel()

	var input bytes.Buffer
	spawn, err := runnerproto.NewFrame(runnerproto.TypeSpawn, runnerproto.Spawn{
		Command:       "Write-Output tty-ok",
		TTY:           true,
		Rows:          24,
		Cols:          80,
		Timeout:       10 * time.Second,
		CapabilitySID: testCapabilitySIDs(t),
	})
	if err != nil {
		t.Fatalf("NewFrame() error = %v", err)
	}
	if err := runnerproto.NewWriter(&input).WriteFrame(spawn); err != nil {
		t.Fatalf("WriteFrame(spawn) error = %v", err)
	}
	resize, err := runnerproto.NewFrame(runnerproto.TypeResize, runnerproto.Resize{Rows: 30, Cols: 100})
	if err != nil {
		t.Fatalf("NewFrame(resize) error = %v", err)
	}
	if err := runnerproto.NewWriter(&input).WriteFrame(resize); err != nil {
		t.Fatalf("WriteFrame(resize) error = %v", err)
	}

	var output bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(&input, &output, &stderr); code != 0 {
		if strings.Contains(stderr.String(), "CreatePseudoConsole") {
			t.Skipf("ConPTY is unavailable in this Windows environment: %s", stderr.String())
		}
		t.Fatalf("Run() code = %d stderr=%s", code, stderr.String())
	}

	reader := runnerproto.NewReader(&output)
	hello, err := reader.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(hello) error = %v", err)
	}
	var helloPayload runnerproto.Hello
	if err := hello.DecodePayload(&helloPayload); err != nil {
		t.Fatalf("DecodePayload(hello) error = %v", err)
	}
	if !containsString(helloPayload.Capabilities, "conpty") || !containsString(helloPayload.Capabilities, "resize") {
		t.Fatalf("hello capabilities = %#v, want conpty and resize", helloPayload.Capabilities)
	}
	var stdout bytes.Buffer
	for {
		frame, err := reader.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}
		switch frame.Type {
		case runnerproto.TypeStdout:
			var payload runnerproto.Bytes
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(stdout) error = %v", err)
			}
			stdout.Write(payload.Data)
		case runnerproto.TypeExit:
			var payload runnerproto.Exit
			if err := frame.DecodePayload(&payload); err != nil {
				t.Fatalf("DecodePayload(exit) error = %v", err)
			}
			if payload.ExitCode != 0 {
				t.Fatalf("exit = %+v stdout=%q", payload, stdout.String())
			}
			if !strings.Contains(stdout.String(), "tty-ok") {
				t.Fatalf("stdout = %q, want tty-ok", stdout.String())
			}
			return
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func testCapabilitySIDs(t *testing.T) []string {
	t.Helper()
	sids, err := win32.DeriveCapabilitySIDs("internetClient")
	if err != nil {
		t.Fatalf("DeriveCapabilitySIDs() error = %v", err)
	}
	if len(sids.Group) == 0 {
		t.Fatalf("DeriveCapabilitySIDs() = %#v, want capability group SID", sids)
	}
	return sids.Group
}

func requireRunnerCommandE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("CAELIS_WINDOWS_SANDBOX_E2E") != "1" {
		t.Skip("set CAELIS_WINDOWS_SANDBOX_E2E=1 to run Windows runner command e2e")
	}
}
