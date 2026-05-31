package settings

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/plugin"
)

func TestFileStoreRedactsTokensByDefaultAndPersistsExplicitTokens(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	manager, err := NewManager(ctx, store, Document{})
	if err != nil {
		t.Fatal(err)
	}
	redacted, err := manager.UpsertModel(ctx, ModelConfig{
		Provider: "openai-compatible",
		Model:    "gpt-test",
		BaseURL:  "https://api.example.test/v1/",
		Token:    "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := manager.UpsertModel(ctx, ModelConfig{
		Provider:     "openai-compatible",
		Model:        "gpt-persisted",
		BaseURL:      "https://api.example.test/v1/",
		Token:        "persist-me",
		PersistToken: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	foundRedacted := false
	foundPersisted := false
	for _, cfg := range doc.Models.Configs {
		switch cfg.ID {
		case redacted.ID:
			foundRedacted = cfg.Token == ""
		case persisted.ID:
			foundPersisted = cfg.Token == "persist-me"
		}
	}
	if !foundRedacted || !foundPersisted {
		t.Fatalf("persisted configs = %#v, want redacted default and explicit persisted token", doc.Models.Configs)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("settings file mode = %v, want 0600", got)
	}
	rootInfo, err := os.Stat(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if got := rootInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("settings dir mode = %v, want 0700", got)
	}
}

func TestManagerModelCatalogSupportsProfilesAliasesAndDefaults(t *testing.T) {
	ctx := context.Background()
	manager, err := NewManager(ctx, nil, Document{
		Models: ModelCatalog{
			Profiles: []ModelProfile{{
				ID:       "primary",
				Provider: "openai-compatible",
				BaseURL:  "https://api.example.test/v1",
				TokenEnv: "CAELIS_TEST_TOKEN",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.UpsertModel(ctx, ModelConfig{
		ProfileID:       "primary",
		Model:           "gpt-test",
		ReasoningLevels: []string{"High", "low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "openai-compatible" || cfg.BaseURL != "https://api.example.test/v1" || cfg.TokenEnv != "CAELIS_TEST_TOKEN" {
		t.Fatalf("model config = %#v, want hydrated profile fields", cfg)
	}
	if cfg.Alias != "openai-compatible/gpt-test" || cfg.ID == "" {
		t.Fatalf("model identity = %#v, want generated alias/id", cfg)
	}
	if !SupportsReasoningEffort(cfg, "high") || !SupportsReasoningEffort(cfg, "low") {
		t.Fatalf("reasoning support for %#v should include high and low", cfg.ReasoningLevels)
	}

	resolved, err := manager.ResolveModel(cfg.Alias)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ID != cfg.ID {
		t.Fatalf("resolved = %#v, want %s", resolved, cfg.ID)
	}
	choices, err := manager.ListModelChoices()
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 1 || !choices[0].Default || choices[0].ID != cfg.ID {
		t.Fatalf("choices = %#v, want one default model", choices)
	}
	if err := manager.DeleteModel(ctx, cfg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ResolveModel(cfg.ID); err == nil {
		t.Fatal("ResolveModel after delete error = nil, want error")
	}
}

func TestManagerCompactionPolicyPersistsNormalizedSettings(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	manager, err := NewManager(ctx, store, Document{})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := manager.SetCompactionPolicy(ctx, CompactionPolicy{
		Prompt:         "  Write a terse checkpoint.  ",
		MaxSourceChars: -1,
		Auto: AutoCompactionPolicy{
			Mode:           "off",
			WatermarkRatio: -0.5,
		},
		Retention: CompactionRetentionPolicy{
			TaskIndexLimit:       -10,
			ControllerIndexLimit: 7,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Prompt != "Write a terse checkpoint." || policy.MaxSourceChars != 0 || policy.Auto.Mode != "disabled" || policy.Auto.WatermarkRatio != 0 ||
		policy.Retention.TaskIndexLimit != 0 || policy.Retention.ControllerIndexLimit != 7 {
		t.Fatalf("policy = %#v, want normalized prompt and non-negative max chars", policy)
	}
	if CompactionTaskIndexLimit(policy) != DefaultCompactionTaskIndexLimit || CompactionControllerIndexLimit(policy) != 7 {
		t.Fatalf("effective retention = task:%d controller:%d, want defaults/controller override", CompactionTaskIndexLimit(policy), CompactionControllerIndexLimit(policy))
	}
	if got := manager.CompactionPolicy(); got != policy {
		t.Fatalf("CompactionPolicy() = %#v, want %#v", got, policy)
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Compaction.Prompt != "Write a terse checkpoint." || doc.Compaction.Auto.Mode != "disabled" || doc.Compaction.Retention.ControllerIndexLimit != 7 {
		t.Fatalf("persisted compaction policy = %#v, want prompt", doc.Compaction)
	}
	loaded, err := NewManager(ctx, store, Document{
		Compaction: CompactionPolicy{Prompt: "default prompt", MaxSourceChars: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.CompactionPolicy(); got.Prompt != "Write a terse checkpoint." || got.MaxSourceChars != 0 || got.Auto.Mode != "disabled" || got.Retention.ControllerIndexLimit != 7 {
		t.Fatalf("loaded compaction policy = %#v, want persisted override", got)
	}
}

func TestManagerSkillPolicyPersistsNormalizedSettings(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	manager, err := NewManager(ctx, store, Document{})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := manager.SetSkillPolicy(ctx, SkillPolicy{
		LoadingMode:       "metadata-only",
		MaxExpansionChars: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.LoadingMode != SkillLoadingModeMetadataOnly || policy.MaxExpansionChars != 0 {
		t.Fatalf("policy = %#v, want metadata-only with non-negative expansion chars", policy)
	}
	if got := manager.SkillPolicy(); got != policy {
		t.Fatalf("SkillPolicy() = %#v, want %#v", got, policy)
	}
	if SkillMetadataEnabled(policy) != true || SkillExpansionEnabled(policy) != false || SkillExpansionBudget(policy) != 0 {
		t.Fatalf("effective metadata/expansion = %v/%v/%d, want true/false/0", SkillMetadataEnabled(policy), SkillExpansionEnabled(policy), SkillExpansionBudget(policy))
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Skills.LoadingMode != SkillLoadingModeMetadataOnly || doc.Skills.MaxExpansionChars != 0 {
		t.Fatalf("persisted skill policy = %#v, want normalized policy", doc.Skills)
	}
	loaded, err := NewManager(ctx, store, Document{
		Skills: SkillPolicy{LoadingMode: SkillLoadingModeExplicit, MaxExpansionChars: 123},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.SkillPolicy(); got.LoadingMode != SkillLoadingModeMetadataOnly || got.MaxExpansionChars != 0 {
		t.Fatalf("loaded skill policy = %#v, want persisted override", got)
	}
}

func TestManagerModelToolsPersistNormalizedSettings(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	manager, err := NewManager(ctx, store, Document{})
	if err != nil {
		t.Fatal(err)
	}
	specs, err := manager.SetModelTools(ctx, []model.ToolSpec{
		model.NewProviderExecutedToolSpec("web_search", map[string]json.RawMessage{
			"openai": json.RawMessage(`{"type":"web_search_preview"}`),
		}),
		model.NewFunctionToolSpec("run_command", "local", map[string]any{"type": "object"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "web_search" {
		t.Fatalf("model tools = %#v, want only provider-native tool", specs)
	}
	listed := manager.ListModelTools()
	listed[0].ProviderPayloads["openai"][0] = '['
	if got := manager.ListModelTools(); string(got[0].ProviderPayloads["openai"]) != `{"type":"web_search_preview"}` {
		t.Fatalf("ListModelTools did not clone payload: %#v", got)
	}
	loaded, err := NewManager(ctx, store, Document{})
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.ListModelTools(); len(got) != 1 || got[0].Name != "web_search" {
		t.Fatalf("loaded model tools = %#v, want persisted web_search", got)
	}
}

func TestManagerSetRuntimePersistsNormalizedSettings(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	manager, err := NewManager(ctx, store, Document{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := manager.SetRuntime(ctx, config.Runtime{
		AppName:      " caelis-app ",
		UserID:       " tester ",
		WorkspaceKey: " repo ",
		WorkspaceCWD: " /repo ",
		Model:        " alpha ",
		Store: config.Store{
			Backend: " SQLITE ",
			URI:     " /tmp/sessions.db ",
		},
		Sandbox: config.Sandbox{
			Backend:       " HOST ",
			Network:       " OFF ",
			HelperPath:    " /usr/local/bin/helper ",
			ReadableRoots: []string{" /read "},
			WritableRoots: []string{" /write "},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.AppName != "caelis-app" || runtime.Store.Backend != "sqlite" || runtime.Sandbox.Backend != "host" || runtime.Sandbox.Network != "off" {
		t.Fatalf("runtime = %#v, want normalized runtime settings", runtime)
	}
	if runtime.Sandbox.ReadableRoots[0] != "/read" || runtime.Sandbox.WritableRoots[0] != "/write" {
		t.Fatalf("sandbox roots = %#v/%#v, want trimmed roots", runtime.Sandbox.ReadableRoots, runtime.Sandbox.WritableRoots)
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Runtime.Store.Backend != "sqlite" || doc.Runtime.Sandbox.HelperPath != "/usr/local/bin/helper" {
		t.Fatalf("persisted runtime = %#v, want normalized runtime", doc.Runtime)
	}
}

func TestManagerACPAgentUpsertDeletePersistsNormalizedAgents(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	manager, err := NewManager(ctx, store, Document{})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := manager.UpsertACPAgent(ctx, plugin.ACPAgentDescriptor{
		Name:        " Helper ",
		Description: " review code ",
		Command:     " helper-acp ",
		Args:        []string{" --stdio "},
		Env:         map[string]string{"HELPER_TOKEN": "secret"},
		WorkDir:     " /repo ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.Name != "helper" || agent.Command != "helper-acp" || agent.WorkDir != "/repo" {
		t.Fatalf("agent = %#v, want normalized fields", agent)
	}
	agents := manager.ListACPAgents()
	if len(agents) != 1 || agents[0].Name != "helper" || agents[0].Env["HELPER_TOKEN"] != "secret" {
		t.Fatalf("agents = %#v, want persisted helper", agents)
	}
	agents[0].Env["HELPER_TOKEN"] = "changed"
	if again := manager.ListACPAgents(); again[0].Env["HELPER_TOKEN"] != "secret" {
		t.Fatalf("agent list was not cloned: %#v", again[0].Env)
	}
	replacement, err := manager.UpsertACPAgent(ctx, plugin.ACPAgentDescriptor{
		Name:    "helper",
		Command: "helper-next",
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Command != "helper-next" || len(manager.ListACPAgents()) != 1 {
		t.Fatalf("replacement = %#v agents=%#v, want one replaced helper", replacement, manager.ListACPAgents())
	}
	if err := manager.DeleteACPAgent(ctx, "helper"); err != nil {
		t.Fatal(err)
	}
	if agents := manager.ListACPAgents(); len(agents) != 0 {
		t.Fatalf("agents after delete = %#v, want none", agents)
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Agents) != 0 {
		t.Fatalf("persisted agents = %#v, want delete persisted", doc.Agents)
	}
}

func TestManagerDisableACPAgentPersistsTombstoneAndUpsertClearsIt(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	manager, err := NewManager(ctx, store, Document{
		Agents: []plugin.ACPAgentDescriptor{{
			Name:    "Helper",
			Command: "helper-acp",
		}},
		DisabledAgents: []string{"old", "Helper"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.DisableACPAgent(ctx, " Helper "); err != nil {
		t.Fatal(err)
	}
	if agents := manager.ListACPAgents(); len(agents) != 0 {
		t.Fatalf("agents after disable = %#v, want none", agents)
	}
	if disabled := manager.ListDisabledACPAgents(); len(disabled) != 2 || disabled[0] != "helper" || disabled[1] != "old" {
		t.Fatalf("disabled agents = %#v, want helper/old", disabled)
	}
	if _, err := manager.UpsertACPAgent(ctx, plugin.ACPAgentDescriptor{Name: "helper", Command: "helper-next"}); err != nil {
		t.Fatal(err)
	}
	if disabled := manager.ListDisabledACPAgents(); len(disabled) != 1 || disabled[0] != "old" {
		t.Fatalf("disabled agents after upsert = %#v, want old", disabled)
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.DisabledAgents) != 1 || doc.DisabledAgents[0] != "old" {
		t.Fatalf("persisted disabled agents = %#v, want old", doc.DisabledAgents)
	}
}

func TestNormalizeModelConfigKnownProviderEndpointIDs(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		baseURL    string
		wantID     string
		wantPrefix string
	}{
		{name: "mimo default", provider: "xiaomi", baseURL: "https://api.xiaomimimo.com/v1", wantID: "api-cn", wantPrefix: "xiaomi@api-cn/"},
		{name: "mimo token plan", provider: "xiaomi", baseURL: "https://token-plan-cn.xiaomimimo.com/v1", wantID: "token-plan-cn", wantPrefix: "xiaomi@token-plan-cn/"},
		{name: "minimax default", provider: "minimax", baseURL: "https://api.minimaxi.com/anthropic", wantID: "default", wantPrefix: "minimax@default/"},
		{name: "gemini default", provider: "gemini", baseURL: "https://generativelanguage.googleapis.com/v1beta", wantID: "default", wantPrefix: "gemini@default/"},
		{name: "volcengine standard", provider: "volcengine", baseURL: "https://ark.cn-beijing.volces.com/api/v3", wantID: "standard", wantPrefix: "volcengine@standard/"},
		{name: "volcengine coding", provider: "volcengine-coding-plan", baseURL: "https://ark.cn-beijing.volces.com/api/coding/v3", wantID: "coding-plan", wantPrefix: "volcengine-coding-plan@coding-plan/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NormalizeModelConfig(ModelConfig{
				Provider: tc.provider,
				BaseURL:  tc.baseURL,
				Model:    "test-model",
			})
			if cfg.EndpointID != tc.wantID {
				t.Fatalf("EndpointID = %q, want %q", cfg.EndpointID, tc.wantID)
			}
			if got := cfg.ID; len(got) < len(tc.wantPrefix) || got[:len(tc.wantPrefix)] != tc.wantPrefix {
				t.Fatalf("ID = %q, want prefix %q", got, tc.wantPrefix)
			}
		})
	}
}

func TestProfilePersistTokenDoesNotPropagateToModelConfig(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	manager, err := NewManager(ctx, store, Document{
		Models: ModelCatalog{
			Profiles: []ModelProfile{{
				ID:           "primary",
				Provider:     "openai-compatible",
				BaseURL:      "https://api.example.test/v1",
				Token:        "profile-secret",
				PersistToken: true,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.UpsertModel(ctx, ModelConfig{
		ProfileID: "primary",
		Model:     "gpt-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "profile-secret" {
		t.Fatalf("runtime token = %q, want profile token", cfg.Token)
	}
	if cfg.PersistToken {
		t.Fatal("model config inherited PersistToken from profile")
	}

	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Models.Profiles) != 1 || doc.Models.Profiles[0].Token != "profile-secret" {
		t.Fatalf("persisted profiles = %#v, want profile token retained", doc.Models.Profiles)
	}
	for _, persisted := range doc.Models.Configs {
		if persisted.ID == cfg.ID {
			if persisted.Token != "" || persisted.PersistToken {
				t.Fatalf("persisted model config = %#v, want token redacted and PersistToken false", persisted)
			}
			return
		}
	}
	t.Fatalf("persisted model config %q not found in %#v", cfg.ID, doc.Models.Configs)
}
