package plugin

import (
	"maps"
	"strings"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
)

const (
	// StateCurrentModeID is the durable session-state key for the selected app-owned mode.
	StateCurrentModeID = "plugin.current_mode_id"
	// StateCurrentConfigValues is the durable session-state key for selected app-owned config values.
	StateCurrentConfigValues = "plugin.current_config_values"
)

// AgentConfig is one pure ACP agent declaration resolved by the app layer.
// Runtime code consumes these values to build concrete registries and managers.
type AgentConfig struct {
	Name        string
	Description string
	Command     string
	Args        []string
	Env         map[string]string
	WorkDir     string
}

// SkillBundle is one pure skill-bundle declaration resolved by the app layer.
// The runtime does not discover or scan skills itself; it only consumes the
// resolved bundle metadata.
type SkillBundle struct {
	Plugin    string
	Namespace string
	Root      string
	Disabled  []string
}

// RuntimeOverrides describes pure runtime-facing effects contributed by one
// selected mode or config option.
type RuntimeOverrides struct {
	PolicyMode      string                   `json:"policy_mode,omitempty"`
	SystemPrompt    string                   `json:"system_prompt,omitempty"`
	Reasoning       sdkmodel.ReasoningConfig `json:"reasoning,omitempty"`
	ExtraReadRoots  []string                 `json:"extra_read_roots,omitempty"`
	ExtraWriteRoots []string                 `json:"extra_write_roots,omitempty"`
}

// ModeConfig is one pure app-owned session mode declaration.
type ModeConfig struct {
	ID          string
	Name        string
	Description string
	Runtime     RuntimeOverrides
}

// ConfigSelectOption is one pure app-owned config option choice.
type ConfigSelectOption struct {
	Value       string
	Name        string
	Description string
	Runtime     RuntimeOverrides
}

// ConfigOption is one pure app-owned session config declaration. Phase 3 keeps
// the shape intentionally narrow and only models select-style options.
type ConfigOption struct {
	ID           string
	Name         string
	Description  string
	Category     string
	DefaultValue string
	Options      []ConfigSelectOption
}

// ResolvedAssembly is the app-owned, pure-data assembly result consumed by the
// runtime.
type ResolvedAssembly struct {
	Agents  []AgentConfig
	Skills  []SkillBundle
	Modes   []ModeConfig
	Configs []ConfigOption
}

// CloneAgentConfig returns one detached copy of the config.
func CloneAgentConfig(in AgentConfig) AgentConfig {
	out := in
	if len(in.Args) > 0 {
		out.Args = append([]string(nil), in.Args...)
	}
	out.Env = maps.Clone(in.Env)
	return out
}

// CloneSkillBundle returns one detached copy of the bundle.
func CloneSkillBundle(in SkillBundle) SkillBundle {
	out := in
	if len(in.Disabled) > 0 {
		out.Disabled = append([]string(nil), in.Disabled...)
	}
	return out
}

// CloneModeConfig returns one detached copy of the mode config.
func CloneModeConfig(in ModeConfig) ModeConfig {
	out := in
	out.Runtime = CloneRuntimeOverrides(in.Runtime)
	return out
}

// CloneConfigSelectOption returns one detached copy of the select option.
func CloneConfigSelectOption(in ConfigSelectOption) ConfigSelectOption {
	out := in
	out.Runtime = CloneRuntimeOverrides(in.Runtime)
	return out
}

// CloneRuntimeOverrides returns one detached copy of the overrides.
func CloneRuntimeOverrides(in RuntimeOverrides) RuntimeOverrides {
	out := in
	if len(in.ExtraReadRoots) > 0 {
		out.ExtraReadRoots = append([]string(nil), in.ExtraReadRoots...)
	}
	if len(in.ExtraWriteRoots) > 0 {
		out.ExtraWriteRoots = append([]string(nil), in.ExtraWriteRoots...)
	}
	return out
}

// CloneConfigOption returns one detached copy of the config option.
func CloneConfigOption(in ConfigOption) ConfigOption {
	out := in
	if len(in.Options) > 0 {
		out.Options = make([]ConfigSelectOption, 0, len(in.Options))
		for _, one := range in.Options {
			out.Options = append(out.Options, CloneConfigSelectOption(one))
		}
	}
	return out
}

// CurrentModeID returns the selected mode id from one session state snapshot.
func CurrentModeID(state map[string]any) string {
	if state == nil {
		return ""
	}
	value, _ := state[StateCurrentModeID].(string)
	return strings.TrimSpace(value)
}

// CurrentConfigValues returns the selected config values from one session state snapshot.
func CurrentConfigValues(state map[string]any) map[string]string {
	if state == nil {
		return nil
	}
	raw, ok := state[StateCurrentConfigValues]
	if !ok {
		return nil
	}
	switch typed := raw.(type) {
	case map[string]string:
		return maps.Clone(typed)
	case map[string]any:
		out := map[string]string{}
		for key, value := range typed {
			text, _ := value.(string)
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				out[strings.TrimSpace(key)] = trimmed
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

// SetCurrentModeID returns one detached state snapshot with the selected mode updated.
func SetCurrentModeID(state map[string]any, modeID string) map[string]any {
	out := maps.Clone(state)
	if out == nil {
		out = map[string]any{}
	}
	modeID = strings.TrimSpace(modeID)
	if modeID == "" {
		delete(out, StateCurrentModeID)
		return out
	}
	out[StateCurrentModeID] = modeID
	return out
}

// SetCurrentConfigValue returns one detached state snapshot with one selected config value updated.
func SetCurrentConfigValue(state map[string]any, configID string, value string) map[string]any {
	out := maps.Clone(state)
	if out == nil {
		out = map[string]any{}
	}
	configID = strings.TrimSpace(configID)
	value = strings.TrimSpace(value)
	current := CurrentConfigValues(out)
	if current == nil {
		current = map[string]string{}
	}
	if configID == "" {
		return out
	}
	if value == "" {
		delete(current, configID)
	} else {
		current[configID] = value
	}
	if len(current) == 0 {
		delete(out, StateCurrentConfigValues)
		return out
	}
	next := map[string]any{}
	for key, one := range current {
		next[key] = one
	}
	out[StateCurrentConfigValues] = next
	return out
}

// LookupMode returns one declared mode by id.
func LookupMode(assembly ResolvedAssembly, modeID string) (ModeConfig, bool) {
	modeID = strings.TrimSpace(modeID)
	for _, one := range assembly.Modes {
		if strings.TrimSpace(one.ID) == modeID {
			return CloneModeConfig(one), true
		}
	}
	return ModeConfig{}, false
}

// LookupConfig returns one declared config by id.
func LookupConfig(assembly ResolvedAssembly, configID string) (ConfigOption, bool) {
	configID = strings.TrimSpace(configID)
	for _, one := range assembly.Configs {
		if strings.TrimSpace(one.ID) == configID {
			return CloneConfigOption(one), true
		}
	}
	return ConfigOption{}, false
}

// LookupConfigSelectOption returns one declared select option by config id and value.
func LookupConfigSelectOption(assembly ResolvedAssembly, configID string, value string) (ConfigSelectOption, bool) {
	cfg, ok := LookupConfig(assembly, configID)
	if !ok {
		return ConfigSelectOption{}, false
	}
	value = strings.TrimSpace(value)
	for _, one := range cfg.Options {
		if strings.TrimSpace(one.Value) == value {
			return CloneConfigSelectOption(one), true
		}
	}
	return ConfigSelectOption{}, false
}

// CloneResolvedAssembly returns one detached copy of the assembly.
func CloneResolvedAssembly(in ResolvedAssembly) ResolvedAssembly {
	out := ResolvedAssembly{}
	if len(in.Agents) > 0 {
		out.Agents = make([]AgentConfig, 0, len(in.Agents))
		for _, one := range in.Agents {
			out.Agents = append(out.Agents, CloneAgentConfig(one))
		}
	}
	if len(in.Skills) > 0 {
		out.Skills = make([]SkillBundle, 0, len(in.Skills))
		for _, one := range in.Skills {
			out.Skills = append(out.Skills, CloneSkillBundle(one))
		}
	}
	if len(in.Modes) > 0 {
		out.Modes = make([]ModeConfig, 0, len(in.Modes))
		for _, one := range in.Modes {
			out.Modes = append(out.Modes, CloneModeConfig(one))
		}
	}
	if len(in.Configs) > 0 {
		out.Configs = make([]ConfigOption, 0, len(in.Configs))
		for _, one := range in.Configs {
			out.Configs = append(out.Configs, CloneConfigOption(one))
		}
	}
	return out
}
