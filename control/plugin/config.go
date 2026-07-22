package plugin

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

// Config is the persisted configuration for one installed plugin.
type Config struct {
	ID          string       `json:"id,omitempty"`
	Name        string       `json:"name,omitempty"`
	Root        string       `json:"root,omitempty"`
	Manifest    string       `json:"manifest,omitempty"`
	Kind        ManifestKind `json:"kind,omitempty"`
	Enabled     bool         `json:"enabled"`
	Version     string       `json:"version,omitempty"`
	Description string       `json:"description,omitempty"`
	Managed     bool         `json:"managed,omitempty"`
	CacheRoot   string       `json:"cache_root,omitempty"`
}

// MarketplaceConfig is the persisted configuration for one plugin marketplace.
type MarketplaceConfig struct {
	Name                              string   `json:"name,omitempty"`
	Description                       string   `json:"description,omitempty"`
	Owner                             string   `json:"owner,omitempty"`
	Source                            string   `json:"source,omitempty"`
	Root                              string   `json:"root,omitempty"`
	Version                           string   `json:"version,omitempty"`
	RepoURL                           string   `json:"repo_url,omitempty"`
	PluginRoot                        string   `json:"plugin_root,omitempty"`
	AllowCrossMarketplaceDependencies []string `json:"allow_cross_marketplace_dependencies,omitempty"`
}

// State is the Plugin-owned part of the product configuration.
type State struct {
	Plugins      []Config
	Marketplaces []MarketplaceConfig
}

// Clone returns a detached copy suitable for mutation.
func (s State) Clone() State {
	return State{
		Plugins:      append([]Config(nil), s.Plugins...),
		Marketplaces: cloneMarketplaceConfigs(s.Marketplaces),
	}
}

// Mutation describes one atomic Plugin configuration change. The host owns
// serialization, persistence, active-Turn fencing, Runtime rebuild, and
// rollback; Control owns the state transition applied inside that boundary.
// AfterCommit runs synchronously with the committed state after persistence
// and any Runtime rebuild succeed, but before the host releases its mutation
// locks. An AfterCommit error reports a committed partial success and must not
// trigger rollback.
type Mutation struct {
	GuardAction   string
	FailureAction string
	Reconfigure   bool
	Apply         func(*State) error
	AfterCommit   func(State) error
}

// Host is the narrow application seam required by the Control Plugin service.
// UpdatePluginState must invoke Apply and AfterCommit synchronously according
// to the Mutation contract.
type Host interface {
	StoreDir() string
	LoadPluginState(context.Context) (State, error)
	UpdatePluginState(context.Context, Mutation) error
	MCPServersStatus(pluginID string) []mcp.MCPServerInfo
}

// NormalizeConfig returns the canonical persisted plugin record.
func NormalizeConfig(in Config) Config {
	out := in
	out.ID = strings.ToLower(strings.TrimSpace(in.ID))
	out.Name = strings.TrimSpace(in.Name)
	out.Root = strings.TrimSpace(in.Root)
	out.Manifest = strings.TrimSpace(in.Manifest)
	out.Kind = ManifestKind(strings.ToLower(strings.TrimSpace(string(in.Kind))))
	out.Version = strings.TrimSpace(in.Version)
	out.Description = strings.TrimSpace(in.Description)
	out.CacheRoot = strings.TrimSpace(in.CacheRoot)
	if !out.Managed {
		out.CacheRoot = ""
	}
	return out
}

// DedupeConfigs normalizes plugins and keeps the first record for each ID.
func DedupeConfigs(configs []Config) []Config {
	if len(configs) == 0 {
		return nil
	}
	out := make([]Config, 0, len(configs))
	seen := make(map[string]struct{}, len(configs))
	for _, cfg := range configs {
		cfg = NormalizeConfig(cfg)
		if cfg.ID == "" {
			continue
		}
		key := strings.ToLower(cfg.ID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cfg)
	}
	return out
}

// NormalizeMarketplaceConfig returns the canonical persisted marketplace record.
func NormalizeMarketplaceConfig(in MarketplaceConfig) MarketplaceConfig {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Description = strings.TrimSpace(in.Description)
	out.Owner = strings.TrimSpace(in.Owner)
	out.Source = strings.TrimSpace(in.Source)
	out.Root = strings.TrimSpace(in.Root)
	out.Version = strings.TrimSpace(in.Version)
	out.RepoURL = strings.TrimSpace(in.RepoURL)
	out.PluginRoot = strings.TrimSpace(in.PluginRoot)
	out.AllowCrossMarketplaceDependencies = dedupeStrings(in.AllowCrossMarketplaceDependencies)
	return out
}

// DedupeMarketplaceConfigs normalizes marketplaces and keeps the first record
// for each name.
func DedupeMarketplaceConfigs(configs []MarketplaceConfig) []MarketplaceConfig {
	if len(configs) == 0 {
		return nil
	}
	out := make([]MarketplaceConfig, 0, len(configs))
	seen := make(map[string]struct{}, len(configs))
	for _, cfg := range configs {
		cfg = NormalizeMarketplaceConfig(cfg)
		if cfg.Name == "" {
			continue
		}
		key := strings.ToLower(cfg.Name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cfg)
	}
	return out
}

// UpsertMarketplaceConfig updates one marketplace by case-insensitive name.
func UpsertMarketplaceConfig(configs []MarketplaceConfig, entry MarketplaceConfig) []MarketplaceConfig {
	entry = NormalizeMarketplaceConfig(entry)
	if entry.Name == "" {
		return DedupeMarketplaceConfigs(configs)
	}
	key := strings.ToLower(entry.Name)
	out := make([]MarketplaceConfig, 0, len(configs)+1)
	replaced := false
	for _, cfg := range configs {
		cfg = NormalizeMarketplaceConfig(cfg)
		if cfg.Name == "" {
			continue
		}
		if strings.ToLower(cfg.Name) == key {
			if !replaced {
				out = append(out, entry)
				replaced = true
			}
			continue
		}
		out = append(out, cfg)
	}
	if !replaced {
		out = append(out, entry)
	}
	return out
}

func cloneMarketplaceConfigs(in []MarketplaceConfig) []MarketplaceConfig {
	if len(in) == 0 {
		return nil
	}
	out := append([]MarketplaceConfig(nil), in...)
	for i := range out {
		out[i].AllowCrossMarketplaceDependencies = append([]string(nil), out[i].AllowCrossMarketplaceDependencies...)
	}
	return out
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
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
