package mcp

import "strings"

// ServerSpec describes one MCP server connection.
type ServerSpec struct {
	PluginID string
	// ReplaySourceIDs lists prior source identities for remapping historical
	// fully qualified names. They never create additional executable names.
	ReplaySourceIDs []string
	Name            string
	Transport       string
	Command         string
	Args            []string
	Env             map[string]string
	WorkDir         string
	URL             string
	Headers         map[string]string
	// DataDir is an optional client-owned persistent directory to create before
	// launching a stdio plugin. It is never derived from the model workspace.
	DataDir string
	// CleanEnvironment keeps plugin subprocesses from inheriting host-private
	// credentials while preserving only platform process essentials.
	CleanEnvironment bool
	// SourceTrusted is set only by trusted Host assembly, never remote MCP
	// metadata or configuration documents. It skips ordinary source approval
	// for all tools on this connection, but preserves per-call policy decisions.
	// Trust is independent of the versioned source fingerprint.
	SourceTrusted bool `json:"-"`
}

// ValidateServerIdentity checks one server before Manager admission. It does
// not resolve collisions between separate configured servers.
func ValidateServerIdentity(spec ServerSpec) error {
	if err := validateMCPIdentity("plugin id", spec.PluginID, maxMCPPluginIDRunes); err != nil {
		return err
	}
	if err := validateMCPIdentity("server name", spec.Name, maxMCPServerNameRunes); err != nil {
		return err
	}
	for _, id := range spec.ReplaySourceIDs {
		if err := validateMCPIdentity("replay source id", id, maxMCPPluginIDRunes); err != nil {
			return err
		}
	}
	return nil
}

const (
	TransportStdio          = "stdio"
	TransportStreamableHTTP = "streamable_http"
	TransportSSE            = "sse"
)

// NormalizeTransport resolves the effective MCP transport from manifest fields.
func NormalizeTransport(transport, command, url string) string {
	transport = strings.TrimSpace(transport)
	command = strings.TrimSpace(command)
	url = strings.TrimSpace(url)
	switch transport {
	case "", "stdio", "command":
		if command != "" || url == "" {
			return TransportStdio
		}
		return TransportStreamableHTTP
	case "http", "streamable", "streamable-http", "streamable_http":
		return TransportStreamableHTTP
	case "sse":
		return TransportSSE
	default:
		return transport
	}
}
