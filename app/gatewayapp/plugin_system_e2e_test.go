package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

const (
	pluginE2ESkillMarker  = "Use for Caelis plugin system E2E validation."
	pluginE2EHookMarker   = "CAELIS_PLUGIN_E2E_HOOK_CONTEXT"
	pluginE2EFinalMessage = "CAELIS_PLUGIN_E2E_OK"
)

func TestPluginSkillAndSessionStartHookReachModel(t *testing.T) {
	tmp := t.TempDir()
	setHomeForGatewayAppTest(t, filepath.Join(tmp, "home"))
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	pluginRoot := filepath.Join(tmp, "caelis-e2e-plugin")
	writePluginSystemE2EPlugin(t, pluginRoot)
	base := AppConfig{Runtime: RuntimeConfig{ApprovalMode: "manual"}, Plugins: []PluginConfig{{
		ID:          "caelis-e2e-plugin",
		Name:        "caelis-e2e-plugin",
		Version:     "1.0.0",
		Description: "Caelis plugin system E2E plugin",
		Root:        pluginRoot,
		Enabled:     true,
	}}}
	if err := newAppConfigStore(storeDir).Save(base); err != nil {
		t.Fatalf("save temp config: %v", err)
	}

	fakeProvider := newGatewayTestHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var skill, hook bool
		for _, message := range request.Messages {
			text := string(message.Content)
			skill = skill || ((message.Role == "system" || message.Role == "developer") && strings.Contains(text, pluginE2ESkillMarker))
			hook = hook || (message.Role == "user" && strings.Contains(text, pluginE2EHookMarker))
		}
		if !skill || !hook {
			t.Errorf("model context missing plugin contributions: skill=%t hook=%t", skill, hook)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writePluginSystemE2ESSE(w, map[string]any{"id": "plugin-context", "object": "chat.completion.chunk", "model": "plugin-e2e-model", "choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": pluginE2EFinalMessage}, "finish_reason": "stop"}}})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(fakeProvider.Close)
	stack, err := newGatewayAppTestStack(t, Config{
		AppName:      "caelis-e2e",
		UserID:       "plugin-e2e",
		StoreDir:     storeDir,
		WorkspaceKey: "plugin-e2e-workspace",
		WorkspaceCWD: workspaceDir,
		ApprovalMode: "manual",
		SkillDirs:    []string{filepath.Join(tmp, "skills")},
		Sandbox:      SandboxConfig{RequestedType: "host"},
		Model: ModelConfig{
			Alias:               "plugin-e2e",
			Provider:            "openai-compatible",
			API:                 model.APIOpenAICompatible,
			Model:               "plugin-e2e-model",
			BaseURL:             fakeProvider.URL,
			HTTPClient:          fakeProvider.Client(),
			Token:               "plugin-e2e-token",
			AuthType:            model.AuthBearerToken,
			ContextWindowTokens: 128000,
			MaxOutputTok:        1024,
			Timeout:             5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack(plugin fixture) error = %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	active, err := startGatewayAppTestSession(ctx, stack, "")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	result, err := runHeadlessOnceForGatewayAppTest(ctx, stack, active, "plugin-e2e", "Read my plugin context.", headless.Options{})
	if err != nil {
		t.Fatalf("headless RunSessionOnce() error = %v", err)
	}
	if strings.TrimSpace(result.Output) != pluginE2EFinalMessage {
		t.Fatalf("headless output = %q, want %q", result.Output, pluginE2EFinalMessage)
	}
	events, err := stack.composition.sessions.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	hookEvents := 0
	for _, event := range events {
		if event.Meta["source"] != "plugin_hook" {
			continue
		}
		hookEvents++
		msg, ok := session.ModelMessageOf(event)
		if !ok || msg.Role != model.RoleUser || !strings.Contains(msg.TextContent(), pluginE2EHookMarker) {
			t.Fatalf("plugin context did not project to a user message carrying hook output: %+v", event)
		}
	}
	if hookEvents != 1 {
		t.Fatalf("hook events = %d, want 1", hookEvents)
	}
}

func writePluginSystemE2EPlugin(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".caelis-plugin"), 0o700); err != nil {
		t.Fatalf("mkdir plugin metadata: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "e2e-skill"), 0o700); err != nil {
		t.Fatalf("mkdir plugin skill: %v", err)
	}
	hookCommand, hookArgs := "/bin/echo", []string{pluginE2EHookMarker}
	if runtime.GOOS == "windows" {
		hookCommand, hookArgs = "cmd.exe", []string{"/d", "/c", "echo", pluginE2EHookMarker}
	}
	args, err := json.Marshal(hookArgs)
	if err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{
  "name": "caelis-e2e-plugin",
  "version": "1.0.0",
  "description": "Caelis plugin system E2E plugin",
  "skills": [{"root": "skills", "namespace": "e2e"}],
  "hooks": {
    "SessionStart": [{
      "command": %q,
      "args": %s
    }]
  }
}`, hookCommand, args)
	if err := os.WriteFile(filepath.Join(root, ".caelis-plugin", "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	skill := fmt.Sprintf("---\nname: e2e-skill\ndescription: Use for Caelis plugin system E2E validation.\n---\n# E2E Skill\n\n%s\n", pluginE2ESkillMarker)
	if err := os.WriteFile(filepath.Join(root, "skills", "e2e-skill", "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatalf("write plugin skill: %v", err)
	}
}

func writePluginSystemE2ESSE(w http.ResponseWriter, payload map[string]any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("marshal e2e SSE payload: %v", err))
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
