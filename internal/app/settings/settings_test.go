package settings

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
