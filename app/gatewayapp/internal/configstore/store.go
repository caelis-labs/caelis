package configstore

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	policyapi "github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/plugin"
)

type MarketplaceConfig = plugin.MarketplaceConfig
type PluginConfig = plugin.Config

type SandboxConfig struct {
	RequestedType    string   `json:"requested_type,omitempty"`
	HelperPath       string   `json:"helper_path,omitempty"`
	ReadableRoots    []string `json:"readable_roots,omitempty"`
	WritableRoots    []string `json:"writable_roots,omitempty"`
	ReadOnlySubpaths []string `json:"read_only_subpaths,omitempty"`
	NetworkEnabled   *bool    `json:"network_enabled,omitempty"`
}

type RuntimeConfig struct {
	ApprovalMode  string `json:"approval_mode,omitempty"`
	PolicyProfile string `json:"policy_profile,omitempty"`
}

// PersistedModelConfig is the current provider infrastructure shape. Provider
// endpoints are deliberately named separately from product ModelProfiles.
type PersistedModelConfig struct {
	DefaultAlias      string                               `json:"default_alias,omitempty"`
	DefaultID         string                               `json:"default_model_id,omitempty"`
	ProviderEndpoints []modelconfig.ProviderEndpointConfig `json:"provider_endpoints,omitempty"`
	Configs           []modelconfig.Config                 `json:"configs,omitempty"`
}

type Store struct {
	mu             sync.Mutex
	path           string
	writeOps       AtomicWriteOps
	backupWriteOps AtomicWriteOps
	migration      MigrationReport
}

func New(root string) *Store {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	return &Store{
		path: filepath.Join(root, "config.json"),
	}
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

func (s *Store) SetPath(path string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path = strings.TrimSpace(path)
	if path != s.path {
		s.migration = MigrationReport{}
	}
	s.path = path
}

// MigrationReport returns a detached report for the legacy conversion most
// recently observed by this Store. The report is process-local operational
// state and is not persisted in AppConfig.
func (s *Store) MigrationReport() MigrationReport {
	if s == nil {
		return MigrationReport{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneMigrationReport(s.migration)
}

func LoadAppConfig(root string) (AppConfig, error) {
	store := New(root)
	if store == nil {
		return AppConfig{SchemaVersion: SchemaVersionV2}, nil
	}
	return store.Load()
}

type AtomicWriteOps struct {
	CreateTemp func(string, string) (*os.File, error)
	Rename     func(string, string) error
	Chmod      func(string, os.FileMode) error
	FsyncDir   func(string) error
}

// CommittedWriteError reports a write failure after the destination file has
// already been replaced. Callers must roll forward from the new file instead
// of restoring state that the file now references.
type CommittedWriteError struct {
	err error
}

func (e *CommittedWriteError) Error() string {
	if e == nil || e.err == nil {
		return "committed write failed"
	}
	return e.err.Error()
}

func (e *CommittedWriteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// WriteCommitted reports whether err represents a failure after the write's
// commit point. It remains true through ordinary wrapping and errors.Join.
func WriteCommitted(err error) bool {
	var committed *CommittedWriteError
	return errors.As(err, &committed)
}

func writeCommittedError(err error) error {
	if err == nil || WriteCommitted(err) {
		return err
	}
	return &CommittedWriteError{err: err}
}

func AtomicWriteFile(path string, data []byte, perm os.FileMode, ops AtomicWriteOps) error {
	if ops.CreateTemp == nil {
		ops.CreateTemp = os.CreateTemp
	}
	renameProvided := ops.Rename != nil
	if ops.Rename == nil {
		ops.Rename = os.Rename
	}
	if ops.Chmod == nil {
		ops.Chmod = os.Chmod
	}
	if ops.FsyncDir == nil {
		ops.FsyncDir = syncDir
	}
	dir := filepath.Dir(path)
	tmp, err := ops.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ops.Chmod(tmpPath, perm); err != nil {
		return err
	}
	if err := ops.Rename(tmpPath, path); err != nil {
		if !renameProvided && runtime.GOOS == "windows" {
			fallbackCommitted, fallbackErr := writeFileInPlace(path, data, perm, ops.Chmod)
			if fallbackErr == nil {
				if fsyncErr := ops.FsyncDir(dir); fsyncErr != nil {
					return writeCommittedError(fsyncErr)
				}
				return nil
			}
			fallbackErr = errors.Join(err, fallbackErr)
			if fallbackCommitted {
				return writeCommittedError(fallbackErr)
			}
			return fallbackErr
		}
		return err
	}
	committed = true
	if err := ops.Chmod(path, perm); err != nil {
		return writeCommittedError(err)
	}
	if err := ops.FsyncDir(dir); err != nil {
		return writeCommittedError(err)
	}
	return nil
}

func writeFileInPlace(path string, data []byte, perm os.FileMode, chmod func(string, os.FileMode) error) (bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return false, err
	}
	committed := true
	writeErr := func() error {
		if _, err := file.Write(data); err != nil {
			return err
		}
		return file.Sync()
	}()
	closeErr := file.Close()
	if writeErr != nil {
		return committed, writeErr
	}
	if closeErr != nil {
		return committed, closeErr
	}
	if chmod != nil {
		return committed, chmod(path, perm)
	}
	return committed, nil
}

func normalizePersistedModelsForSave(models PersistedModelConfig) PersistedModelConfig {
	for _, cfg := range models.Configs {
		if modelconfig.ConfigCarriesProviderEndpointFields(cfg) {
			models.ProviderEndpoints = append(models.ProviderEndpoints, modelconfig.ProviderEndpointFromConfig(cfg))
		}
	}
	return models
}

func dedupeModelConfigsForSave(configs []modelconfig.Config) []modelconfig.Config {
	if len(configs) == 0 {
		return nil
	}
	out := make([]modelconfig.Config, 0, len(configs))
	seen := make(map[string]struct{}, len(configs))
	for _, cfg := range configs {
		cfg = modelconfig.SanitizePersistedConfig(cfg)
		if cfg.ID == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(cfg.ID))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cfg)
	}
	return out
}

func dedupeProviderEndpointsForSave(endpoints []modelconfig.ProviderEndpointConfig) []modelconfig.ProviderEndpointConfig {
	if len(endpoints) == 0 {
		return nil
	}
	out := make([]modelconfig.ProviderEndpointConfig, 0, len(endpoints))
	seen := make(map[string]struct{}, len(endpoints))
	for _, endpoint := range endpoints {
		endpoint = modelconfig.SanitizePersistedProviderEndpoint(endpoint)
		if endpoint.ID == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(endpoint.ID))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, endpoint)
	}
	return out
}

func NormalizeSandboxConfig(cfg SandboxConfig) SandboxConfig {
	cfg.RequestedType = strings.ToLower(strings.TrimSpace(cfg.RequestedType))
	cfg.HelperPath = strings.TrimSpace(cfg.HelperPath)
	cfg.ReadableRoots = DedupeStrings(cfg.ReadableRoots)
	cfg.WritableRoots = DedupeStrings(cfg.WritableRoots)
	cfg.ReadOnlySubpaths = DedupeStrings(cfg.ReadOnlySubpaths)
	if cfg.NetworkEnabled != nil {
		value := *cfg.NetworkEnabled
		cfg.NetworkEnabled = &value
	}
	return cfg
}

func NormalizeRuntimeConfig(cfg RuntimeConfig) RuntimeConfig {
	cfg.ApprovalMode = normalizeApprovalMode(cfg.ApprovalMode)
	cfg.PolicyProfile = policyapi.NormalizeProfileName(cfg.PolicyProfile)
	return cfg
}

func normalizeApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "manual":
		return "manual"
	case "", "auto", "auto-review", "auto_review", "autoreview":
		return "auto-review"
	default:
		return "auto-review"
	}
}

func DefaultSandboxConfig(cfg SandboxConfig) SandboxConfig {
	cfg = NormalizeSandboxConfig(cfg)
	if cfg.NetworkEnabled == nil {
		cfg.NetworkEnabled = boolPtr(true)
	}
	return cfg
}

func SandboxNetworkEnabled(cfg SandboxConfig) bool {
	cfg = NormalizeSandboxConfig(cfg)
	if cfg.NetworkEnabled == nil {
		return true
	}
	return *cfg.NetworkEnabled
}

func boolPtr(value bool) *bool {
	return &value
}

func DedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func DedupePluginConfigs(configs []PluginConfig) []PluginConfig {
	return plugin.DedupeConfigs(configs)
}

func DedupeMarketplaceConfigs(configs []MarketplaceConfig) []MarketplaceConfig {
	return plugin.DedupeMarketplaceConfigs(configs)
}

func UpsertMarketplaceConfig(configs []MarketplaceConfig, entry MarketplaceConfig) []MarketplaceConfig {
	return plugin.UpsertMarketplaceConfig(configs, entry)
}

func NormalizeMarketplaceConfig(in MarketplaceConfig) MarketplaceConfig {
	return plugin.NormalizeMarketplaceConfig(in)
}

func NormalizePluginConfig(in PluginConfig) PluginConfig {
	return plugin.NormalizeConfig(in)
}
