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
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestDiscoverUsesTemporarySessionAndCleansUpProcess(t *testing.T) {
	markerDir := t.TempDir()
	connection := helperConnection(markerDir, "catalog")
	snapshot, err := (Service{Clock: func() time.Time { return time.Unix(123, 0) }}).Discover(context.Background(), connection, markerDir, "")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if snapshot.ConnectionID != "claude" || snapshot.CurrentModelID != "sonnet" {
		t.Fatalf("Discover() = %#v", snapshot)
	}
	if snapshot.ModelControl.Kind != controlagents.ModelControlConfigOption || len(snapshot.Models) != 2 {
		t.Fatalf("Discover() model catalog = %#v %#v", snapshot.ModelControl, snapshot.Models)
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

func TestDiscoverSelectedModelCapturesModelScopedOptions(t *testing.T) {
	markerDir := t.TempDir()
	snapshot, err := (Service{}).Discover(context.Background(), helperConnection(markerDir, "catalog"), markerDir, "opus")
	if err != nil {
		t.Fatalf("Discover(selected model) error = %v", err)
	}
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

func TestDiscoverContinuesWhenAuthenticationMethodsAreAdvertised(t *testing.T) {
	markerDir := t.TempDir()
	snapshot, err := (Service{}).Discover(context.Background(), helperConnection(markerDir, "auth"), markerDir, "")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if snapshot.CurrentModelID != "sonnet" {
		t.Fatalf("Discover() = %#v, want catalog returned by session/new", snapshot)
	}
	for _, marker := range []string{"session-new", "session-close", "process-exit"} {
		if _, err := os.Stat(filepath.Join(markerDir, marker)); err != nil {
			t.Fatalf("missing marker %q: %v", marker, err)
		}
	}
}

func TestDiscoverAuthenticatesDeclaredAgentMethodAndRetriesSessionNew(t *testing.T) {
	markerDir := t.TempDir()
	snapshot, err := (Service{}).Discover(context.Background(), helperConnection(markerDir, "agent-auth-required"), markerDir, "")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if snapshot.Authentication != (controlagents.Authentication{MethodID: "agent-login", Type: controlagents.AuthenticationAgent}) {
		t.Fatalf("authentication = %#v", snapshot.Authentication)
	}
	for _, marker := range []string{"authenticate", "session-close", "process-exit"} {
		if _, statErr := os.Stat(filepath.Join(markerDir, marker)); statErr != nil {
			t.Fatalf("missing marker %q: %v", marker, statErr)
		}
	}
}

func TestDiscoverReturnsRepeatedAuthRequiredWithoutLooping(t *testing.T) {
	t.Parallel()

	markerDir := t.TempDir()
	_, err := (Service{}).Discover(
		context.Background(),
		helperConnection(markerDir, "agent-auth-required-twice"),
		markerDir,
		"",
	)
	if err == nil {
		t.Fatal("Discover() error = nil for repeated auth_required")
	}
	for _, want := range []string{"retry authenticated operation", "Authentication required"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Discover() error = %q, want %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(markerDir, "authenticate")); statErr != nil {
		t.Fatalf("declared method was not attempted once: %v", statErr)
	}
}

func TestDiscoverRunsDeclaredTerminalMethodThenReconnects(t *testing.T) {
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
	snapshot, err := (Service{}).Discover(ctx, connection, markerDir, "")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if snapshot.Authentication != (controlagents.Authentication{MethodID: "terminal-login", Type: controlagents.AuthenticationTerminal}) {
		t.Fatalf("authentication = %#v", snapshot.Authentication)
	}
	for _, marker := range []string{"terminal-capability", "terminal-authenticated", "session-close", "process-exit"} {
		if _, statErr := os.Stat(filepath.Join(markerDir, marker)); statErr != nil {
			t.Fatalf("missing marker %q: %v", marker, statErr)
		}
	}
}

func TestDiscoverTerminalAuthenticationCancellationClosesClientOnce(t *testing.T) {
	markerDir := t.TempDir()
	connection := helperConnection(markerDir, "terminal-auth-required")
	ctx := controlagents.WithTerminalAuthentication(context.Background(), func(
		context.Context,
		controlagents.TerminalAuthenticationRequest,
	) error {
		return errors.New("terminal login cancelled")
	})

	_, err := (Service{}).Discover(ctx, connection, markerDir, "")
	if err == nil {
		t.Fatal("Discover() error = nil after terminal authentication cancellation")
	}
	if !strings.Contains(err.Error(), "terminal login cancelled") {
		t.Fatalf("Discover() error = %q, want terminal cancellation", err)
	}
	if strings.Contains(err.Error(), "Wait was already called") {
		t.Fatalf("Discover() closed the same ACP process twice: %v", err)
	}
}

func TestDiscoverPreservesNonAuthenticationSessionFailure(t *testing.T) {
	markerDir := t.TempDir()
	_, err := (Service{}).Discover(context.Background(), helperConnection(markerDir, "session-failure"), markerDir, "")
	if err == nil {
		t.Fatal("Discover() error = nil, want session/new failure")
	}
	for _, want := range []string{"create discovery session", "helper session failure"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Discover() error = %q, want original session/new failure containing %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(markerDir, "process-exit")); statErr != nil {
		t.Fatalf("process was not cleaned up after session/new failure: %v", statErr)
	}
}

func TestDiscoverCancellationTerminatesProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kill -0 liveness probe is Unix-specific")
	}
	markerDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := (Service{}).Discover(ctx, helperConnection(markerDir, "block"), markerDir, "")
		result <- err
	}()
	waitForDiscoveryMarker(t, filepath.Join(markerDir, "initialize-ready"), 3*time.Second)
	pid := readDiscoveryHelperPID(t, filepath.Join(markerDir, "pid"))
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Discover() error = nil after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Discover() did not return after cancellation")
	}
	waitForDiscoveryHelperExit(t, pid, 2*time.Second)
}

func TestDiscoverBoundsUnresponsiveSessionCloseAndTerminatesProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("kill -0 liveness probe is Unix-specific")
	}
	markerDir := t.TempDir()
	started := time.Now()
	_, err := (Service{CleanupTimeout: 50 * time.Millisecond}).Discover(
		context.Background(),
		helperConnection(markerDir, "close-block"),
		markerDir,
		"",
	)
	if err == nil || !strings.Contains(err.Error(), "close discovery session") || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("Discover() error = %v, want bounded session/close failure", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Discover() elapsed = %v, want bounded cleanup", elapsed)
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
			t.Fatalf("helper process %d is still alive after Discover returned", pid)
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
			if mode == "terminal-auth-required" {
				var request client.InitializeRequest
				if err := json.Unmarshal(msg.Params, &request); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
				}
				auth, _ := request.ClientCapabilities["auth"].(map[string]any)
				if auth["terminal"] != true {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "terminal auth capability missing"}
				}
				writeMarker("terminal-capability", "yes")
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
			if mode == "agent-auth-required" {
				if _, err := os.Stat(filepath.Join(markerDir, "authenticate")); err != nil {
					return nil, &jsonrpc.RPCError{Code: client.ErrorCodeAuthRequired, Message: "Authentication required"}
				}
			}
			if mode == "agent-auth-required-twice" {
				return nil, &jsonrpc.RPCError{Code: client.ErrorCodeAuthRequired, Message: "Authentication required"}
			}
			if mode == "terminal-auth-required" {
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
