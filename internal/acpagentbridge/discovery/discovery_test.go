package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/authentication"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestPrepareUsesTemporarySessionAndCleansUpProcess(t *testing.T) {
	markerDir := t.TempDir()
	connection := helperConnection(markerDir, "catalog")
	result, err := (Service{Clock: func() time.Time { return time.Unix(123, 0) }}).Prepare(
		context.Background(),
		PrepareRequest{Connection: connection, CWD: markerDir},
	)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	snapshot := result.Snapshot
	if snapshot.ConnectionID != "claude" || snapshot.CurrentModelID != "sonnet" {
		t.Fatalf("Prepare() = %#v", snapshot)
	}
	if snapshot.ModelControl.Kind != controlagents.ModelControlConfigOption || len(snapshot.Models) != 2 {
		t.Fatalf("Prepare() model catalog = %#v %#v", snapshot.ModelControl, snapshot.Models)
	}
	for _, marker := range []string{"session-close", "process-exit"} {
		if _, err := os.Stat(filepath.Join(markerDir, marker)); err != nil {
			t.Fatalf("missing cleanup marker %q: %v", marker, err)
		}
	}
	if _, err := os.Stat(filepath.Join(markerDir, "prompt")); !os.IsNotExist(err) {
		t.Fatalf("discovery sent a prompt: %v", err)
	}
}

func TestPrepareSelectedModelCapturesModelScopedOptions(t *testing.T) {
	markerDir := t.TempDir()
	result, err := (Service{}).Prepare(context.Background(), PrepareRequest{
		Connection: helperConnection(markerDir, "catalog"), CWD: markerDir, SelectedModelID: "opus",
	})
	if err != nil {
		t.Fatalf("Prepare(selected model) error = %v", err)
	}
	snapshot := result.Snapshot
	if snapshot.SelectedModelID != "opus" || snapshot.CurrentModelID != "opus" {
		t.Fatalf("selected snapshot = %#v, want opus", snapshot)
	}
	var effort controlagents.ConfigOption
	for _, option := range snapshot.ConfigOptions {
		if option.ID == "effort" {
			effort = option
		}
	}
	if len(effort.Options) != 1 || effort.Options[0].Value != "max" {
		t.Fatalf("model-scoped effort option = %#v, want max", effort)
	}
	if _, err := os.Stat(filepath.Join(markerDir, "model-selected")); err != nil {
		t.Fatalf("selected model was not applied to temporary session: %v", err)
	}
}

func TestPrepareContinuesWhenAuthenticationMethodsAreAdvertised(t *testing.T) {
	markerDir := t.TempDir()
	result, err := (Service{}).Prepare(context.Background(), PrepareRequest{
		Connection: helperConnection(markerDir, "auth"), CWD: markerDir,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	snapshot := result.Snapshot
	if snapshot.CurrentModelID != "sonnet" {
		t.Fatalf("Prepare() = %#v, want catalog returned by session/new", snapshot)
	}
	for _, marker := range []string{"session-new", "session-close", "process-exit"} {
		if _, err := os.Stat(filepath.Join(markerDir, marker)); err != nil {
			t.Fatalf("missing marker %q: %v", marker, err)
		}
	}
}

func TestPrepareReturnsReadyDiscoverySnapshot(t *testing.T) {
	markerDir := t.TempDir()
	result, err := (Service{Clock: func() time.Time { return time.Unix(456, 0) }}).Prepare(
		context.Background(),
		PrepareRequest{Connection: helperConnection(markerDir, "catalog"), CWD: markerDir},
	)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if result.State != PrepareReady || result.Snapshot.ConnectionID != "claude" || result.Snapshot.CurrentModelID != "sonnet" {
		t.Fatalf("Prepare() = %#v, want ready discovery snapshot", result)
	}
	if !result.Snapshot.DiscoveredAt.Equal(time.Unix(456, 0).UTC()) {
		t.Fatalf("DiscoveredAt = %v", result.Snapshot.DiscoveredAt)
	}
	for _, marker := range []string{"session-close", "process-exit"} {
		if _, statErr := os.Stat(filepath.Join(markerDir, marker)); statErr != nil {
			t.Fatalf("missing cleanup marker %q: %v", marker, statErr)
		}
	}
}

func TestPrepareReturnsMultipleMethodChallengeWithoutContextSelection(t *testing.T) {
	markerDir := t.TempDir()
	selectorCalls := 0
	terminalCalls := 0
	ctx := controlagents.WithAuthenticationSelection(context.Background(), func(
		context.Context,
		controlagents.AuthenticationSelectionRequest,
	) (string, error) {
		selectorCalls++
		return "agent-login", nil
	})
	ctx = controlagents.WithTerminalAuthentication(ctx, func(
		context.Context,
		controlagents.TerminalAuthenticationRequest,
	) error {
		terminalCalls++
		return nil
	})
	result, err := (Service{}).Prepare(ctx, PrepareRequest{
		Connection: helperConnection(markerDir, "multi-auth-required"),
		CWD:        markerDir,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if result.State != PrepareNeedsAuth || len(result.AuthenticationMethods) != 2 {
		t.Fatalf("Prepare() = %#v, want needs_auth with two methods", result)
	}
	if result.AuthenticationMethods[0].ID != "agent-login" || result.AuthenticationMethods[1].Type != controlagents.AuthenticationTerminal {
		t.Fatalf("authentication methods = %#v", result.AuthenticationMethods)
	}
	if selectorCalls != 0 || terminalCalls != 0 {
		t.Fatalf("selector calls = %d, terminal calls = %d; want zero", selectorCalls, terminalCalls)
	}
	for _, marker := range []string{"authenticate", "terminal-authenticated"} {
		if _, statErr := os.Stat(filepath.Join(markerDir, marker)); !os.IsNotExist(statErr) {
			t.Fatalf("unexpected authentication effect %q: %v", marker, statErr)
		}
	}
	if _, statErr := os.Stat(filepath.Join(markerDir, "process-exit")); statErr != nil {
		t.Fatalf("probe process was not cleaned up: %v", statErr)
	}
}

func TestPrepareExecutesOnlyExplicitAgentMethod(t *testing.T) {
	markerDir := t.TempDir()
	selectorCalls := 0
	ctx := controlagents.WithAuthenticationSelection(context.Background(), func(
		context.Context,
		controlagents.AuthenticationSelectionRequest,
	) (string, error) {
		selectorCalls++
		return "unexpected", nil
	})
	result, err := (Service{}).Prepare(ctx, PrepareRequest{
		Connection:             helperConnection(markerDir, "agent-auth-required"),
		CWD:                    markerDir,
		AuthenticationMethodID: "agent-login",
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if result.State != PrepareReady || result.Authentication != (controlagents.Authentication{
		MethodID: "agent-login",
		Type:     controlagents.AuthenticationAgent,
	}) || result.Snapshot.Authentication != result.Authentication {
		t.Fatalf("Prepare() = %#v, want ready selected agent auth", result)
	}
	if selectorCalls != 0 {
		t.Fatalf("selector calls = %d, want zero", selectorCalls)
	}
	if _, statErr := os.Stat(filepath.Join(markerDir, "authenticate")); statErr != nil {
		t.Fatalf("selected agent method was not executed: %v", statErr)
	}
}

func TestPrepareExplicitTerminalMethodRequiresContextRunner(t *testing.T) {
	markerDir := t.TempDir()
	result, err := (Service{}).Prepare(context.Background(), PrepareRequest{
		Connection:             helperConnection(markerDir, "terminal-auth-required-unconditional"),
		CWD:                    markerDir,
		AuthenticationMethodID: "terminal-login",
	})
	var unavailable *authentication.TerminalUnavailableError
	if !errors.As(err, &unavailable) || !errors.Is(err, controlagents.ErrTerminalAuthenticationUnavailable) {
		t.Fatalf("Prepare() error = %v, want typed terminal unavailable", err)
	}
	if result.State == PrepareReady || unavailable.MethodID != "terminal-login" {
		t.Fatalf("Prepare() = %#v, unavailable = %#v", result, unavailable)
	}
	if _, statErr := os.Stat(filepath.Join(markerDir, "terminal-authenticated")); !os.IsNotExist(statErr) {
		t.Fatalf("terminal login ran without explicit runner: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(markerDir, "session-new")); !os.IsNotExist(statErr) {
		t.Fatalf("session/new ran before terminal capability preflight: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(markerDir, "process-exit")); statErr != nil {
		t.Fatalf("process was not cleaned up after unavailable terminal auth: %v", statErr)
	}
}

func TestPrepareAuthenticatesExplicitAgentMethodAndRetriesSessionNew(t *testing.T) {
	markerDir := t.TempDir()
	result, err := (Service{}).Prepare(context.Background(), PrepareRequest{
		Connection:             helperConnection(markerDir, "agent-auth-required"),
		CWD:                    markerDir,
		AuthenticationMethodID: "agent-login",
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	snapshot := result.Snapshot
	if snapshot.Authentication != (controlagents.Authentication{MethodID: "agent-login", Type: controlagents.AuthenticationAgent}) {
		t.Fatalf("authentication = %#v", snapshot.Authentication)
	}
	for _, marker := range []string{"authenticate", "session-close", "process-exit"} {
		if _, statErr := os.Stat(filepath.Join(markerDir, marker)); statErr != nil {
			t.Fatalf("missing marker %q: %v", marker, statErr)
		}
	}
}

func TestPrepareReturnsRepeatedAuthRequiredWithoutLooping(t *testing.T) {
	t.Parallel()

	markerDir := t.TempDir()
	_, err := (Service{}).Prepare(context.Background(), PrepareRequest{
		Connection:             helperConnection(markerDir, "agent-auth-required-twice"),
		CWD:                    markerDir,
		AuthenticationMethodID: "agent-login",
	})
	if err == nil {
		t.Fatal("Prepare() error = nil for repeated auth_required")
	}
	for _, want := range []string{"retry authenticated operation", "Authentication required"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Prepare() error = %q, want %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(markerDir, "authenticate")); statErr != nil {
		t.Fatalf("declared method was not attempted once: %v", statErr)
	}
}

func TestPrepareRunsExplicitTerminalMethodThenReconnects(t *testing.T) {
	markerDir := t.TempDir()
	connection := helperConnection(markerDir, "terminal-auth-required")
	ctx := controlagents.WithTerminalAuthentication(context.Background(), func(
		_ context.Context,
		request controlagents.TerminalAuthenticationRequest,
	) error {
		if request.Command != connection.Launcher.Command {
			t.Fatalf("terminal command = %q, want configured command %q", request.Command, connection.Launcher.Command)
		}
		wantArgs := append(append([]string(nil), connection.Launcher.Args...), "--login")
		if !reflect.DeepEqual(request.Args, wantArgs) {
			t.Fatalf("terminal args = %#v, want %#v", request.Args, wantArgs)
		}
		if request.Env["CAELIS_DISCOVERY_HELPER"] != "terminal-auth-required" || request.Env["ACP_INTERACTIVE_LOGIN"] != "1" {
			t.Fatalf("terminal env = %#v", request.Env)
		}
		return writeDiscoveryMarker(markerDir, "terminal-authenticated", "yes")
	})
	result, err := (Service{}).Prepare(ctx, PrepareRequest{
		Connection: connection, CWD: markerDir, AuthenticationMethodID: "terminal-login",
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	snapshot := result.Snapshot
	if snapshot.Authentication != (controlagents.Authentication{MethodID: "terminal-login", Type: controlagents.AuthenticationTerminal}) {
		t.Fatalf("authentication = %#v", snapshot.Authentication)
	}
	for _, marker := range []string{"terminal-capability", "terminal-authenticated", "session-close", "process-exit"} {
		if _, statErr := os.Stat(filepath.Join(markerDir, marker)); statErr != nil {
			t.Fatalf("missing marker %q: %v", marker, statErr)
		}
	}
}

func TestPrepareTerminalAuthenticationCancellationClosesClientOnce(t *testing.T) {
	markerDir := t.TempDir()
	connection := helperConnection(markerDir, "terminal-auth-required")
	ctx := controlagents.WithTerminalAuthentication(context.Background(), func(
		context.Context,
		controlagents.TerminalAuthenticationRequest,
	) error {
		return errors.New("terminal login cancelled")
	})

	_, err := (Service{}).Prepare(ctx, PrepareRequest{
		Connection: connection, CWD: markerDir, AuthenticationMethodID: "terminal-login",
	})
	if err == nil {
		t.Fatal("Prepare() error = nil after terminal authentication cancellation")
	}
	if !strings.Contains(err.Error(), "terminal login cancelled") {
		t.Fatalf("Prepare() error = %q, want terminal cancellation", err)
	}
	if strings.Contains(err.Error(), "Wait was already called") {
		t.Fatalf("Prepare() closed the same ACP process twice: %v", err)
	}
}

func TestPreparePreservesNonAuthenticationSessionFailure(t *testing.T) {
	markerDir := t.TempDir()
	_, err := (Service{}).Prepare(context.Background(), PrepareRequest{
		Connection: helperConnection(markerDir, "session-failure"), CWD: markerDir,
	})
	if err == nil {
		t.Fatal("Prepare() error = nil, want session/new failure")
	}
	for _, want := range []string{"create prepare session", "helper session failure"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Prepare() error = %q, want original session/new failure containing %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(markerDir, "process-exit")); statErr != nil {
		t.Fatalf("process was not cleaned up after session/new failure: %v", statErr)
	}
}

func TestPrepareCancellationTerminatesProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kill -0 liveness probe is Unix-specific")
	}
	markerDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := (Service{}).Prepare(ctx, PrepareRequest{
			Connection: helperConnection(markerDir, "block"), CWD: markerDir,
		})
		result <- err
	}()
	waitForDiscoveryMarker(t, filepath.Join(markerDir, "initialize-ready"), 3*time.Second)
	pid := readDiscoveryHelperPID(t, filepath.Join(markerDir, "pid"))
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Prepare() error = nil after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Prepare() did not return after cancellation")
	}
	waitForDiscoveryHelperExit(t, pid, 2*time.Second)
}

func TestPrepareBoundsUnresponsiveSessionCloseAndTerminatesProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kill -0 liveness probe is Unix-specific")
	}
	markerDir := t.TempDir()
	started := time.Now()
	_, err := (Service{CleanupTimeout: 50 * time.Millisecond}).Prepare(context.Background(), PrepareRequest{
		Connection: helperConnection(markerDir, "close-block"), CWD: markerDir,
	})
	if err == nil || !strings.Contains(err.Error(), "close prepare session") || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("Prepare() error = %v, want bounded session/close failure", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Prepare() elapsed = %v, want bounded cleanup", elapsed)
	}
	pid := readDiscoveryHelperPID(t, filepath.Join(markerDir, "pid"))
	waitForDiscoveryHelperExit(t, pid, 2*time.Second)
}

func TestPrepareReportsUnknownCleanupAndPreservesSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kill -0 liveness probe is Unix-specific")
	}
	markerDir := t.TempDir()
	started := time.Now()
	result, err := (Service{CleanupTimeout: 50 * time.Millisecond}).Prepare(
		context.Background(),
		PrepareRequest{Connection: helperConnection(markerDir, "close-block"), CWD: markerDir},
	)
	if err == nil || !strings.Contains(err.Error(), "close prepare session") || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("Prepare() error = %v, want bounded session/close failure", err)
	}
	if result.State != PrepareUnknownCleanup || result.Snapshot.CurrentModelID != "sonnet" {
		t.Fatalf("Prepare() = %#v, want unknown_cleanup with preserved snapshot", result)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Prepare() elapsed = %v, want bounded cleanup", elapsed)
	}
	pid := readDiscoveryHelperPID(t, filepath.Join(markerDir, "pid"))
	waitForDiscoveryHelperExit(t, pid, 2*time.Second)
}

func waitForDiscoveryMarker(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if raw, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(raw)) != "" {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("helper process did not publish marker %q", filepath.Base(path))
		case <-ticker.C:
		}
	}
}

func readDiscoveryHelperPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read helper pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("helper pid = %q, %v", raw, err)
	}
	return pid
}

func waitForDiscoveryHelperExit(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run(); err != nil {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("helper process %d is still alive after Prepare returned", pid)
		case <-ticker.C:
		}
	}
}

func helperConnection(markerDir string, mode string) controlagents.Connection {
	return controlagents.Connection{
		ID: "claude",
		Launcher: controlagents.Launcher{
			Kind:    controlagents.LaunchKindExecutable,
			Command: os.Args[0],
			Args:    []string{"-test.run=TestDiscoveryHelperProcess", "--"},
			Env: map[string]string{
				"CAELIS_DISCOVERY_HELPER": mode,
				"CAELIS_DISCOVERY_MARKER": markerDir,
			},
		},
	}
}

func TestDiscoveryHelperProcess(t *testing.T) {
	mode := os.Getenv("CAELIS_DISCOVERY_HELPER")
	if mode == "" {
		return
	}
	markerDir := os.Getenv("CAELIS_DISCOVERY_MARKER")
	writeMarker := func(name string, value string) {
		if err := writeDiscoveryMarker(markerDir, name, value); err != nil {
			panic(err)
		}
	}
	// Publish the process identity before initialize completes. The close-block
	// case deliberately races a short client cleanup deadline against process
	// termination, so publishing it from the close handler can lose the marker
	// when the client kills the helper immediately after the deadline.
	if mode == "close-block" {
		writeMarker("pid", strconv.Itoa(os.Getpid()))
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	_ = conn.Serve(context.Background(), func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case client.MethodInitialize:
			if mode == "block" {
				writeMarker("pid", strconv.Itoa(os.Getpid()))
				writeMarker("initialize-ready", "yes")
				select {}
			}
			response := client.InitializeResponse{ProtocolVersion: 1, AgentCapabilities: schema.AgentCapabilities{SessionCapabilities: map[string]json.RawMessage{"close": json.RawMessage(`{}`)}}}
			if mode == "auth" {
				response.AuthMethods = []json.RawMessage{json.RawMessage(`{"id":"login","name":"Login"}`)}
			}
			if mode == "agent-auth-required" || mode == "agent-auth-required-twice" {
				response.AuthMethods = []json.RawMessage{json.RawMessage(`{"id":"agent-login","name":"Agent login"}`)}
			}
			if mode == "multi-auth-required" {
				response.AuthMethods = []json.RawMessage{
					json.RawMessage(`{"id":"agent-login","name":"Agent login"}`),
					json.RawMessage(`{"id":"terminal-login","name":"Terminal login","type":"terminal","args":["--login"]}`),
				}
			}
			if mode == "terminal-auth-required" || mode == "terminal-auth-required-unconditional" {
				var request client.InitializeRequest
				if err := json.Unmarshal(msg.Params, &request); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
				}
				auth, _ := request.ClientCapabilities["auth"].(map[string]any)
				if mode == "terminal-auth-required" && auth["terminal"] != true {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "terminal auth capability missing"}
				}
				if auth["terminal"] == true {
					writeMarker("terminal-capability", "yes")
				}
				response.AuthMethods = []json.RawMessage{json.RawMessage(
					`{"id":"terminal-login","name":"Terminal login","type":"terminal","args":["--login"],"env":{"ACP_INTERACTIVE_LOGIN":"1"}}`,
				)}
			}
			return response, nil
		case client.MethodAuthenticate:
			var request client.AuthenticateRequest
			if err := json.Unmarshal(msg.Params, &request); err != nil || request.MethodID != "agent-login" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected authenticate request"}
			}
			writeMarker("authenticate", request.MethodID)
			return client.AuthenticateResponse{}, nil
		case client.MethodSessionNew:
			writeMarker("session-new", "yes")
			if mode == "multi-auth-required" {
				return nil, &jsonrpc.RPCError{Code: client.ErrorCodeAuthRequired, Message: "Authentication required"}
			}
			if mode == "agent-auth-required" {
				if _, err := os.Stat(filepath.Join(markerDir, "authenticate")); err != nil {
					return nil, &jsonrpc.RPCError{Code: client.ErrorCodeAuthRequired, Message: "Authentication required"}
				}
			}
			if mode == "agent-auth-required-twice" {
				return nil, &jsonrpc.RPCError{Code: client.ErrorCodeAuthRequired, Message: "Authentication required"}
			}
			if mode == "terminal-auth-required" || mode == "terminal-auth-required-unconditional" {
				if _, err := os.Stat(filepath.Join(markerDir, "terminal-authenticated")); err != nil {
					return nil, &jsonrpc.RPCError{Code: client.ErrorCodeAuthRequired, Message: "Authentication required"}
				}
			}
			if mode == "session-failure" {
				return nil, &jsonrpc.RPCError{Code: -32001, Message: "helper session failure"}
			}
			return client.NewSessionResponse{
				SessionID: "discovery-session",
				ConfigOptions: []client.SessionConfigOption{{
					ID: "model", Name: "Model", Type: "select", Category: "model", CurrentValue: "sonnet",
					Options: []client.SessionConfigSelectOption{{Value: "sonnet", Name: "Sonnet"}, {Value: "opus", Name: "Opus"}},
				}, {
					ID: "effort", Name: "Effort", Type: "select", Category: "reasoning", CurrentValue: "high",
					Options: []client.SessionConfigSelectOption{{Value: "high", Name: "High"}},
				}},
			}, nil
		case client.MethodSessionSetConfig:
			var request client.SetSessionConfigOptionRequest
			if err := json.Unmarshal(msg.Params, &request); err != nil || request.SessionID != "discovery-session" || request.ConfigID != "model" || fmt.Sprint(request.Value) != "opus" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/set_config_option"}
			}
			writeMarker("model-selected", "opus")
			return client.SetSessionConfigOptionResponse{ConfigOptions: []client.SessionConfigOption{{
				ID: "model", Name: "Model", Type: "select", Category: "model", CurrentValue: "opus",
				Options: []client.SessionConfigSelectOption{{Value: "sonnet", Name: "Sonnet"}, {Value: "opus", Name: "Opus"}},
			}, {
				ID: "effort", Name: "Effort", Type: "select", Category: "reasoning", CurrentValue: "max",
				Options: []client.SessionConfigSelectOption{{Value: "max", Name: "Max"}},
			}}}, nil
		case client.MethodSessionClose:
			var request client.CloseSessionRequest
			if err := json.Unmarshal(msg.Params, &request); err != nil || request.SessionID != "discovery-session" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/close"}
			}
			writeMarker("session-close", "yes")
			if mode == "close-block" {
				select {}
			}
			return client.CloseSessionResponse{}, nil
		case client.MethodSessionPrompt:
			writeMarker("prompt", "unexpected")
			return nil, &jsonrpc.RPCError{Code: -32602, Message: "discovery must not prompt"}
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: fmt.Sprintf("unknown method %s", msg.Method)}
		}
	}, nil)
	writeMarker("process-exit", "yes")
	os.Exit(0)
}

func writeDiscoveryMarker(markerDir, name, value string) error {
	temp, err := os.CreateTemp(markerDir, "."+name+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.WriteString(value); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, filepath.Join(markerDir, name))
}
