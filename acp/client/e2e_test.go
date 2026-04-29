//go:build e2e

package client_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	sdkacpclient "github.com/OnslaughtSnail/caelis/acp/client"
)

func TestPublicClientLifecycleAndLoadE2E(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var (
		mu      sync.Mutex
		updates []sdkacpclient.UpdateEnvelope
	)
	client := startE2EClient(ctx, t, e2eClientConfig{
		SessionRoot: filepath.Join(root, "sessions"),
		TaskRoot:    filepath.Join(root, "tasks"),
		Env: map[string]string{
			"SDK_ACP_STUB_REPLY": "client lifecycle ok",
		},
		OnUpdate: func(update sdkacpclient.UpdateEnvelope) {
			mu.Lock()
			defer mu.Unlock()
			updates = append(updates, update)
		},
	})
	defer client.Close(ctx)

	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v; stderr=%q", err, client.StderrTail(4096))
	}
	session, err := client.NewSession(ctx, cwd, nil)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	resp, err := client.Prompt(ctx, session.SessionID, "Reply with exactly: client lifecycle ok", nil)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if resp.StopReason == "" {
		t.Fatal("Prompt() returned empty stop reason")
	}
	if got := collectedUpdateKinds(updates); !containsAll(got, sdkacpclient.UpdateUserMessage, sdkacpclient.UpdateAgentMessage) {
		t.Fatalf("prompt update kinds = %v, want user+assistant", got)
	}
	_ = client.Close(ctx)

	var replay []sdkacpclient.UpdateEnvelope
	reload := startE2EClient(ctx, t, e2eClientConfig{
		SessionRoot: filepath.Join(root, "sessions"),
		TaskRoot:    filepath.Join(root, "tasks"),
		Env: map[string]string{
			"SDK_ACP_STUB_REPLY": "client lifecycle ok",
		},
		OnUpdate: func(update sdkacpclient.UpdateEnvelope) {
			replay = append(replay, update)
		},
	})
	defer reload.Close(ctx)
	if _, err := reload.Initialize(ctx); err != nil {
		t.Fatalf("reload Initialize() error = %v; stderr=%q", err, reload.StderrTail(4096))
	}
	if _, err := reload.LoadSession(ctx, session.SessionID, cwd, nil); err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got := collectedUpdateKinds(replay); !containsAll(got, sdkacpclient.UpdateUserMessage, sdkacpclient.UpdateAgentMessage) {
		t.Fatalf("replay update kinds = %v, want user+assistant", got)
	}
}

func TestPublicClientPermissionAndTerminalE2E(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var (
		mu              sync.Mutex
		permissionCount int
		terminalID      string
	)
	client := startE2EClient(ctx, t, e2eClientConfig{
		SessionRoot: filepath.Join(root, "sessions"),
		TaskRoot:    filepath.Join(root, "tasks"),
		Env: map[string]string{
			"SDK_ACP_SCRIPTED_MODE": "approval_bash",
		},
		OnPermissionRequest: func(_ context.Context, _ sdkacpclient.RequestPermissionRequest) (sdkacpclient.RequestPermissionResponse, error) {
			mu.Lock()
			permissionCount++
			mu.Unlock()
			return sdkacpclient.RequestPermissionResponse{
				Outcome: sdkacpclient.PermissionOutcome{
					Outcome:  "selected",
					OptionID: "allow_once",
				},
			}, nil
		},
		OnUpdate: func(update sdkacpclient.UpdateEnvelope) {
			call, ok := update.Update.(sdkacpclient.ToolCallUpdate)
			if !ok {
				return
			}
			for _, content := range call.Content {
				if content.Type == "terminal" && strings.TrimSpace(content.TerminalID) != "" {
					mu.Lock()
					terminalID = strings.TrimSpace(content.TerminalID)
					mu.Unlock()
					return
				}
			}
		},
	})
	defer client.Close(ctx)

	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v; stderr=%q", err, client.StderrTail(4096))
	}
	session, err := client.NewSession(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := client.Prompt(ctx, session.SessionID, "Run the scripted approval bash flow.", nil); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}

	mu.Lock()
	gotTerminalID := terminalID
	gotPermissionCount := permissionCount
	mu.Unlock()
	if gotPermissionCount != 1 {
		t.Fatalf("permission requests = %d, want 1", gotPermissionCount)
	}
	if gotTerminalID == "" {
		t.Fatal("missing terminal id from tool_call_update content")
	}

	output, err := client.TerminalOutput(ctx, session.SessionID, gotTerminalID)
	if err != nil {
		t.Fatalf("TerminalOutput() error = %v", err)
	}
	if !strings.Contains(output.Output, "child approval ok") {
		t.Fatalf("terminal output = %q, want child approval text", output.Output)
	}
	wait, err := client.TerminalWaitForExit(ctx, session.SessionID, gotTerminalID)
	if err != nil {
		t.Fatalf("TerminalWaitForExit() error = %v", err)
	}
	if wait.ExitCode == nil || *wait.ExitCode != 0 {
		t.Fatalf("terminal exit = %#v, want exit code 0", wait)
	}
	if err := client.TerminalRelease(ctx, session.SessionID, gotTerminalID); err != nil {
		t.Fatalf("TerminalRelease() error = %v", err)
	}
}

func TestPublicClientModeAndConfigE2E(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var updates []sdkacpclient.UpdateEnvelope
	client := startE2EClient(ctx, t, e2eClientConfig{
		SessionRoot: filepath.Join(root, "sessions"),
		TaskRoot:    filepath.Join(root, "tasks"),
		Env: map[string]string{
			"SDK_ACP_SCRIPTED_MODE":      "mode_config",
			"SDK_ACP_ENABLE_MODE_CONFIG": "1",
		},
		OnUpdate: func(update sdkacpclient.UpdateEnvelope) {
			updates = append(updates, update)
		},
	})
	defer client.Close(ctx)

	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v; stderr=%q", err, client.StderrTail(4096))
	}
	session, err := client.NewSession(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if session.Modes == nil || session.Modes.CurrentModeID != "default" {
		t.Fatalf("session.Modes = %#v, want default assembly mode state", session.Modes)
	}
	if got, want := len(session.ConfigOptions), 1; got != want {
		t.Fatalf("len(session.ConfigOptions) = %d, want %d", got, want)
	}
	if got := session.ConfigOptions[0].CurrentValue; got != "balanced" {
		t.Fatalf("session.ConfigOptions[0].CurrentValue = %#v, want balanced", got)
	}

	if err := client.SetMode(ctx, session.SessionID, "plan"); err != nil {
		t.Fatalf("SetMode() error = %v", err)
	}
	configResp, err := client.SetConfigOption(ctx, session.SessionID, "reasoning", "deep")
	if err != nil {
		t.Fatalf("SetConfigOption() error = %v", err)
	}
	if got := configResp.ConfigOptions[0].CurrentValue; got != "deep" {
		t.Fatalf("configResp.ConfigOptions[0].CurrentValue = %#v, want deep", got)
	}

	loadResp, err := client.LoadSession(ctx, session.SessionID, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if loadResp.Modes == nil || loadResp.Modes.CurrentModeID != "plan" {
		t.Fatalf("loadResp.Modes = %#v, want current mode plan", loadResp.Modes)
	}
	if got, want := len(loadResp.ConfigOptions), 1; got != want {
		t.Fatalf("len(loadResp.ConfigOptions) = %d, want %d", got, want)
	}
	if got := loadResp.ConfigOptions[0].CurrentValue; got != "deep" {
		t.Fatalf("loadResp.ConfigOptions[0].CurrentValue = %#v, want deep", got)
	}

	updates = nil
	resp, err := client.Prompt(ctx, session.SessionID, "Report current mode and reasoning effort.", nil)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if resp.StopReason == "" {
		t.Fatal("Prompt() returned empty stop reason")
	}
	if got := latestAgentText(updates); got != "mode=plan effort=high" {
		t.Fatalf("latest agent text = %q, want %q", got, "mode=plan effort=high")
	}
}

type e2eClientConfig struct {
	SessionRoot         string
	TaskRoot            string
	Env                 map[string]string
	OnUpdate            func(sdkacpclient.UpdateEnvelope)
	OnPermissionRequest sdkacpclient.PermissionHandler
}

func startE2EClient(ctx context.Context, t *testing.T, cfg e2eClientConfig) *sdkacpclient.Client {
	t.Helper()
	env := map[string]string{
		"SDK_ACP_SESSION_ROOT": cfg.SessionRoot,
		"SDK_ACP_TASK_ROOT":    cfg.TaskRoot,
	}
	for k, v := range cfg.Env {
		env[k] = v
	}
	client, err := sdkacpclient.Start(ctx, sdkacpclient.Config{
		Command:             "go",
		Args:                []string{"run", "./acpbridge/cmd/e2eagent"},
		WorkDir:             repoRoot(t),
		Env:                 env,
		OnUpdate:            cfg.OnUpdate,
		OnPermissionRequest: cfg.OnPermissionRequest,
		ClientInfo: &sdkacpclient.Implementation{
			Name:    "sdk-acp-client-test",
			Version: "0.1.0",
		},
	})
	if err != nil {
		t.Fatalf("client.Start() error = %v", err)
	}
	return client
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func collectedUpdateKinds(updates []sdkacpclient.UpdateEnvelope) []string {
	kinds := make([]string, 0, len(updates))
	for _, update := range updates {
		switch one := update.Update.(type) {
		case sdkacpclient.ContentChunk:
			kinds = append(kinds, one.SessionUpdate)
		case sdkacpclient.ToolCall:
			kinds = append(kinds, one.SessionUpdate)
		case sdkacpclient.ToolCallUpdate:
			kinds = append(kinds, one.SessionUpdate)
		case sdkacpclient.PlanUpdate:
			kinds = append(kinds, one.SessionUpdate)
		case sdkacpclient.AvailableCommandsUpdate:
			kinds = append(kinds, one.SessionUpdate)
		case sdkacpclient.CurrentModeUpdate:
			kinds = append(kinds, one.SessionUpdate)
		case sdkacpclient.ConfigOptionUpdate:
			kinds = append(kinds, one.SessionUpdate)
		case sdkacpclient.SessionInfoUpdate:
			kinds = append(kinds, one.SessionUpdate)
		}
	}
	return kinds
}

func containsAll(items []string, want ...string) bool {
	for _, one := range want {
		found := false
		for _, item := range items {
			if item == one {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func latestAgentText(updates []sdkacpclient.UpdateEnvelope) string {
	for i := len(updates) - 1; i >= 0; i-- {
		chunk, ok := updates[i].Update.(sdkacpclient.ContentChunk)
		if !ok || chunk.SessionUpdate != sdkacpclient.UpdateAgentMessage {
			continue
		}
		var text sdkacpclient.TextChunk
		if err := json.Unmarshal(chunk.Content, &text); err != nil {
			continue
		}
		if strings.TrimSpace(text.Text) != "" {
			return strings.TrimSpace(text.Text)
		}
	}
	return ""
}
