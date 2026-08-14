package providers

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type providerExecutedToolMatcher struct {
	ProviderAliases []string
	Names           []string
	DetailKeys      []string
	NestedControls  bool
	IncludeExtra    bool
}

type providerExecutedToolMatch struct {
	Enabled  bool
	Disabled bool
	Extra    map[string]json.RawMessage
}

func providerExecutedToolMatchFromSpecs(specs []model.ToolSpec, matcher providerExecutedToolMatcher) providerExecutedToolMatch {
	var out providerExecutedToolMatch
	for _, spec := range specs {
		if spec.Kind != model.ToolSpecKindProviderExecuted || spec.ProviderExecuted == nil {
			continue
		}
		match := providerExecutedToolMatchFromSpec(spec.ProviderExecuted, matcher)
		if match.Disabled {
			return match
		}
		if !match.Enabled {
			continue
		}
		out.Enabled = true
		out.Extra = mergeRawMessageMaps(out.Extra, match.Extra)
	}
	return out
}

func providerExecutedToolMatchFromSpec(tool *model.ProviderExecutedToolSpec, matcher providerExecutedToolMatcher) providerExecutedToolMatch {
	if tool == nil || !providerExecutedProviderMatches(tool.Provider, matcher.ProviderAliases) {
		return providerExecutedToolMatch{}
	}
	nameMatches := providerExecutedNameMatches(tool.Name, matcher.Names)
	detailsMatch := providerExecutedDetailsMatch(tool.ProviderDetails, matcher)
	if !nameMatches && !detailsMatch {
		return providerExecutedToolMatch{}
	}
	if providerExecutedDisabledByDetails(tool.ProviderDetails, matcher) {
		return providerExecutedToolMatch{Disabled: true}
	}
	match := providerExecutedToolMatch{Enabled: true}
	if matcher.IncludeExtra {
		match.Extra = providerExecutedToolExtra(tool.ProviderDetails, matcher)
	}
	return match
}

func providerExecutedProviderMatches(provider string, aliases []string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, alias := range aliases {
		if provider == strings.ToLower(strings.TrimSpace(alias)) {
			return true
		}
	}
	return false
}

func providerExecutedNameMatches(name string, names []string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, candidate := range names {
		if name == strings.ToLower(strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func providerExecutedDetailsMatch(details map[string]json.RawMessage, matcher providerExecutedToolMatcher) bool {
	if len(details) == 0 {
		return false
	}
	for _, key := range matcher.DetailKeys {
		if raw, ok := details[key]; ok && raw != nil {
			return true
		}
	}
	if raw, ok := details["type"]; ok {
		var typ string
		if err := json.Unmarshal(raw, &typ); err == nil && providerExecutedNameMatches(typ, matcher.Names) {
			return true
		}
	}
	return false
}

func providerExecutedDisabledByDetails(details map[string]json.RawMessage, matcher providerExecutedToolMatcher) bool {
	if len(details) == 0 {
		return false
	}
	if disabled, ok := boolProviderDetail(details, "disabled"); ok && disabled {
		return true
	}
	if enabled, ok := boolProviderDetail(details, "enabled"); ok && !enabled {
		return true
	}
	for _, key := range matcher.DetailKeys {
		if enabled, ok := boolProviderDetail(details, key); ok && !enabled {
			return true
		}
		if !matcher.NestedControls {
			continue
		}
		nested := rawObjectProviderDetail(details, key)
		if disabled, ok := boolProviderDetail(nested, "disabled"); ok && disabled {
			return true
		}
		if enabled, ok := boolProviderDetail(nested, "enabled"); ok && !enabled {
			return true
		}
	}
	return false
}

func providerExecutedToolExtra(details map[string]json.RawMessage, matcher providerExecutedToolMatcher) map[string]json.RawMessage {
	if len(details) == 0 {
		return nil
	}
	out := map[string]json.RawMessage{}
	for _, key := range matcher.DetailKeys {
		for nestedKey, raw := range rawObjectProviderDetail(details, key) {
			nestedKey = strings.TrimSpace(nestedKey)
			if nestedKey == "" || raw == nil || providerExecutedControlDetail(nestedKey, matcher) {
				continue
			}
			out[nestedKey] = append(json.RawMessage(nil), raw...)
		}
	}
	for key, raw := range details {
		key = strings.TrimSpace(key)
		if key == "" || raw == nil || providerExecutedControlDetail(key, matcher) {
			continue
		}
		out[key] = append(json.RawMessage(nil), raw...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func providerExecutedControlDetail(key string, matcher providerExecutedToolMatcher) bool {
	switch key {
	case "type", "function", "enabled", "disabled":
		return true
	}
	for _, detailKey := range matcher.DetailKeys {
		if key == detailKey {
			return true
		}
	}
	return false
}

func boolProviderDetail(details map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := details[key]
	if !ok || raw == nil {
		return false, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}

func rawObjectProviderDetail(details map[string]json.RawMessage, key string) map[string]json.RawMessage {
	raw, ok := details[key]
	if !ok || raw == nil {
		return nil
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || len(value) == 0 {
		return nil
	}
	return value
}

func mergeRawMessageMaps(base map[string]json.RawMessage, next map[string]json.RawMessage) map[string]json.RawMessage {
	if len(next) == 0 {
		return base
	}
	if base == nil {
		base = map[string]json.RawMessage{}
	}
	for key, raw := range next {
		key = strings.TrimSpace(key)
		if key == "" || raw == nil {
			continue
		}
		base[key] = append(json.RawMessage(nil), raw...)
	}
	return base
}
