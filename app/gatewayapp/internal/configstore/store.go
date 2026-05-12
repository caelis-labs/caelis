package configstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/modelregistry"
)

type AppConfig struct {
	Models         PersistedModelConfig  `json:"models,omitempty"`
	Agents         []AgentConfig         `json:"agents,omitempty"`
	AgentProviders []AgentProviderConfig `json:"agent_providers,omitempty"`
	Sandbox        SandboxConfig         `json:"sandbox,omitempty"`
}

type AgentConfig struct {
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	WorkDir     string            `json:"work_dir,omitempty"`
	Builtin     bool              `json:"builtin,omitempty"`
}

type SandboxConfig struct {
	RequestedType    string   `json:"requested_type,omitempty"`
	HelperPath       string   `json:"helper_path,omitempty"`
	ReadableRoots    []string `json:"readable_roots,omitempty"`
	WritableRoots    []string `json:"writable_roots,omitempty"`
	ReadOnlySubpaths []string `json:"read_only_subpaths,omitempty"`
}

type AgentProviderConfig struct {
	ID       string         `json:"id,omitempty"`
	Kind     string         `json:"kind,omitempty"`
	Label    string         `json:"label,omitempty"`
	BaseURL  string         `json:"base_url,omitempty"`
	TokenEnv string         `json:"token_env,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type PersistedModelConfig struct {
	DefaultAlias string                        `json:"default_alias,omitempty"`
	DefaultID    string                        `json:"default_model_id,omitempty"`
	Profiles     []modelregistry.ProfileConfig `json:"profiles,omitempty"`
	Configs      []modelregistry.Config        `json:"configs,omitempty"`
}

type Store struct {
	mu   sync.Mutex
	path string
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
	return s.path
}

func (s *Store) SetPath(path string) {
	if s == nil {
		return
	}
	s.path = strings.TrimSpace(path)
}

func LoadAppConfig(root string) (AppConfig, error) {
	store := New(root)
	if store == nil {
		return AppConfig{}, nil
	}
	return store.Load()
}

func (s *Store) Load() (AppConfig, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return AppConfig{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked()
}

func (s *Store) loadUnlocked() (AppConfig, error) {
	data, err := os.ReadFile(s.path)
	if err == nil {
		var doc AppConfig
		if err := json.Unmarshal(data, &doc); err != nil {
			return AppConfig{}, fmt.Errorf("gatewayapp: decode app config: %w", err)
		}
		doc.Models.Profiles = dedupeModelProfiles(doc.Models.Profiles)
		doc.Models.Configs = dedupeModelConfigs(doc.Models.Configs)
		doc.Agents = DedupeAgentConfigs(doc.Agents)
		doc.Sandbox = NormalizeSandboxConfig(doc.Sandbox)
		return doc, nil
	}
	if !os.IsNotExist(err) {
		return AppConfig{}, err
	}
	return AppConfig{}, nil
}

func (s *Store) Save(doc AppConfig) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc.Models = normalizePersistedModelsForSave(doc.Models)
	doc.Models.Configs = dedupeModelConfigsForSave(doc.Models.Configs)
	doc.Models.Profiles = dedupeModelProfilesForSave(doc.Models.Profiles)
	doc.Agents = DedupeAgentConfigs(doc.Agents)
	doc.Sandbox = NormalizeSandboxConfig(doc.Sandbox)
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("gatewayapp: encode app config: %w", err)
	}
	if err := AtomicWriteFile(s.path, data, 0o600, AtomicWriteOps{}); err != nil {
		return err
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return err
	}
	return nil
}

type AtomicWriteOps struct {
	CreateTemp func(string, string) (*os.File, error)
	Rename     func(string, string) error
	Chmod      func(string, os.FileMode) error
	FsyncDir   func(string) error
}

func AtomicWriteFile(path string, data []byte, perm os.FileMode, ops AtomicWriteOps) error {
	if ops.CreateTemp == nil {
		ops.CreateTemp = os.CreateTemp
	}
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
		return err
	}
	committed = true
	if err := ops.Chmod(path, perm); err != nil {
		return err
	}
	return ops.FsyncDir(dir)
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return err
	}
	return nil
}

func DedupeAgentConfigs(configs []AgentConfig) []AgentConfig {
	if len(configs) == 0 {
		return nil
	}
	out := make([]AgentConfig, 0, len(configs))
	seen := make(map[string]struct{}, len(configs))
	for _, cfg := range configs {
		cfg = NormalizeAgentConfig(cfg)
		if cfg.Name == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(cfg.Name))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cfg)
	}
	return out
}

func NormalizeAgentConfig(in AgentConfig) AgentConfig {
	out := in
	out.Name = strings.ToLower(strings.TrimSpace(in.Name))
	out.Description = strings.TrimSpace(in.Description)
	out.Command = strings.TrimSpace(in.Command)
	out.WorkDir = strings.TrimSpace(in.WorkDir)
	if len(in.Args) > 0 {
		out.Args = append([]string(nil), in.Args...)
	}
	if len(in.Env) > 0 {
		out.Env = map[string]string{}
		for key, value := range in.Env {
			if trimmed := strings.TrimSpace(key); trimmed != "" {
				out.Env[trimmed] = value
			}
		}
	}
	return out
}

func dedupeModelConfigs(configs []modelregistry.Config) []modelregistry.Config {
	if len(configs) == 0 {
		return nil
	}
	out := make([]modelregistry.Config, 0, len(configs))
	seen := make(map[string]struct{}, len(configs))
	for _, cfg := range configs {
		hadPersistedToken := strings.TrimSpace(cfg.Token) != ""
		cfg = modelregistry.NormalizeConfig(cfg)
		if hadPersistedToken {
			cfg.PersistToken = true
		}
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

func normalizePersistedModelsForSave(models PersistedModelConfig) PersistedModelConfig {
	for _, cfg := range models.Configs {
		if modelregistry.ConfigCarriesProfileFields(cfg) {
			models.Profiles = append(models.Profiles, modelregistry.ProfileFromConfig(cfg))
		}
	}
	return models
}

func dedupeModelConfigsForSave(configs []modelregistry.Config) []modelregistry.Config {
	if len(configs) == 0 {
		return nil
	}
	out := make([]modelregistry.Config, 0, len(configs))
	seen := make(map[string]struct{}, len(configs))
	for _, cfg := range configs {
		cfg = modelregistry.SanitizePersistedConfig(cfg)
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

func dedupeModelProfiles(profiles []modelregistry.ProfileConfig) []modelregistry.ProfileConfig {
	if len(profiles) == 0 {
		return nil
	}
	out := make([]modelregistry.ProfileConfig, 0, len(profiles))
	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		hadPersistedToken := strings.TrimSpace(profile.Token) != ""
		profile = modelregistry.NormalizeProfileConfig(profile)
		if hadPersistedToken {
			profile.PersistToken = true
		}
		if profile.ID == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(profile.ID))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, profile)
	}
	return out
}

func dedupeModelProfilesForSave(profiles []modelregistry.ProfileConfig) []modelregistry.ProfileConfig {
	if len(profiles) == 0 {
		return nil
	}
	out := make([]modelregistry.ProfileConfig, 0, len(profiles))
	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		profile = modelregistry.SanitizePersistedProfile(profile)
		if profile.ID == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(profile.ID))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, profile)
	}
	return out
}

func NormalizeSandboxConfig(cfg SandboxConfig) SandboxConfig {
	cfg.RequestedType = strings.ToLower(strings.TrimSpace(cfg.RequestedType))
	cfg.HelperPath = strings.TrimSpace(cfg.HelperPath)
	cfg.ReadableRoots = DedupeStrings(cfg.ReadableRoots)
	cfg.WritableRoots = DedupeStrings(cfg.WritableRoots)
	cfg.ReadOnlySubpaths = DedupeStrings(cfg.ReadOnlySubpaths)
	return cfg
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
