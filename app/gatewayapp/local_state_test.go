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
	"sync/atomic"
	"testing"

	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/compact"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp"
	"github.com/OnslaughtSnail/caelis/surfaces/headless"
)

func TestStackSessionRuntimeStateTracksModelAndSessionModeOverrides(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)

	alias, err := stack.Connect(ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "alt-model",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := stack.UseModel(ctx, activeSession.SessionRef, alias); err != nil {
		t.Fatalf("UseModel() error = %v", err)
	}
	if mode, err := stack.SetSessionMode(ctx, activeSession.SessionRef, "manual"); err != nil {
		t.Fatalf("SetSessionMode(manual) error = %v", err)
	} else if mode != "manual" {
		t.Fatalf("SetSessionMode() = %q, want manual", mode)
	}

	state, err := stack.SessionRuntimeState(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SessionRuntimeState() error = %v", err)
	}
	if state.ModelID != alias {
		t.Fatalf("model id = %q, want %q", state.ModelID, alias)
	}
	if state.ModelAlias != "ollama/alt-model" {
		t.Fatalf("model alias = %q, want ollama/alt-model", state.ModelAlias)
	}
	if state.SessionMode != "manual" {
		t.Fatalf("session mode = %q, want manual", state.SessionMode)
	}

	if err := stack.DeleteModel(ctx, activeSession.SessionRef, alias); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}
	if mode, err := stack.SetSessionMode(ctx, activeSession.SessionRef, "auto-review"); err != nil {
		t.Fatalf("SetSessionMode(auto-review) error = %v", err)
	} else if mode != "auto-review" {
		t.Fatalf("SetSessionMode(auto-review) = %q, want auto-review", mode)
	}

	state, err = stack.SessionRuntimeState(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SessionRuntimeState() after reset error = %v", err)
	}
	if state.ModelAlias != "" {
		t.Fatalf("model alias after delete = %q, want empty", state.ModelAlias)
	}
	if state.SessionMode != "auto-review" {
		t.Fatalf("session mode after reset = %q, want auto-review", state.SessionMode)
	}
}

func TestPolicyModeDefaultsUnknownAndLegacyValues(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":             "auto-review",
		"default":      "auto-review",
		"plan":         "auto-review",
		"full_access":  "auto-review",
		"full_control": "auto-review",
		"manual":       "manual",
		"unknown":      "auto-review",
	}
	for input, want := range tests {
		if got := policyMode(input); got != want {
			t.Fatalf("policyMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestModelLookupResolvesMiniMaxThroughProviderFactory(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("request path = %q, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer minimax-secret" {
			t.Fatalf("authorization = %q, want bearer minimax token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"MiniMax-M2","stop_reason":"end_turn","stop_sequence":"","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	defer server.Close()

	lookup, err := newModelLookup(nil, ModelConfig{
		Provider: "minimax",
		Model:    "MiniMax-M2",
		BaseURL:  server.URL,
		Token:    "minimax-secret",
	}, 204800)
	if err != nil {
		t.Fatalf("newModelLookup() error = %v", err)
	}
	cfg, ok := lookup.Config("minimax/minimax-m2")
	if !ok {
		t.Fatal("expected minimax config")
	}
	if cfg.API != providers.APIMiniMax {
		t.Fatalf("cfg.API = %q, want %q", cfg.API, providers.APIMiniMax)
	}
	if cfg.AuthType != providers.AuthBearerToken {
		t.Fatalf("cfg.AuthType = %q, want %q", cfg.AuthType, providers.AuthBearerToken)
	}

	resolved, err := lookup.ResolveModel(context.Background(), "minimax/minimax-m2", 0)
	if err != nil {
		t.Fatalf("ResolveModel() error = %v", err)
	}
	var finalText string
	for event, err := range resolved.Model.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			finalText = event.Response.Message.TextContent()
		}
	}
	if finalText != "ok" {
		t.Fatalf("final text = %q, want ok", finalText)
	}
}

func TestStackSandboxBackendPersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	workdir := t.TempDir()

	stack, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "sandbox-persist-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	if err := stack.saveSandboxConfig(); err != nil {
		t.Fatalf("saveSandboxConfig() error = %v", err)
	}
	status := stack.SandboxStatus()
	if status.RequestedBackend != "host" {
		t.Fatalf("requested backend = %q, want host", status.RequestedBackend)
	}

	reloaded, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "sandbox-persist-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack(reloaded) error = %v", err)
	}
	if got := reloaded.SandboxStatus().RequestedBackend; got != "host" {
		t.Fatalf("SandboxStatus().RequestedBackend = %q, want host", got)
	}
	doc, err := LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	if got := doc.Sandbox.RequestedType; got != "host" {
		t.Fatalf("config sandbox requested_type = %q, want host", got)
	}
}

func TestStackDeleteModelRemovesConfiguredAlias(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)

	alias, err := stack.Connect(ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "alt-model",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := stack.UseModel(ctx, activeSession.SessionRef, alias); err != nil {
		t.Fatalf("UseModel() error = %v", err)
	}
	if err := stack.DeleteModel(ctx, activeSession.SessionRef, alias); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}

	aliases, err := stack.ListModelAliases(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("ListModelAliases() error = %v", err)
	}
	for _, item := range aliases {
		if item == alias {
			t.Fatalf("deleted alias %q still present in %#v", alias, aliases)
		}
	}
	if got := stack.DefaultModelAlias(); got == alias {
		t.Fatalf("default alias = %q, want deleted alias removed", got)
	}
}

func TestStackDeleteModelDropsUnreferencedProfile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "delete-profile-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "delete-profile-session", "surface")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	firstID, err := stack.Connect(ModelConfig{
		Provider:     "deepseek",
		API:          providers.APIDeepSeek,
		Model:        "deepseek-v4-flash",
		Token:        "secret",
		PersistToken: true,
	})
	if err != nil {
		t.Fatalf("Connect(first) error = %v", err)
	}
	secondID, err := stack.Connect(ModelConfig{
		Provider: "deepseek",
		API:      providers.APIDeepSeek,
		Model:    "deepseek-v4-pro",
	})
	if err != nil {
		t.Fatalf("Connect(second) error = %v", err)
	}
	if err := stack.DeleteModel(ctx, activeSession.SessionRef, firstID); err != nil {
		t.Fatalf("DeleteModel(first) error = %v", err)
	}
	doc, err := LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig(after first delete) error = %v", err)
	}
	if len(doc.Models.Profiles) != 1 {
		t.Fatalf("profiles after deleting one model = %#v, want shared profile retained", doc.Models.Profiles)
	}
	if err := stack.DeleteModel(ctx, activeSession.SessionRef, secondID); err != nil {
		t.Fatalf("DeleteModel(second) error = %v", err)
	}
	doc, err = LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig(after second delete) error = %v", err)
	}
	if len(doc.Models.Profiles) != 0 {
		t.Fatalf("profiles after deleting last model = %#v, want none", doc.Models.Profiles)
	}
}

func TestStackUseModelReportsAmbiguousVisibleAlias(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)

	for _, cfg := range []ModelConfig{
		{Provider: "xiaomi", API: providers.APIMimo, Model: "mimo-v2.5-pro", BaseURL: "https://api.xiaomimimo.com/v1"},
		{Provider: "xiaomi", API: providers.APIMimo, Model: "mimo-v2.5-pro", BaseURL: "https://token-plan-cn.xiaomimimo.com/v1"},
	} {
		if _, err := stack.Connect(cfg); err != nil {
			t.Fatalf("Connect(%s) error = %v", cfg.BaseURL, err)
		}
	}
	if !stack.lookup.HasAlias("xiaomi/mimo-v2.5-pro") {
		t.Fatal("HasAlias(duplicate visible alias) = false, want true")
	}
	err := stack.UseModel(ctx, activeSession.SessionRef, "xiaomi/mimo-v2.5-pro")
	if err == nil || !strings.Contains(err.Error(), "ambiguous model alias") {
		t.Fatalf("UseModel(duplicate visible alias) error = %v, want ambiguity", err)
	}
}

func TestACPSurfaceUsesStableModelIDsForDuplicateAliases(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	apiID, err := stack.Connect(ModelConfig{
		Provider: "xiaomi",
		API:      providers.APIMimo,
		Model:    "mimo-v2.5-pro",
		BaseURL:  "https://api.xiaomimimo.com/v1",
	})
	if err != nil {
		t.Fatalf("Connect(api) error = %v", err)
	}
	tokenPlanID, err := stack.Connect(ModelConfig{
		Provider: "xiaomi",
		API:      providers.APIMimo,
		Model:    "mimo-v2.5-pro",
		BaseURL:  "https://token-plan-cn.xiaomimimo.com/v1",
	})
	if err != nil {
		t.Fatalf("Connect(token plan) error = %v", err)
	}
	surface := stack.ACPSurface(nil, false, nil)
	models, err := surface.SessionModels(ctx, activeSession)
	if err != nil {
		t.Fatalf("SessionModels() error = %v", err)
	}
	if models == nil {
		t.Fatal("SessionModels() = nil, want models")
	}
	if models.CurrentModelID != tokenPlanID {
		t.Fatalf("CurrentModelID = %q, want %q", models.CurrentModelID, tokenPlanID)
	}
	seen := map[string]string{}
	for _, model := range models.AvailableModels {
		seen[model.ModelID] = model.Name
	}
	if seen[apiID] != "xiaomi/mimo-v2.5-pro" || seen[tokenPlanID] != "xiaomi/mimo-v2.5-pro" {
		t.Fatalf("available models = %#v, want stable ids with visible alias names", models.AvailableModels)
	}
	if _, err := surface.SetSessionModel(ctx, acp.SetSessionModelRequest{SessionID: activeSession.SessionID, ModelID: apiID}); err != nil {
		t.Fatalf("SetSessionModel(stable id) error = %v", err)
	}
	state, err := stack.SessionRuntimeState(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SessionRuntimeState() error = %v", err)
	}
	if state.ModelID != apiID || state.ModelAlias != "xiaomi/mimo-v2.5-pro" {
		t.Fatalf("runtime state = %#v, want API profile selected by stable id", state)
	}
}

func TestStackDeleteOnlyModelClearsRuntimeModelState(t *testing.T) {
	ctx := context.Background()
	workdir := t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "delete-only-model-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "delete-only-model-session", "surface")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	alias, err := stack.Connect(ModelConfig{
		Provider:        "deepseek",
		API:             providers.APIDeepSeek,
		Model:           "deepseek-v4-pro",
		ReasoningLevels: []string{"none", "high", "max"},
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := stack.UseModel(ctx, activeSession.SessionRef, alias, "high"); err != nil {
		t.Fatalf("UseModel() error = %v", err)
	}
	if err := stack.DeleteModel(ctx, activeSession.SessionRef, alias); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}
	if got := stack.DefaultModelAlias(); got != "" {
		t.Fatalf("DefaultModelAlias() = %q, want empty", got)
	}
	aliases, err := stack.ListModelAliases(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("ListModelAliases() error = %v", err)
	}
	if len(aliases) != 0 {
		t.Fatalf("ListModelAliases() = %#v, want empty", aliases)
	}
	state, err := stack.SessionRuntimeState(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SessionRuntimeState() error = %v", err)
	}
	if state.ModelAlias != "" || state.ReasoningEffort != "" {
		t.Fatalf("runtime state = %#v, want model and reasoning cleared", state)
	}
}

func TestSessionRuntimeStateIgnoresStaleModelAliasOutsideConfig(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	if err := stack.Sessions.UpdateState(ctx, activeSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next["kernel.current_model_alias"] = "minimax/minimax-m2.7-highspeed"
		return next, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	state, err := stack.SessionRuntimeState(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SessionRuntimeState() error = %v", err)
	}
	if state.ModelAlias != "" {
		t.Fatalf("ModelAlias = %q, want empty because alias is not in config", state.ModelAlias)
	}
}

func TestLocalStackPersistsMultipleProviderModelsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()

	stack, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "persist-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "persist-session", "surface-persist")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	minimaxAlias, err := stack.Connect(ModelConfig{
		Provider: "minimax",
		Model:    "MiniMax-M2.7-highspeed",
		Token:    "minimax-secret",
	})
	if err != nil {
		t.Fatalf("Connect(minimax) error = %v", err)
	}
	if _, err := stack.Connect(ModelConfig{
		Provider: "deepseek",
		API:      providers.APIDeepSeek,
		Model:    "deepseek-v4-pro",
		Token:    "deepseek-secret",
	}); err != nil {
		t.Fatalf("Connect(deepseek) error = %v", err)
	}
	if err := stack.UseModel(ctx, activeSession.SessionRef, minimaxAlias); err != nil {
		t.Fatalf("UseModel(minimax) error = %v", err)
	}

	reloaded, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "persist-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack(reloaded) error = %v", err)
	}
	reloadedSession, err := reloaded.StartSession(ctx, "persist-session", "surface-persist")
	if err != nil {
		t.Fatalf("StartSession(reloaded) error = %v", err)
	}
	aliases, err := reloaded.ListModelAliases(ctx, reloadedSession.SessionRef)
	if err != nil {
		t.Fatalf("ListModelAliases(reloaded) error = %v", err)
	}
	if len(aliases) < 2 {
		t.Fatalf("reloaded aliases = %#v, want both minimax and deepseek aliases", aliases)
	}
	if !containsStringFold(aliases, "minimax/minimax-m2.7-highspeed") {
		t.Fatalf("reloaded aliases = %#v, missing minimax/minimax-m2.7-highspeed", aliases)
	}
	if !containsStringFold(aliases, "deepseek/deepseek-v4-pro") {
		t.Fatalf("reloaded aliases = %#v, missing deepseek/deepseek-v4-pro", aliases)
	}
	if got := reloaded.DefaultModelAlias(); got != "minimax/minimax-m2.7-highspeed" {
		t.Fatalf("DefaultModelAlias(reloaded) = %q, want minimax/minimax-m2.7-highspeed", got)
	}
	doc, err := LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	if got := doc.Models.DefaultAlias; got != "minimax/minimax-m2.7-highspeed" {
		t.Fatalf("config default alias = %q, want minimax/minimax-m2.7-highspeed", got)
	}
	if got := doc.Models.DefaultID; got != minimaxAlias {
		t.Fatalf("config default model id = %q, want %q", got, minimaxAlias)
	}
	if len(doc.Models.Configs) < 2 {
		t.Fatalf("config models = %#v, want both minimax and deepseek configs", doc.Models.Configs)
	}
	if _, err := os.Stat(filepath.Join(root, "config.json")); err != nil {
		t.Fatalf("config.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "config", "models.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy models.json should be removed, stat err = %v", err)
	}
}

func TestNewLocalStackAllowsEmptyInitialModelConfig(t *testing.T) {
	root := t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "empty-model-test",
		StoreDir:       root,
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "auto-review",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	if got := stack.DefaultModelAlias(); got != "" {
		t.Fatalf("DefaultModelAlias() = %q, want empty", got)
	}
	raw, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatalf("ReadFile(config.json) error = %v", err)
	}
	if !strings.Contains(string(raw), `"network_enabled": true`) {
		t.Fatalf("config.json = %s, want persisted sandbox network default", raw)
	}
}

func TestLocalStackDefaultRuntimeAutoCompactionEnabled(t *testing.T) {
	ctx := context.Background()
	server := newGatewayAppCompactionOllamaServer(t)
	stack, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "auto-compact-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "auto-review",
		ContextWindow:  64,
		Assembly:       assembly.ResolvedAssembly{},
		Model: ModelConfig{
			Provider:   "ollama",
			API:        providers.APIOllama,
			Model:      "compact-test",
			BaseURL:    server.URL,
			HTTPClient: server.Client(),
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "auto compact session", "surface-auto-compact")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	appendGatewayAppEvent(t, stack, activeSession.SessionRef, gatewayAppUserEvent("Project objective: app default auto compact should be enabled in the upper app assembly."))
	appendGatewayAppEvent(t, stack, activeSession.SessionRef, gatewayAppAssistantEvent("ack"))
	appendGatewayAppEvent(t, stack, activeSession.SessionRef, gatewayAppUserEvent("Current blocker: app assembly previously left compaction disabled unless tests opted in explicitly."))
	appendGatewayAppEvent(t, stack, activeSession.SessionRef, gatewayAppAssistantEvent("ack"))
	appendGatewayAppEvent(t, stack, activeSession.SessionRef, gatewayAppUserEvent("Next action: verify the default app runtime invokes model-backed compact before the turn."))

	if _, err := headless.RunOnce(ctx, stack.Gateway, kernel.BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue after app auto compact",
		Surface:    "headless-auto-compact-test",
	}, headless.Options{}); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if got := server.compactionCalls.Load(); got == 0 {
		t.Fatal("expected app default runtime to invoke compaction")
	}
	loaded, err := stack.Sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	compactEvent, ok := latestGatewayAppCompactEvent(loaded.Events)
	if !ok {
		t.Fatal("missing compact event after auto compact")
	}
	data, ok := compact.CompactEventDataFromEvent(compactEvent)
	if !ok || data.SourceEventCount == 0 {
		t.Fatalf("auto compact event missing compact metadata: meta=%+v", compactEvent.Meta)
	}
	promptEvents := compact.PromptEventsFromLatestCompact(loaded.Events)
	if len(promptEvents) == 0 || strings.TrimSpace(session.EventText(promptEvents[0])) == "" {
		t.Fatalf("auto compact prompt overlay missing checkpoint text: %+v", promptEvents)
	}
}

func TestLocalStackAutoCompactCountsPromptPrefix(t *testing.T) {
	ctx := context.Background()
	server := newGatewayAppCompactionOllamaServer(t)
	stack, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "auto-compact-prefix-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "auto-review",
		ContextWindow:  4096,
		SystemPrompt:   strings.Repeat("stable prompt prefix token. ", 600),
		Assembly:       assembly.ResolvedAssembly{},
		Model: ModelConfig{
			Provider:   "ollama",
			API:        providers.APIOllama,
			Model:      "compact-test",
			BaseURL:    server.URL,
			HTTPClient: server.Client(),
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "auto compact prefix session", "surface-auto-compact-prefix")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	appendGatewayAppEvent(t, stack, activeSession.SessionRef, gatewayAppUserEvent("Short durable event."))

	if _, err := headless.RunOnce(ctx, stack.Gateway, kernel.BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "continue after prefix pressure",
		Surface:    "headless-auto-compact-prefix-test",
	}, headless.Options{}); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if got := server.compactionCalls.Load(); got == 0 {
		t.Fatal("expected prompt-prefix pressure to trigger auto compaction")
	}
}

func TestLocalStackManualCompactUsesStructuredRuntimeCompaction(t *testing.T) {
	ctx := context.Background()
	server := newGatewayAppCompactionOllamaServer(t)
	stack, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "manual-compact-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "auto-review",
		ContextWindow:  4096,
		Assembly:       assembly.ResolvedAssembly{},
		Model: ModelConfig{
			Provider:   "ollama",
			API:        providers.APIOllama,
			Model:      "compact-test",
			BaseURL:    server.URL,
			HTTPClient: server.Client(),
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "manual compact session", "surface-manual-compact")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	appendGatewayAppEvent(t, stack, activeSession.SessionRef, gatewayAppUserEvent("Project objective: manual compact must preserve context with checkpoint overlay."))
	appendGatewayAppEvent(t, stack, activeSession.SessionRef, gatewayAppAssistantEvent("ack"))
	appendGatewayAppEvent(t, stack, activeSession.SessionRef, gatewayAppUserEvent("Current blocker: a bare manual compact event truncates all prior prompt-visible history."))
	appendGatewayAppEvent(t, stack, activeSession.SessionRef, gatewayAppAssistantEvent("ack"))
	appendGatewayAppEvent(t, stack, activeSession.SessionRef, gatewayAppUserEvent("Next action: force the runtime compactor with trigger manual."))

	if err := stack.CompactSession(ctx, activeSession.SessionRef); err != nil {
		t.Fatalf("CompactSession() error = %v", err)
	}
	if got := server.compactionCalls.Load(); got != 1 {
		t.Fatalf("compactionCalls = %d, want 1", got)
	}
	loaded, err := stack.Sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	compactEvent, ok := latestGatewayAppCompactEvent(loaded.Events)
	if !ok {
		t.Fatal("missing compact event after manual compact")
	}
	data, ok := compact.CompactEventDataFromEvent(compactEvent)
	if !ok {
		t.Fatalf("manual compact event missing structured metadata: meta=%+v", compactEvent.Meta)
	}
	if data.Trigger != "manual" {
		t.Fatalf("compact trigger = %q, want manual", data.Trigger)
	}
	if data.SourceEventCount == 0 {
		t.Fatalf("manual compact source event count = %d, want > 0", data.SourceEventCount)
	}
	promptEvents := compact.PromptEventsFromLatestCompact(loaded.Events)
	if len(promptEvents) == 0 || strings.TrimSpace(session.EventText(promptEvents[0])) == "" {
		t.Fatalf("manual compact prompt overlay missing checkpoint text: %+v", promptEvents)
	}
}

func TestSessionUsageSnapshotKeepsPromptPrefixVisibleAfterCompact(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "compact-usage-prefix-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "auto-review",
		SystemPrompt:   strings.Repeat("count this stable prefix instruction. ", 2000),
		Model: ModelConfig{
			Provider:            "ollama",
			API:                 providers.APIOllama,
			Model:               "llama3",
			ContextWindowTokens: 1000000,
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "compact usage prefix session", "surface-compact-usage-prefix")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	compactMessage := model.NewTextMessage(model.RoleUser, "CONTEXT CHECKPOINT\nObjective: compacted baseline")
	appendGatewayAppEvent(t, stack, activeSession.SessionRef, &session.Event{
		Type:       session.EventTypeCompact,
		Visibility: session.VisibilityCanonical,
		Message:    &compactMessage,
		Text:       compactMessage.TextContent(),
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodContextCheckpoint,
			Update: &session.ProtocolUpdate{
				SessionUpdate: "compact",
				Content:       session.ProtocolTextContent(compactMessage.TextContent()),
			},
		},
		Meta: map[string]any{
			compact.MetaKeyCompact: compact.CompactEventDataValue(compact.CompactEventData{
				ContractVersion: compact.CompactContractVersion,
			}),
		},
	})

	usage, err := stack.SessionUsageSnapshot(ctx, activeSession.SessionRef, "ollama/llama3")
	if err != nil {
		t.Fatalf("SessionUsageSnapshot() error = %v", err)
	}
	if usage.Source != compact.UsageSourceEstimated {
		t.Fatalf("usage source = %q, want estimated after compact without provider baseline", usage.Source)
	}
	if usage.EstimatedPrefixTokens < 5000 {
		t.Fatalf("estimated prefix tokens = %d, want stable prompt prefix included", usage.EstimatedPrefixTokens)
	}
	if usage.TotalTokens <= usage.EstimatedPrefixTokens {
		t.Fatalf("total tokens = %d, want compact history plus prefix %d", usage.TotalTokens, usage.EstimatedPrefixTokens)
	}
}

func TestNewLocalStackInfersCodeFreeAPIFromProvider(t *testing.T) {
	stack, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "codefree-api-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "auto-review",
		Assembly:       assembly.ResolvedAssembly{},
		Model: ModelConfig{
			Provider: "codefree",
			Model:    "GLM-5.1",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	cfg, ok := stack.lookup.Config("codefree/glm-5.1")
	if !ok {
		t.Fatal("missing codefree model config")
	}
	if cfg.API != providers.APICodeFree {
		t.Fatalf("codefree API = %q, want %q", cfg.API, providers.APICodeFree)
	}
}

func TestDefaultStoreDirUsesHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("home directory unavailable")
	}
	want := filepath.Join(home, ".caelis")
	if got := defaultStoreDir(); got != want {
		t.Fatalf("defaultStoreDir() = %q, want %q", got, want)
	}
}

func newLocalStateTestStack(t *testing.T) (*Stack, session.Session) {
	t.Helper()
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newGatewayAppTestStack(t, Config{
		AppName:        "caelis",
		UserID:         "state-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Assembly:       assembly.ResolvedAssembly{},
		Model: ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(context.Background(), "state-test-session", "surface-state-test")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return stack, activeSession
}

type gatewayAppCompactionOllamaServer struct {
	URL             string
	client          *http.Client
	compactionCalls atomic.Int64
	normalCalls     atomic.Int64
}

func newGatewayAppCompactionOllamaServer(t *testing.T) *gatewayAppCompactionOllamaServer {
	t.Helper()
	out := &gatewayAppCompactionOllamaServer{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		joined := gatewayAppOllamaMessages(payload.Messages)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(joined, "CONTEXT CHECKPOINT COMPACTION") {
			out.compactionCalls.Add(1)
			fmt.Fprint(w, `{"model":"compact-test","message":{"role":"assistant","content":"CONTEXT CHECKPOINT\nObjective: app compact preserves context\nBlocker: bare compact events truncate prompt-visible history\nNext action: continue from structured checkpoint overlay\n\n## Current Progress\n- app runtime used model-backed compaction"},"done":true,"prompt_eval_count":64,"eval_count":12}`)
			return
		}
		out.normalCalls.Add(1)
		fmt.Fprint(w, `{"model":"compact-test","message":{"role":"assistant","content":"app turn ok"},"done":true,"prompt_eval_count":32,"eval_count":8}`)
	})
	out.URL = "http://gatewayapp.test"
	out.client = &http.Client{Transport: gatewayAppRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			handler.ServeHTTP(recorder, req)
		}()
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-done:
			resp := recorder.Result()
			resp.Request = req
			return resp, nil
		}
	})}
	return out
}

func (s *gatewayAppCompactionOllamaServer) Client() *http.Client {
	return s.client
}

type gatewayAppRoundTripFunc func(*http.Request) (*http.Response, error)

func (f gatewayAppRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func gatewayAppOllamaMessages(messages []struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		parts = append(parts, strings.TrimSpace(message.Role)+": "+strings.TrimSpace(message.Content))
	}
	return strings.Join(parts, "\n")
}

func appendGatewayAppEvent(t *testing.T, stack *Stack, ref session.SessionRef, event *session.Event) {
	t.Helper()
	if _, err := stack.Sessions.AppendEvent(context.Background(), session.AppendEventRequest{
		SessionRef: ref,
		Event:      event,
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
}

func gatewayAppUserEvent(text string) *session.Event {
	message := model.NewTextMessage(model.RoleUser, text)
	return &session.Event{
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityCanonical,
		Message:    &message,
		Text:       strings.TrimSpace(text),
	}
}

func gatewayAppAssistantEvent(text string) *session.Event {
	message := model.NewTextMessage(model.RoleAssistant, text)
	return &session.Event{
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &message,
		Text:       strings.TrimSpace(text),
	}
}

func latestGatewayAppCompactEvent(events []*session.Event) (*session.Event, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i] != nil && events[i].Type == session.EventTypeCompact {
			return events[i], true
		}
	}
	return nil, false
}

func containsStringFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
