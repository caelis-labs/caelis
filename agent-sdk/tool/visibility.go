package tool

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// ToolVisibility owns the current model-visible tool set for one run. It keeps
// deferred-tool policy and tool_search replay interpretation in one place.
type ToolVisibility struct {
	tools   []Tool
	visible map[string]bool
}

// NewToolVisibility returns the initial model-visible tool set. When the
// tool_search builtin is present, MCP tools are deferred until discovered.
func NewToolVisibility(tools []Tool) ToolVisibility {
	visibility := ToolVisibility{
		tools:   append([]Tool(nil), tools...),
		visible: map[string]bool{},
	}
	deferMCP := hasToolSearchTool(tools)
	for _, item := range tools {
		if item == nil {
			continue
		}
		def := item.Definition()
		name := CanonicalName(def.Name)
		if name == "" || (deferMCP && IsMCPDefinition(def)) {
			continue
		}
		visibility.visible[name] = true
	}
	return visibility
}

// ApplyToolResult restores visibility changes from one durable tool result.
func (v *ToolVisibility) ApplyToolResult(name string, output map[string]any) {
	if !strings.EqualFold(strings.TrimSpace(name), ToolSearchToolName) {
		return
	}
	v.ApplyToolSearchOutput(output)
}

// ApplyToolSearchOutput reveals tools returned by the canonical tool_search
// result contract. Malformed payloads are ignored.
func (v *ToolVisibility) ApplyToolSearchOutput(output map[string]any) {
	if v == nil {
		return
	}
	v.ApplyDiscoveredToolNames(ParseToolSearchOutput(output).DiscoveredToolNames())
}

// ApplyDiscoveredToolNames reveals already-discovered tools from replay
// metadata.
func (v *ToolVisibility) ApplyDiscoveredToolNames(names []string) {
	if v == nil {
		return
	}
	for _, name := range names {
		v.Reveal(name)
	}
}

// Reveal marks one named tool as model-visible.
func (v *ToolVisibility) Reveal(name string) {
	if v == nil {
		return
	}
	if v.visible == nil {
		v.visible = map[string]bool{}
	}
	if canonical := CanonicalName(name); canonical != "" {
		v.visible[canonical] = true
	}
}

// ModelSpecs returns the currently model-visible tools in registration order.
func (v ToolVisibility) ModelSpecs() []model.ToolSpec {
	if len(v.tools) == 0 {
		return nil
	}
	definitions := make([]Definition, 0, len(v.tools))
	for _, item := range v.tools {
		if item == nil {
			continue
		}
		def := item.Definition()
		name := CanonicalName(def.Name)
		if name == "" || !v.visible[name] {
			continue
		}
		definitions = append(definitions, CloneDefinition(def))
	}
	return modelSpecsFromDefinitions(definitions)
}

func hasToolSearchTool(tools []Tool) bool {
	for _, item := range tools {
		if item != nil && IsToolSearchDefinition(item.Definition()) {
			return true
		}
	}
	return false
}

// ToolSearchResult is the canonical JSON result contract emitted by tool_search.
type ToolSearchResult struct {
	Tools []ToolSearchDiscoveredTool `json:"tools,omitempty"`
	Count int                        `json:"count,omitempty"`
}

// ToolSearchDiscoveredTool describes one deferred tool that can be made visible
// after a tool_search result.
type ToolSearchDiscoveredTool struct {
	Type         string                      `json:"type"`
	Name         string                      `json:"name,omitempty"`
	Description  string                      `json:"description,omitempty"`
	Parameters   map[string]any              `json:"parameters,omitempty"`
	DeferLoading bool                        `json:"defer_loading,omitempty"`
	Source       map[string]any              `json:"source,omitempty"`
	Function     *ToolSearchFunctionContract `json:"function,omitempty"`
}

// ToolSearchFunctionContract accepts the nested function shape used by some
// provider tool payloads when parsing old or hand-written search results.
type ToolSearchFunctionContract struct {
	Name string `json:"name,omitempty"`
}

// NewToolSearchResult constructs one canonical tool_search result from deferred
// tool definitions.
func NewToolSearchResult(definitions []Definition) ToolSearchResult {
	result := ToolSearchResult{
		Tools: make([]ToolSearchDiscoveredTool, 0, len(definitions)),
	}
	for _, def := range definitions {
		result.Tools = append(result.Tools, NewToolSearchDiscoveredTool(def))
	}
	result.Count = len(result.Tools)
	return result
}

// NewToolSearchDiscoveredTool converts one deferred definition into the
// tool_search result contract.
func NewToolSearchDiscoveredTool(def Definition) ToolSearchDiscoveredTool {
	return ToolSearchDiscoveredTool{
		Type:         "function",
		Name:         strings.TrimSpace(def.Name),
		Description:  strings.TrimSpace(def.Description),
		Parameters:   jsonvalue.CloneMap(def.InputSchema),
		DeferLoading: true,
		Source:       toolSearchSource(def),
	}
}

// ParseToolSearchOutput decodes one tool_search output map into the canonical
// result type. Invalid payloads return an empty result.
func ParseToolSearchOutput(output map[string]any) ToolSearchResult {
	if len(output) == 0 {
		return ToolSearchResult{}
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return ToolSearchResult{}
	}
	var result ToolSearchResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return ToolSearchResult{}
	}
	if result.Count == 0 && len(result.Tools) > 0 {
		result.Count = len(result.Tools)
	}
	return result
}

// DiscoveredToolNames returns the tool names that should become visible.
func (r ToolSearchResult) DiscoveredToolNames() []string {
	if len(r.Tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.Tools))
	for _, discovered := range r.Tools {
		if name := strings.TrimSpace(discovered.Name); name != "" {
			names = append(names, name)
			continue
		}
		if discovered.Function != nil {
			if name := strings.TrimSpace(discovered.Function.Name); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// DiscoveredToolNamesMetadataValue returns a normalized metadata value for
// replaying discovered deferred-tool visibility.
func DiscoveredToolNamesMetadataValue(names []string) []string {
	return normalizeToolNameList(names)
}

// DiscoveredToolNamesFromMetadata reads discovered deferred-tool visibility
// replay metadata.
func DiscoveredToolNamesFromMetadata(meta map[string]any) []string {
	if len(meta) == 0 {
		return nil
	}
	switch typed := meta[MetadataDiscoveredToolNames].(type) {
	case []string:
		return normalizeToolNameList(typed)
	case []any:
		names := make([]string, 0, len(typed))
		for _, item := range typed {
			if name, _ := item.(string); strings.TrimSpace(name) != "" {
				names = append(names, name)
			}
		}
		return normalizeToolNameList(names)
	default:
		return nil
	}
}

func normalizeToolNameList(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		canonical := CanonicalName(name)
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, name)
	}
	return out
}

func toolSearchSource(def Definition) map[string]any {
	source := map[string]any{}
	if value, _ := def.Metadata[MetadataPluginID].(string); strings.TrimSpace(value) != "" {
		source["plugin_id"] = strings.TrimSpace(value)
	}
	if value, _ := def.Metadata[MetadataMCPServer].(string); strings.TrimSpace(value) != "" {
		source["mcp_server"] = strings.TrimSpace(value)
	}
	if value, _ := def.Metadata[MetadataMCPTool].(string); strings.TrimSpace(value) != "" {
		source["mcp_tool"] = strings.TrimSpace(value)
	}
	if len(source) == 0 {
		return nil
	}
	return source
}
