package mcp

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const MaxToolSearchResultPromptTokens = 8192

// ToolSearchResult is the canonical JSON result contract emitted by tool_search.
type ToolSearchResult struct {
	Tools        []ToolSearchDiscoveredTool `json:"tools,omitempty"`
	Count        int                        `json:"count,omitempty"`
	Truncated    bool                       `json:"truncated,omitempty"`
	OmittedCount int                        `json:"omitted_count,omitempty"`
	// Next is the last emitted name when an exact service has another page.
	Next string `json:"next,omitempty"`
}

// ToolSearchDiscoveredTool identifies one deferred tool. The full accepted schema is returned in discovery results.
type ToolSearchDiscoveredTool struct {
	Description  string                      `json:"description,omitempty"`
	InputSchema  map[string]any              `json:"input_schema,omitempty"`
	OutputSchema map[string]any              `json:"output_schema,omitempty"`
	Name         string                      `json:"name,omitempty"`
	Binding      *tool.Binding               `json:"binding,omitempty"`
	Function     *ToolSearchFunctionContract `json:"function,omitempty"`
}

// ToolSearchFunctionContract accepts the nested function shape used by some
// provider tool payloads when parsing old or hand-written search results.
type ToolSearchFunctionContract struct {
	Name string `json:"name,omitempty"`
}

// NewToolSearchResult constructs one canonical tool_search result from deferred
// tool definitions.
func NewToolSearchResult(definitions []tool.Definition) ToolSearchResult {
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
func NewToolSearchDiscoveredTool(def tool.Definition) ToolSearchDiscoveredTool {
	entry := ToolSearchDiscoveredTool{
		Name:         def.Name,
		Description:  def.Description,
		InputSchema:  jsonvalue.CloneMap(def.InputSchema),
		OutputSchema: jsonvalue.CloneMap(def.OutputSchema),
	}
	if binding := BindingForDefinition(def); binding.Identity != "" {
		entry.Binding = &binding
	}
	return entry
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

// DiscoveredToolNames returns exact names in the result; it grants no call eligibility.
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

// EstimateToolSearchResultPromptTokens returns the approximate model-context
// cost of the complete serialized ToolSearch result, including projected source
// metadata. It deliberately uses serialized bytes so the admission budget is
// no weaker than the shared tool-result truncation budget for non-ASCII text.
func EstimateToolSearchResultPromptTokens(result ToolSearchResult) int {
	const bytesPerSearchToken = 4
	raw, err := json.Marshal(result)
	if err != nil {
		return MaxToolSearchResultPromptTokens + 1
	}
	if len(raw) == 0 {
		return 0
	}
	tokens := len(raw) / bytesPerSearchToken
	if len(raw)%bytesPerSearchToken != 0 {
		tokens++
	}
	return tokens
}
