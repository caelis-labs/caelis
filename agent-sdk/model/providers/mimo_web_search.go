package providers

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// mimoProviderWebSearchWireType is the MiMo wire-level provider tool type.
// It is distinct from the Caelis builtin function tool named web_search.
const mimoProviderWebSearchWireType = "web_search"

var mimoWebSearchMatcher = providerExecutedToolMatcher{
	ProviderAliases: []string{"", "mimo", "xiaomi", "xiaomimimo"},
	Names:           []string{mimoProviderWebSearchWireType, "web-search", "websearch"},
	DetailKeys:      []string{"web_search", "webSearch"},
	NestedControls:  true,
	IncludeExtra:    true,
}

func mimoProviderTools(_ string, specs []model.ToolSpec) []openAICompatTool {
	enabled, extra := mimoProviderWebSearchToolConfig(specs)
	if !enabled {
		return nil
	}
	return []openAICompatTool{mimoProviderWebSearchTool(extra)}
}

func mimoUsesProviderExecutedTools(_ string, specs []model.ToolSpec) bool {
	enabled, _ := mimoProviderWebSearchToolConfig(specs)
	return enabled
}

func mimoProviderWebSearchToolConfig(specs []model.ToolSpec) (bool, map[string]json.RawMessage) {
	match := providerExecutedToolMatchFromSpecs(specs, mimoWebSearchMatcher)
	return match.Enabled && !match.Disabled, match.Extra
}

func mimoRuntimeProviderMatches(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return provider == "mimo" || provider == "xiaomi" || provider == "xiaomimimo"
}

func mimoProviderWebSearchTool(extra map[string]json.RawMessage) openAICompatTool {
	return openAICompatTool{
		Type:  mimoProviderWebSearchWireType,
		Extra: extra,
	}
}

func mimoProviderWebSearchDefaultExtra(maxResults int) map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"max_keyword":  mustRawJSON(3),
		"force_search": mustRawJSON(true),
		"limit":        mustRawJSON(maxResults),
	}
}

func mustRawJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}
