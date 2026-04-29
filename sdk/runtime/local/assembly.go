package local

import (
	"strings"

	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
)

func (r *Runtime) applyAssemblySpec(state map[string]any, spec sdkruntime.AgentSpec) sdkruntime.AgentSpec {
	if len(r.assembly.Modes) == 0 && len(r.assembly.Configs) == 0 {
		return spec
	}
	spec = cloneAgentSpec(spec)
	modeID := sdkplugin.CurrentModeID(state)
	if modeID == "" {
		modeID = defaultAssemblyModeID(r.assembly)
	}
	if mode, ok := sdkplugin.LookupMode(r.assembly, modeID); ok {
		applyRuntimeOverrides(&spec, mode.Runtime)
	}
	for _, selection := range assemblyConfigSelections(r.assembly, state) {
		option, ok := sdkplugin.LookupConfigSelectOption(r.assembly, selection.ID, selection.Value)
		if !ok {
			continue
		}
		applyRuntimeOverrides(&spec, option.Runtime)
	}
	if len(spec.Metadata) == 0 {
		spec.Metadata = nil
	}
	return spec
}

type assemblyConfigSelection struct {
	ID    string
	Value string
}

func assemblyConfigSelections(assembly sdkplugin.ResolvedAssembly, state map[string]any) []assemblyConfigSelection {
	selected := sdkplugin.CurrentConfigValues(state)
	out := make([]assemblyConfigSelection, 0, len(assembly.Configs))
	for _, config := range assembly.Configs {
		configID := strings.TrimSpace(config.ID)
		if configID == "" {
			continue
		}
		value := assemblyConfigValue(config, strings.TrimSpace(selected[configID]))
		if value == "" {
			continue
		}
		out = append(out, assemblyConfigSelection{
			ID:    configID,
			Value: value,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func assemblyConfigValue(config sdkplugin.ConfigOption, selected string) string {
	if assemblyConfigHasValue(config, selected) {
		return selected
	}
	if def := strings.TrimSpace(config.DefaultValue); assemblyConfigHasValue(config, def) {
		return def
	}
	for _, option := range config.Options {
		if value := strings.TrimSpace(option.Value); value != "" {
			return value
		}
	}
	return ""
}

func assemblyConfigHasValue(config sdkplugin.ConfigOption, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, option := range config.Options {
		if strings.TrimSpace(option.Value) == value {
			return true
		}
	}
	return false
}

func defaultAssemblyModeID(assembly sdkplugin.ResolvedAssembly) string {
	for _, one := range assembly.Modes {
		if strings.EqualFold(strings.TrimSpace(one.ID), "default") {
			return strings.TrimSpace(one.ID)
		}
	}
	for _, one := range assembly.Modes {
		if trimmed := strings.TrimSpace(one.ID); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func applyRuntimeOverrides(spec *sdkruntime.AgentSpec, overrides sdkplugin.RuntimeOverrides) {
	if spec == nil {
		return
	}
	if spec.Metadata == nil {
		spec.Metadata = map[string]any{}
	}
	if trimmed := strings.TrimSpace(overrides.PolicyMode); trimmed != "" {
		spec.Metadata["policy_mode"] = trimmed
	}
	if trimmed := strings.TrimSpace(overrides.SystemPrompt); trimmed != "" {
		spec.Metadata["system_prompt"] = trimmed
	}
	if trimmed := strings.TrimSpace(overrides.Reasoning.Effort); trimmed != "" {
		spec.Metadata["reasoning_effort"] = trimmed
	}
	if overrides.Reasoning.BudgetTokens > 0 {
		spec.Metadata["reasoning_budget_tokens"] = overrides.Reasoning.BudgetTokens
	}
	if len(overrides.ExtraReadRoots) > 0 {
		spec.Metadata["policy_extra_read_roots"] = mergeStringSliceMetadata(spec.Metadata["policy_extra_read_roots"], overrides.ExtraReadRoots)
	}
	if len(overrides.ExtraWriteRoots) > 0 {
		spec.Metadata["policy_extra_write_roots"] = mergeStringSliceMetadata(spec.Metadata["policy_extra_write_roots"], overrides.ExtraWriteRoots)
	}
}

func mergeStringSliceMetadata(existing any, values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	appendOne := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	switch typed := existing.(type) {
	case []string:
		for _, one := range typed {
			appendOne(one)
		}
	case []any:
		for _, one := range typed {
			text, _ := one.(string)
			appendOne(text)
		}
	}
	for _, one := range values {
		appendOne(one)
	}
	return out
}

func cloneAgentSpec(in sdkruntime.AgentSpec) sdkruntime.AgentSpec {
	out := in
	if len(in.Metadata) > 0 {
		out.Metadata = map[string]any{}
		for key, value := range in.Metadata {
			out.Metadata[key] = value
		}
	}
	return out
}
