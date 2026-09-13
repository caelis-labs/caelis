package codex

import (
	"path/filepath"
	"testing"

	acp "github.com/caelis-labs/acp-go-sdk"
)

func TestMCPInjectionCreatesOnlyPerServerThreadOverrides(t *testing.T) {
	command := filepath.Join(t.TempDir(), "caelis")
	config, err := mcpConfig([]acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "caelis-collaboration", Command: command, Args: []string{"collaboration", "mcp", "--stdio"}, Env: []acp.EnvVariable{{Name: "TOKEN", Value: "scoped"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	server, ok := config["mcp_servers.caelis-collaboration"].(map[string]any)
	if !ok || len(config) != 1 {
		t.Fatalf("config %#v", config)
	}
	if server["env"].(map[string]string)["TOKEN"] != "scoped" {
		t.Fatal("credential missing")
	}
	if _, err = mcpConfig([]acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "escape.config", Command: command}}}); err == nil {
		t.Fatal("accepted dotted override")
	}
}
