package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Routing metadata is set by the MCP adapter when it binds the tool directory.
const (
	MetadataKind              = "caelis.tool.kind"
	MetadataPluginID          = "caelis.plugin.id"
	MetadataServer            = "caelis.mcp.server"
	MetadataTool              = "caelis.mcp.tool"
	MetadataSourceFingerprint = "caelis.mcp.source_fingerprint"
	MetadataModelFunction     = "caelis.mcp.model_function"
	KindTool                  = "mcp"
	SearchToolName            = "tool_search"
)

// IsDefinition reports whether the MCP adapter marked the definition as a remote tool.
func IsDefinition(def tool.Definition) bool { return definitionKind(def) == KindTool }

// IsModelFunction reports whether the embedder selected this MCP tool for
// direct model declaration. A remote MCP catalog cannot set this marker.
func IsModelFunction(def tool.Definition) bool {
	selected, _ := def.Metadata[MetadataModelFunction].(bool)
	return selected && IsDefinition(def)
}

// IsSearchDefinition identifies the internal discovery declaration.
func IsSearchDefinition(def tool.Definition) bool {
	return definitionKind(def) == SearchToolName && def.Name == SearchToolName
}

func definitionKind(def tool.Definition) string {
	kind, _ := def.Metadata[MetadataKind].(string)
	return strings.ToLower(strings.TrimSpace(kind))
}

// BindingForDefinition returns an opaque source identity and definition version.
// Incomplete external definitions cannot participate in version-bound discovery.
func BindingForDefinition(def tool.Definition) tool.Binding {
	field := func(k string) string { value, _ := def.Metadata[k].(string); return value }
	remote, server, version := field(MetadataTool), field(MetadataServer), field(MetadataSourceFingerprint)
	if !IsDefinition(def) || remote == "" || server == "" || version == "" {
		return tool.Binding{}
	}
	raw, _ := json.Marshal([]string{field(MetadataPluginID), server, remote})
	sum := sha256.Sum256(raw)
	return tool.Binding{Name: def.Name, Identity: hex.EncodeToString(sum[:]), Version: version}
}
