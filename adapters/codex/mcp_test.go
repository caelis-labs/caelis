package codex

import (
	acp "github.com/caelis-labs/acp-go-sdk"
	"testing"
)

func TestMCPInjectionCreatesOnlyPerServerThreadOverrides(t *testing.T) {
	config, err := mcpConfig([]acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "caelis-collaboration", Command: "/bin/caelis", Args: []string{"collaboration", "mcp", "--stdio"}, Env: []acp.EnvVariable{{Name: "TOKEN", Value: "scoped"}}}}})
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
	if _, err = mcpConfig([]acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "escape.config", Command: "/bin/caelis"}}}); err == nil {
		t.Fatal("accepted dotted override")
	}
}
