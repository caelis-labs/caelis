package mcp

import "strings"

// ServerSpec describes one MCP server connection.
type ServerSpec struct {
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
