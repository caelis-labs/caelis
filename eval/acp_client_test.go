//go:build e2e

package eval

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
)

func TestPublicClientLifecycleAndLoadE2E(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var (
		mu      sync.Mutex
		updates []client.UpdateEnvelope
	)
	acpClient := startE2EClient(ctx, t, e2eClientConfig{
		SessionRoot: filepath.Join(root, "sessions"),
		TaskRoot:    filepath.Join(root, "tasks"),
		Env: map[string]string{
			"SDK_ACP_STUB_REPLY": "client lifecycle ok",
		},
		OnUpdate: func(update client.UpdateEnvelope) {
			mu.Lock()
			defer mu.Unlock()
			updates = append(updates, update)
		},
	})
	defer acpClient.Close(ctx)

	if _, err := acpClient.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v; stderr=%q", err, acpClient.StderrTail(4096))
	}
	session, err := acpClient.NewSession(ctx, cwd, nil)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	resp, err := acpClient.Prompt(ctx, session.SessionID, "Reply with exactly: client lifecycle ok", nil)
	if err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if resp.StopReason == "" {
		t.Fatal("Prompt() returned empty stop reason")
	}
	if got := collectedUpdateKinds(updates); !containsAll(got, client.UpdateUserMessage, client.UpdateAgentMessage) {
		t.Fatalf("prompt update kinds = %v, want user+assistant", got)
	}
	_ = acpClient.Close(ctx)

	var replay []client.UpdateEnvelope
	reload := startE2EClient(ctx, t, e2eClientConfig{
		SessionRoot: filepath.Join(root, "sessions"),
		TaskRoot:    filepath.Join(root, "tasks"),
		Env: map[string]string{
			"SDK_ACP_STUB_REPLY": "client lifecycle ok",
		},
		OnUpdate: func(update client.UpdateEnvelope) {
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
	if got := collectedUpdateKinds(replay); !containsAll(got, client.UpdateUserMessage, client.UpdateAgentMessage) {
		t.Fatalf("replay update kinds = %v, want user+assistant", got)
	}
}

func TestPublicClientPermissionAndTerminalE2E(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var (
		mu                    sync.Mutex
		permissionCount       int
		terminalID            string
		displayTerminalOpen   bool
		displayTerminalOutput bool
		displayTerminalFinal  bool
		displayTerminalDone   bool
	)
	acpClient := startE2EClient(ctx, t, e2eClientConfig{
		SessionRoot: filepath.Join(root, "sessions"),
		TaskRoot:    filepath.Join(root, "tasks"),
		Env: map[string]string{
			"SDK_ACP_SCRIPTED_MODE": "approval_command",
		},
		OnPermissionRequest: func(_ context.Context, _ client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			mu.Lock()
			permissionCount++
			mu.Unlock()
			return client.RequestPermissionResponse{
				Outcome: client.PermissionOutcome{
					Outcome:  "selected",
					OptionID: "allow_once",
				},
			}, nil
		},
		OnUpdate: func(update client.UpdateEnvelope) {
			switch call := update.Update.(type) {
			case client.ToolCall:
				for _, content := range call.Content {
					if strings.TrimSpace(content.Type) == "terminal" && strings.TrimSpace(content.TerminalID) == "command-approval-1" {
						mu.Lock()
						displayTerminalOpen = true
						terminalID = strings.TrimSpace(content.TerminalID)
						mu.Unlock()
					}
				}
			case client.ToolCallUpdate:
				if strings.TrimSpace(call.ToolCallID) == "command-approval-1" && call.Status != nil && *call.Status == "completed" {
					mu.Lock()
					displayTerminalDone = true
					mu.Unlock()
				}
				for _, content := range call.Content {
					if content.Type == "terminal" && strings.TrimSpace(content.TerminalID) != "" {
						if strings.TrimSpace(content.TerminalID) == "command-approval-1" {
							mu.Lock()
							displayTerminalOpen = true
							terminalID = strings.TrimSpace(content.TerminalID)
							mu.Unlock()
							if text := clientTerminalContentText(content); strings.Contains(text, "child approval ok") {
								mu.Lock()
								displayTerminalOutput = true
								mu.Unlock()
							}
							if call.Status != nil && *call.Status == "completed" {
								mu.Lock()
								displayTerminalFinal = true
								mu.Unlock()
							}
						}
					}
				}
			}
		},
	})
	defer acpClient.Close(ctx)

	if _, err := acpClient.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v; stderr=%q", err, acpClient.StderrTail(4096))
	}
	session, err := acpClient.NewSession(ctx, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if _, err := acpClient.Prompt(ctx, session.SessionID, "Run the scripted approval command flow.", nil); err != nil {
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

	output, err := acpClient.TerminalOutput(ctx, session.SessionID, gotTerminalID)
	if err != nil {
		t.Fatalf("TerminalOutput() error = %v", err)
	}
	if !strings.Contains(output.Output, "child approval ok") {
		t.Fatalf("terminal output = %q, want child approval text", output.Output)
	}
	wait, err := acpClient.TerminalWaitForExit(ctx, session.SessionID, gotTerminalID)
	if err != nil {
		t.Fatalf("TerminalWaitForExit() error = %v", err)
	}
	if wait.ExitCode == nil || *wait.ExitCode != 0 {
		t.Fatalf("terminal exit = %#v, want exit code 0", wait)
	}
	if err := acpClient.TerminalRelease(ctx, session.SessionID, gotTerminalID); err != nil {
		t.Fatalf("TerminalRelease() error = %v", err)
	}
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		gotOpen := displayTerminalOpen
		gotOutput := displayTerminalOutput
		gotFinal := displayTerminalFinal
		gotDone := displayTerminalDone
		mu.Unlock()
		if gotOpen && gotOutput && gotFinal && gotDone {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("display terminal open=%v output=%v final=%v done=%v, want all true", gotOpen, gotOutput, gotFinal, gotDone)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func clientTerminalContentText(content client.ToolCallContent) string {
	switch typed := content.Content.(type) {
	case client.TextContent:
		return typed.Text
	case map[string]any:
		if typ, _ := typed["type"].(string); typ != "text" {
			return ""
		}
		text, _ := typed["text"].(string)
		return text
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		var decoded client.TextContent
		if err := json.Unmarshal(data, &decoded); err != nil || decoded.Type != "text" {
			return ""
		}
		return decoded.Text
	}
}

func TestPublicClientModeAndConfigE2E(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var updates []client.UpdateEnvelope
	acpClient := startE2EClient(ctx, t, e2eClientConfig{
		SessionRoot: filepath.Join(root, "sessions"),
		TaskRoot:    filepath.Join(root, "tasks"),
		Env: map[string]string{
			"SDK_ACP_SCRIPTED_MODE":      "mode_config",
			"SDK_ACP_ENABLE_MODE_CONFIG": "1",
		},
		OnUpdate: func(update client.UpdateEnvelope) {
			updates = append(updates, update)
		},
	})
	defer acpClient.Close(ctx)

	if _, err := acpClient.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v; stderr=%q", err, acpClient.StderrTail(4096))
	}
	session, err := acpClient.NewSession(ctx, t.TempDir(), nil)
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

	if err := acpClient.SetMode(ctx, session.SessionID, "plan"); err != nil {
		t.Fatalf("SetMode() error = %v", err)
	}
	configResp, err := acpClient.SetConfigOption(ctx, session.SessionID, "reasoning", "deep")
	if err != nil {
		t.Fatalf("SetConfigOption() error = %v", err)
	}
	if got := configResp.ConfigOptions[0].CurrentValue; got != "deep" {
		t.Fatalf("configResp.ConfigOptions[0].CurrentValue = %#v, want deep", got)
	}

	loadResp, err := acpClient.LoadSession(ctx, session.SessionID, t.TempDir(), nil)
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
	resp, err := acpClient.Prompt(ctx, session.SessionID, "Report current mode and reasoning effort.", nil)
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
	OnUpdate            func(client.UpdateEnvelope)
	OnPermissionRequest client.PermissionHandler
}

func startE2EClient(ctx context.Context, t *testing.T, cfg e2eClientConfig) *client.Client {
	t.Helper()
	env := map[string]string{
		"SDK_ACP_SESSION_ROOT": cfg.SessionRoot,
		"SDK_ACP_TASK_ROOT":    cfg.TaskRoot,
	}
	for k, v := range cfg.Env {
		env[k] = v
	}
	acpClient, err := client.Start(ctx, client.Config{
		Command:             "go",
		Args:                []string{"run", "./internal/acpe2eagent"},
		WorkDir:             repoRoot(t),
		Env:                 env,
		OnUpdate:            cfg.OnUpdate,
		OnPermissionRequest: cfg.OnPermissionRequest,
		ClientInfo: &client.Implementation{
			Name:    "sdk-acp-client-test",
			Version: "0.1.0",
		},
	})
	if err != nil {
		t.Fatalf("client.Start() error = %v", err)
	}
	return acpClient
}

func repoRoot(t *testing.T) string {
	t.Helper()
	return repoRootForEval(t)
}

func collectedUpdateKinds(updates []client.UpdateEnvelope) []string {
	kinds := make([]string, 0, len(updates))
	for _, update := range updates {
		switch one := update.Update.(type) {
		case client.ContentChunk:
			kinds = append(kinds, one.SessionUpdate)
		case client.ToolCall:
			kinds = append(kinds, one.SessionUpdate)
		case client.ToolCallUpdate:
			kinds = append(kinds, one.SessionUpdate)
		case client.PlanUpdate:
			kinds = append(kinds, one.SessionUpdate)
		case client.AvailableCommandsUpdate:
			kinds = append(kinds, one.SessionUpdate)
		case client.CurrentModeUpdate:
			kinds = append(kinds, one.SessionUpdate)
		case client.ConfigOptionUpdate:
			kinds = append(kinds, one.SessionUpdate)
		case client.SessionInfoUpdate:
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

func latestAgentText(updates []client.UpdateEnvelope) string {
	for i := len(updates) - 1; i >= 0; i-- {
		chunk, ok := updates[i].Update.(client.ContentChunk)
		if !ok || chunk.SessionUpdate != client.UpdateAgentMessage {
			continue
		}
		var text client.TextChunk
		if err := json.Unmarshal(chunk.Content, &text); err != nil {
			continue
		}
		if strings.TrimSpace(text.Text) != "" {
			return strings.TrimSpace(text.Text)
		}
	}
	return ""
}
