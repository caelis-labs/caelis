package client

import "github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"

// Codex's structured MCP identity selects only the Caelis SendMessage display
// profile. Standard kind/input and an existing exact name remain authoritative.
// Remove this provider hint when ACP supplies a standard structured MCP identity.
func normalizeCodexMCPDisplay(meta map[string]any, kind string) map[string]any {
	if kind != "" && kind != toolKindOther || acpmeta.ToolName(meta) != "" {
		return meta
	}
	mcp, _ := meta["codex/mcp_tool"].(map[string]any)
	if mapString(mcp, "server") != "caelis-collaboration" || mapString(mcp, "name") != "SendMessage" {
		return meta
	}
	return acpmeta.WithToolName(meta, "SendMessage")
}
