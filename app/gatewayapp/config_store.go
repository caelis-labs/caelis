package gatewayapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type AppConfig struct {
	Models         persistedModelConfig  `json:"models,omitempty"`
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

// AgentProviderConfig is reserved for future /agent add style third-party ACP
// provider registration. Keep it in the unified app config even before the TUI
// path is implemented so all user-managed providers share one root file.
type AgentProviderConfig struct {
	ID       string         `json:"id,omitempty"`
	Kind     string         `json:"kind,omitempty"`
	Label    string         `json:"label,omitempty"`
	BaseURL  string         `json:"base_url,omitempty"`
	TokenEnv string         `json:"token_env,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type persistedModelConfig struct {
	DefaultAlias string               `json:"default_alias,omitempty"`
	DefaultID    string               `json:"default_model_id,omitempty"`
	Profiles     []ModelProfileConfig `json:"profiles,omitempty"`
	Configs      []ModelConfig        `json:"configs,omitempty"`
}

type appConfigStore struct {
	mu   sync.Mutex
	path string
}

func newAppConfigStore(root string) *appConfigStore {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	return &appConfigStore{
		path: filepath.Join(root, "config.json"),
	}
}

func LoadAppConfig(root string) (AppConfig, error) {
	store := newAppConfigStore(root)
	if store == nil {
		return AppConfig{}, nil
	}
	return store.Load()
}

func (s *appConfigStore) Load() (AppConfig, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return AppConfig{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked()
}

func (s *appConfigStore) loadUnlocked() (AppConfig, error) {
	data, err := os.ReadFile(s.path)
	if err == nil {
		var doc AppConfig
		if err := json.Unmarshal(data, &doc); err != nil {
			return AppConfig{}, fmt.Errorf("gatewayapp: decode app config: %w", err)
		}
		doc.Models.Profiles = dedupeModelProfiles(doc.Models.Profiles)
		doc.Models.Configs = dedupeModelConfigs(doc.Models.Configs)
		doc.Agents = dedupeAgentConfigs(doc.Agents)
		doc.Sandbox = normalizeSandboxConfig(doc.Sandbox)
		return doc, nil
	}
	if !os.IsNotExist(err) {
		return AppConfig{}, err
	}
	return AppConfig{}, nil
}

func (s *appConfigStore) Save(doc AppConfig) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc.Models = normalizePersistedModelsForSave(doc.Models)
	doc.Models.Configs = dedupeModelConfigsForSave(doc.Models.Configs)
	doc.Models.Profiles = dedupeModelProfilesForSave(doc.Models.Profiles)
	doc.Agents = dedupeAgentConfigs(doc.Agents)
	doc.Sandbox = normalizeSandboxConfig(doc.Sandbox)
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
	if err := atomicWriteFile(s.path, data, 0o600, atomicWriteOps{}); err != nil {
		return err
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return err
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
	if ops.createTemp == nil {
		ops.createTemp = os.CreateTemp
	}
	if ops.rename == nil {
		ops.rename = os.Rename
	}
	if ops.chmod == nil {
		ops.chmod = os.Chmod
	}
	if ops.fsyncDir == nil {
		ops.fsyncDir = syncDir
	}
	dir := filepath.Dir(path)
	tmp, err := ops.createTemp(dir, "."+filepath.Base(path)+".*.tmp")
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
	if err := ops.chmod(tmpPath, perm); err != nil {
		return err
	}
	if err := ops.rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	if err := ops.chmod(path, perm); err != nil {
		return err
	}
	return ops.fsyncDir(dir)
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

func dedupeAgentConfigs(configs []AgentConfig) []AgentConfig {
	if len(configs) == 0 {
		return nil
	}
	out := make([]AgentConfig, 0, len(configs))
	seen := make(map[string]struct{}, len(configs))
	for _, cfg := range configs {
		cfg = normalizeAgentConfig(cfg)
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

func normalizeAgentConfig(in AgentConfig) AgentConfig {
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

func dedupeModelConfigs(configs []ModelConfig) []ModelConfig {
	if len(configs) == 0 {
		return nil
	}
	out := make([]ModelConfig, 0, len(configs))
	seen := make(map[string]struct{}, len(configs))
	for _, cfg := range configs {
		hadPersistedToken := strings.TrimSpace(cfg.Token) != ""
		cfg = normalizeModelConfig(cfg)
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

func normalizePersistedModelsForSave(models persistedModelConfig) persistedModelConfig {
	for _, cfg := range models.Configs {
		if modelConfigCarriesProfileFields(cfg) {
			models.Profiles = append(models.Profiles, modelProfileFromModelConfig(cfg))
		}
	}
	return models
}

func dedupeModelConfigsForSave(configs []ModelConfig) []ModelConfig {
	if len(configs) == 0 {
		return nil
	}
	out := make([]ModelConfig, 0, len(configs))
	seen := make(map[string]struct{}, len(configs))
	for _, cfg := range configs {
		cfg = sanitizePersistedModelConfig(cfg)
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

func dedupeModelProfiles(profiles []ModelProfileConfig) []ModelProfileConfig {
	if len(profiles) == 0 {
		return nil
	}
	out := make([]ModelProfileConfig, 0, len(profiles))
	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		hadPersistedToken := strings.TrimSpace(profile.Token) != ""
		profile = normalizeModelProfileConfig(profile)
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

func dedupeModelProfilesForSave(profiles []ModelProfileConfig) []ModelProfileConfig {
	if len(profiles) == 0 {
		return nil
	}
	out := make([]ModelProfileConfig, 0, len(profiles))
	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		profile = sanitizePersistedModelProfile(profile)
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

func normalizeSandboxConfig(cfg SandboxConfig) SandboxConfig {
	cfg.RequestedType = strings.ToLower(strings.TrimSpace(cfg.RequestedType))
	cfg.HelperPath = strings.TrimSpace(cfg.HelperPath)
	cfg.ReadableRoots = dedupeStrings(cfg.ReadableRoots)
	cfg.WritableRoots = dedupeStrings(cfg.WritableRoots)
	cfg.ReadOnlySubpaths = dedupeStrings(cfg.ReadOnlySubpaths)
	return cfg
}

func dedupeStrings(values []string) []string {
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
