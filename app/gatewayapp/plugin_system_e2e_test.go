package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/surfaces/headless"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	pluginE2EHookEnv      = "CAELIS_PLUGIN_SYSTEM_E2E_HOOK"
	pluginE2EMCPEnv       = "CAELIS_PLUGIN_SYSTEM_E2E_MCP"
	pluginE2ESkillMarker  = "Use for Caelis plugin system E2E validation."
	pluginE2EHookMarker   = "CAELIS_PLUGIN_E2E_HOOK_CONTEXT"
	pluginE2EMCPMarker    = "CAELIS_PLUGIN_E2E_MCP_RESULT"
	pluginE2EToolName     = "mcp__caelis_e2e_plugin__demo__read_e2e_fixture"
	pluginE2EFinalMessage = "CAELIS_PLUGIN_E2E_OK"
)

func TestRealConfigPluginSystemE2E(t *testing.T) {
	if os.Getenv("CAELIS_REAL_PLUGIN_E2E") != "1" {
		t.Skip("set CAELIS_REAL_PLUGIN_E2E=1 to run the real-config plugin system E2E")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	sourceConfig := filepath.Join(home, ".caelis", "config.json")
	rawConfig, err := os.ReadFile(sourceConfig)
	if err != nil {
		t.Fatalf("read %s: %v", sourceConfig, err)
	}
	var base AppConfig
	if err := json.Unmarshal(rawConfig, &base); err != nil {
		t.Fatalf("decode %s: %v", sourceConfig, err)
	}

	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	workspaceDir := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	pluginRoot := filepath.Join(tmp, "caelis-e2e-plugin")
	writePluginSystemE2EPlugin(t, pluginRoot)
	base.Plugins = append(base.Plugins, PluginConfig{
		ID:          "caelis-e2e-plugin",
		Name:        "caelis-e2e-plugin",
		Version:     "1.0.0",
		Description: "Caelis plugin system E2E plugin",
		Root:        pluginRoot,
		Enabled:     true,
	})
	if err := newAppConfigStore(storeDir).Save(base); err != nil {
		t.Fatalf("save temp config: %v", err)
	}

	fakeProvider := newPluginSystemE2EProvider(t)
	stack, err := NewLocalStack(Config{
		AppName:      "caelis-e2e",
		UserID:       "plugin-e2e",
		StoreDir:     storeDir,
		WorkspaceKey: "plugin-e2e-workspace",
		WorkspaceCWD: workspaceDir,
		Sandbox:      SandboxConfig{RequestedType: "host"},
		Model: ModelConfig{
			Alias:               "plugin-e2e",
			Provider:            "openai-compatible",
			API:                 providers.APIOpenAICompatible,
			Model:               "plugin-e2e-model",
			BaseURL:             fakeProvider.URL,
			HTTPClient:          fakeProvider.Client(),
			Token:               "plugin-e2e-token",
			AuthType:            providers.AuthBearerToken,
			ContextWindowTokens: 128000,
			MaxOutputTok:        1024,
			Timeout:             5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack(real config copy + e2e plugin) error = %v", err)
	}
	defer stack.Close()

	list, err := stack.Plugins().List(context.Background())
	if err != nil {
		t.Fatalf("Plugins().List() error = %v", err)
	}
	if !pluginSystemE2EPluginActive(list) {
		t.Fatalf("e2e plugin not active in list: %+v", list)
	}

	session, err := stack.StartSession(context.Background(), "", "plugin-e2e")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	result, err := runHeadlessOnceForGatewayAppTest(context.Background(), stack, session, "plugin-e2e", "Run the Caelis plugin system E2E. Use the available MCP tool.", headless.Options{})
	if err != nil {
		t.Fatalf("headless RunOnce() error = %v", err)
	}
	if strings.TrimSpace(result.Output) != pluginE2EFinalMessage {
		t.Fatalf("RunOnce output = %q, want %q; provider=%s; events=%s", result.Output, pluginE2EFinalMessage, fakeProvider.Summary(), pluginSystemE2EEventSummary(t, stack, session.SessionRef))
	}
	fakeProvider.Assert(t, pluginSystemE2EEventSummary(t, stack, session.SessionRef))
}

func TestPluginSystemE2EHookHelperProcess(t *testing.T) {
	if os.Getenv(pluginE2EHookEnv) != "1" {
		return
	}
	fmt.Print(pluginE2EHookMarker)
	os.Exit(0)
}

func TestPluginSystemE2EMCPServerHelperProcess(t *testing.T) {
	if os.Getenv(pluginE2EMCPEnv) != "1" {
		return
	}
	type args struct {
		Name string `json:"name"`
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "caelis-plugin-e2e", Version: "1.0.0"}, nil)
	mcpsdk.AddTool[args, any](server, &mcpsdk.Tool{
		Name:        "read_e2e_fixture",
		Description: "Reads the plugin E2E fixture.",
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input args) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: pluginE2EMCPMarker + ":" + input.Name}},
		}, nil, nil
	})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func writePluginSystemE2EPlugin(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".caelis-plugin"), 0o700); err != nil {
		t.Fatalf("mkdir plugin metadata: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "e2e-skill"), 0o700); err != nil {
		t.Fatalf("mkdir plugin skill: %v", err)
	}
	manifest := fmt.Sprintf(`{
  "name": "caelis-e2e-plugin",
  "version": "1.0.0",
  "description": "Caelis plugin system E2E plugin",
  "skills": [{"root": "skills", "namespace": "e2e"}],
  "hooks": {
    "SessionStart": [{
      "command": %q,
      "args": ["-test.run=^TestPluginSystemE2EHookHelperProcess$"],
      "env": {%q: "1"}
    }]
  },
  "mcpServers": {
    "demo": {
      "command": %q,
      "args": ["-test.run=^TestPluginSystemE2EMCPServerHelperProcess$"],
      "env": {%q: "1"}
    }
  }
}`, os.Args[0], pluginE2EHookEnv, os.Args[0], pluginE2EMCPEnv)
	if err := os.WriteFile(filepath.Join(root, ".caelis-plugin", "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	skill := fmt.Sprintf("---\nname: e2e-skill\ndescription: Use for Caelis plugin system E2E validation.\n---\n# E2E Skill\n\n%s\n", pluginE2ESkillMarker)
	if err := os.WriteFile(filepath.Join(root, "skills", "e2e-skill", "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatalf("write plugin skill: %v", err)
	}
}

type pluginSystemE2EProvider struct {
	*httptest.Server
	mu                  sync.Mutex
	calls               int
	payloadSummaries    []string
	sawSkill            bool
	sawHook             bool
	sawToolSearch       bool
	sawTool             bool
	sawToolBeforeSearch bool
	sawToolResult       bool
	sawAuthorization    bool
}

func newPluginSystemE2EProvider(t *testing.T) *pluginSystemE2EProvider {
	t.Helper()
	provider := &pluginSystemE2EProvider{}
	provider.Server = httptest.NewServer(http.HandlerFunc(provider.handle))
	t.Cleanup(provider.Close)
	return provider
}

func (p *pluginSystemE2EProvider) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/chat/completions" {
		http.NotFound(w, r)
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.calls++
	callIndex := p.calls
	if strings.TrimSpace(r.Header.Get("Authorization")) == "Bearer plugin-e2e-token" {
		p.sawAuthorization = true
	}
	p.observePayload(callIndex, payload)
	p.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	if callIndex == 1 {
		writePluginSystemE2ESSE(w, map[string]any{
			"id":     "plugin-e2e-1",
			"object": "chat.completion.chunk",
			"model":  "plugin-e2e-model",
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"role":    "assistant",
					"content": "Searching plugin MCP.",
					"tool_calls": []map[string]any{{
						"index": 0,
						"id":    "call_plugin_e2e_search",
						"type":  "function",
						"function": map[string]any{
							"name":      "tool_search",
							"arguments": `{"query":"read e2e fixture"}`,
						},
					}},
				},
				"finish_reason": nil,
			}},
		})
		writePluginSystemE2ESSE(w, map[string]any{
			"id":     "plugin-e2e-1",
			"object": "chat.completion.chunk",
			"model":  "plugin-e2e-model",
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{
				"prompt_tokens":     11,
				"completion_tokens": 7,
				"total_tokens":      18,
			},
		})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	if callIndex == 2 {
		writePluginSystemE2ESSE(w, map[string]any{
			"id":     "plugin-e2e-2",
			"object": "chat.completion.chunk",
			"model":  "plugin-e2e-model",
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{
					"role":    "assistant",
					"content": "Using plugin MCP.",
					"tool_calls": []map[string]any{{
						"index": 0,
						"id":    "call_plugin_e2e",
						"type":  "function",
						"function": map[string]any{
							"name":      pluginE2EToolName,
							"arguments": `{"name":"fixture"}`,
						},
					}},
				},
				"finish_reason": nil,
			}},
		})
		writePluginSystemE2ESSE(w, map[string]any{
			"id":     "plugin-e2e-2",
			"object": "chat.completion.chunk",
			"model":  "plugin-e2e-model",
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{
				"prompt_tokens":     13,
				"completion_tokens": 7,
				"total_tokens":      20,
			},
		})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	writePluginSystemE2ESSE(w, map[string]any{
		"id":     "plugin-e2e-3",
		"object": "chat.completion.chunk",
		"model":  "plugin-e2e-model",
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{
				"role":    "assistant",
				"content": pluginE2EFinalMessage,
			},
			"finish_reason": nil,
		}},
	})
	writePluginSystemE2ESSE(w, map[string]any{
		"id":     "plugin-e2e-3",
		"object": "chat.completion.chunk",
		"model":  "plugin-e2e-model",
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     13,
			"completion_tokens": 5,
			"total_tokens":      18,
		},
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func (p *pluginSystemE2EProvider) observePayload(callIndex int, payload map[string]any) {
	raw, _ := json.Marshal(payload)
	text := string(raw)
	p.payloadSummaries = append(p.payloadSummaries, summarizePluginSystemE2EPayload(payload))
	if strings.Contains(text, pluginE2ESkillMarker) {
		p.sawSkill = true
	}
	if strings.Contains(text, pluginE2EHookMarker) {
		p.sawHook = true
	}
	if strings.Contains(text, pluginE2EMCPMarker) {
		p.sawToolResult = true
	}
	if tools, _ := payload["tools"].([]any); len(tools) > 0 {
		for _, item := range tools {
			toolMap, _ := item.(map[string]any)
			fn, _ := toolMap["function"].(map[string]any)
			if fn["name"] == "tool_search" {
				p.sawToolSearch = true
			}
			if fn["name"] == pluginE2EToolName {
				p.sawTool = true
				if callIndex == 1 {
					p.sawToolBeforeSearch = true
				}
			}
		}
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

func (p *pluginSystemE2EProvider) Assert(t *testing.T, events string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != 3 {
		t.Fatalf("provider calls = %d, want 3; %s; events=%s", p.calls, p.summaryLocked(), events)
	}
	if !p.sawAuthorization {
		t.Fatalf("provider did not observe Authorization header; %s; events=%s", p.summaryLocked(), events)
	}
	if !p.sawSkill {
		t.Fatalf("provider did not observe plugin skill marker in model request; %s; events=%s", p.summaryLocked(), events)
	}
	if !p.sawHook {
		t.Fatalf("provider did not observe plugin hook context in model request; %s; events=%s", p.summaryLocked(), events)
	}
	if !p.sawToolSearch {
		t.Fatalf("provider did not observe tool_search declaration; %s; events=%s", p.summaryLocked(), events)
	}
	if p.sawToolBeforeSearch {
		t.Fatalf("provider observed concrete MCP tool before tool_search result; %s; events=%s", p.summaryLocked(), events)
	}
	if !p.sawTool {
		t.Fatalf("provider did not observe plugin MCP tool declaration after tool_search; %s; events=%s", p.summaryLocked(), events)
	}
	if !p.sawToolResult {
		t.Fatalf("provider did not observe MCP tool result in follow-up model request; %s; events=%s", p.summaryLocked(), events)
	}
}

func (p *pluginSystemE2EProvider) Summary() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.summaryLocked()
}

func (p *pluginSystemE2EProvider) summaryLocked() string {
	return fmt.Sprintf(
		"calls=%d auth=%t skill=%t hook=%t tool_search=%t tool=%t tool_before_search=%t tool_result=%t summaries=%q",
		p.calls,
		p.sawAuthorization,
		p.sawSkill,
		p.sawHook,
		p.sawToolSearch,
		p.sawTool,
		p.sawToolBeforeSearch,
		p.sawToolResult,
		p.payloadSummaries,
	)
}

func summarizePluginSystemE2EPayload(payload map[string]any) string {
	messages, _ := payload["messages"].([]any)
	parts := make([]string, 0, len(messages))
	for i, item := range messages {
		msg, _ := item.(map[string]any)
		role, _ := msg["role"].(string)
		toolCallID, _ := msg["tool_call_id"].(string)
		contentRaw, _ := json.Marshal(msg["content"])
		toolCalls := pluginSystemE2EToolCallNames(msg["tool_calls"])
		parts = append(parts, fmt.Sprintf(
			"%d:%s:tool_call_id=%q:content_bytes=%d:skill=%t:hook=%t:mcp_result=%t:tool_calls=%q",
			i,
			role,
			toolCallID,
			len(contentRaw),
			strings.Contains(string(contentRaw), pluginE2ESkillMarker),
			strings.Contains(string(contentRaw), pluginE2EHookMarker),
			strings.Contains(string(contentRaw), pluginE2EMCPMarker),
			toolCalls,
		))
	}
	return strings.Join(parts, " | ")
}

func pluginSystemE2EToolCallNames(value any) []string {
	items, _ := value.([]any)
	names := make([]string, 0, len(items))
	for _, item := range items {
		toolCall, _ := item.(map[string]any)
		fn, _ := toolCall["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if strings.TrimSpace(name) != "" {
			names = append(names, name)
		}
	}
	return names
}

func pluginSystemE2EPluginActive(list []PluginInfo) bool {
	for _, item := range list {
		if item.ID != "caelis-e2e-plugin" || !item.Enabled || item.Status != "active" {
			continue
		}
		for _, server := range item.MCPServers {
			if server.Name == "demo" && server.Status == "running" && len(server.Tools) == 1 && server.Tools[0] == "read_e2e_fixture" {
				return true
			}
		}
	}
	return false
}

func pluginSystemE2EEventSummary(t *testing.T, stack *Stack, ref session.SessionRef) string {
	t.Helper()
	events, err := stack.Sessions.Events(context.Background(), session.EventsRequest{
		SessionRef:       ref,
		IncludeTransient: true,
	})
	if err != nil {
		return "events_error=" + err.Error()
	}
	parts := make([]string, 0, len(events))
	for _, ev := range events {
		if ev == nil {
			continue
		}
		detail := ""
		if msg, ok := session.ModelMessageOf(ev); ok {
			if calls := msg.ToolCalls(); len(calls) > 0 {
				detail = fmt.Sprintf(":tool_calls=%d:%s", len(calls), calls[0].Name)
			} else if resp := msg.ToolResponse(); resp != nil {
				raw, _ := json.Marshal(resp.Result)
				detail = fmt.Sprintf(":tool_result=%s:mcp_result=%t", resp.Name, strings.Contains(string(raw), pluginE2EMCPMarker))
			} else if text := strings.TrimSpace(msg.TextContent()); text != "" {
				detail = fmt.Sprintf(":text_bytes=%d:skill=%t:hook=%t", len(text), strings.Contains(text, pluginE2ESkillMarker), strings.Contains(text, pluginE2EHookMarker))
			}
		}
		parts = append(parts, fmt.Sprintf("%s/%s/%s%s", ev.ID, ev.Type, ev.Visibility, detail))
	}
	return strings.Join(parts, ",")
}
