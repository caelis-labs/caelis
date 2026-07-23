package gatewayapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
)

func TestAppConfigStoreRejectsCredentialMaterial(t *testing.T) {
	t.Parallel()

	for name, configured := range map[string]ModelConfig{
		"token":     {Provider: "deepseek", Model: "reasoner", Token: "secret"},
		"token env": {Provider: "deepseek", Model: "reasoner", TokenEnv: "DEEPSEEK_API_KEY"},
		"flag":      {Provider: "deepseek", Model: "reasoner", PersistToken: true},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			err := newAppConfigStore(root).Save(AppConfig{Models: persistedModelConfig{Configs: []ModelConfig{configured}}})
			if err == nil || !strings.Contains(err.Error(), "credential store") {
				t.Fatalf("Save() error = %v, want credential-store diagnostic", err)
			}
			if _, statErr := os.Stat(filepath.Join(root, "config.json")); !os.IsNotExist(statErr) {
				t.Fatalf("config file after rejected write: %v", statErr)
			}
		})
	}
}

func TestNewLocalStackStoresStartupAPIKeyBehindOpaqueReference(t *testing.T) {
	root := t.TempDir()
	secret := "startup-secret"
	stack, err := NewLocalStack(Config{
		StoreDir: root, WorkspaceKey: "credential-test", WorkspaceCWD: t.TempDir(),
		Model: ModelConfig{Provider: "deepseek", API: providers.APIDeepSeek, Model: "reasoner", Token: secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Models.ProviderEndpoints) != 1 || !strings.HasPrefix(doc.Models.ProviderEndpoints[0].CredentialRef, "apikey:") {
		t.Fatalf("persisted provider endpoints = %#v, want opaque credential reference", doc.Models.ProviderEndpoints)
	}
	raw := readConfigFileForTest(t, root)
	if strings.Contains(raw, secret) || strings.Contains(raw, `"token"`) || strings.Contains(raw, `"token_env"`) {
		t.Fatalf("config persisted credential material:\n%s", raw)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(filepath.Join(root, "config.json"))
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("config permissions = %#o, want 0600", info.Mode().Perm())
		}
	}
}

func TestLegacyMigrationMovesEnvironmentCredentialBehindOpaqueReference(t *testing.T) {
	root := t.TempDir()
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
        "token_env": "DEEPSEEK_API_KEY"
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
		t.Fatal(err)
	}
	if len(doc.Models.ProviderEndpoints) != 1 || !strings.HasPrefix(doc.Models.ProviderEndpoints[0].CredentialRef, "apikey:") {
		t.Fatalf("migrated provider endpoints = %#v", doc.Models.ProviderEndpoints)
	}
	persisted := readConfigFileForTest(t, root)
	if strings.Contains(persisted, "DEEPSEEK_API_KEY") || !strings.Contains(persisted, `"schema_version": 2`) {
		t.Fatalf("migrated AppConfig = %s", persisted)
	}
	credentials, err := credentialstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	source, err := credentials.LookupSource(context.Background(), doc.Models.ProviderEndpoints[0].CredentialRef)
	if err != nil || source.Environment != "DEEPSEEK_API_KEY" {
		t.Fatalf("credential source = %#v, %v", source, err)
	}
}

func TestAppConfigStoreDoesNotPersistImplicitSandboxNetworkDefault(t *testing.T) {
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
	if doc.Sandbox.NetworkEnabled != nil {
		t.Fatalf("Sandbox.NetworkEnabled = %#v, want unset persisted config value", doc.Sandbox.NetworkEnabled)
	}
	effective := configstore.DefaultSandboxConfig(doc.Sandbox)
	if effective.NetworkEnabled == nil || !*effective.NetworkEnabled {
		t.Fatalf("effective Sandbox.NetworkEnabled = %#v, want semantic true default", effective.NetworkEnabled)
	}
	assertConfigSandboxNetworkUnset(t, filepath.Join(root, "config.json"))
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
	if len(doc.Models.ProviderEndpoints) != 0 {
		t.Fatalf("provider endpoints loaded from intermediate connections = %#v, want none", doc.Models.ProviderEndpoints)
	}
	if err := store.Save(doc); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	persisted := readConfigFileForTest(t, root)
	if strings.Contains(persisted, `"connections"`) || strings.Contains(persisted, "MIMO_TOKEN_PLAN_API_KEY") {
		t.Fatalf("config persisted intermediate connections data:\n%s", persisted)
	}
}

func TestAppConfigStoreMigratesRecognizableLegacyUppercaseModelConfig(t *testing.T) {
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
	if doc.SchemaVersion != configstore.SchemaVersionV2 || len(doc.Models.Configs) != 1 || len(doc.Models.ProviderEndpoints) != 1 || len(doc.ModelProfiles.Profiles) != 1 {
		t.Fatalf("LoadAppConfig() = %#v, want one migrated provider profile", doc)
	}
	if got := readConfigFileForTest(t, root); strings.Contains(got, "legacy-token") || !strings.Contains(got, `"schema_version": 2`) {
		t.Fatalf("migrated AppConfig = %s", got)
	}
	backup, err := os.ReadFile(filepath.Join(root, "config.json.v1.bak"))
	if err != nil || string(backup) != raw {
		t.Fatalf("legacy backup = %q, %v; want original bytes", backup, err)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(filepath.Join(root, "config.json.v1.bak"))
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("legacy backup permissions = %#o, want 0600", info.Mode().Perm())
		}
	}
	credentials, err := credentialstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	source, err := credentials.LookupSource(context.Background(), doc.Models.ProviderEndpoints[0].CredentialRef)
	if err != nil || source.APIKey != "legacy-token" {
		t.Fatalf("credential source = %#v, %v", source, err)
	}
}

func TestAppConfigStoreIgnoresUnrecognizedLegacyUppercaseModelsKey(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll(root) error = %v", err)
	}
	raw := `{
  "Models": {
    "default_alias": "deepseek/deepseek-v4-flash"
  }
}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error = %v", err)
	}
	doc, err := LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	if doc.SchemaVersion != configstore.SchemaVersionV2 || len(doc.Models.Configs) != 0 {
		t.Fatalf("LoadAppConfig() = %#v, want empty current configuration", doc)
	}
	if got := readConfigFileForTest(t, root); got != raw {
		t.Fatalf("legacy file was rewritten:\n%s", got)
	}
	replacement := newAppConfigStore(root)
	if err := replacement.Save(AppConfig{}); err != nil {
		t.Fatalf("Save(current replacement) error = %v", err)
	}
	backup, err := os.ReadFile(filepath.Join(root, "config.json.v1.bak"))
	if err != nil || string(backup) != raw {
		t.Fatalf("explicit replacement backup = %q, %v; want original bytes", backup, err)
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

func TestAppConfigStoreCanPersistPlugins(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := newAppConfigStore(root)
	err := store.Save(AppConfig{
		Plugins: []PluginConfig{
			{
				ID:          "superpowers",
				Name:        "Superpowers",
				Root:        "/some/root/superpowers",
				Manifest:    "/some/root/superpowers/.caelis-plugin/plugin.json",
				Kind:        "caelis",
				Enabled:     true,
				Version:     "5.1.0",
				Description: "A great plugin",
				Managed:     true,
				CacheRoot:   "/some/root",
			},
		},
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	doc, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(doc.Plugins) != 1 {
		t.Fatalf("len(doc.Plugins) = %d, want 1", len(doc.Plugins))
	}
	p := doc.Plugins[0]
	if p.ID != "superpowers" || p.Name != "Superpowers" || p.Root != "/some/root/superpowers" || p.Kind != "caelis" || !p.Enabled || !p.Managed || p.CacheRoot != "/some/root" {
		t.Errorf("unexpected plugin persisted contents: %+v", p)
	}
}
