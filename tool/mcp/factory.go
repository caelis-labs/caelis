package mcp

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	caelisplugin "github.com/OnslaughtSnail/caelis/plugin"
)

// ClientFactory opens transport-specific MCP clients for plugin declarations.
type ClientFactory interface {
	NewClient(context.Context, caelisplugin.MCPServer, string) (Client, error)
}

// ClientFactoryFunc adapts a function into a ClientFactory.
type ClientFactoryFunc func(context.Context, caelisplugin.MCPServer, string) (Client, error)

func (f ClientFactoryFunc) NewClient(ctx context.Context, server caelisplugin.MCPServer, pluginRoot string) (Client, error) {
	return f(ctx, server, pluginRoot)
}

// DefaultClientFactory supports plugin-declared stdio MCP servers.
type DefaultClientFactory struct{}

func (DefaultClientFactory) NewClient(ctx context.Context, server caelisplugin.MCPServer, pluginRoot string) (Client, error) {
	transport := strings.ToLower(strings.TrimSpace(server.Transport))
	if transport == "" {
		transport = "stdio"
	}
	switch transport {
	case "stdio":
		return NewStdioClient(ctx, StdioConfig{
			Command: strings.TrimSpace(server.Command),
			Args:    append([]string(nil), server.Args...),
			Env:     mergedEnv(server.Env),
			Dir:     strings.TrimSpace(pluginRoot),
		})
	case "http", "streamable-http", "streamable_http":
		return NewHTTPClient(ctx, HTTPConfig{URL: server.URL})
	case "sse":
		return NewHTTPClient(ctx, HTTPConfig{URL: server.URL, SSE: true})
	default:
		return nil, fmt.Errorf("tool/mcp: transport %q is not supported", transport)
	}
}

func mergedEnv(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return nil
	}
	out := os.Environ()
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+overrides[key])
	}
	return out
}
