// Package mcpconfig owns the product MCP server catalog and assembly-time
// overlay files. Native servers persist in AppConfig; compatibility files are
// sampled only when a Runtime is assembled.
package mcpconfig

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	sdkmcp "github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

const (
	// NamespaceUser is the reserved MCP identity for user-global servers.
	// It is not a plugin ID and must not collide with plugin contributions.
	NamespaceUser = "caelis.mcp.user"
	// NamespaceProject is the reserved MCP identity for trusted workspace overlays.
	NamespaceProject = "caelis.mcp.project"
)

// ReservedNamespace reports whether id is a catalog overlay namespace.
func ReservedNamespace(id string) bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case NamespaceUser, NamespaceProject:
		return true
	default:
		return false
	}
}

// ServerConfig is one named MCP server in the product catalog or an overlay file.
type ServerConfig struct {
	Transport string            `json:"transport,omitempty"`
	Type      string            `json:"type,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	WorkDir   string            `json:"work_dir,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Enabled   *bool             `json:"enabled,omitempty"`
}

// Servers is the persisted native MCP catalog keyed by server name.
type Servers map[string]ServerConfig

// IsEnabled reports whether the server should be started. Omitted enabled
// defaults to true.
func (s ServerConfig) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// ValidateIdentities rejects empty or colliding names before Normalize would
// drop or fold them.
func ValidateIdentities(in Servers) error {
	seen := make(map[string]struct{}, len(in))
	for _, name := range slices.Sorted(maps.Keys(in)) {
		normalizedName := normalizeServerName(name)
		if normalizedName == "" {
			return fmt.Errorf("mcpconfig: MCP server name is required")
		}
		if _, exists := seen[normalizedName]; exists {
			return fmt.Errorf("mcpconfig: duplicate MCP server %q", normalizedName)
		}
		seen[normalizedName] = struct{}{}
	}
	return nil
}

// Normalize returns a detached catalog with trimmed identities. Empty names are
// dropped. Duplicate names after trimming keep the first record.
func Normalize(in Servers) Servers {
	if len(in) == 0 {
		return nil
	}
	out := make(Servers, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, name := range slices.Sorted(maps.Keys(in)) {
		normalizedName := normalizeServerName(name)
		if normalizedName == "" {
			continue
		}
		if _, exists := seen[normalizedName]; exists {
			continue
		}
		seen[normalizedName] = struct{}{}
		out[normalizedName] = normalizeServerConfig(in[name])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Validate checks enabled native servers for a usable transport and identity.
func Validate(in Servers) error {
	for _, name := range slices.Sorted(maps.Keys(in)) {
		cfg := in[name]
		normalizedName := normalizeServerName(name)
		if normalizedName == "" {
			return fmt.Errorf("mcpconfig: MCP server name is required")
		}
		if !cfg.IsEnabled() {
			continue
		}
		if err := validateEnabledServer(normalizedName, cfg); err != nil {
			return err
		}
	}
	return nil
}

func normalizeServerName(name string) string {
	return strings.TrimSpace(name)
}

func normalizeServerConfig(in ServerConfig) ServerConfig {
	out := in
	out.Transport = strings.TrimSpace(in.Transport)
	out.Type = strings.TrimSpace(in.Type)
	out.Command = strings.TrimSpace(in.Command)
	out.WorkDir = strings.TrimSpace(in.WorkDir)
	out.URL = strings.TrimSpace(in.URL)
	if len(in.Args) > 0 {
		out.Args = append([]string(nil), in.Args...)
	} else {
		out.Args = nil
	}
	out.Env = cloneStringMap(in.Env)
	out.Headers = cloneStringMap(in.Headers)
	if in.Enabled != nil {
		enabled := *in.Enabled
		out.Enabled = &enabled
	}
	return out
}

func validateEnabledServer(name string, cfg ServerConfig) error {
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("mcpconfig: MCP server %q must not contain a path separator", name)
	}
	transport := sdkmcp.NormalizeTransport(firstNonEmpty(cfg.Transport, cfg.Type), cfg.Command, cfg.URL)
	switch transport {
	case sdkmcp.TransportStdio:
		if cfg.Command == "" {
			return fmt.Errorf("mcpconfig: MCP server %q requires a command", name)
		}
	case sdkmcp.TransportStreamableHTTP, sdkmcp.TransportSSE:
		if cfg.URL == "" {
			return fmt.Errorf("mcpconfig: MCP server %q requires a URL", name)
		}
	default:
		return fmt.Errorf("mcpconfig: MCP server %q has unsupported transport %q", name, transport)
	}
	return nil
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
