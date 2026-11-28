package gatewayapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
)

type shellFixture struct {
	client *httpclient.Client
	host   *exec.Cmd
	record controlserver.DiscoveryRecord
	store  string
	token  string
	work   string
}

func TestControlShellNativeServe(t *testing.T) {
	binary := nativeShellBinary(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work, store := filepath.Join(root, "work"), filepath.Join(root, "store")
	for _, dir := range []string{work, store} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	probeMode := filepath.Join(root, "probe-mode")
	probeReady := filepath.Join(root, "probe-ready")
	probeRelease := filepath.Join(root, "probe-release")
	if runtime.GOOS == "darwin" {
		writeShellFile(t, probeMode, "fail")
		helper := filepath.Join(root, "seatbelt-helper")
		writeShellFile(t, helper, fmt.Sprintf(`#!/bin/bash
if [ "$2" = --probe ] && [ -f %s ]; then
  : > %s
  while [ ! -f %s ]; do /bin/sleep .03; done
  exit 71
fi
exec %s "$@"
`, shellQuote(probeMode), shellQuote(probeReady), shellQuote(probeRelease), shellQuote(binary)))
		writeShellFile(t, filepath.Join(store, "config.json"), fmt.Sprintf(`{"schema_version":2,"sandbox":{"requested_type":"seatbelt","helper_path":%s}}`, mustShellJSON(t, helper)))
	}
	f := launchShellFixture(t, binary, root, work, store)
	ctx := t.Context()
	info, err := f.client.Initialize(ctx)
	if err != nil || !slices.Contains(info.Capabilities, appserver.CapabilityShellExecution) {
		t.Fatalf("managed initialize = %+v, %v", info, err)
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(probeReady); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("sandbox helper ran during Host startup: %v", err)
		}
	}
	f.checkHealth(t, "")
	initial, err := f.client.ListSessions(ctx, appserver.ListSessionsRequest{})
	if err != nil || len(initial.Sessions) != 0 {
		t.Fatalf("initial Sessions = %+v, %v", initial, err)
	}
	shell := func(command string, location appserver.ShellLocation) (appserver.ShellResult, error) {
		return f.client.ExecuteShell(ctx, appserver.ShellRequest{Workspace: work, Command: command, Location: location})
	}
	hostOK := "printf host"
	sandboxOK := "printf native"
	exit7 := "printf stdout; printf stderr >&2; exit 7"
	outside := filepath.Join(root, "outside")
	changeOutside := "printf changed > " + shellQuote(outside)
	backend := "seatbelt"
	if runtime.GOOS == "windows" {
		hostOK, sandboxOK = "Write-Output host", "Write-Output native"
		exit7 = "[Console]::Out.WriteLine('stdout'); [Console]::Error.WriteLine('stderr'); exit 7"
		changeOutside = "$ErrorActionPreference='Stop'; Set-Content -LiteralPath " + psQuote(outside) + " -Value changed"
		backend = "windows"
	}
	assertShell(t, shell, hostOK, appserver.ShellHost, "host", "host")
	withoutSession, err := f.client.ListSessions(ctx, appserver.ListSessionsRequest{})
	if err != nil || len(withoutSession.Sessions) != 0 {
		t.Fatalf("Host Shell changed the Session directory: %+v, %v", withoutSession, err)
	}
	// Create an independent ordinary Session only after establishing the
	// Session-free path. No Turn or provider configuration is needed.
	created, err := f.client.CreateSession(ctx, appserver.CreateSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "shell-acceptance-independent-session"}, CWD: work,
	})
	if err != nil || created.SessionID == "" {
		t.Fatalf("create independent Session = %+v, %v", created, err)
	}
	sessionID := created.SessionID
	f.checkHealth(t, sessionID)
	if runtime.GOOS == "darwin" {
		t.Run("sandbox_initialization_failure", func(t *testing.T) {
			defer os.Remove(probeMode)
			done := asyncShell(t.Context(), f.client, work, changeOutside, "")
			waitShellFile(t, probeReady)
			f.checkHealth(t, sessionID)
			assertShell(t, shell, hostOK, appserver.ShellHost, "host", "host")
			writeShellFile(t, probeRelease, "")
			requireShellError(t, <-done, 503, errorcode.Unavailable)
			if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed sandbox wrote outside workspace: %v", err)
			}
			f.checkHealth(t, sessionID)
		})
	} else {
		t.Run("sandbox_initialization_failure", func(t *testing.T) {
			failedWorkspace := filepath.Join(root, "blocked-work")
			if err := os.Mkdir(failedWorkspace, 0o700); err != nil {
				t.Fatal(err)
			}
			env := filepath.Join(store, ".sandbox", "env")
			if err := os.MkdirAll(env, 0o700); err != nil {
				t.Fatal(err)
			}
			// Block just this workspace's existing backend environment path.
			key := sha256.Sum256([]byte(strings.ToLower(failedWorkspace)))
			broken := filepath.Join(env, fmt.Sprintf("%x", key[:8]))
			writeShellFile(t, broken, "ordinary file blocks sandbox preparation")
			_, err := f.client.ExecuteShell(t.Context(), appserver.ShellRequest{Workspace: failedWorkspace, Command: changeOutside})
			requireShellError(t, err, 503, errorcode.Unavailable)
			if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed sandbox wrote outside workspace: %v", err)
			}
			assertShell(t, shell, hostOK, appserver.ShellHost, "host", "host")
			f.checkHealth(t, sessionID)
		})
	}

	assertShell(t, shell, sandboxOK, "", backend, "native")
	assertShell(t, shell, sandboxOK, appserver.ShellSandbox, backend, "native")
	writeShellFile(t, outside, "untouched")
	denied, err := shell(changeOutside, "")
	if err != nil || denied.ExitCode == 0 || readShellFile(t, outside) != "untouched" {
		t.Fatalf("sandbox outside write = %+v, %v", denied, err)
	}
	allowed, err := shell(changeOutside, appserver.ShellHost)
	if err != nil || allowed.ExitCode != 0 || strings.TrimSpace(readShellFile(t, outside)) != "changed" {
		t.Fatalf("Host outside write = %+v, %v", allowed, err)
	}
	for _, location := range []appserver.ShellLocation{appserver.ShellSandbox, appserver.ShellHost} {
		got, err := shell(exit7, location)
		if err != nil || got.ExitCode != 7 || strings.TrimSpace(got.Stdout) != "stdout" || strings.TrimSpace(got.Stderr) != "stderr" {
			t.Fatalf("%s nonzero receipt = %+v, %v", location, got, err)
		}
	}

	afterShell, err := f.client.ListSessions(ctx, appserver.ListSessionsRequest{})
	if err != nil || len(afterShell.Sessions) != 1 || afterShell.Sessions[0].SessionID != sessionID {
		t.Fatalf("Shell changed the Session directory: %+v, %v", afterShell, err)
	}
	t.Run("credentials", func(t *testing.T) {
		acpToken := strings.TrimSpace(readShellFile(t, controlserver.DefaultACPIngressTokenFile(store)))
		for _, token := range []string{acpToken, sessionID, "invalid-token"} {
			client := shellClient(t, f.record.Endpoint, token, nil)
			_, err := client.ExecuteShell(t.Context(), appserver.ShellRequest{Workspace: work, Command: hostOK, Location: appserver.ShellHost})
			if token == acpToken {
				requireShellError(t, err, 403, errorcode.PermissionDenied)
			} else {
				requireShellError(t, err, 401, errorcode.Unauthenticated)
			}
		}
		forwarded := shellClient(t, f.record.Endpoint, f.token, &http.Client{Transport: shellForwarder{}})
		_, err := forwarded.ExecuteShell(t.Context(), appserver.ShellRequest{Workspace: work, Command: hostOK, Location: appserver.ShellHost})
		requireShellError(t, err, 403, errorcode.PermissionDenied)
		for _, location := range []appserver.ShellLocation{"auto", "remote"} {
			_, err := shell(hostOK, location)
			requireShellError(t, err, 400, errorcode.InvalidArgument)
		}
		_, err = f.client.ExecuteShell(t.Context(), appserver.ShellRequest{Workspace: store, Command: hostOK})
		requireShellError(t, err, 403, errorcode.PermissionDenied)
	})
	for _, location := range []appserver.ShellLocation{appserver.ShellSandbox, appserver.ShellHost} {
		t.Run(string(location), func(t *testing.T) {
			runShellFaults(t, f, sessionID, location, hostOK)
		})
	}
	if runtime.GOOS == "darwin" {
		t.Run("worker_crash", func(t *testing.T) {
			pids, command := shellWaitingCommand(work, "worker-crash", "wait")
			done := asyncShell(t.Context(), f.client, work, command, appserver.ShellHost)
			shellPID, childPID := shellPIDs(t, pids)
			workerPID := shellParentPID(t, shellPID)
			if shellParentPID(t, workerPID) != f.host.Process.Pid {
				t.Fatalf("worker %d belongs to an unrelated process", workerPID)
			}
			args, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(workerPID)).Output()
			if err != nil || !strings.HasSuffix(strings.TrimSpace(string(args)), " --control-shell-worker") {
				t.Fatalf("PID %d is not the test worker: %s, %v", workerPID, args, err)
			}
			f.checkHealth(t, sessionID)
			assertShell(t, shell, hostOK, appserver.ShellHost, "host", "host")
			worker, err := os.FindProcess(workerPID)
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.Kill(); err != nil {
				t.Fatal(err)
			}
			requireShellError(t, <-done, 500, errorcode.UnknownOutcome)
			assertShellProcessesGone(t, workerPID, shellPID, childPID)
			f.checkHealth(t, sessionID)
			assertShell(t, shell, hostOK, appserver.ShellHost, "host", "host")
		})
	}
	t.Run("admission_and_deadline", func(t *testing.T) {
		checkShellAdmission(t, f, sessionID, hostOK)
	})
	f.checkHealth(t, sessionID)
	t.Logf("native %s/%s serve PID %d instance %s remained ready", runtime.GOOS, runtime.GOARCH, f.host.Process.Pid, f.record.InstanceID)
}

func nativeShellBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("native acceptance covers macOS and Windows")
	}
	binary := os.Getenv("CAELIS_TEST_CLI_BINARY")
	if binary == "" {
		t.Skip("set CAELIS_TEST_CLI_BINARY to a native Core CLI build")
	}
	path, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func launchShellFixture(t *testing.T, binary, root, work, store string) *shellFixture {
	t.Helper()
	cmd := exec.Command(binary, "serve", "--store-dir", store, "--listen", "127.0.0.1:0")
	cmd.Dir = work
	cmd.Env = slices.DeleteFunc(os.Environ(), func(value string) bool {
		return strings.HasPrefix(strings.ToUpper(value), "CAELIS_")
	})
	log, err := os.Create(filepath.Join(root, "serve.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	f := &shellFixture{host: cmd, store: store, work: work}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		if f.client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, _ = f.client.ShutdownHost(ctx)
			cancel()
		}
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		_ = log.Close()
		if t.Failed() {
			out, _ := os.ReadFile(log.Name())
			t.Logf("isolated serve log:\n%s", out)
		}
	})
	discovery := controlserver.DefaultDiscoveryFile(store)
	waitShellFile(t, discovery)
	if err := json.Unmarshal([]byte(readShellFile(t, discovery)), &f.record); err != nil {
		t.Fatal(err)
	}
	if f.record.PID != cmd.Process.Pid {
		t.Fatalf("discovery selected PID %d, started PID %d", f.record.PID, cmd.Process.Pid)
	}
	f.token = strings.TrimSpace(readShellFile(t, controlserver.DefaultTokenFile(store)))
	f.client = shellClient(t, f.record.Endpoint, f.token, nil)
	return f
}

func (f *shellFixture) checkHealth(t *testing.T, sessionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	for _, get := range []func(context.Context) (appserver.HostStatus, error){f.client.Health, f.client.Readiness} {
		status, err := get(ctx)
		if err != nil || !status.Ready || status.InstanceID != f.record.InstanceID {
			t.Fatalf("liveness/readiness = %+v, %v", status, err)
		}
	}
	var record controlserver.DiscoveryRecord
	if err := json.Unmarshal([]byte(readShellFile(t, controlserver.DefaultDiscoveryFile(f.store))), &record); err != nil ||
		record.PID != f.host.Process.Pid || record.InstanceID != f.record.InstanceID {
		t.Fatalf("Host changed during Shell: %+v, %v", record, err)
	}
	if sessionID != "" {
		state, err := f.client.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
		if err != nil || state.SessionID != sessionID {
			t.Fatalf("independent Session = %+v, %v", state, err)
		}
	}
}

func runShellFaults(t *testing.T, f *shellFixture, sessionID string, location appserver.ShellLocation, hostOK string) {
	for _, fault := range []string{"exit-7", "abnormal", "overflow", "cancel", "completed-with-child"} {
		t.Run(fault, func(t *testing.T) {
			if runtime.GOOS == "windows" && fault == "abnormal" {
				t.Skip("Windows command exits have a process exit code")
			}
			pids, command := shellWaitingCommand(f.work, string(location)+"-"+fault, fault)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan shellObservation, 1)
			go func() {
				result, err := f.client.ExecuteShell(ctx, appserver.ShellRequest{Workspace: f.work, Command: command, Location: location})
				done <- shellObservation{result, err}
			}()
			waitShellBarrier(t, pids, done)
			shellPID, childPID := shellPIDs(t, pids)
			f.checkHealth(t, sessionID)
			second := func() {
				t.Helper()
				result, err := f.client.ExecuteShell(t.Context(), appserver.ShellRequest{Workspace: f.work, Command: hostOK, Location: appserver.ShellHost})
				if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != "host" {
					t.Fatalf("independent Shell = %+v, %v", result, err)
				}
			}
			second()
			if fault == "cancel" {
				cancel()
			} else {
				writeShellFile(t, pids+".release", "")
			}
			var observed shellObservation
			select {
			case observed = <-done:
			case <-time.After(25 * time.Second):
				t.Fatal("Shell request did not finish")
			}
			switch fault {
			case "exit-7":
				if observed.err != nil || observed.result.ExitCode != 7 {
					t.Fatalf("nonzero = %+v", observed)
				}
			case "completed-with-child":
				if observed.err != nil || observed.result.ExitCode != 0 {
					t.Fatalf("successful Shell with child = %+v", observed)
				}
			case "abnormal":
				requireShellError(t, observed.err, 503, errorcode.Unavailable)
			case "overflow":
				requireShellError(t, observed.err, 413, errorcode.ResourceExhausted)
			case "cancel":
				if !errors.Is(observed.err, context.Canceled) {
					t.Fatalf("cancellation = %v", observed.err)
				}
			}
			assertShellProcessesGone(t, shellPID, childPID)
			f.checkHealth(t, sessionID)
			second()
		})
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := f.client.ExecuteShell(cancelled, appserver.ShellRequest{Workspace: f.work, Command: hostOK, Location: location})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("already cancelled request = %v", err)
	}
	f.checkHealth(t, sessionID)
}

func checkShellAdmission(t *testing.T, f *shellFixture, sessionID, hostOK string) {
	t.Helper()
	type running struct {
		cancel context.CancelFunc
		done   <-chan error
		pids   []int
	}
	var calls []running
	for i := range 4 {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		path, command := shellWaitingCommand(f.work, fmt.Sprintf("quota-%d", i), "wait")
		done := asyncShell(ctx, f.client, f.work, command, appserver.ShellHost)
		rootPID, childPID := shellPIDs(t, path)
		calls = append(calls, running{cancel, done, []int{rootPID, childPID}})
	}
	f.checkHealth(t, sessionID)
	req := appserver.ShellRequest{Workspace: f.work, Command: hostOK, Location: appserver.ShellHost}
	_, err := f.client.ExecuteShell(t.Context(), req)
	requireShellError(t, err, 429, errorcode.Overloaded)
	select {
	case err := <-calls[0].done:
		requireShellError(t, err, 504, errorcode.Timeout)
	case <-time.After(25 * time.Second):
		t.Fatal("server did not return its execution deadline")
	}
	for _, call := range calls[1:] {
		call.cancel()
		<-call.done
	}
	for _, call := range calls {
		assertShellProcessesGone(t, call.pids...)
	}
	f.checkHealth(t, sessionID)
	result, err := f.client.ExecuteShell(t.Context(), req)
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != "host" {
		t.Fatalf("Shell after deadline and reap = %+v, %v", result, err)
	}
}

type shellObservation struct {
	result appserver.ShellResult
	err    error
}

type shellForwarder struct{}

func (shellForwarder) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Forwarded", "for=192.0.2.1")
	return http.DefaultTransport.RoundTrip(request)
}

func shellWaitingCommand(work, name, fault string) (string, string) {
	pids := filepath.Join(work, name+".pids")
	if runtime.GOOS == "windows" {
		action := map[string]string{"exit-7": "exit 7", "overflow": "[Console]::WriteLine('x' * 70000); Start-Sleep -Seconds 60", "completed-with-child": "exit 0"}[fault]
		return pids, fmt.Sprintf(`$ErrorActionPreference='Stop'; $child=Start-Process -NoNewWindow -FilePath (Join-Path $PSHOME 'powershell.exe') -ArgumentList '-NoProfile -NonInteractive -Command Start-Sleep -Seconds 60' -PassThru; [IO.File]::WriteAllText(%s+'.tmp', "$PID $($child.Id)"); Move-Item -LiteralPath (%s+'.tmp') -Destination %s; while (!(Test-Path -LiteralPath %s)) {Start-Sleep -Milliseconds 30}; %s`, psQuote(pids), psQuote(pids), psQuote(pids), psQuote(pids+".release"), action)
	}
	action := map[string]string{"exit-7": "exit 7", "abnormal": "kill -TERM \"$$\"", "overflow": "printf '%070000d' 0; /bin/sleep 60", "completed-with-child": "exit 0"}[fault]
	local := filepath.Base(pids)
	return pids, fmt.Sprintf(`set -e; /bin/sleep 60 & child=$!; printf '%%s %%s\n' "$$" "$child" > %s; /bin/mv %s %s; while [ ! -f %s ]; do /bin/sleep .03; done; %s`, shellQuote(local+".tmp"), shellQuote(local+".tmp"), shellQuote(local), shellQuote(local+".release"), action)
}

func shellParentPID(t *testing.T, pid int) int {
	t.Helper()
	raw, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	parent, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || parseErr != nil {
		t.Fatalf("PID %d parent = %q, %v, %v", pid, raw, err, parseErr)
	}
	return parent
}

func assertShellProcessesGone(t *testing.T, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var live []int
		for _, pid := range pids {
			if shellProcessAlive(t, pid) {
				live = append(live, pid)
			}
		}
		if len(live) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Shell left live process IDs %v", live)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func shellProcessAlive(t *testing.T, pid int) bool {
	t.Helper()
	if runtime.GOOS == "windows" {
		out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			fmt.Sprintf("if (Get-Process -Id %d -ErrorAction SilentlyContinue) {'live'}", pid)).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out)) == "live"
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		t.Fatal(err)
	}
	return len(out) != 0
}

func waitShellBarrier(t *testing.T, path string, done <-chan shellObservation) {
	t.Helper()
	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case res := <-done:
			t.Fatalf("Shell exited before test barrier: %+v", res)
		case <-deadline.C:
			t.Fatalf("Shell did not reach the test barrier: %s", path)
		case <-tick.C:
		}
	}
}

func shellPIDs(t *testing.T, path string) (int, int) {
	t.Helper()
	waitShellFile(t, path)
	var root, child int
	if n, err := fmt.Sscan(readShellFile(t, path), &root, &child); n != 2 || err != nil || root < 2 || child < 2 {
		t.Fatalf("invalid Shell fixture PIDs %d %d: %v", root, child, err)
	}
	return root, child
}

func shellClient(t *testing.T, endpoint, token string, client *http.Client) *httpclient.Client {
	t.Helper()
	if client == nil {
		transport := &http.Transport{DialContext: (&net.Dialer{Timeout: time.Second}).DialContext}
		client = &http.Client{Transport: transport, Timeout: 28 * time.Second}
		t.Cleanup(transport.CloseIdleConnections)
	}
	c, err := httpclient.New(httpclient.Config{BaseURL: endpoint, BearerToken: token, HTTPClient: client, Compatibility: appserver.CurrentCompatibility()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func assertShell(t *testing.T, run func(string, appserver.ShellLocation) (appserver.ShellResult, error), command string, location appserver.ShellLocation, backend, stdout string) {
	t.Helper()
	wantLocation := location
	if wantLocation == "" {
		wantLocation = appserver.ShellSandbox
	}
	got, err := run(command, location)
	if err != nil || got.ExitCode != 0 || got.Location != wantLocation || got.Backend != backend || strings.TrimSpace(got.Stdout) != stdout {
		t.Fatalf("Shell = %+v, %v, want %s/%s exit 0 stdout %q", got, err, wantLocation, backend, stdout)
	}
}

func asyncShell(ctx context.Context, client *httpclient.Client, work, command string, location appserver.ShellLocation) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := client.ExecuteShell(ctx, appserver.ShellRequest{Workspace: work, Command: command, Location: location})
		done <- err
	}()
	return done
}

func requireShellError(t *testing.T, err error, status int, code errorcode.Code) {
	t.Helper()
	var remote *httpclient.RemoteError
	if !errors.As(err, &remote) || remote.StatusCode != status || remote.ErrorCode() != code {
		t.Fatalf("Shell error = %v, want HTTP %d / %s", err, status, code)
	}
}

func waitShellFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func readShellFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeShellFile(t *testing.T, path, value string) {
	t.Helper()
	mode := os.FileMode(0o600)
	if strings.HasSuffix(path, "seatbelt-helper") {
		mode = 0o700
	}
	if err := os.WriteFile(path, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}

func mustShellJSON(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
func psQuote(value string) string    { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
