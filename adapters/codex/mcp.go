package codex

import (
	"errors"
	"path/filepath"
	"regexp"

	acp "github.com/caelis-labs/acp-go-sdk"
)

var mcpConfigName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// mcpConfig produces per-thread overrides; it never mutates the user's global
// MCP configuration. Only stdio injection is supported by this adapter.
func mcpConfig(servers []acp.McpServer) (map[string]any, error) {
	if len(servers) == 0 {
		return nil, nil
	}
	config := map[string]any{}
	for _, server := range servers {
		s := server.Stdio
		if s == nil || server.Http != nil || server.Sse != nil {
			return nil, errors.New("codex adapter MCP injection requires stdio")
		}
		if !mcpConfigName.MatchString(s.Name) || !filepath.IsAbs(s.Command) {
			return nil, errors.New("invalid MCP server name or executable path")
		}
		key := "mcp_servers." + s.Name
		if _, exists := config[key]; exists {
			return nil, errors.New("duplicate MCP server name")
		}
		env := map[string]string{}
		for _, v := range s.Env {
			env[v.Name] = v.Value
		}
		config[key] = map[string]any{"command": s.Command, "args": append([]string{}, s.Args...), "env": env, "enabled": true}
	}
	return config, nil
}
