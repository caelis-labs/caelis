package gatewayapp

import (
	"os"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/configstore"
)

type AppConfig = configstore.AppConfig
type AgentConfig = configstore.AgentConfig
type SandboxConfig = configstore.SandboxConfig
type RuntimeConfig = configstore.RuntimeConfig
type AgentProviderConfig = configstore.AgentProviderConfig
type persistedModelConfig = configstore.PersistedModelConfig
type PluginConfig = configstore.PluginConfig
type MarketplaceConfig = configstore.MarketplaceConfig

type appConfigStore struct {
	path     string
	inner    *configstore.Store
	saveHook func(AppConfig) error
}

func newAppConfigStore(root string) *appConfigStore {
	inner := configstore.New(root)
	if inner == nil {
		return nil
	}
	return &appConfigStore{
		path:  inner.Path(),
		inner: inner,
	}
}

func LoadAppConfig(root string) (AppConfig, error) {
	return configstore.LoadAppConfig(root)
}

func (s *appConfigStore) Load() (AppConfig, error) {
	if s == nil || s.inner == nil {
		return AppConfig{}, nil
	}
	s.inner.SetPath(s.path)
	return s.inner.Load()
}

func (s *appConfigStore) Save(doc AppConfig) error {
	if s == nil || s.inner == nil {
		return nil
	}
	if s.saveHook != nil {
		if err := s.saveHook(doc); err != nil {
			return err
		}
	}
	s.inner.SetPath(s.path)
	return s.inner.Save(doc)
}

type atomicWriteOps struct {
	createTemp func(string, string) (*os.File, error)
	rename     func(string, string) error
	chmod      func(string, os.FileMode) error
	fsyncDir   func(string) error
}

func atomicWriteFile(path string, data []byte, perm os.FileMode, ops atomicWriteOps) error {
	return configstore.AtomicWriteFile(path, data, perm, configstore.AtomicWriteOps{
		CreateTemp: ops.createTemp,
		Rename:     ops.rename,
		Chmod:      ops.chmod,
		FsyncDir:   ops.fsyncDir,
	})
}

func dedupeAgentConfigs(configs []AgentConfig) []AgentConfig {
	return configstore.DedupeAgentConfigs(configs)
}

func normalizeAgentConfig(in AgentConfig) AgentConfig {
	return configstore.NormalizeAgentConfig(in)
}

func normalizeSandboxConfig(cfg SandboxConfig) SandboxConfig {
	return configstore.NormalizeSandboxConfig(cfg)
}

func dedupeStrings(values []string) []string {
	return configstore.DedupeStrings(values)
}
