package local

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/plugin"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	sandboxhost "github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/host"
	storememory "github.com/OnslaughtSnail/caelis/internal/adapters/store/memory"
	toolshell "github.com/OnslaughtSnail/caelis/internal/adapters/tools/shell"
	"github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	"github.com/OnslaughtSnail/caelis/protocol/acp/jsonrpc"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestNewRequiresProvider(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New without provider error = nil, want error")
	}
}

func TestStackRunsTurnThroughServices(t *testing.T) {
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("pong")},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceKey: "repo",
			WorkspaceCWD: t.TempDir(),
		},
		Provider: provider,
		ExternalACPAgents: []acpexternal.Config{{
			AgentID:   "reviewer",
			AgentName: "Reviewer",
			Command:   "reviewer-acp",
			Args:      []string{"--stdio"},
		}},
		ToolList: []tool.Tool{
			tool.NamedTool{Def: tool.Definition{Name: "noop", Description: "no operation"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{Title: "scratch"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "ping",
		Model:      "gpt-test",
		Surface:    "tui",
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []session.Event
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
		events = append(events, env.Event)
	}
	if len(events) != 2 {
		t.Fatalf("turn events = %d, want user and assistant", len(events))
	}
	if got := session.EventText(events[1]); got != "pong" {
		t.Fatalf("assistant text = %q, want pong", got)
	}
	if provider.request.Model != "gpt-test" {
		t.Fatalf("provider model = %q, want gpt-test", provider.request.Model)
	}
	agents, err := stack.Services().Agents().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != "reviewer" || agents[0].Kind != services.AgentKindExternalACP {
		t.Fatalf("agents = %#v, want reviewer ACP agent", agents)
	}
	if len(provider.request.Tools) != 1 || provider.request.Tools[0].Name != "noop" {
		t.Fatalf("provider tools = %#v, want noop tool", provider.request.Tools)
	}

	snapshot, err := stack.Services().Sessions().Load(context.Background(), session.Ref{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 {
		t.Fatalf("snapshot events = %d, want 2", len(snapshot.Events))
	}
	if got := session.EventText(snapshot.Events[0]); got != "ping" {
		t.Fatalf("stored user text = %q, want ping", got)
	}
}

func TestStackInvokesExternalACPAgentThroughServices(t *testing.T) {
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("local baseline")},
	}}
	stack, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester", WorkspaceCWD: t.TempDir()},
		Provider: provider,
		ExternalACPAgents: []acpexternal.Config{{
			AgentID:   "helper",
			AgentName: "Helper",
			Command:   os.Args[0],
			Args:      []string{"-test.run=TestExternalACPHelperProcess", "--"},
			Env:       []string{"CAELIS_TEST_EXTERNAL_ACP_HELPER=1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := stack.Services().Agents().Invoke(context.Background(), services.AgentInvokeRequest{
		AgentID:    "helper",
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "delegate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || session.EventText(result.Events[0]) != "external helper response" {
		t.Fatalf("invoke result = %#v, want helper response", result)
	}
	snapshot, err := stack.Services().Sessions().Load(context.Background(), session.Ref{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 || session.EventText(snapshot.Events[0]) != "external helper response" {
		t.Fatalf("stored events = %#v, want helper response", snapshot.Events)
	}
	if snapshot.Events[0].Scope == nil || snapshot.Events[0].Scope.Participant.ID != "helper" || snapshot.Events[0].Scope.ACP.SessionID == "" {
		t.Fatalf("stored event scope = %#v, want participant and remote ACP session", snapshot.Events[0].Scope)
	}
}

func TestExternalACPHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_TEST_EXTERNAL_ACP_HELPER") != "1" {
		return
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(ctx context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case schema.MethodInitialize:
			return schema.InitializeResponse{
				ProtocolVersion: schema.CurrentProtocolVersion,
				AgentCapabilities: schema.AgentCapabilities{
					PromptCapabilities: schema.PromptCapabilities{Image: true},
				},
				AgentInfo: &schema.Implementation{Name: "helper", Version: "test"},
			}, nil
		case schema.MethodSessionNew:
			return schema.NewSessionResponse{SessionID: "remote-helper-session"}, nil
		case schema.MethodSessionPrompt:
			var req schema.PromptRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			_ = conn.Notify(schema.MethodSessionUpdate, schema.SessionNotification{
				SessionID: req.SessionID,
				Update: schema.ContentChunk{
					SessionUpdate: schema.UpdateAgentMessage,
					Content:       schema.TextContent{Type: "text", Text: "external helper response"},
				},
			})
			return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestStackRunsConcreteShellToolThroughSandbox(t *testing.T) {
	rt, err := sandboxhost.New(context.Background(), sandbox.Config{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	runTool, err := toolshell.NewRunCommandTool(rt)
	if err != nil {
		t.Fatal(err)
	}
	command := "printf hello"
	if runtime.GOOS == "windows" {
		command = "echo hello"
	}
	rawInput, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "call-1",
					Name:  toolshell.RunCommandToolName,
					Input: rawInput,
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("done")},
		},
	}}
	stack, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Provider: provider,
		Sandbox:  rt,
		ToolList: []tool.Tool{runTool},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stack.Sandbox() == nil {
		t.Fatal("stack sandbox = nil, want configured runtime")
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "run command",
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []session.Event
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
		events = append(events, env.Event)
	}
	if len(events) != 5 {
		t.Fatalf("turn events = %d, want user, assistant, tool_call, tool_result, assistant", len(events))
	}
	if events[3].Type != session.EventToolResult || !strings.Contains(session.EventText(events[3]), "hello") {
		t.Fatalf("tool result event = %#v, want shell stdout", events[3])
	}
	if got := session.EventText(events[4]); got != "done" {
		t.Fatalf("final assistant text = %q, want done", got)
	}
}

func TestStackRegistersCoreFilesystemBuiltinTools(t *testing.T) {
	workspace := t.TempDir()
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("ok")},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceCWD: workspace,
			Sandbox:      config.Sandbox{Backend: "host"},
		},
		Provider:     provider,
		BuiltinTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "inspect files",
	})
	if err != nil {
		t.Fatal(err)
	}
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
	}
	for _, name := range []string{"read_file", "list_directory", "glob_files", "search_files", "update_plan", "run_command"} {
		if !capturedTool(provider.request.Tools, name) {
			t.Fatalf("provider tools = %#v, missing %s", provider.request.Tools, name)
		}
	}
}

func TestStackBuildsConfiguredOpenAIProviderAndJSONLStore(t *testing.T) {
	var captured struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"gpt-test",
			"choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	storeDir := t.TempDir()
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Model:   "gpt-test",
			Store:   config.Store{Backend: "jsonl", URI: storeDir},
		},
		Model: config.ModelProfile{
			Provider: "openai_compatible",
			BaseURL:  server.URL + "/v1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
	}
	if captured.Model != "gpt-test" || len(captured.Messages) < 2 || captured.Messages[0].Role != "system" || captured.Messages[len(captured.Messages)-1].Role != "user" {
		t.Fatalf("captured request = %#v", captured)
	}

	reloaded, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Model:   "gpt-test",
			Store:   config.Store{Backend: "jsonl", URI: storeDir},
		},
		Model: config.ModelProfile{
			Provider: "openai_compatible",
			BaseURL:  server.URL + "/v1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reloaded.Services().Sessions().Load(context.Background(), session.Ref{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 || session.EventText(snapshot.Events[1]) != "pong" {
		t.Fatalf("reloaded events = %#v, want persisted ping/pong", snapshot.Events)
	}
}

func TestStackRoutesConfiguredSettingsModelAtTurnTime(t *testing.T) {
	var captured []struct {
		Model string `json:"model"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		var req struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		captured = append(captured, req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"gpt-settings",
			"choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	manager, err := appsettings.NewManager(context.Background(), nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.UpsertModel(context.Background(), appsettings.ModelConfig{
		Provider: "openai_compatible",
		Model:    "gpt-settings",
		BaseURL:  server.URL + "/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := New(Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.Services().Models().Use(context.Background(), session.Ref{SessionID: active.SessionID}, cfg.ID, ""); err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
	}
	if len(captured) != 1 || captured[0].Model != "gpt-settings" {
		t.Fatalf("captured requests = %#v, want routed raw model", captured)
	}
}

func TestStackBuildsConfiguredSQLiteStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("pong")},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Store:   config.Store{Backend: "sqlite", URI: dbPath},
		},
		Provider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
	}
	if closer, ok := stack.Store().(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}

	reloaded, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Store:   config.Store{Backend: "sqlite", URI: dbPath},
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := reloaded.Store().(interface{ Close() error }); ok {
		t.Cleanup(func() { _ = closer.Close() })
	}
	snapshot, err := reloaded.Services().Sessions().Load(context.Background(), session.Ref{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 || session.EventText(snapshot.Events[0]) != "ping" || session.EventText(snapshot.Events[1]) != "pong" {
		t.Fatalf("reloaded events = %#v, want persisted ping/pong", snapshot.Events)
	}
}

func TestStackDiscoversPluginAndWorkspaceResources(t *testing.T) {
	workspace := t.TempDir()
	pluginDir := filepath.Join(t.TempDir(), "plugin")
	if err := os.MkdirAll(filepath.Join(pluginDir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "prompts", "review.md"), []byte("review prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"id":"reviewer",
		"name":"Reviewer",
		"prompts":[{"id":"reviewer.system","priority":50,"paths":["prompts/review.md"]}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("done")},
	}}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceCWD: workspace,
			Plugins: []config.Plugin{{
				Source:  pluginDir,
				Enabled: true,
			}},
		},
		Provider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := stack.Services().Resources().Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasPrompt(catalog.Prompts, "reviewer.system") {
		t.Fatalf("prompts = %#v, missing plugin prompt", catalog.Prompts)
	}
	if !hasPrompt(catalog.Prompts, "agents.workspace") {
		t.Fatalf("prompts = %#v, missing workspace AGENTS prompt", catalog.Prompts)
	}
	if len(catalog.Plugins) != 1 || catalog.Plugins[0].Manifest.ID != "reviewer" {
		t.Fatalf("plugins = %#v, want reviewer plugin", catalog.Plugins)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
	}
	joined := strings.Join(provider.request.Instructions, "\n\n")
	for _, want := range []string{"review prompt", "workspace rule", "You are caelis"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("instructions = %q, missing %q", joined, want)
		}
	}
}

func TestStackInvokesPluginDeclaredACPAgent(t *testing.T) {
	pluginDir := filepath.Join(t.TempDir(), "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(map[string]any{
		"id":   "helper-plugin",
		"name": "Helper Plugin",
		"acp_agents": []map[string]any{{
			"name":    "plugin-helper",
			"command": executable,
			"args":    []string{"-test.run=TestExternalACPHelperProcess", "--"},
			"env":     map[string]string{"CAELIS_TEST_EXTERNAL_ACP_HELPER": "1"},
			"roles":   []string{"participant"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Plugins: []config.Plugin{{
				Source:  pluginDir,
				Enabled: true,
			}},
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	agents, err := stack.Services().Agents().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != "plugin-helper" || agents[0].WorkDir != pluginDir {
		t.Fatalf("agents = %#v, want plugin-helper with plugin workdir", agents)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := stack.Services().Agents().Invoke(context.Background(), services.AgentInvokeRequest{
		AgentID:    "plugin-helper",
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "delegate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || session.EventText(result.Events[0]) != "external helper response" {
		t.Fatalf("invoke result = %#v, want helper response", result)
	}
	snapshot, err := stack.Services().Sessions().Load(context.Background(), session.Ref{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 || snapshot.Events[0].Scope == nil || snapshot.Events[0].Scope.Participant.ID != "plugin-helper" {
		t.Fatalf("stored events = %#v, want plugin helper participant event", snapshot.Events)
	}
}

func TestStackAppliesPluginContributionStoreFactory(t *testing.T) {
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Store:   config.Store{Backend: "contrib-memory"},
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
		Contributions: []plugin.Contribution{
			contributionFunc(func(_ context.Context, registry plugin.Registry) error {
				return registry.RegisterStore("contrib-memory", func(context.Context, plugin.StoreConfig) (session.Store, error) {
					return storememory.New(), nil
				})
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stack.Store().(*storememory.Store); !ok {
		t.Fatalf("store = %T, want contributed memory store", stack.Store())
	}
}

func TestStackAppliesManifestDeclaredStoreAlias(t *testing.T) {
	pluginDir := filepath.Join(t.TempDir(), "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), []byte(`{
		"id":"storage-plugin",
		"stores":[{"name":"plugin-memory","uses":"memory"}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
			Store:   config.Store{Backend: "plugin-memory"},
			Plugins: []config.Plugin{{
				Source:  pluginDir,
				Enabled: true,
			}},
		},
		Provider: &capturingProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stack.Store().(*storememory.Store); !ok {
		t.Fatalf("store = %T, want plugin-declared memory alias", stack.Store())
	}
	catalog, err := stack.Services().Resources().Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Stores) != 1 || catalog.Stores[0].Name != "plugin-memory" || catalog.Stores[0].Uses != "memory" {
		t.Fatalf("catalog stores = %#v, want plugin-memory alias", catalog.Stores)
	}
}

func TestConfiguredStackRunsBuiltinShellTool(t *testing.T) {
	var calls atomic.Int32
	command := "printf hello"
	if runtime.GOOS == "windows" {
		command = "echo hello"
	}
	rawInput, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured struct {
			Messages []struct {
				Role       string `json:"role"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		call := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch call {
		case 1:
			if !capturedOpenAITool(captured.Tools, toolshell.RunCommandToolName) {
				t.Fatalf("tools = %#v, want run_command", captured.Tools)
			}
			_, _ = w.Write([]byte(`{
				"model":"gpt-test",
				"choices":[{
					"message":{
						"role":"assistant",
						"tool_calls":[{
							"id":"call-1",
							"type":"function",
							"function":{"name":"run_command","arguments":` + strconvQuote(string(rawInput)) + `}
						}]
					},
					"finish_reason":"tool_calls"
				}]
			}`))
		case 2:
			if len(captured.Messages) == 0 || captured.Messages[len(captured.Messages)-1].Role != "tool" || captured.Messages[len(captured.Messages)-1].ToolCallID != "call-1" {
				t.Fatalf("second request messages = %#v, want final tool result message", captured.Messages)
			}
			_, _ = w.Write([]byte(`{
				"model":"gpt-test",
				"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]
			}`))
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}))
	defer server.Close()

	stack, err := New(Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			Model:        "gpt-test",
			WorkspaceCWD: t.TempDir(),
			Sandbox:      config.Sandbox{Backend: "host"},
		},
		Model: config.ModelProfile{
			Provider: "openai_compatible",
			BaseURL:  server.URL,
		},
		BuiltinTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err := stack.Services().Sessions().Start(context.Background(), services.StartSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := stack.Services().Turns().Begin(context.Background(), services.BeginTurnRequest{
		SessionRef: session.Ref{SessionID: active.SessionID},
		Input:      "run command",
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []session.Event
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatal(env.Err)
		}
		events = append(events, env.Event)
	}
	if len(events) != 5 || events[3].Type != session.EventToolResult || !strings.Contains(session.EventText(events[3]), "hello") {
		t.Fatalf("events = %#v, want shell tool result", events)
	}
	if got := session.EventText(events[4]); got != "done" {
		t.Fatalf("final assistant text = %q, want done", got)
	}
}

func strconvQuote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func capturedOpenAITool(tools []struct {
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}, name string) bool {
	for _, item := range tools {
		if item.Function.Name == name {
			return true
		}
	}
	return false
}

func hasPrompt(prompts []plugin.PromptFragment, id string) bool {
	for _, prompt := range prompts {
		if prompt.ID == id {
			return true
		}
	}
	return false
}

func capturedTool(tools []model.ToolSpec, name string) bool {
	for _, item := range tools {
		if item.Name == name {
			return true
		}
	}
	return false
}

type capturingProvider struct {
	request model.Request
	message model.Message
}

func (p *capturingProvider) ID() string {
	return "test-provider"
}

func (p *capturingProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "gpt-test", Provider: "test-provider"}}, nil
}

func (p *capturingProvider) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	p.request = model.Request{
		Model:        req.Model,
		Messages:     cloneMessages(req.Messages),
		Tools:        req.Tools,
		Instructions: append([]string(nil), req.Instructions...),
		Stream:       req.Stream,
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: model.StreamTurnDone,
		Response: &model.Response{
			Status:  model.ResponseCompleted,
			Message: model.CloneMessage(p.message),
		},
	}}}, nil
}

func cloneMessages(in []model.Message) []model.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.Message, 0, len(in))
	for _, message := range in {
		out = append(out, model.CloneMessage(message))
	}
	return out
}

type scriptedProvider struct {
	requests  []model.Request
	responses []model.Message
}

type contributionFunc func(context.Context, plugin.Registry) error

func (f contributionFunc) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "test"}
}

func (f contributionFunc) Register(ctx context.Context, registry plugin.Registry) error {
	return f(ctx, registry)
}

func (p *scriptedProvider) ID() string {
	return "scripted"
}

func (p *scriptedProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "scripted", Provider: "scripted"}}, nil
}

func (p *scriptedProvider) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	p.requests = append(p.requests, model.Request{
		Model:    req.Model,
		Messages: cloneMessages(req.Messages),
		Tools:    req.Tools,
		Stream:   req.Stream,
	})
	response := model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("default")},
	}
	if len(p.responses) > 0 {
		response = model.CloneMessage(p.responses[0])
		p.responses = p.responses[1:]
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: model.StreamTurnDone,
		Response: &model.Response{
			Status:  model.ResponseCompleted,
			Message: response,
		},
	}}}, nil
}
