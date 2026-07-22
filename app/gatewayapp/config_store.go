package gatewayapp

import (
	"fmt"
	"os"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	controlagents "github.com/caelis-labs/caelis/control/agents"
)

type AppConfig = configstore.AppConfig
type SandboxConfig = configstore.SandboxConfig
type RuntimeConfig = configstore.RuntimeConfig
type persistedModelConfig = configstore.PersistedModelConfig
type PluginConfig = configstore.PluginConfig
type MarketplaceConfig = configstore.MarketplaceConfig

type appConfigStore struct {
	path      string
	inner     *configstore.Store
	saveHook  func(AppConfig) error
	savedHook func()
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
	doc, err := configstore.LoadAppConfig(root)
	if err != nil {
		return AppConfig{}, err
	}
	if err := validateExternalAgents(doc.ExternalAgents); err != nil {
		return AppConfig{}, err
	}
	return doc, nil
}

func (s *appConfigStore) Load() (AppConfig, error) {
	if s == nil || s.inner == nil {
		return AppConfig{}, nil
	}
	s.inner.SetPath(s.path)
	doc, err := s.inner.Load()
	if err != nil {
		return AppConfig{}, err
	}
	if err := validateExternalAgents(doc.ExternalAgents); err != nil {
		return AppConfig{}, err
	}
	return doc, nil
}

func (s *appConfigStore) MigrationReport() configstore.MigrationReport {
	if s == nil || s.inner == nil {
		return configstore.MigrationReport{}
	}
	s.inner.SetPath(s.path)
	return s.inner.MigrationReport()
}

func (s *appConfigStore) Save(doc AppConfig) error {
	if s == nil || s.inner == nil {
		return nil
	}
	if err := validateExternalAgents(doc.ExternalAgents); err != nil {
		return err
	}
	if s.saveHook != nil {
		if err := s.saveHook(doc); err != nil {
			if configstore.WriteCommitted(err) && s.savedHook != nil {
				s.savedHook()
			}
			return err
		}
	}
	s.inner.SetPath(s.path)
	err := s.inner.Save(doc)
	if s.savedHook != nil && (err == nil || configstore.WriteCommitted(err)) {
		s.savedHook()
	}
	return err
}

func validateExternalAgents(configuration controlagents.Configuration) error {
	if err := controlagents.ValidateConfiguration(configuration); err != nil {
		return fmt.Errorf("gatewayapp: invalid external Agent configuration: %w", err)
	}
	for _, agent := range controlagents.ListAgents(configuration) {
		if forbiddenExternalAgentID(agent.ID) {
			return fmt.Errorf("gatewayapp: external Agent %q conflicts with a product command or system Agent", agent.ID)
		}
	}
	return nil
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
