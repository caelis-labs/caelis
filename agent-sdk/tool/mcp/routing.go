package mcp

import (
	"context"
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

// IsCallDefinition identifies the fixed MCP invocation envelope.
func IsCallDefinition(def tool.Definition) bool {
	return definitionKind(def) == CallToolName && def.Name == CallToolName
}

func definitionKind(def tool.Definition) string {
	kind, _ := def.Metadata[MetadataKind].(string)
	return strings.ToLower(strings.TrimSpace(kind))
}

const CallToolName = "mcp_call"

// NewCallTool declares the fixed MCP invocation envelope. Runtime resolves
// it to an already discovered real Tool before entering its execution wrappers.
func NewCallTool() tool.Tool {
	return tool.NamedTool{Def: tool.Definition{Name: CallToolName, Description: "Call a previously bound MCP tool by exact name using arguments matching its input_schema.", InputSchema: map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"tool_name", "arguments"}, "properties": map[string]any{
			"tool_name": map[string]any{"type": "string", "minLength": 1}, "arguments": map[string]any{"type": "object", "additionalProperties": true},
		}}, Metadata: map[string]any{MetadataKind: CallToolName}}, Invoke: func(_ context.Context, _ tool.Call) (tool.Result, error) {
		return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, "mcp_call requires Runtime discovery and routing")
	}}
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
