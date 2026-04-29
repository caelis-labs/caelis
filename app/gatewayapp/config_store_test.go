package gatewayapp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	sdkproviders "github.com/OnslaughtSnail/caelis/sdk/model/providers"
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
				API:      sdkproviders.APIAnthropicCompatible,
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
	if got := doc.Models.Configs[0].TokenEnv; got != "MINIMAX_API_KEY" {
		t.Fatalf("persisted token_env = %q, want MINIMAX_API_KEY", got)
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
				API:          sdkproviders.APIDeepSeek,
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
	if len(doc.Models.Configs) != 1 || doc.Models.Configs[0].Token != "persist-me" {
		t.Fatalf("persisted configs = %#v, want explicit token persistence", doc.Models.Configs)
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
	if cfg.Alias != "deepseek/deepseek-v4-flash" || cfg.Token != "legacy-token" || cfg.AuthType != sdkproviders.AuthAPIKey {
		t.Fatalf("loaded legacy config = %#v", cfg)
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
