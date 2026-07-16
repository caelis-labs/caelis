package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func TestDiscoverPreservesSessionNewAuthenticationFailure(t *testing.T) {
	markerDir := t.TempDir()
	_, err := (Service{}).Discover(context.Background(), helperConnection(markerDir, "auth-failure"), markerDir, "")
	if err == nil {
		t.Fatal("Discover() error = nil, want session/new authentication failure")
	}
	for _, want := range []string{"create discovery session", "authentication required by helper"} {
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
	pidPath := filepath.Join(markerDir, "pid")
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidPath)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		cancel()
		t.Fatal("helper process did not publish its pid")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Discover() error = nil after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Discover() did not return after cancellation")
	}
	for time.Now().Before(deadline) {
		if err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run(); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper process %d is still alive after Discover returned", pid)
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
	rawPID, readErr := os.ReadFile(filepath.Join(markerDir, "pid"))
	if readErr != nil {
		t.Fatalf("read helper pid: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if parseErr != nil || pid <= 0 {
		t.Fatalf("helper pid = %q, %v", rawPID, parseErr)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run(); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper process %d is still alive after bounded session/close cleanup", pid)
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
		_ = os.WriteFile(filepath.Join(markerDir, name), []byte(value), 0o600)
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	_ = conn.Serve(context.Background(), func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case client.MethodInitialize:
			if mode == "block" {
				writeMarker("pid", strconv.Itoa(os.Getpid()))
				select {}
			}
			response := client.InitializeResponse{ProtocolVersion: 1, AgentCapabilities: schema.AgentCapabilities{SessionCapabilities: map[string]json.RawMessage{"close": json.RawMessage(`{}`)}}}
			if mode == "auth" || mode == "auth-failure" {
				response.AuthMethods = []json.RawMessage{json.RawMessage(`{"id":"login"}`)}
			}
			return response, nil
		case client.MethodSessionNew:
			writeMarker("session-new", "yes")
			if mode == "auth-failure" {
				return nil, &jsonrpc.RPCError{Code: -32001, Message: "authentication required by helper"}
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
				writeMarker("pid", strconv.Itoa(os.Getpid()))
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
