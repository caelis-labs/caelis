package plugin

import "strings"

type ManifestKind string

const (
	ManifestKindCaelis ManifestKind = "caelis"
	ManifestKindCodex  ManifestKind = "codex"
	ManifestKindClaude ManifestKind = "claude"
	ManifestKindGemini ManifestKind = "gemini"
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

type MCPServerSpec struct {
	PluginID  string
	Name      string
	Transport string
	Command   string
	Args      []string
	Env       map[string]string
	WorkDir   string
	URL       string
	Headers   map[string]string
}

const (
	MCPTransportStdio          = "stdio"
	MCPTransportStreamableHTTP = "streamable_http"
	MCPTransportSSE            = "sse"
)

func NormalizeMCPTransport(transport, command, url string) string {
	transport = strings.TrimSpace(transport)
	command = strings.TrimSpace(command)
	url = strings.TrimSpace(url)
	switch transport {
	case "", "stdio", "command":
		if command != "" || url == "" {
			return MCPTransportStdio
		}
		return MCPTransportStreamableHTTP
	case "http", "streamable", "streamable-http", "streamable_http":
		return MCPTransportStreamableHTTP
	case "sse":
		return MCPTransportSSE
	default:
		return transport
	}
}

type AgentContribution struct {
	Name        string
	Description string
	Command     string
	Args        []string
	Env         map[string]string
	WorkDir     string
}
