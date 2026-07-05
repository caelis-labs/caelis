package plugin

import sdkmcp "github.com/caelis-labs/caelis/agent-sdk/tool/mcp"

type ManifestKind string

const (
	ManifestKindCaelis ManifestKind = "caelis"
	ManifestKindClaude ManifestKind = "claude"
)

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

type SkillContribution struct {
	Namespace string
	Root      string
	Disabled  []string
}

type HookEvent string

const (
	HookEventSessionStart     HookEvent = "SessionStart"
	HookEventUserPromptSubmit HookEvent = "UserPromptSubmit"
	HookEventPreToolUse       HookEvent = "PreToolUse"
	HookEventPostToolUse      HookEvent = "PostToolUse"
)

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

type MCPServerSpec = sdkmcp.ServerSpec

const (
	MCPTransportStdio          = sdkmcp.TransportStdio
	MCPTransportStreamableHTTP = sdkmcp.TransportStreamableHTTP
	MCPTransportSSE            = sdkmcp.TransportSSE
)

func NormalizeMCPTransport(transport, command, url string) string {
	return sdkmcp.NormalizeTransport(transport, command, url)
}

type AgentContribution struct {
	Name        string
	Description string
	Command     string
	Args        []string
	Env         map[string]string
	WorkDir     string
}
