// Package settings owns product settings for the reimplemented app layer.
// It is intentionally independent from the old gatewayapp config store.
package settings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/plugin"
)

type Document struct {
	Runtime        config.Runtime              `json:"runtime,omitempty"`
	Models         ModelCatalog                `json:"models,omitempty"`
	Compaction     CompactionPolicy            `json:"compaction,omitempty"`
	Skills         SkillPolicy                 `json:"skills,omitempty"`
	Agents         []plugin.ACPAgentDescriptor `json:"acp_agents,omitempty"`
	DisabledAgents []string                    `json:"disabled_acp_agents,omitempty"`
	Meta           map[string]any              `json:"meta,omitempty"`
}

type ModelCatalog struct {
	DefaultID string         `json:"default_model_id,omitempty"`
	Profiles  []ModelProfile `json:"profiles,omitempty"`
	Configs   []ModelConfig  `json:"configs,omitempty"`
}

type ModelProfile struct {
	ID           string         `json:"id,omitempty"`
	Provider     string         `json:"provider,omitempty"`
	EndpointID   string         `json:"endpoint_id,omitempty"`
	BaseURL      string         `json:"base_url,omitempty"`
	Token        string         `json:"token,omitempty"`
	TokenEnv     string         `json:"token_env,omitempty"`
	PersistToken bool           `json:"persist_token,omitempty"`
	AuthType     string         `json:"auth_type,omitempty"`
	HeaderKey    string         `json:"header_key,omitempty"`
	Timeout      time.Duration  `json:"timeout,omitempty"`
	Meta         map[string]any `json:"meta,omitempty"`
}

type ModelConfig struct {
	ID                     string         `json:"id,omitempty"`
	Alias                  string         `json:"alias,omitempty"`
	ProfileID              string         `json:"profile_id,omitempty"`
	Provider               string         `json:"provider,omitempty"`
	EndpointID             string         `json:"endpoint_id,omitempty"`
	Model                  string         `json:"model,omitempty"`
	BaseURL                string         `json:"base_url,omitempty"`
	Token                  string         `json:"token,omitempty"`
	TokenEnv               string         `json:"token_env,omitempty"`
	PersistToken           bool           `json:"persist_token,omitempty"`
	AuthType               string         `json:"auth_type,omitempty"`
	HeaderKey              string         `json:"header_key,omitempty"`
	ContextWindowTokens    int            `json:"context_window_tokens,omitempty"`
	MaxOutputTokens        int            `json:"max_output_tokens,omitempty"`
	ReasoningEffort        string         `json:"reasoning_effort,omitempty"`
	DefaultReasoningEffort string         `json:"default_reasoning_effort,omitempty"`
	ReasoningMode          string         `json:"reasoning_mode,omitempty"`
	ReasoningLevels        []string       `json:"reasoning_levels,omitempty"`
	Timeout                time.Duration  `json:"timeout,omitempty"`
	Meta                   map[string]any `json:"meta,omitempty"`
}

type ModelChoice struct {
	ID         string `json:"id,omitempty"`
	Alias      string `json:"alias,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	ProfileID  string `json:"profile_id,omitempty"`
	EndpointID string `json:"endpoint_id,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Default    bool   `json:"default,omitempty"`
}

type CompactionPolicy struct {
	Prompt         string               `json:"prompt,omitempty"`
	MaxSourceChars int                  `json:"max_source_chars,omitempty"`
	Auto           AutoCompactionPolicy `json:"auto,omitempty"`
}

type AutoCompactionPolicy struct {
	Mode           string  `json:"mode,omitempty"`
	WatermarkRatio float64 `json:"watermark_ratio,omitempty"`
}

const (
	SkillLoadingModeExplicit     = "explicit"
	SkillLoadingModeMetadataOnly = "metadata_only"
	SkillLoadingModeDisabled     = "disabled"

	DefaultSkillExpansionChars = 64000
)

type SkillPolicy struct {
	LoadingMode       string `json:"loading_mode,omitempty"`
	MaxExpansionChars int    `json:"max_expansion_chars,omitempty"`
}

type Store interface {
	Load(context.Context) (Document, error)
	Save(context.Context, Document) error
	Path() string
}

type FileStore struct {
	mu   sync.Mutex
	path string
}

func NewFileStore(root string) *FileStore {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	return &FileStore{path: filepath.Join(root, "config.json")}
}

func (s *FileStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *FileStore) Load(ctx context.Context) (Document, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	if s == nil || strings.TrimSpace(s.path) == "" {
		return Document{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Document{}, nil
	}
	if err != nil {
		return Document{}, err
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Document{}, fmt.Errorf("app/settings: decode settings: %w", err)
	}
	return NormalizeDocument(doc), nil
}

func (s *FileStore) Save(ctx context.Context, doc Document) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc = sanitizeDocumentForSave(doc)
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("app/settings: encode settings: %w", err)
	}
	if err := atomicWriteFile(s.path, raw, 0o600); err != nil {
		return err
	}
	return os.Chmod(s.path, 0o600)
}

type Manager struct {
	mu    sync.Mutex
	store Store
	doc   Document
}

func NewManager(ctx context.Context, store Store, defaults Document) (*Manager, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	doc := NormalizeDocument(defaults)
	if store != nil {
		loaded, err := store.Load(ctx)
		if err != nil {
			return nil, err
		}
		doc = mergeDocuments(doc, loaded)
	}
	return &Manager{store: store, doc: NormalizeDocument(doc)}, nil
}

func (m *Manager) Document(context.Context) (Document, error) {
	if m == nil {
		return Document{}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return CloneDocument(m.doc), nil
}

func (m *Manager) Save(ctx context.Context, doc Document) error {
	if m == nil {
		return nil
	}
	doc = NormalizeDocument(doc)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store != nil {
		if err := m.store.Save(ctx, doc); err != nil {
			return err
		}
	}
	m.doc = CloneDocument(doc)
	return nil
}

func (m *Manager) SetRuntime(ctx context.Context, runtime config.Runtime) (config.Runtime, error) {
	if m == nil {
		return config.Runtime{}, errors.New("app/settings: manager is nil")
	}
	runtime = NormalizeRuntime(runtime)
	m.mu.Lock()
	defer m.mu.Unlock()
	doc := CloneDocument(m.doc)
	doc.Runtime = runtime
	if err := m.saveDocumentLocked(ctx, doc); err != nil {
		return config.Runtime{}, err
	}
	return runtime, nil
}

func (m *Manager) UpsertModel(ctx context.Context, cfg ModelConfig) (ModelConfig, error) {
	if m == nil {
		return ModelConfig{}, errors.New("app/settings: manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index, err := newModelIndex(m.doc.Models)
	if err != nil {
		return ModelConfig{}, err
	}
	next, err := index.upsert(cfg, true)
	if err != nil {
		return ModelConfig{}, err
	}
	return next, m.saveLocked(ctx, index)
}

func (m *Manager) DeleteModel(ctx context.Context, ref string) error {
	if m == nil {
		return errors.New("app/settings: manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index, err := newModelIndex(m.doc.Models)
	if err != nil {
		return err
	}
	if err := index.delete(ref); err != nil {
		return err
	}
	return m.saveLocked(ctx, index)
}

func (m *Manager) SetDefaultModel(ctx context.Context, ref string) (ModelConfig, error) {
	if m == nil {
		return ModelConfig{}, errors.New("app/settings: manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index, err := newModelIndex(m.doc.Models)
	if err != nil {
		return ModelConfig{}, err
	}
	cfg, err := index.setDefault(ref)
	if err != nil {
		return ModelConfig{}, err
	}
	return cfg, m.saveLocked(ctx, index)
}

func (m *Manager) ResolveModel(ref string) (ModelConfig, error) {
	if m == nil {
		return ModelConfig{}, errors.New("app/settings: manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index, err := newModelIndex(m.doc.Models)
	if err != nil {
		return ModelConfig{}, err
	}
	return index.resolve(firstNonEmpty(ref, index.defaultID))
}

func (m *Manager) ListModelChoices() ([]ModelChoice, error) {
	if m == nil {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index, err := newModelIndex(m.doc.Models)
	if err != nil {
		return nil, err
	}
	return index.choices(), nil
}

func (m *Manager) CompactionPolicy() CompactionPolicy {
	if m == nil {
		return CompactionPolicy{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return NormalizeCompactionPolicy(m.doc.Compaction)
}

func (m *Manager) SetCompactionPolicy(ctx context.Context, policy CompactionPolicy) (CompactionPolicy, error) {
	if m == nil {
		return CompactionPolicy{}, errors.New("app/settings: manager is nil")
	}
	policy = NormalizeCompactionPolicy(policy)
	m.mu.Lock()
	defer m.mu.Unlock()
	doc := CloneDocument(m.doc)
	doc.Compaction = policy
	if err := m.saveDocumentLocked(ctx, doc); err != nil {
		return CompactionPolicy{}, err
	}
	return policy, nil
}

func (m *Manager) SkillPolicy() SkillPolicy {
	if m == nil {
		return SkillPolicy{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return NormalizeSkillPolicy(m.doc.Skills)
}

func (m *Manager) SetSkillPolicy(ctx context.Context, policy SkillPolicy) (SkillPolicy, error) {
	if m == nil {
		return SkillPolicy{}, errors.New("app/settings: manager is nil")
	}
	policy = NormalizeSkillPolicy(policy)
	m.mu.Lock()
	defer m.mu.Unlock()
	doc := CloneDocument(m.doc)
	doc.Skills = policy
	if err := m.saveDocumentLocked(ctx, doc); err != nil {
		return SkillPolicy{}, err
	}
	return policy, nil
}

func (m *Manager) ListACPAgents() []plugin.ACPAgentDescriptor {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneAgents(m.doc.Agents)
}

func (m *Manager) ListDisabledACPAgents() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return normalizeAgentNames(m.doc.DisabledAgents)
}

func (m *Manager) UpsertACPAgent(ctx context.Context, agent plugin.ACPAgentDescriptor) (plugin.ACPAgentDescriptor, error) {
	if m == nil {
		return plugin.ACPAgentDescriptor{}, errors.New("app/settings: manager is nil")
	}
	normalized := cloneAgents([]plugin.ACPAgentDescriptor{agent})
	if len(normalized) == 0 || strings.TrimSpace(normalized[0].Name) == "" {
		return plugin.ACPAgentDescriptor{}, errors.New("app/settings: ACP agent name is required")
	}
	agent = normalized[0]
	if strings.TrimSpace(agent.Command) == "" {
		return plugin.ACPAgentDescriptor{}, fmt.Errorf("app/settings: command is required for ACP agent %q", agent.Name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make([]plugin.ACPAgentDescriptor, 0, len(m.doc.Agents)+1)
	replaced := false
	for _, existing := range cloneAgents(m.doc.Agents) {
		if strings.EqualFold(strings.TrimSpace(existing.Name), agent.Name) {
			next = append(next, agent)
			replaced = true
			continue
		}
		next = append(next, existing)
	}
	if !replaced {
		next = append(next, agent)
	}
	doc := CloneDocument(m.doc)
	doc.Agents = next
	doc.DisabledAgents = removeAgentName(doc.DisabledAgents, agent.Name)
	if err := m.saveDocumentLocked(ctx, doc); err != nil {
		return plugin.ACPAgentDescriptor{}, err
	}
	return agent, nil
}

func (m *Manager) DeleteACPAgent(ctx context.Context, name string) error {
	if m == nil {
		return errors.New("app/settings: manager is nil")
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return errors.New("app/settings: ACP agent name is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := make([]plugin.ACPAgentDescriptor, 0, len(m.doc.Agents))
	removed := false
	for _, existing := range cloneAgents(m.doc.Agents) {
		if strings.EqualFold(strings.TrimSpace(existing.Name), name) {
			removed = true
			continue
		}
		next = append(next, existing)
	}
	if !removed {
		return fmt.Errorf("app/settings: ACP agent %q is not configured", name)
	}
	doc := CloneDocument(m.doc)
	doc.Agents = next
	return m.saveDocumentLocked(ctx, doc)
}

func (m *Manager) DisableACPAgent(ctx context.Context, name string) error {
	if m == nil {
		return errors.New("app/settings: manager is nil")
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return errors.New("app/settings: ACP agent name is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	doc := CloneDocument(m.doc)
	doc.Agents = removeACPAgentDescriptor(doc.Agents, name)
	doc.DisabledAgents = normalizeAgentNames(append(doc.DisabledAgents, name))
	return m.saveDocumentLocked(ctx, doc)
}

func (m *Manager) saveLocked(ctx context.Context, index *modelIndex) error {
	m.doc.Models = index.snapshot()
	return m.saveDocumentLocked(ctx, m.doc)
}

func (m *Manager) saveDocumentLocked(ctx context.Context, doc Document) error {
	next := NormalizeDocument(doc)
	if m.store != nil {
		if err := m.store.Save(ctx, next); err != nil {
			return err
		}
	}
	m.doc = CloneDocument(next)
	return nil
}

type modelIndex struct {
	configs   map[string]ModelConfig
	profiles  map[string]ModelProfile
	defaultID string
}

func newModelIndex(catalog ModelCatalog) (*modelIndex, error) {
	index := &modelIndex{
		configs:   map[string]ModelConfig{},
		profiles:  map[string]ModelProfile{},
		defaultID: strings.ToLower(strings.TrimSpace(catalog.DefaultID)),
	}
	for _, profile := range catalog.Profiles {
		profile = NormalizeModelProfile(profile)
		if profile.ID != "" {
			index.profiles[strings.ToLower(profile.ID)] = profile
		}
	}
	for _, cfg := range catalog.Configs {
		cfg = NormalizeModelConfig(cfg)
		if cfg.ID == "" {
			continue
		}
		if profile, ok := index.profiles[strings.ToLower(cfg.ProfileID)]; ok {
			cfg = MergeModelProfile(cfg, profile)
		}
		index.configs[strings.ToLower(cfg.ID)] = cfg
	}
	if index.defaultID == "" && len(index.configs) > 0 {
		keys := make([]string, 0, len(index.configs))
		for key := range index.configs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		index.defaultID = index.configs[keys[0]].ID
	}
	if index.defaultID != "" {
		if cfg, ok, err := index.tryResolve(index.defaultID); err == nil && ok {
			index.defaultID = cfg.ID
		}
	}
	return index, nil
}

func (i *modelIndex) upsert(cfg ModelConfig, setDefault bool) (ModelConfig, error) {
	hadAlias := strings.TrimSpace(cfg.Alias) != ""
	hadProfileAuth := modelCarriesProfileAuth(cfg)
	cfg = NormalizeModelConfig(cfg)
	if existing, ok := i.profiles[strings.ToLower(strings.TrimSpace(cfg.ProfileID))]; ok {
		cfg.Provider = firstNonEmpty(cfg.Provider, existing.Provider)
		cfg.EndpointID = firstNonEmpty(cfg.EndpointID, existing.EndpointID)
		cfg.BaseURL = firstNonEmpty(cfg.BaseURL, existing.BaseURL)
		if !hadProfileAuth {
			cfg.Token = existing.Token
			cfg.TokenEnv = existing.TokenEnv
			cfg.AuthType = existing.AuthType
			cfg.HeaderKey = existing.HeaderKey
			cfg.Timeout = existing.Timeout
		}
		cfg = NormalizeModelConfig(cfg)
		if !hadAlias {
			cfg.Alias = ""
			cfg.ID = ""
			cfg = NormalizeModelConfig(cfg)
		}
	}
	if cfg.Provider == "" || cfg.Model == "" {
		return ModelConfig{}, errors.New("app/settings: model provider and model are required")
	}
	profile := NormalizeModelProfile(ModelProfile{
		ID:           cfg.ProfileID,
		Provider:     cfg.Provider,
		EndpointID:   cfg.EndpointID,
		BaseURL:      cfg.BaseURL,
		Token:        cfg.Token,
		TokenEnv:     cfg.TokenEnv,
		PersistToken: cfg.PersistToken,
		AuthType:     cfg.AuthType,
		HeaderKey:    cfg.HeaderKey,
		Timeout:      cfg.Timeout,
		Meta:         maps.Clone(cfg.Meta),
	})
	if existing, ok := i.profiles[strings.ToLower(profile.ID)]; ok && !hadProfileAuth {
		profile = existing
	}
	i.profiles[strings.ToLower(profile.ID)] = profile
	cfg.ProfileID = profile.ID
	cfg = MergeModelProfile(cfg, profile)
	i.configs[strings.ToLower(cfg.ID)] = cfg
	if setDefault {
		i.defaultID = cfg.ID
	}
	return cfg, nil
}

func (i *modelIndex) delete(ref string) error {
	cfg, err := i.resolve(ref)
	if err != nil {
		return err
	}
	delete(i.configs, strings.ToLower(cfg.ID))
	if !i.profileReferenced(cfg.ProfileID) {
		delete(i.profiles, strings.ToLower(cfg.ProfileID))
	}
	if strings.EqualFold(i.defaultID, cfg.ID) {
		i.defaultID = ""
		keys := make([]string, 0, len(i.configs))
		for key := range i.configs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			i.defaultID = i.configs[keys[0]].ID
		}
	}
	return nil
}

func (i *modelIndex) setDefault(ref string) (ModelConfig, error) {
	cfg, err := i.resolve(ref)
	if err != nil {
		return ModelConfig{}, err
	}
	i.defaultID = cfg.ID
	return cfg, nil
}

func (i *modelIndex) resolve(ref string) (ModelConfig, error) {
	cfg, ok, err := i.tryResolve(ref)
	if err != nil {
		return ModelConfig{}, err
	}
	if !ok {
		if strings.TrimSpace(ref) == "" {
			return ModelConfig{}, errors.New("app/settings: no model configured")
		}
		return ModelConfig{}, fmt.Errorf("app/settings: unknown model %q", ref)
	}
	return cfg, nil
}

func (i *modelIndex) tryResolve(ref string) (ModelConfig, bool, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ref == "" {
		return ModelConfig{}, false, nil
	}
	if cfg, ok := i.configs[ref]; ok {
		return cfg, true, nil
	}
	var match ModelConfig
	count := 0
	for _, cfg := range i.configs {
		if strings.EqualFold(cfg.Alias, ref) {
			match = cfg
			count++
		}
	}
	if count > 1 {
		return ModelConfig{}, false, fmt.Errorf("app/settings: ambiguous model alias %q; use model id", ref)
	}
	return match, count == 1, nil
}

func (i *modelIndex) choices() []ModelChoice {
	if i == nil || len(i.configs) == 0 {
		return nil
	}
	configs := make([]ModelConfig, 0, len(i.configs))
	if i.defaultID != "" {
		if cfg, ok := i.configs[strings.ToLower(i.defaultID)]; ok {
			configs = append(configs, cfg)
		}
	}
	rest := make([]ModelConfig, 0, len(i.configs))
	for key, cfg := range i.configs {
		if strings.EqualFold(key, i.defaultID) {
			continue
		}
		rest = append(rest, cfg)
	}
	sort.Slice(rest, func(a, b int) bool {
		left := strings.ToLower(strings.TrimSpace(rest[a].Alias + " " + rest[a].ID))
		right := strings.ToLower(strings.TrimSpace(rest[b].Alias + " " + rest[b].ID))
		return left < right
	})
	configs = append(configs, rest...)
	out := make([]ModelChoice, 0, len(configs))
	for _, cfg := range configs {
		out = append(out, ModelChoiceFromConfig(cfg, strings.EqualFold(cfg.ID, i.defaultID)))
	}
	return out
}

func (i *modelIndex) snapshot() ModelCatalog {
	configs := make([]ModelConfig, 0, len(i.configs))
	for _, cfg := range i.configs {
		configs = append(configs, cfg)
	}
	sort.Slice(configs, func(a, b int) bool {
		return strings.ToLower(configs[a].Alias+" "+configs[a].ID) < strings.ToLower(configs[b].Alias+" "+configs[b].ID)
	})
	profiles := make([]ModelProfile, 0, len(i.profiles))
	for _, profile := range i.profiles {
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(a, b int) bool {
		return strings.ToLower(profiles[a].ID) < strings.ToLower(profiles[b].ID)
	})
	return ModelCatalog{
		DefaultID: i.defaultID,
		Profiles:  profiles,
		Configs:   configs,
	}
}

func (i *modelIndex) profileReferenced(profileID string) bool {
	for _, cfg := range i.configs {
		if strings.EqualFold(cfg.ProfileID, profileID) {
			return true
		}
	}
	return false
}

func NormalizeDocument(doc Document) Document {
	doc.Runtime = NormalizeRuntime(doc.Runtime)
	doc.Models = NormalizeModelCatalog(doc.Models)
	doc.Compaction = NormalizeCompactionPolicy(doc.Compaction)
	doc.Skills = NormalizeSkillPolicy(doc.Skills)
	doc.Agents = cloneAgents(doc.Agents)
	doc.DisabledAgents = normalizeAgentNames(doc.DisabledAgents)
	doc.Meta = maps.Clone(doc.Meta)
	return doc
}

func NormalizeRuntime(in config.Runtime) config.Runtime {
	return cloneRuntime(in)
}

func NormalizeCompactionPolicy(policy CompactionPolicy) CompactionPolicy {
	policy.Prompt = strings.TrimSpace(policy.Prompt)
	if policy.MaxSourceChars < 0 {
		policy.MaxSourceChars = 0
	}
	policy.Auto = NormalizeAutoCompactionPolicy(policy.Auto)
	return policy
}

func NormalizeAutoCompactionPolicy(policy AutoCompactionPolicy) AutoCompactionPolicy {
	policy.Mode = normalizeAutoCompactionMode(policy.Mode)
	if policy.WatermarkRatio < 0 {
		policy.WatermarkRatio = 0
	}
	return policy
}

func NormalizeSkillPolicy(policy SkillPolicy) SkillPolicy {
	policy.LoadingMode = normalizeSkillLoadingMode(policy.LoadingMode)
	if policy.MaxExpansionChars < 0 {
		policy.MaxExpansionChars = 0
	}
	return policy
}

func SkillLoadingMode(policy SkillPolicy) string {
	policy = NormalizeSkillPolicy(policy)
	switch policy.LoadingMode {
	case "", SkillLoadingModeExplicit:
		return SkillLoadingModeExplicit
	case SkillLoadingModeMetadataOnly:
		return SkillLoadingModeMetadataOnly
	case SkillLoadingModeDisabled:
		return SkillLoadingModeDisabled
	default:
		return SkillLoadingModeExplicit
	}
}

func SkillMetadataEnabled(policy SkillPolicy) bool {
	return SkillLoadingMode(policy) != SkillLoadingModeDisabled
}

func SkillExpansionEnabled(policy SkillPolicy) bool {
	return SkillLoadingMode(policy) == SkillLoadingModeExplicit
}

func SkillExpansionBudget(policy SkillPolicy) int {
	policy = NormalizeSkillPolicy(policy)
	if !SkillExpansionEnabled(policy) {
		return 0
	}
	if policy.MaxExpansionChars <= 0 {
		return DefaultSkillExpansionChars
	}
	return policy.MaxExpansionChars
}

func normalizeSkillLoadingMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "default":
		return ""
	case "explicit", "expand", "expanded", "enabled", "enable", "on", "true", "yes":
		return SkillLoadingModeExplicit
	case "metadata", "metadata_only", "metadata-only", "meta":
		return SkillLoadingModeMetadataOnly
	case "disabled", "disable", "off", "false", "no":
		return SkillLoadingModeDisabled
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

func normalizeAutoCompactionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "default":
		return ""
	case "enabled", "enable", "on", "true", "yes":
		return "enabled"
	case "disabled", "disable", "off", "false", "no":
		return "disabled"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

func NormalizeModelCatalog(catalog ModelCatalog) ModelCatalog {
	index, err := newModelIndex(catalog)
	if err != nil {
		return ModelCatalog{}
	}
	return index.snapshot()
}

func NormalizeModelProfile(profile ModelProfile) ModelProfile {
	profile.ID = strings.ToLower(strings.TrimSpace(profile.ID))
	profile.Provider = strings.ToLower(strings.TrimSpace(profile.Provider))
	profile.BaseURL = strings.TrimRight(strings.TrimSpace(profile.BaseURL), "/")
	profile.EndpointID = normalizeEndpointID(profile.Provider, profile.EndpointID, profile.BaseURL)
	if profile.ID == "" {
		profile.ID = BuildProfileID(profile.Provider, profile.EndpointID, profile.BaseURL)
	}
	profile.Token = strings.TrimSpace(profile.Token)
	profile.TokenEnv = strings.TrimSpace(profile.TokenEnv)
	profile.AuthType = defaultAuthType(profile.Provider, profile.AuthType)
	profile.HeaderKey = strings.TrimSpace(profile.HeaderKey)
	profile.Meta = maps.Clone(profile.Meta)
	return profile
}

func NormalizeModelConfig(cfg ModelConfig) ModelConfig {
	cfg.ID = strings.ToLower(strings.TrimSpace(cfg.ID))
	cfg.Alias = strings.ToLower(strings.TrimSpace(cfg.Alias))
	cfg.ProfileID = strings.ToLower(strings.TrimSpace(cfg.ProfileID))
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	cfg.EndpointID = normalizeEndpointID(cfg.Provider, cfg.EndpointID, cfg.BaseURL)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Token = strings.TrimSpace(cfg.Token)
	cfg.TokenEnv = strings.TrimSpace(cfg.TokenEnv)
	cfg.AuthType = defaultAuthType(cfg.Provider, cfg.AuthType)
	cfg.HeaderKey = strings.TrimSpace(cfg.HeaderKey)
	cfg.ReasoningEffort = strings.ToLower(strings.TrimSpace(cfg.ReasoningEffort))
	cfg.DefaultReasoningEffort = strings.ToLower(strings.TrimSpace(cfg.DefaultReasoningEffort))
	if cfg.DefaultReasoningEffort == "" {
		cfg.DefaultReasoningEffort = cfg.ReasoningEffort
	}
	cfg.ReasoningMode = strings.ToLower(strings.TrimSpace(cfg.ReasoningMode))
	cfg.ReasoningLevels = DedupeStrings(cfg.ReasoningLevels)
	if cfg.ContextWindowTokens < 0 {
		cfg.ContextWindowTokens = 0
	}
	if cfg.MaxOutputTokens < 0 {
		cfg.MaxOutputTokens = 0
	}
	if cfg.Alias == "" {
		cfg.Alias = BuildAlias(cfg.Provider, cfg.Model)
	}
	if cfg.ProfileID == "" {
		cfg.ProfileID = BuildProfileID(cfg.Provider, cfg.EndpointID, cfg.BaseURL)
	}
	if id := BuildModelID(cfg.ProfileID, cfg.Alias); id != "" {
		cfg.ID = id
	}
	cfg.Meta = maps.Clone(cfg.Meta)
	return cfg
}

func MergeModelProfile(cfg ModelConfig, profile ModelProfile) ModelConfig {
	cfg = NormalizeModelConfig(cfg)
	profile = NormalizeModelProfile(profile)
	cfg.ProfileID = profile.ID
	cfg.Provider = firstNonEmpty(profile.Provider, cfg.Provider)
	cfg.EndpointID = firstNonEmpty(profile.EndpointID, cfg.EndpointID)
	cfg.BaseURL = firstNonEmpty(profile.BaseURL, cfg.BaseURL)
	cfg.Token = firstNonEmpty(profile.Token, cfg.Token)
	cfg.TokenEnv = firstNonEmpty(profile.TokenEnv, cfg.TokenEnv)
	cfg.AuthType = firstNonEmpty(profile.AuthType, cfg.AuthType)
	cfg.HeaderKey = firstNonEmpty(profile.HeaderKey, cfg.HeaderKey)
	if profile.Timeout > 0 {
		cfg.Timeout = profile.Timeout
	}
	return NormalizeModelConfig(cfg)
}

func ModelChoiceFromConfig(cfg ModelConfig, isDefault bool) ModelChoice {
	cfg = NormalizeModelConfig(cfg)
	return ModelChoice{
		ID:         cfg.ID,
		Alias:      cfg.Alias,
		Provider:   cfg.Provider,
		Model:      cfg.Model,
		ProfileID:  cfg.ProfileID,
		EndpointID: cfg.EndpointID,
		BaseURL:    cfg.BaseURL,
		Detail:     ModelChoiceDetail(cfg),
		Default:    isDefault,
	}
}

func ModelChoiceDetail(cfg ModelConfig) string {
	parts := []string{}
	if cfg.ProfileID != "" {
		parts = append(parts, "profile:"+cfg.ProfileID)
	}
	if cfg.EndpointID != "" && cfg.EndpointID != "default" {
		parts = append(parts, cfg.EndpointID)
	}
	if cfg.BaseURL != "" {
		parts = append(parts, cfg.BaseURL)
	}
	if cfg.TokenEnv != "" {
		parts = append(parts, "env:"+cfg.TokenEnv)
	}
	if len(parts) == 0 {
		return "configured model"
	}
	return strings.Join(parts, " | ")
}

func RuntimeModelProfile(cfg ModelConfig) config.ModelProfile {
	cfg = NormalizeModelConfig(cfg)
	return config.ModelProfile{
		ID:                     cfg.ID,
		Alias:                  cfg.Alias,
		ProfileID:              cfg.ProfileID,
		EndpointID:             cfg.EndpointID,
		Provider:               cfg.Provider,
		Model:                  cfg.Model,
		BaseURL:                cfg.BaseURL,
		Token:                  cfg.Token,
		TokenEnv:               cfg.TokenEnv,
		PersistToken:           cfg.PersistToken,
		AuthType:               cfg.AuthType,
		HeaderKey:              cfg.HeaderKey,
		ContextWindowTokens:    cfg.ContextWindowTokens,
		MaxOutputTokens:        cfg.MaxOutputTokens,
		ReasoningEffort:        cfg.ReasoningEffort,
		DefaultReasoningEffort: cfg.DefaultReasoningEffort,
		ReasoningMode:          cfg.ReasoningMode,
		ReasoningLevels:        slices.Clone(cfg.ReasoningLevels),
		Timeout:                cfg.Timeout,
		Meta:                   maps.Clone(cfg.Meta),
	}
}

func SupportsReasoningEffort(cfg ModelConfig, effort string) bool {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		return true
	}
	for _, level := range cfg.ReasoningLevels {
		if strings.EqualFold(level, effort) {
			return true
		}
	}
	switch strings.ToLower(strings.TrimSpace(cfg.ReasoningMode)) {
	case "toggle":
		return effort == "none" || effort == "high" || effort == "max" || effort == "enabled"
	case "fixed":
		return effort == "low" || effort == "medium" || effort == "high"
	case "":
		return true
	default:
		return false
	}
}

func CloneDocument(doc Document) Document {
	return NormalizeDocument(doc)
}

func sanitizeDocumentForSave(doc Document) Document {
	doc = NormalizeDocument(doc)
	for i := range doc.Models.Profiles {
		if !doc.Models.Profiles[i].PersistToken {
			doc.Models.Profiles[i].Token = ""
		}
	}
	for i := range doc.Models.Configs {
		if !doc.Models.Configs[i].PersistToken {
			doc.Models.Configs[i].Token = ""
		}
	}
	return doc
}

func mergeDocuments(defaults Document, loaded Document) Document {
	out := defaults
	if loaded.Runtime.AppName != "" || loaded.Runtime.UserID != "" || loaded.Runtime.WorkspaceKey != "" ||
		loaded.Runtime.WorkspaceCWD != "" || loaded.Runtime.Model != "" || loaded.Runtime.Store.Backend != "" ||
		loaded.Runtime.Sandbox.Backend != "" || len(loaded.Runtime.Plugins) > 0 || len(loaded.Runtime.Meta) > 0 {
		out.Runtime = loaded.Runtime
	}
	if len(loaded.Models.Configs) > 0 || len(loaded.Models.Profiles) > 0 || strings.TrimSpace(loaded.Models.DefaultID) != "" {
		out.Models = loaded.Models
	}
	if compactionPolicyConfigured(loaded.Compaction) {
		out.Compaction = loaded.Compaction
	}
	if skillPolicyConfigured(loaded.Skills) {
		out.Skills = loaded.Skills
	}
	if len(loaded.Agents) > 0 {
		out.Agents = loaded.Agents
	}
	if len(loaded.DisabledAgents) > 0 {
		out.DisabledAgents = loaded.DisabledAgents
	}
	if len(loaded.Meta) > 0 {
		out.Meta = maps.Clone(loaded.Meta)
	}
	return NormalizeDocument(out)
}

func compactionPolicyConfigured(policy CompactionPolicy) bool {
	policy = NormalizeCompactionPolicy(policy)
	return policy.Prompt != "" ||
		policy.MaxSourceChars > 0 ||
		policy.Auto.Mode != "" ||
		policy.Auto.WatermarkRatio > 0
}

func skillPolicyConfigured(policy SkillPolicy) bool {
	policy = NormalizeSkillPolicy(policy)
	return policy.LoadingMode != "" || policy.MaxExpansionChars > 0
}

func modelCarriesProfileAuth(cfg ModelConfig) bool {
	return strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.TokenEnv) != "" ||
		strings.TrimSpace(cfg.AuthType) != "" ||
		strings.TrimSpace(cfg.HeaderKey) != "" ||
		cfg.PersistToken ||
		cfg.Timeout > 0
}

func cloneRuntime(in config.Runtime) config.Runtime {
	out := in
	out.AppName = strings.TrimSpace(in.AppName)
	out.UserID = strings.TrimSpace(in.UserID)
	out.WorkspaceKey = strings.TrimSpace(in.WorkspaceKey)
	out.WorkspaceCWD = strings.TrimSpace(in.WorkspaceCWD)
	out.Model = strings.TrimSpace(in.Model)
	out.Store.Backend = strings.ToLower(strings.TrimSpace(in.Store.Backend))
	out.Store.URI = strings.TrimSpace(in.Store.URI)
	out.Store.Meta = maps.Clone(in.Store.Meta)
	out.Sandbox.Backend = strings.ToLower(strings.TrimSpace(in.Sandbox.Backend))
	out.Sandbox.Network = strings.ToLower(strings.TrimSpace(in.Sandbox.Network))
	out.Sandbox.HelperPath = strings.TrimSpace(in.Sandbox.HelperPath)
	out.Sandbox.ReadableRoots = slices.Clone(in.Sandbox.ReadableRoots)
	out.Sandbox.WritableRoots = slices.Clone(in.Sandbox.WritableRoots)
	for i := range out.Sandbox.ReadableRoots {
		out.Sandbox.ReadableRoots[i] = strings.TrimSpace(out.Sandbox.ReadableRoots[i])
	}
	for i := range out.Sandbox.WritableRoots {
		out.Sandbox.WritableRoots[i] = strings.TrimSpace(out.Sandbox.WritableRoots[i])
	}
	out.Plugins = slices.Clone(in.Plugins)
	for i := range out.Plugins {
		out.Plugins[i].Meta = maps.Clone(in.Plugins[i].Meta)
	}
	out.Meta = maps.Clone(in.Meta)
	return out
}

func cloneAgents(in []plugin.ACPAgentDescriptor) []plugin.ACPAgentDescriptor {
	if len(in) == 0 {
		return nil
	}
	out := make([]plugin.ACPAgentDescriptor, 0, len(in))
	seen := map[string]struct{}{}
	for _, agent := range in {
		agent.Name = strings.ToLower(strings.TrimSpace(agent.Name))
		agent.Description = strings.TrimSpace(agent.Description)
		agent.Command = strings.TrimSpace(agent.Command)
		agent.WorkDir = strings.TrimSpace(agent.WorkDir)
		agent.Args = slices.Clone(agent.Args)
		agent.Env = maps.Clone(agent.Env)
		agent.Roles = slices.Clone(agent.Roles)
		if agent.Name == "" {
			continue
		}
		if _, ok := seen[agent.Name]; ok {
			continue
		}
		seen[agent.Name] = struct{}{}
		out = append(out, agent)
	}
	return out
}

func removeACPAgentDescriptor(in []plugin.ACPAgentDescriptor, name string) []plugin.ACPAgentDescriptor {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || len(in) == 0 {
		return cloneAgents(in)
	}
	out := make([]plugin.ACPAgentDescriptor, 0, len(in))
	for _, agent := range cloneAgents(in) {
		if strings.EqualFold(strings.TrimSpace(agent.Name), name) {
			continue
		}
		out = append(out, agent)
	}
	return out
}

func normalizeAgentNames(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, name := range in {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func removeAgentName(in []string, name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || len(in) == 0 {
		return normalizeAgentNames(in)
	}
	out := make([]string, 0, len(in))
	for _, existing := range normalizeAgentNames(in) {
		if existing == name {
			continue
		}
		out = append(out, existing)
	}
	return out
}

func DedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.ToLower(strings.TrimSpace(value))
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func BuildAlias(provider string, modelName string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	modelName = strings.TrimSpace(modelName)
	if provider == "" {
		return strings.ToLower(modelName)
	}
	if modelName == "" {
		return provider
	}
	return strings.ToLower(provider + "/" + modelName)
}

func BuildProfileID(provider string, endpointID string, baseURL string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	endpointID = sanitizeIDPart(firstNonEmpty(endpointID, "default"))
	if strings.HasPrefix(endpointID, "custom") {
		endpointID = "custom-" + shortHash(strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/")))
	}
	if provider == "" {
		return endpointID
	}
	return provider + "@" + endpointID
}

func BuildModelID(profileID string, alias string) string {
	profileID = strings.ToLower(strings.TrimSpace(profileID))
	alias = strings.ToLower(strings.TrimSpace(alias))
	if profileID == "" {
		return alias
	}
	if alias == "" {
		return profileID
	}
	return profileID + "/" + alias
}

func normalizeEndpointID(provider string, endpointID string, baseURL string) string {
	endpointID = sanitizeIDPart(endpointID)
	if endpointID != "" {
		return endpointID
	}
	normalizedBaseURL := strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai", "openai_compatible", "openai-compatible":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://api.openai.com/v1" {
			return "default"
		}
	case "openrouter":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://openrouter.ai/api/v1" {
			return "default"
		}
	case "gemini":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://generativelanguage.googleapis.com/v1beta" {
			return "default"
		}
	case "anthropic", "anthropic-compatible":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://api.anthropic.com" {
			return "default"
		}
	case "minimax":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://api.minimaxi.com/anthropic" {
			return "default"
		}
	case "deepseek":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://api.deepseek.com/v1" {
			return "default"
		}
	case "mimo", "xiaomi":
		switch normalizedBaseURL {
		case "", "https://api.xiaomimimo.com/v1":
			return "api-cn"
		case "https://token-plan-cn.xiaomimimo.com/v1":
			return "token-plan-cn"
		}
	case "volcengine":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://ark.cn-beijing.volces.com/api/v3" {
			return "standard"
		}
		if normalizedBaseURL == "https://ark.cn-beijing.volces.com/api/coding/v3" {
			return "coding-plan"
		}
	case "volcengine-coding-plan", "volcengine_coding_plan":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://ark.cn-beijing.volces.com/api/coding/v3" {
			return "coding-plan"
		}
	case "ollama":
		if normalizedBaseURL == "" || normalizedBaseURL == "http://localhost:11434" {
			return "default"
		}
	case "codefree":
		if normalizedBaseURL == "" || normalizedBaseURL == "https://www.srdcloud.cn" {
			return "default"
		}
	case "":
		return "default"
	}
	if normalizedBaseURL == "" {
		return "default"
	}
	return "custom-" + shortHash(normalizedBaseURL)
}

func defaultAuthType(provider string, authType string) string {
	if trimmed := strings.ToLower(strings.TrimSpace(authType)); trimmed != "" {
		return trimmed
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "ollama", "codefree":
		return "none"
	case "minimax":
		return "bearer_token"
	default:
		return "api_key"
	}
}

func sanitizeIDPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func shortHash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "default"
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:10]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
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
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	committed = true
	return syncDir(dir)
}

var _ Store = (*FileStore)(nil)
