package modelcatalog

import (
	"encoding/json"
	"sort"
	"strings"

	_ "embed"
)

//go:embed provider_capability_overlay.json
var embeddedProviderOverlayBytes []byte

type overlayEntry struct {
	ContextWindow          *int     `json:"context_window,omitempty"`
	MaxOutput              *int     `json:"max_output,omitempty"`
	DefaultMaxOutput       *int     `json:"default_max_output,omitempty"`
	ToolCalls              *bool    `json:"tool_calls,omitempty"`
	Reasoning              *bool    `json:"reasoning,omitempty"`
	ReasoningMode          string   `json:"reasoning_mode,omitempty"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	Images                 *bool    `json:"images,omitempty"`
	JSONOutput             *bool    `json:"json_output,omitempty"`
}

type overlaySnapshot map[string]overlayEntry

var providerOverlayCatalog = parseOverlayBytes(embeddedProviderOverlayBytes)

func parseOverlayBytes(data []byte) overlaySnapshot {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	out := make(overlaySnapshot, len(raw))
	for key, value := range raw {
		if key == "_comment" {
			continue
		}
		var entry overlayEntry
		if err := json.Unmarshal(value, &entry); err != nil {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(key))] = entry
	}
	return out
}

func searchOverlay(provider, modelName string) (overlayEntry, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	if provider == "" || len(providerOverlayCatalog) == 0 {
		return overlayEntry{}, false
	}
	bestLen := -1
	bestWildcard := false
	var best overlayEntry
	found := false
	for key, entry := range providerOverlayCatalog {
		keyProvider, pattern, ok := splitCatalogKey(key)
		if !ok || keyProvider != provider {
			continue
		}
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		wildcard := pattern == "*"
		if !wildcard {
			if modelName == "" {
				continue
			}
			if modelName != pattern && !strings.HasPrefix(modelName, pattern) {
				continue
			}
		}
		matchLen := 0
		if !wildcard {
			matchLen = len(pattern)
		}
		if !found || wildcard != bestWildcard && !wildcard || matchLen > bestLen || (wildcard == bestWildcard && matchLen > bestLen) {
			best = entry
			bestLen = matchLen
			bestWildcard = wildcard
			found = true
		}
	}
	return best, found
}

func mergeCapabilities(base ModelCapabilities, overlay overlayEntry) ModelCapabilities {
	out := base
	if overlay.ContextWindow != nil && *overlay.ContextWindow > 0 && out.ContextWindowTokens <= 0 {
		out.ContextWindowTokens = *overlay.ContextWindow
	}
	if overlay.MaxOutput != nil && *overlay.MaxOutput > 0 && out.MaxOutputTokens <= 0 {
		out.MaxOutputTokens = *overlay.MaxOutput
	}
	if overlay.DefaultMaxOutput != nil && *overlay.DefaultMaxOutput > 0 && out.DefaultMaxOutputTokens <= 0 {
		out.DefaultMaxOutputTokens = *overlay.DefaultMaxOutput
	}
	if overlay.ToolCalls != nil && !out.SupportsToolCalls {
		out.SupportsToolCalls = *overlay.ToolCalls
	}
	if overlay.Reasoning != nil {
		out.SupportsReasoning = *overlay.Reasoning
	}
	if normalized := NormalizeReasoningMode(overlay.ReasoningMode); normalized != "" {
		out.ReasoningMode = normalized
	}
	if overlay.Images != nil && !out.SupportsImages {
		out.SupportsImages = *overlay.Images
	}
	if overlay.JSONOutput != nil && !out.SupportsJSONOutput {
		out.SupportsJSONOutput = *overlay.JSONOutput
	}
	if normalized := normalizeReasoningEffortList(overlay.ReasoningEfforts); len(normalized) > 0 {
		out.ReasoningEfforts = normalized
		out.SupportsReasoning = true
	}
	if normalized := NormalizeReasoningEffort(overlay.DefaultReasoningEffort); normalized != "" {
		out.DefaultReasoningEffort = normalized
	}
	normalizeModelCapabilitiesReasoning(&out)
	if out.DefaultMaxOutputTokens <= 0 {
		out.DefaultMaxOutputTokens = defaultMaxOutputHeuristic(out.MaxOutputTokens, out.ContextWindowTokens, out.SupportsReasoning)
	}
	return out
}

func lookupOverlayModelCapabilities(provider, modelName string) (ModelCapabilities, bool) {
	entry, ok := searchOverlay(provider, modelName)
	if !ok {
		return ModelCapabilities{}, false
	}
	caps := mergeCapabilities(ModelCapabilities{}, entry)
	defaults := DefaultModelCapabilities()
	if caps.ContextWindowTokens <= 0 {
		caps.ContextWindowTokens = defaults.ContextWindowTokens
	}
	if caps.MaxOutputTokens <= 0 {
		caps.MaxOutputTokens = defaults.MaxOutputTokens
	}
	if caps.DefaultMaxOutputTokens <= 0 {
		caps.DefaultMaxOutputTokens = defaults.DefaultMaxOutputTokens
	}
	if !caps.SupportsToolCalls {
		caps.SupportsToolCalls = defaults.SupportsToolCalls
	}
	if !caps.SupportsJSONOutput {
		caps.SupportsJSONOutput = defaults.SupportsJSONOutput
	}
	return caps, true
}

func LookupOverlayModelCapabilities(provider, modelName string) (ModelCapabilities, bool) {
	return lookupOverlayModelCapabilities(provider, modelName)
}

func ListCatalogModels(provider string) []string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}
	seen := map[string]string{}
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || model == "*" {
			return
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = model
	}

	dynamicMu.RLock()
	local := localOverrides
	dynamicMu.RUnlock()

	for key := range local {
		p, model, ok := splitCatalogKey(key)
		if !ok || !providerMatches(provider, p) {
			continue
		}
		add(model)
	}
	for _, entry := range builtinCatalog {
		if strings.EqualFold(strings.TrimSpace(entry.provider), provider) {
			add(entry.pattern)
		}
	}
	for key := range providerOverlayCatalog {
		p, model, ok := splitCatalogKey(key)
		if !ok || p != provider {
			continue
		}
		add(model)
	}
	out := make([]string, 0, len(seen))
	for _, model := range seen {
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}
