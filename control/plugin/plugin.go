// Package plugin owns product plugin configuration, discovery, installation,
// marketplace resolution, lifecycle mutation, and normalized contributions.
package plugin

import sdkmcp "github.com/caelis-labs/caelis/agent-sdk/tool/mcp"

// ManifestKind identifies a supported plugin manifest format.
type ManifestKind string

const (
	ManifestKindCaelis ManifestKind = "caelis"
	ManifestKindClaude ManifestKind = "claude"
)

// InstalledPlugin is the normalized product view of one installed plugin.
type InstalledPlugin struct {
	ID          string
	Name        string
	Version     string
	Root        string
	Manifest    string
	Kind        ManifestKind
	Enabled     bool
	Description string
	Skills      []SkillContribution
	Hooks       []HookSpec
	MCPServers  []MCPServerSpec
	Agents      []AgentContribution
	InstalledAt string
}

// SkillContribution describes one plugin-provided skill tree.
type SkillContribution struct {
	Namespace string
	Root      string
	Disabled  []string
}

// HookEvent identifies a product lifecycle point supported by plugin hooks.
type HookEvent string

const (
	HookEventSessionStart     HookEvent = "SessionStart"
	HookEventUserPromptSubmit HookEvent = "UserPromptSubmit"
	HookEventPreToolUse       HookEvent = "PreToolUse"
	HookEventPostToolUse      HookEvent = "PostToolUse"
)

// HookSpec is the normalized executable configuration for one plugin hook.
type HookSpec struct {
	PluginID   string
	Event      HookEvent
	Command    string
	Args       []string
	RawCommand string
	Env        map[string]string
	WorkDir    string
	Timeout    string
	PluginDir  string
}

// MCPServerSpec reuses the SDK MCP server configuration contract.
type MCPServerSpec = sdkmcp.ServerSpec

const (
	MCPTransportStdio          = sdkmcp.TransportStdio
	MCPTransportStreamableHTTP = sdkmcp.TransportStreamableHTTP
	MCPTransportSSE            = sdkmcp.TransportSSE
)

// NormalizeMCPTransport returns the canonical SDK transport name.
func NormalizeMCPTransport(transport, command, url string) string {
	return sdkmcp.NormalizeTransport(transport, command, url)
}

// AgentContribution describes one external Agent contributed by a plugin.
type AgentContribution struct {
	Name        string
	Description string
	Command     string
	Args        []string
	Env         map[string]string
	WorkDir     string
}
