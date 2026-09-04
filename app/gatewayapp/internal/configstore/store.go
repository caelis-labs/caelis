package configstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/atomicfile"
	policyapi "github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/plugin"
)

// ErrConfigurationRevisionConflict identifies a compare-and-save request that
// observed a different persisted configuration revision.
var ErrConfigurationRevisionConflict = errors.New("gatewayapp: configuration revision conflict")

// ConfigurationRevisionConflict reports the expected and persisted revisions
// for a failed compare-and-save operation.
type ConfigurationRevisionConflict struct {
	Expected uint64
	Actual   uint64
}

func (e *ConfigurationRevisionConflict) Error() string {
	if e == nil {
		return ErrConfigurationRevisionConflict.Error()
	}
	return fmt.Sprintf("%s: expected %d, actual %d", ErrConfigurationRevisionConflict, e.Expected, e.Actual)
}

func (e *ConfigurationRevisionConflict) Is(target error) bool {
	return target == ErrConfigurationRevisionConflict
}

type MarketplaceConfig = plugin.MarketplaceConfig
type PluginConfig = plugin.Config

type SandboxConfig struct {
	RequestedType    string   `json:"requested_type,omitempty"`
	HelperPath       string   `json:"helper_path,omitempty"`
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
	// DefaultAlias and DefaultID are decoded only to promote older v2 records
	// into ModelProfiles' single default selection. Normalize clears them
	// before current documents are persisted.
	DefaultAlias      string                               `json:"default_alias,omitempty"`
	DefaultID         string                               `json:"default_model_id,omitempty"`
	DefaultEffort     string                               `json:"-"`
	DefaultFastMode   bool                                 `json:"-"`
	ProviderEndpoints []modelconfig.ProviderEndpointConfig `json:"provider_endpoints,omitempty"`
	Configs           []modelconfig.Config                 `json:"configs,omitempty"`
}

type Store struct {
	gate           *operationGate
	pathMu         sync.RWMutex
	migrationMu    sync.RWMutex
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
		gate: newOperationGate(),
		path: filepath.Join(root, "config.json"),
	}
}

type operationGate struct {
	token chan struct{}
}

func newOperationGate() *operationGate {
	gate := &operationGate{token: make(chan struct{}, 1)}
	gate.token <- struct{}{}
	return gate
}

func (g *operationGate) Lock() {
	_ = g.LockContext(context.Background())
}

func (g *operationGate) LockContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.token:
		if err := ctx.Err(); err != nil {
			g.token <- struct{}{}
			return err
		}
		return nil
	}
}

func (g *operationGate) Unlock() {
	g.token <- struct{}{}
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	s.pathMu.RLock()
	defer s.pathMu.RUnlock()
	return s.path
}

func (s *Store) SetPath(path string) {
	_ = s.SetPathContext(context.Background(), path)
}

func (s *Store) SetPathContext(ctx context.Context, path string) error {
	if s == nil {
		return nil
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	s.pathMu.Lock()
	defer s.pathMu.Unlock()
	path = strings.TrimSpace(path)
	if path != s.path {
		s.migrationMu.Lock()
		s.migration = MigrationReport{}
		s.migrationMu.Unlock()
	}
	s.path = path
	return nil
}

// MigrationReport returns a detached report for the legacy conversion most
// recently observed by this Store. The report is process-local operational
// state and is not persisted in AppConfig.
func (s *Store) MigrationReport() MigrationReport {
	if s == nil {
		return MigrationReport{}
	}
	s.migrationMu.RLock()
	defer s.migrationMu.RUnlock()
	return cloneMigrationReport(s.migration)
}

func LoadAppConfig(root string) (AppConfig, error) {
	store := New(root)
	if store == nil {
		return AppConfig{SchemaVersion: SchemaVersionV2}, nil
	}
	return store.Load()
}

// CompareAndSave atomically persists doc only when the current configuration
// revision equals expected. The returned document carries the committed next
// revision, including when a post-commit durability error is reported.
func (s *Store) CompareAndSave(ctx context.Context, expected uint64, doc AppConfig) (AppConfig, error) {
	return s.save(ctx, &expected, doc)
}

type AtomicWriteOps struct {
	CreateTemp func(string, string) (*os.File, error)
	SyncFile   func(*os.File) error
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

// MarkWriteCommitted classifies an error produced after a caller-owned commit
// point so higher layers roll forward instead of reporting a rejected write.
func MarkWriteCommitted(err error) error {
	return writeCommittedError(err)
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
	if ops.SyncFile == nil {
		ops.SyncFile = func(file *os.File) error {
			return file.Sync()
		}
	}
	if ops.Rename == nil {
		ops.Rename = atomicfile.Replace
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
	if err := ops.SyncFile(tmp); err != nil {
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
		// Never fall back to an in-place truncate-and-write. A failed atomic
		// replacement must leave the previous canonical document intact so
		// credential retirement recovery always has a readable reachability
		// source after abrupt process termination.
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
