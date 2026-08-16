package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

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
	return s.LoadContext(context.Background())
}

func (s *appConfigStore) LoadContext(ctx context.Context) (AppConfig, error) {
	if s == nil || s.inner == nil {
		return AppConfig{}, nil
	}
	if err := s.inner.SetPathContext(ctx, s.path); err != nil {
		return AppConfig{}, err
	}
	doc, err := s.inner.LoadContext(ctx)
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

func (s *appConfigStore) CompareAndSave(ctx context.Context, expected uint64, doc AppConfig) (AppConfig, error) {
	if s == nil || s.inner == nil {
		return doc, nil
	}
	if err := validateExternalAgents(doc.ExternalAgents); err != nil {
		return AppConfig{}, err
	}
	if s.saveHook != nil {
		if err := s.saveHook(doc); err != nil {
			if configstore.WriteCommitted(err) && s.savedHook != nil {
				s.savedHook()
			}
			if configstore.WriteCommitted(err) {
				readCtx, cancel := context.WithTimeout(context.WithoutCancel(contextOrBackground(ctx)), 5*time.Second)
				defer cancel()
				saved, loadErr := s.LoadContext(readCtx)
				if loadErr == nil {
					return saved, err
				}
				// The write reached config.json, but its committed revision could
				// not be observed. Preserve the candidate for live roll-forward
				// while keeping the revision explicitly unknown; callers must not
				// bind Runtime state or attempt compensation against the stale
				// pre-save revision carried by doc.
				doc.ConfigurationRevision = 0
				return doc, errors.Join(err, loadErr)
			}
			return doc, err
		}
	}
	if err := s.inner.SetPathContext(ctx, s.path); err != nil {
		return AppConfig{}, err
	}
	saved, err := s.inner.CompareAndSave(ctx, expected, doc)
	if s.savedHook != nil && (err == nil || configstore.WriteCommitted(err)) {
		s.savedHook()
	}
	return saved, err
}

// ConfigurationRevision returns the canonical persisted Host configuration
// revision. It reads the shared document instead of a process-local cache so
// status and later compare-and-save operations observe external writers.
func (s *runtimeComposition) ConfigurationRevision(ctx context.Context) (uint64, error) {
	if s == nil || s.store == nil {
		return 0, fmt.Errorf("gatewayapp: app config store unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	doc, err := s.store.LoadContext(ctx)
	if err != nil {
		return 0, err
	}
	return doc.ConfigurationRevision, nil
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
