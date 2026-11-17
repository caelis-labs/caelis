package runtime

import "github.com/caelis-labs/caelis/agent-sdk/tool"

func fixtureMCPDefinition(source string) tool.Definition {
	return tool.Definition{Name: "docs__read", InputSchema: map[string]any{"type": "object"}, Metadata: map[string]any{
		tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataPluginID: "fixture",
		tool.MetadataMCPServer: "docs", tool.MetadataMCPTool: "read",
		tool.MetadataMCPSourceFingerprint: source,
	}}
}
