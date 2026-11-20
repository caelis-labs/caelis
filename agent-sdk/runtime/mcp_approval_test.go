package runtime

import (
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func fixtureMCPDefinition(source string) tool.Definition {
	return tool.Definition{Name: "docs__read", InputSchema: map[string]any{"type": "object"}, Metadata: map[string]any{
		mcp.MetadataKind: mcp.KindTool, mcp.MetadataPluginID: "fixture",
		mcp.MetadataServer: "docs", mcp.MetadataTool: "read",
		mcp.MetadataSourceFingerprint: source,
	}}
}
