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
	doc, err := configstore.LoadAppConfig(root)
	if err != nil {
		return AppConfig{}, err
	}
	if err := validateProductAgentRoster(doc.AgentRoster); err != nil {
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
	if err := validateProductAgentRoster(doc.AgentRoster); err != nil {
		return AppConfig{}, err
	}
	return doc, nil
}

func (s *appConfigStore) Save(doc AppConfig) error {
	if s == nil || s.inner == nil {
		return nil
	}
	if err := validateProductAgentRoster(doc.AgentRoster); err != nil {
		return err
	}
	if s.saveHook != nil {
		if err := s.saveHook(doc); err != nil {
			return err
		}
	}
	s.inner.SetPath(s.path)
	return s.inner.Save(doc)
}

func validateProductAgentRoster(roster controlagents.Configuration) error {
	if err := controlagents.ValidateConfiguration(roster); err != nil {
		return fmt.Errorf("gatewayapp: invalid Agent roster: %w", err)
	}
	for _, agent := range controlagents.ListAgents(roster) {
		if forbiddenRosterAgentID(agent.ID) {
			return fmt.Errorf("gatewayapp: roster Agent %q conflicts with a product command or system Agent", agent.ID)
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
