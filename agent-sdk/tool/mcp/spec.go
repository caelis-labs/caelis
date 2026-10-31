package mcp

import (
	"fmt"
	"strings"
)

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
	// for all tools on this connection, but preserves resource approval and
	// per-call policy decisions.
	// Trust is independent of the versioned source fingerprint.
	SourceTrusted bool `json:"-"`
	// SourceTrustedTools skips ordinary source approval for exactly these raw
	// tool names. Only trusted Host assembly may set this list. Resource
	// approval and per-call policy are independent and remain enforced.
	SourceTrustedTools []string `json:"-"`
	// ToolResourceParameters selects a required top-level string parameter for
	// each raw tool name. Its exact value needs a Session-only grant, replacing
	// that tool's ordinary source gate. This is Host policy, not MCP metadata
	// or a field accepted from an MCP configuration document.
	ToolResourceParameters map[string]string `json:"-"`
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
	for _, name := range spec.SourceTrustedTools {
		if err := validateMCPIdentity("trusted tool name", name, maxMCPRemoteToolNameRunes); err != nil {
			return err
		}
	}
	for name, parameter := range spec.ToolResourceParameters {
		if err := validateMCPIdentity("resource tool name", name, maxMCPRemoteToolNameRunes); err != nil {
			return err
		}
		if strings.TrimSpace(parameter) == "" {
			return fmt.Errorf("resource parameter for %q is required", name)
		}
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
