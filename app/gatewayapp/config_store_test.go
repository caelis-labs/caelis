package gatewayapp

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/impl/model/providers"
)

func TestAppConfigStoreSaveUsesSecurePermissionsAndRedactsTokenByDefault(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newAppConfigStore(root)
	err := store.Save(AppConfig{
		Models: persistedModelConfig{
			DefaultAlias: "minimax/minimax-m1",
			Configs: []ModelConfig{{
				Alias:    "minimax/minimax-m1",
				Provider: "minimax",
				API:      providers.APIAnthropicCompatible,
				Model:    "MiniMax-M1",
				Token:    "super-secret",
				TokenEnv: "MINIMAX_API_KEY",
			}},
		},
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	doc, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(doc.Models.Configs) != 1 {
		t.Fatalf("len(doc.Models.Configs) = %d, want 1", len(doc.Models.Configs))
	}
	if got := doc.Models.Configs[0].Token; got != "" {
		t.Fatalf("persisted token = %q, want redacted empty token", got)
	}
	if len(doc.Models.Profiles) != 1 {
		t.Fatalf("len(doc.Models.Profiles) = %d, want 1", len(doc.Models.Profiles))
	}
	if got := doc.Models.Profiles[0].TokenEnv; got != "MINIMAX_API_KEY" {
		t.Fatalf("persisted profile token_env = %q, want MINIMAX_API_KEY", got)
	}
	raw := readConfigFileForTest(t, root)
	for _, forbidden := range []string{"Token", "TokenEnv", "AuthType", "HeaderKey", "PersistToken", "MaxOutputTok"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("config contains legacy/noisy key %q:\n%s", forbidden, raw)
		}
	}
	if !strings.Contains(raw, `"token_env": "MINIMAX_API_KEY"`) {
		t.Fatalf("config = %s, want compact token_env key", raw)
	}

	if runtime.GOOS == "windows" {
		return
	}
	configInfo, err := os.Stat(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatalf("Stat(config.json) error = %v", err)
	}
	if got := configInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("config.json mode = %#o, want %#o", got, os.FileMode(0o600))
	}
	dirInfo, err := os.Stat(root)
	if err != nil {
		t.Fatalf("Stat(root) error = %v", err)
	}
	if got := dirInfo.Mode().Perm() & 0o077; got != 0 {
		t.Fatalf("root mode = %#o, want no group/world bits", dirInfo.Mode().Perm())
	}
}

func TestAppConfigStoreCanPersistTokenOnlyWhenExplicitlyEnabled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newAppConfigStore(root)
	err := store.Save(AppConfig{
		Models: persistedModelConfig{
			DefaultAlias: "deepseek/reasoner",
			Configs: []ModelConfig{{
				Alias:        "deepseek/reasoner",
				Provider:     "deepseek",
				API:          providers.APIDeepSeek,
				Model:        "deepseek-v4-pro",
				Token:        "persist-me",
				PersistToken: true,
			}},
		},
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	doc, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(doc.Models.Profiles) != 1 || doc.Models.Profiles[0].Token != "persist-me" {
		t.Fatalf("persisted profiles = %#v, want explicit token persistence", doc.Models.Profiles)
	}
	raw := readConfigFileForTest(t, root)
	if !strings.Contains(raw, `"token": "persist-me"`) {
		t.Fatalf("config = %s, want compact persisted token field", raw)
	}
	for _, forbidden := range []string{"Alias", "API", "AuthType", "HeaderKey", "TokenEnv", "PersistToken", "persist_token", "DefaultReasoningEffort", "Timeout", "timeout"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("config contains legacy/derived key %q:\n%s", forbidden, raw)
		}
	}
}

func TestAppConfigStoreDoesNotPersistEnvHydratedToken(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CAELIS_CONFIG_STORE_TOKEN", "env-secret")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll(root) error = %v", err)
	}
	raw := `{
  "models": {
    "default_alias": "deepseek/reasoner",
    "configs": [
      {
        "alias": "deepseek/reasoner",
        "provider": "deepseek",
        "api": "deepseek",
        "model": "deepseek-reasoner",
        "token_env": "CAELIS_CONFIG_STORE_TOKEN"
      }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error = %v", err)
	}
	store := newAppConfigStore(root)
	doc, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(doc.Models.Configs) != 1 {
		t.Fatalf("len(configs) = %d, want 1", len(doc.Models.Configs))
	}
	cfg := doc.Models.Configs[0]
	if cfg.Token != "env-secret" {
		t.Fatalf("loaded token = %q, want env token for runtime use", cfg.Token)
	}
	if cfg.PersistToken {
		t.Fatal("loaded env token set PersistToken=true, want false")
	}
	doc.Agents = []AgentConfig{{
		Name:    "helper",
		Command: "true",
	}}
	if err := store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	persisted := readConfigFileForTest(t, root)
	if strings.Contains(persisted, "env-secret") || strings.Contains(persisted, `"token"`) {
		t.Fatalf("config persisted env token:\n%s", persisted)
	}
	if !strings.Contains(persisted, `"token_env": "CAELIS_CONFIG_STORE_TOKEN"`) {
		t.Fatalf("config = %s, want token_env retained", persisted)
	}
}

func TestAppConfigStorePersistsSandboxNetworkEnabledDefault(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newAppConfigStore(root)
	if err := store.Save(AppConfig{}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	doc, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if doc.Sandbox.NetworkEnabled == nil || !*doc.Sandbox.NetworkEnabled {
		t.Fatalf("Sandbox.NetworkEnabled = %#v, want persisted true default", doc.Sandbox.NetworkEnabled)
	}
	raw := readConfigFileForTest(t, root)
	if !strings.Contains(raw, `"network_enabled": true`) {
		t.Fatalf("config = %s, want sandbox network_enabled default", raw)
	}
}

func TestAppConfigStoreLoadsManualSandboxNetworkDisabled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll(root) error = %v", err)
	}
	raw := `{"sandbox":{"network_enabled":false}}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error = %v", err)
	}

	store := newAppConfigStore(root)
	doc, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if doc.Sandbox.NetworkEnabled == nil || *doc.Sandbox.NetworkEnabled {
		t.Fatalf("Sandbox.NetworkEnabled = %#v, want manual false", doc.Sandbox.NetworkEnabled)
	}
	if err := store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	persisted := readConfigFileForTest(t, root)
	if !strings.Contains(persisted, `"network_enabled": false`) {
		t.Fatalf("config = %s, want manual false retained", persisted)
	}
}

func TestAppConfigStoreNormalizesRuntimeConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll(root) error = %v", err)
	}
	raw := `{"runtime":{"approval_mode":"auto","policy_profile":"workspace_write"}}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error = %v", err)
	}

	store := newAppConfigStore(root)
	doc, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if doc.Runtime.ApprovalMode != "auto-review" {
		t.Fatalf("Runtime.ApprovalMode = %q, want auto-review", doc.Runtime.ApprovalMode)
	}
	if doc.Runtime.PolicyProfile != "workspace-write" {
		t.Fatalf("Runtime.PolicyProfile = %q, want workspace-write", doc.Runtime.PolicyProfile)
	}

	doc.Runtime = RuntimeConfig{ApprovalMode: "manual", PolicyProfile: "auto-review"}
	if err := store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	persisted := readConfigFileForTest(t, root)
	if !strings.Contains(persisted, `"approval_mode": "manual"`) {
		t.Fatalf("config = %s, want manual approval mode retained", persisted)
	}
	if strings.Contains(persisted, `"policy_profile": "auto-review"`) {
		t.Fatalf("config = %s, want legacy approval mode removed from policy profile", persisted)
	}
}

func TestAppConfigStoreDefaultsRuntimeApprovalModeToAutoReview(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newAppConfigStore(root)
	if err := store.Save(AppConfig{}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	doc, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if doc.Runtime.ApprovalMode != "auto-review" {
		t.Fatalf("Runtime.ApprovalMode = %q, want auto-review", doc.Runtime.ApprovalMode)
	}
	raw := readConfigFileForTest(t, root)
	if !strings.Contains(raw, `"approval_mode": "auto-review"`) {
		t.Fatalf("config = %s, want explicit auto-review runtime default", raw)
	}
}

func TestAppConfigStoreIgnoresIntermediateConnectionsConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll(root) error = %v", err)
	}
	raw := `{
  "models": {
    "connections": [
      {
        "id": "xiaomi@token-plan-cn",
        "provider": "xiaomi",
        "endpoint_id": "token-plan-cn",
        "api": "mimo",
        "base_url": "https://token-plan-cn.xiaomimimo.com/v1",
        "token_env": "MIMO_TOKEN_PLAN_API_KEY"
      }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error = %v", err)
	}
	store := newAppConfigStore(root)
	doc, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(doc.Models.Profiles) != 0 {
		t.Fatalf("profiles loaded from intermediate connections = %#v, want none", doc.Models.Profiles)
	}
	if err := store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	persisted := readConfigFileForTest(t, root)
	if strings.Contains(persisted, `"connections"`) || strings.Contains(persisted, "MIMO_TOKEN_PLAN_API_KEY") {
		t.Fatalf("config persisted intermediate connections data:\n%s", persisted)
	}
}

func TestAppConfigStoreLoadsLegacyUppercaseModelConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll(root) error = %v", err)
	}
	raw := `{
  "models": {
    "default_alias": "deepseek/deepseek-v4-flash",
    "configs": [
      {
        "Alias": "deepseek/deepseek-v4-flash",
        "Provider": "deepseek",
        "API": "deepseek",
        "Model": "deepseek-v4-flash",
        "BaseURL": "https://api.deepseek.com/v1",
        "Token": "legacy-token",
        "PersistToken": true,
        "AuthType": "api_key"
      }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error = %v", err)
	}
	doc, err := LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	if len(doc.Models.Configs) != 1 {
		t.Fatalf("len(configs) = %d, want 1", len(doc.Models.Configs))
	}
	cfg := doc.Models.Configs[0]
	if cfg.Alias != "deepseek/deepseek-v4-flash" || cfg.Token != "legacy-token" || cfg.AuthType != providers.AuthAPIKey {
		t.Fatalf("loaded legacy config = %#v", cfg)
	}
}

func TestAtomicWriteFileKeepsOriginalOnRenameFailure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	original := []byte(`{"models":{"default_model_id":"old"}}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("WriteFile(original) error = %v", err)
	}
	renameErr := errors.New("rename failed")
	err := atomicWriteFile(path, []byte(`{"models":{"default_model_id":"new"}}`), 0o600, atomicWriteOps{
		rename: func(string, string) error { return renameErr },
	})
	if !errors.Is(err, renameErr) {
		t.Fatalf("atomicWriteFile() error = %v, want rename failure", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(config.json) error = %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("config after failed rename = %s, want original %s", got, original)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".config.json.*.tmp"))
	if err != nil {
		t.Fatalf("Glob(temp) error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files left after failed write: %#v", matches)
	}
}

func readConfigFileForTest(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatalf("ReadFile(config.json) error = %v", err)
	}
	return string(data)
}
