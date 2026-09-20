package client

import "github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"

// Codex's structured MCP identity selects only the Caelis collaboration
// SendMessage and ReadMessages display profiles. Standard kind/input/content and
// an existing exact name remain authoritative. ReadMessages is a TUI-only
// compatibility hint; remove these provider hints when ACP supplies a standard
// structured MCP identity.
func normalizeCodexMCPDisplay(meta map[string]any, kind string) map[string]any {
	if kind != "" && kind != toolKindOther || acpmeta.ToolName(meta) != "" {
		return meta
	}
	mcp, _ := meta["codex/mcp_tool"].(map[string]any)
	if mapString(mcp, "server") != "caelis-collaboration" {
		return meta
	}
	switch name := mapString(mcp, "name"); name {
	case "SendMessage", "ReadMessages":
		return acpmeta.WithToolName(meta, name)
	default:
		return meta
	}
}
