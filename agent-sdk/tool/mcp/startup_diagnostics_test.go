package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStartupNPMDiagnosticsRequireFetchEvidenceAndBoundSecrets(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{"ENOTFOUND", "npm registry DNS failure (ENOTFOUND)"},
		{"EAI_AGAIN", "npm registry DNS failure (EAI_AGAIN)"},
		{"ECONNREFUSED", "npm registry connection refused (ECONNREFUSED)"},
		{"ETIMEDOUT", "npm registry request timed out"},
		{"E401", "npm authentication rejected (E401)"},
		{"E403", "npm access denied (E403)"},
		{"E404", "npm package resolution/download failed (E404)"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			d := new(startupDiagnostics)
			_, _ = d.Write([]byte(strings.Repeat("secret-prefix", 2000)))
			_, _ = d.Write([]byte("\nnpm error code " + tc.code + "\nnpm error request to https://user:password@private.invalid/pkg?token=secret failed; Authorization: Bearer secret\n"))
			if d.used != startupStderrLimit {
				t.Fatalf("tail=%d", d.used)
			}
			got := startupFailureSummary(&startupError{Stage: "initialize", stdio: true, npm: true, reason: d.reason(io.EOF), Err: fmt.Errorf("secret=hidden: %w", io.EOF)})
			if got != "startup/initialize: "+tc.want {
				t.Fatalf("summary=%q", got)
			}
			for _, secret := range []string{"user", "password", "private.invalid", "Bearer", "secret"} {
				if strings.Contains(got, secret) {
					t.Fatalf("leaked %s", secret)
				}
			}
			d.stopStderr()
			_, _ = d.Write([]byte("npm error code E401\n"))
			if d.used != 0 || strings.Trim(string(d.tail[:]), "\x00") != "" {
				t.Fatal("stderr retained after startup")
			}
		})
	}
	for _, raw := range []string{"npm error code ENOTFOUND\n", "ordinary server ENOTFOUND request to registry\n", "npm error request to registry failed\n"} {
		if got := npmStderrReason(raw); got != "" {
			t.Fatalf("unproven registry claim %q from %q", got, raw)
		}
	}
	d := new(startupDiagnostics)
	_, _ = d.Write([]byte("npm error code ENOTFOUND\nnpm error request to registry failed\n"))
	_, _ = d.Write([]byte(strings.Repeat("x", startupStderrLimit)))
	if d.reason(io.EOF) != "" {
		t.Fatal("evidence outside the bounded tail was retained")
	}
	got := startupFailureSummary(&startupError{Stage: "initialize", stdio: true, npm: true, Err: context.DeadlineExceeded})
	if got != "startup/initialize: startup incomplete; check npm registry network" {
		t.Fatal(got)
	}
	if got := startupFailureSummary(&startupError{Stage: "initialize", stdio: true, Err: context.DeadlineExceeded}); strings.Contains(got, "npm") {
		t.Fatal(got)
	}
	if got := startupFailureSummary(&startupError{Stage: "initialize", Err: errors.New("token=secret"), reason: "token=secret"}); got != "initialize: failed" {
		t.Fatal(got)
	}
}

func TestStartupHTTPDiagnosticsPreserveAuthenticationAndErrorTypes(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "token=secret")
			}))
			defer server.Close()
			_, err := StartClient(t.Context(), ServerSpec{PluginID: "p", Name: "http", Transport: TransportStreamableHTTP, URL: server.URL})
			if err == nil {
				t.Fatal("HTTP rejection accepted")
			}
			want := fmt.Sprint("HTTP ", status)
			got := (ServerFailure{Err: err}).Summary()
			if !strings.Contains(got, want) || strings.Contains(got, "secret") || strings.Contains(got, server.URL) {
				t.Fatal(got)
			}
		})
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&net.DNSError{Err: "secret DNS", Name: "private.invalid"}, "DNS lookup failed"},
		{&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, "connection refused"},
		{&net.DNSError{IsTimeout: true}, "DNS lookup timed out"},
	} {
		base := &http.Client{Transport: mcpTestRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, tc.err })}
		_, err := startClientWithHTTPClient(t.Context(), ServerSpec{PluginID: "p", Name: "net", Transport: TransportStreamableHTTP, URL: "http://test.invalid"}, base)
		if !(strings.Contains((ServerFailure{Err: err}).Summary(), tc.want)) {
			t.Fatalf("summary=%s", (ServerFailure{Err: err}).Summary())
		}
	}
	if networkFailureReason(context.DeadlineExceeded) != "" {
		t.Fatal("generic deadline called a network failure")
	}
}

func TestMCPStartupDiagnosticHelper(t *testing.T) {
	mode := os.Getenv("CAELIS_MCP_STARTUP_DIAGNOSTIC_HELPER")
	if mode == "" {
		return
	}
	if mode == "ignore" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if mode == "wait" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, strings.Repeat("secret-prefix", 2000))
	fmt.Fprintln(os.Stderr, "npm error code ENOTFOUND\nnpm error request to https://user:password@private.invalid/pkg?token=secret failed, reason: getaddrinfo ENOTFOUND private.invalid")
	os.Exit(1)
}

func TestStartupNPMStderrFlowsThroughRealCommandWithoutOrdinaryMCPMislabel(t *testing.T) {
	name := "npx"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	npm := filepath.Join(t.TempDir(), name)
	// A test executable with the launcher's name; never runs npm or installs.
	if err := os.Link(executable, npm); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{npm, executable} {
		_, err := StartClient(t.Context(), ServerSpec{PluginID: "p", Name: "child", WorkDir: t.TempDir(), Command: command, Args: []string{"-test.run=^TestMCPStartupDiagnosticHelper$"}, Env: map[string]string{"CAELIS_MCP_STARTUP_DIAGNOSTIC_HELPER": "network"}})
		if err == nil {
			t.Fatal("dead peer accepted")
		}
		summary := (ServerFailure{Err: err}).Summary()
		if command == npm && summary != "startup/initialize: npm registry DNS failure (ENOTFOUND)" {
			t.Fatal(summary)
		}
		if command != npm && strings.Contains(summary, "npm") {
			t.Fatal(summary)
		}
		t.Log(summary)
	}
}

func TestStartupParentCancellationReapsActualCommand(t *testing.T) {
	for _, mode := range []string{"wait", "ignore"} {
		t.Run(mode, func(t *testing.T) {
			transport, _, err := transportForSpecWithHTTPClient(ServerSpec{PluginID: "p", Name: "pending", Command: os.Args[0], Args: []string{"-test.run=^TestMCPStartupDiagnosticHelper$"}, WorkDir: t.TempDir(), Env: map[string]string{"CAELIS_MCP_STARTUP_DIAGNOSTIC_HELPER": mode}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			transport.(*mcpsdk.CommandTransport).TerminateDuration = 20 * time.Millisecond
			cmd := transport.(*mcpsdk.CommandTransport).Command
			startup := &startupTransport{Transport: transport}
			sdk := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
			parent, cancel := context.WithCancel(t.Context())
			lifetime, stop := context.WithCancel(context.Background())
			defer stop()
			done := make(chan error, 1)
			go func() {
				_, err := connectWithTimeout(parent, sdk, lifetime, stop, startup, DefaultStartupTimeout)
				done <- err
			}()
			deadline := time.After(5 * time.Second)
			for !startup.connected.Load() {
				select {
				case <-deadline:
					cancel()
					t.Fatal("child did not start")
				case <-time.After(time.Millisecond):
				}
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if cmd.ProcessState == nil {
				t.Fatal("cancellation returned before Wait/reap")
			}
			t.Logf("reaped pid=%d", cmd.Process.Pid)
		})
	}
}
