package tool

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

const (
	// MaxDeferredToolsPerRun bounds the number of deferred MCP definitions that
	// can become model-visible during one Runtime run.
	MaxDeferredToolsPerRun = 64
	// MaxDeferredToolPromptTokensPerRun bounds their cumulative projected cost.
	MaxDeferredToolPromptTokensPerRun = 32768
	// MaxToolSearchResultPromptTokens bounds one complete model-visible and
	// durable ToolSearch result before it changes run-local visibility.
	MaxToolSearchResultPromptTokens = 8192
	maxToolSearchSourceRunes        = 256
)

// ToolVisibility owns the current model-visible tool set for one run. It keeps
// deferred-tool policy and tool_search replay interpretation in one place.
type ToolVisibility struct {
	tools                []Tool
	visible              map[string]bool
	available            map[string]bool
	definitions          map[string]Definition
	deferred             map[string]bool
	deferredCount        int
	deferredPromptTokens int
}

// NewToolVisibility returns the initial model-visible tool set. When the
// tool_search builtin is present, MCP tools are deferred until discovered.
// Model capability requirements remain unenforced until a model is supplied.
func NewToolVisibility(tools []Tool) ToolVisibility {
	return newToolVisibility(tools, nil)
}

// NewToolVisibilityForModel returns the initial model-visible tool set after
// applying each tool's optional model-capability requirement.
func NewToolVisibilityForModel(tools []Tool, llm model.LLM) ToolVisibility {
	return newToolVisibility(tools, llm)
}

func newToolVisibility(tools []Tool, llm model.LLM) ToolVisibility {
	visibility := ToolVisibility{
		tools:       append([]Tool(nil), tools...),
		visible:     map[string]bool{},
		definitions: map[string]Definition{},
		deferred:    map[string]bool{},
	}
	if llm != nil {
		visibility.available = map[string]bool{}
	}
	deferMCP := hasToolSearchTool(tools)
	for _, item := range tools {
		if item == nil {
			continue
		}
		def := item.Definition()
		name := CanonicalName(def.Name)
		if name == "" {
			continue
		}
		visibility.definitions[name] = CloneDefinition(def)
		available := true
		if llm != nil {
			available = AvailableForModel(item, llm)
			visibility.available[name] = available
		}
		if deferMCP && IsMCPDefinition(def) {
			visibility.deferred[name] = true
		}
		if !available || visibility.deferred[name] {
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
	v.AdmitToolSearchResult(ParseToolSearchOutput(output))
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
func (v *ToolVisibility) Reveal(name string) bool {
	if v == nil {
		return false
	}
	if v.visible == nil {
		v.visible = map[string]bool{}
	}
	canonical := CanonicalName(name)
	def, known := v.definitions[canonical]
	if canonical == "" || !known {
		return false
	}
	if available, constrained := v.available[canonical]; constrained && !available {
		return false
	}
	if v.visible[canonical] {
		return true
	}
	if v.deferred[canonical] {
		cost := EstimateDefinitionPromptTokens(def)
		if v.deferredCount >= MaxDeferredToolsPerRun || v.deferredPromptTokens+cost > MaxDeferredToolPromptTokensPerRun {
			return false
		}
		v.deferredCount++
		v.deferredPromptTokens += cost
	}
	v.visible[canonical] = true
	return true
}

// AdmitToolSearchResult replaces external discovery payloads with canonical
// registered definitions and applies the per-run visibility budget. Repeated
// results do not duplicate already-visible schemas in durable/model history.
func (v *ToolVisibility) AdmitToolSearchResult(result ToolSearchResult) ToolSearchResult {
	if v == nil {
		return ToolSearchResult{}
	}
	maxAccepted := len(result.Tools)
	for {
		admitted, planned := v.planToolSearchAdmission(result, maxAccepted)
		if EstimateToolSearchResultPromptTokens(admitted) <= MaxToolSearchResultPromptTokens {
			// Commit visibility only after the complete payload that will be sent to
			// the model and durable history has passed its own budget.
			return v.commitToolSearchAdmission(admitted, planned)
		}
		if len(planned) == 0 {
			return failedToolSearchAdmission(admitted)
		}
		maxAccepted = len(planned) - 1
	}
}

func (v *ToolVisibility) commitToolSearchAdmission(admitted ToolSearchResult, planned []string) ToolSearchResult {
	next := v.cloneForAdmission()
	for _, name := range planned {
		if !next.Reveal(name) {
			// Keep the commit atomic. The model cannot receive definitions that were
			// not made executable, but the fail-closed result must retain the exact
			// counts already established by planning.
			return failedToolSearchAdmission(admitted)
		}
	}
	*v = next
	return admitted
}

func failedToolSearchAdmission(planned ToolSearchResult) ToolSearchResult {
	omitted := max(planned.OmittedCount, 0) + len(planned.Tools)
	return ToolSearchResult{
		Truncated:           true,
		OmittedCount:        omitted,
		AlreadyVisibleCount: max(planned.AlreadyVisibleCount, 0),
	}
}

func (v *ToolVisibility) cloneForAdmission() ToolVisibility {
	next := *v
	next.visible = make(map[string]bool, len(v.visible))
	for name, value := range v.visible {
		next.visible[name] = value
	}
	return next
}

func (v *ToolVisibility) planToolSearchAdmission(result ToolSearchResult, maxAccepted int) (ToolSearchResult, []string) {
	admitted := ToolSearchResult{
		Tools:        make([]ToolSearchDiscoveredTool, 0, len(result.Tools)),
		Truncated:    result.Truncated,
		OmittedCount: max(result.OmittedCount, 0),
	}
	planned := make([]string, 0, min(len(result.Tools), max(maxAccepted, 0)))
	visible := make(map[string]bool, len(v.visible)+len(result.Tools))
	for name, value := range v.visible {
		visible[name] = value
	}
	deferredCount := v.deferredCount
	deferredPromptTokens := v.deferredPromptTokens
	for _, discovered := range result.Tools {
		name := discovered.Name
		if strings.TrimSpace(name) == "" && discovered.Function != nil {
			name = discovered.Function.Name
		}
		canonical := CanonicalName(name)
		def, known := v.definitions[canonical]
		if !known || !v.deferred[canonical] {
			admitted.OmittedCount++
			admitted.Truncated = true
			continue
		}
		if visible[canonical] {
			admitted.AlreadyVisibleCount++
			continue
		}
		cost := EstimateDefinitionPromptTokens(def)
		available := true
		if constrained, exists := v.available[canonical]; exists && !constrained {
			available = false
		}
		if !available || len(planned) >= maxAccepted || deferredCount >= MaxDeferredToolsPerRun ||
			deferredPromptTokens+cost > MaxDeferredToolPromptTokensPerRun {
			admitted.OmittedCount++
			admitted.Truncated = true
			continue
		}
		admitted.Tools = append(admitted.Tools, NewToolSearchDiscoveredTool(def))
		planned = append(planned, canonical)
		visible[canonical] = true
		deferredCount++
		deferredPromptTokens += cost
	}
	admitted.Count = len(admitted.Tools)
	return admitted, planned
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
		if name == "" || !v.visible[name] || (v.available != nil && !v.available[name]) {
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
	Tools               []ToolSearchDiscoveredTool `json:"tools,omitempty"`
	Count               int                        `json:"count,omitempty"`
	Truncated           bool                       `json:"truncated,omitempty"`
	OmittedCount        int                        `json:"omitted_count,omitempty"`
	AlreadyVisibleCount int                        `json:"already_visible_count,omitempty"`
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
		source["plugin_id"] = truncateToolSearchSource(value)
	}
	if value, _ := def.Metadata[MetadataMCPServer].(string); strings.TrimSpace(value) != "" {
		source["mcp_server"] = truncateToolSearchSource(value)
	}
	if value, _ := def.Metadata[MetadataMCPTool].(string); strings.TrimSpace(value) != "" {
		source["mcp_tool"] = truncateToolSearchSource(value)
	}
	if len(source) == 0 {
		return nil
	}
	return source
}

func truncateToolSearchSource(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxToolSearchSourceRunes {
		return value
	}
	return strings.TrimSpace(string(runes[:maxToolSearchSourceRunes]))
}
