// Package delegation owns Caelis delegation profiles and their bindings to the
// Control Agent roster.
package delegation

import (
	"context"
	"fmt"
	"sort"
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

// Profile is one stable delegation capability level understood by Control and
// model-facing orchestration. Self always follows the current Session
// controller; the other profiles may be bound to an Agent from the one
// Control-owned roster.
type Profile string

const (
	// ProfileSelf uses the current Session controller model and effort.
	ProfileSelf Profile = "self"
	// ProfileBreeze is intended for fast, bounded work.
	ProfileBreeze Profile = "breeze"
	// ProfileOrbit is intended for general implementation and review.
	ProfileOrbit Profile = "orbit"
	// ProfileZenith is intended for difficult, high-risk, or architectural work.
	ProfileZenith Profile = "zenith"
)

const directRunSourcePrefix = "slash_profile_"

// Definition describes one fixed Caelis delegation profile.
type Definition struct {
	Profile      Profile
	Name         string
	Description  string
	Configurable bool
}

var definitions = []Definition{
	{
		Profile:     ProfileSelf,
		Name:        "Session Default",
		Description: "Use the current Session controller model and reasoning effort.",
	},
	{
		Profile:      ProfileBreeze,
		Name:         "Caelis Breeze",
		Description:  "Fast, bounded work such as lookup, focused edits, and quick checks.",
		Configurable: true,
	},
	{
		Profile:      ProfileOrbit,
		Name:         "Caelis Orbit",
		Description:  "General implementation, review, and multi-file analysis.",
		Configurable: true,
	},
	{
		Profile:      ProfileZenith,
		Name:         "Caelis Zenith",
		Description:  "Deep architecture, difficult debugging, and high-risk analysis.",
		Configurable: true,
	},
}

// Definitions returns the fixed profiles in their presentation and routing
// order. The returned slice is detached from package state.
func Definitions() []Definition {
	return append([]Definition(nil), definitions...)
}

// DirectRunProfiles returns the user-addressable fixed profile names. Self is
// model-facing only and is not exposed as a direct slash command.
func DirectRunProfiles() []Profile {
	out := make([]Profile, 0, len(definitions)-1)
	for _, definition := range definitions {
		if definition.Configurable {
			out = append(out, definition.Profile)
		}
	}
	return out
}

// BoundProfiles returns explicitly Agent-bound configurable profiles in fixed
// presentation order. The fixed self profile is intentionally excluded.
func BoundProfiles(in Configuration) []Profile {
	normalized := NormalizeConfiguration(in)
	bound := make(map[Profile]struct{}, len(normalized.Bindings))
	for _, binding := range normalized.Bindings {
		if binding.Target == TargetAgent && strings.TrimSpace(binding.AgentID) != "" {
			bound[binding.Profile] = struct{}{}
		}
	}
	out := make([]Profile, 0, len(bound))
	for _, definition := range definitions {
		if !definition.Configurable {
			continue
		}
		if _, ok := bound[definition.Profile]; ok {
			out = append(out, definition.Profile)
		}
	}
	return out
}

// IsDirectRunProfile reports whether a name is a user-addressable profile.
func IsDirectRunProfile(name string) bool {
	profile := NormalizeProfile(Profile(name))
	for _, candidate := range DirectRunProfiles() {
		if candidate == profile {
			return true
		}
	}
	return false
}

// DirectRunSource returns the durable participant source for one profile run.
func DirectRunSource(profile Profile) string {
	profile = NormalizeProfile(profile)
	if !IsDirectRunProfile(string(profile)) {
		return ""
	}
	return directRunSourcePrefix + string(profile)
}

// DirectRunProfileFromSource recovers a fixed profile from its typed Control
// participant source. Raw roster Agent names are intentionally not accepted.
func DirectRunProfileFromSource(source string) (Profile, bool) {
	profile := NormalizeProfile(Profile(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(source)), directRunSourcePrefix)))
	return profile, strings.HasPrefix(strings.ToLower(strings.TrimSpace(source)), directRunSourcePrefix) && IsDirectRunProfile(string(profile))
}

// DirectRunFromParticipant projects one attached profile participant into its
// stable <profile>(<handle>) address. Only ACP sidecars started through a fixed
// profile are addressable.
func DirectRunFromParticipant(label, kind, role, source string) controlagents.Run {
	profile, ok := DirectRunProfileFromSource(source)
	return controlagents.Run{
		Name:        controlagents.FormatRunName(string(profile), label),
		Agent:       string(profile),
		Addressable: ok && strings.EqualFold(strings.TrimSpace(kind), "acp") && strings.EqualFold(strings.TrimSpace(role), "sidecar"),
	}
}

// TargetKind identifies whether a profile has no explicit roster binding or
// resolves through the Control Agent roster.
type TargetKind string

const (
	// TargetSelf is the implicit unbound state for configurable profiles. The
	// fixed self profile still follows the current Session controller.
	TargetSelf TargetKind = "self"
	// TargetAgent resolves one stable Agent ID from the Control roster.
	TargetAgent TargetKind = "agent"
)

// Binding maps one configurable profile to an execution target. TargetSelf is
// the implicit unbound state and is omitted from normalized persistence.
// ReasoningEffort is an optional execution override and is valid only for a
// model-backed Agent.
type Binding struct {
	Profile         Profile    `json:"profile,omitempty"`
	Target          TargetKind `json:"target,omitempty"`
	AgentID         string     `json:"agent_id,omitempty"`
	ReasoningEffort string     `json:"reasoning_effort,omitempty"`
}

// Configuration is the persisted Control-owned delegation configuration. It
// stores only profile bindings and never duplicates the Agent roster.
type Configuration struct {
	Bindings []Binding `json:"bindings,omitempty"`
}

// Resolution is one effective profile target. Agent is populated only when
// Target is TargetAgent.
type Resolution struct {
	Binding Binding
	Agent   controlagents.Agent
}

// ProfileStatus is one effective profile binding projected for configuration
// surfaces. It is a detached Control view, not a second Agent catalog.
type ProfileStatus struct {
	Definition Definition
	Binding    Binding
	Agent      controlagents.Agent
}

// IsProfileBound reports whether a projected configurable profile has an
// explicit roster Agent binding and can be advertised for direct execution.
func IsProfileBound(status ProfileStatus) bool {
	profile := status.Definition.Profile
	if profile == "" {
		profile = status.Binding.Profile
	}
	return IsDirectRunProfile(string(profile)) &&
		status.Binding.Target == TargetAgent &&
		strings.TrimSpace(status.Binding.AgentID) != ""
}

// TargetStatus describes one roster Agent that may back a configurable
// profile. ReasoningLevels is populated only for model-backed Agents.
type TargetStatus struct {
	Agent           controlagents.Agent
	ReasoningLevels []string
}

// Status is the complete delegation configuration view used by presentation
// adapters.
type Status struct {
	Profiles []ProfileStatus
	Targets  []TargetStatus
}

// BindRequest binds one fixed configurable profile to an existing roster
// Agent. ReasoningEffort is optional and model-backed only.
type BindRequest struct {
	Profile         Profile
	AgentID         string
	ReasoningEffort string
}

// Service is the narrow Control-owned delegation configuration capability.
// Surfaces render this state and submit explicit mutations; they do not own
// binding validation or persistence.
type Service interface {
	DelegationStatus(context.Context) (Status, error)
	BindDelegation(context.Context, BindRequest) (Status, error)
	ResetDelegation(context.Context, Profile) (Status, error)
}

// NormalizeProfile canonicalizes one profile identifier.
func NormalizeProfile(profile Profile) Profile {
	return Profile(strings.ToLower(strings.TrimSpace(string(profile))))
}

// NormalizeBinding returns a canonical detached binding.
func NormalizeBinding(binding Binding) Binding {
	binding.Profile = NormalizeProfile(binding.Profile)
	binding.Target = TargetKind(strings.ToLower(strings.TrimSpace(string(binding.Target))))
	binding.AgentID = controlagents.NormalizeName(binding.AgentID)
	binding.ReasoningEffort = modelcatalog.NormalizeReasoningEffort(binding.ReasoningEffort)
	return binding
}

// NormalizeConfiguration returns a detached, deterministic configuration.
// Explicit TargetSelf entries are omitted because unbound is the implicit
// configurable-profile state.
func NormalizeConfiguration(in Configuration) Configuration {
	out := Configuration{}
	seen := make(map[Profile]struct{}, len(in.Bindings))
	for _, raw := range in.Bindings {
		binding := NormalizeBinding(raw)
		if binding.Target == TargetSelf {
			continue
		}
		if _, ok := seen[binding.Profile]; ok {
			continue
		}
		seen[binding.Profile] = struct{}{}
		out.Bindings = append(out.Bindings, binding)
	}
	sort.Slice(out.Bindings, func(i, j int) bool {
		left := profileOrder(out.Bindings[i].Profile)
		right := profileOrder(out.Bindings[j].Profile)
		if left != right {
			return left < right
		}
		return out.Bindings[i].Profile < out.Bindings[j].Profile
	})
	return out
}

// ListBindings returns every effective profile binding in fixed order. Missing
// configurable bindings are represented explicitly as the unbound TargetSelf
// sentinel.
func ListBindings(in Configuration) []Binding {
	normalized := NormalizeConfiguration(in)
	configured := make(map[Profile]Binding, len(normalized.Bindings))
	for _, binding := range normalized.Bindings {
		configured[binding.Profile] = binding
	}
	out := make([]Binding, 0, len(definitions))
	for _, definition := range definitions {
		binding, ok := configured[definition.Profile]
		if !ok {
			binding = Binding{Profile: definition.Profile, Target: TargetSelf}
		}
		out = append(out, binding)
	}
	return out
}

// LookupBinding returns one effective binding. Unknown profiles are rejected.
func LookupBinding(in Configuration, profile Profile) (Binding, bool) {
	profile = NormalizeProfile(profile)
	if !isKnownProfile(profile) {
		return Binding{}, false
	}
	for _, binding := range ListBindings(in) {
		if binding.Profile == profile {
			return binding, true
		}
	}
	return Binding{}, false
}

// ValidateConfiguration validates fixed profile names, roster references, and
// model-backed effort overrides. A bound Agent that disappears is an error;
// validation never silently changes it to self.
func ValidateConfiguration(in Configuration, roster controlagents.Configuration, models []modelconfig.Config) error {
	if err := controlagents.ValidateConfiguration(roster); err != nil {
		return fmt.Errorf("control/delegation: invalid Agent roster: %w", err)
	}
	seen := make(map[Profile]struct{}, len(in.Bindings))
	configuredModels := configuredModelsByID(models)
	for _, raw := range in.Bindings {
		binding := NormalizeBinding(raw)
		if !isConfigurableProfile(binding.Profile) {
			if binding.Profile == ProfileSelf {
				return fmt.Errorf("control/delegation: self is fixed and cannot be configured")
			}
			return fmt.Errorf("control/delegation: unknown profile %q", strings.TrimSpace(string(raw.Profile)))
		}
		if _, exists := seen[binding.Profile]; exists {
			return fmt.Errorf("control/delegation: duplicate binding for profile %q", binding.Profile)
		}
		seen[binding.Profile] = struct{}{}

		switch binding.Target {
		case TargetSelf:
			if binding.AgentID != "" || binding.ReasoningEffort != "" {
				return fmt.Errorf("control/delegation: unbound profile %q cannot declare an Agent or reasoning effort", binding.Profile)
			}
		case TargetAgent:
			if binding.AgentID == "" {
				return fmt.Errorf("control/delegation: Agent binding for profile %q requires an Agent ID", binding.Profile)
			}
			agent, ok := controlagents.LookupAgent(roster, binding.AgentID)
			if !ok {
				return fmt.Errorf("control/delegation: profile %q references unknown Agent %q", binding.Profile, binding.AgentID)
			}
			if err := validateReasoningEffort(binding, agent, configuredModels); err != nil {
				return err
			}
		default:
			return fmt.Errorf("control/delegation: profile %q has unsupported target %q", binding.Profile, binding.Target)
		}
	}
	return nil
}

// BindAgent binds one configurable profile to an existing roster Agent. The
// optional reasoning effort is accepted only for a model-backed Agent.
func BindAgent(
	current Configuration,
	profile Profile,
	agentID string,
	reasoningEffort string,
	roster controlagents.Configuration,
	models []modelconfig.Config,
) (Configuration, error) {
	profile = NormalizeProfile(profile)
	if !isConfigurableProfile(profile) {
		if profile == ProfileSelf {
			return Configuration{}, fmt.Errorf("control/delegation: self is fixed and cannot be configured")
		}
		return Configuration{}, fmt.Errorf("control/delegation: unknown profile %q", profile)
	}
	binding := NormalizeBinding(Binding{
		Profile:         profile,
		Target:          TargetAgent,
		AgentID:         agentID,
		ReasoningEffort: reasoningEffort,
	})
	next := Configuration{}
	for _, existing := range current.Bindings {
		if NormalizeProfile(existing.Profile) != profile {
			next.Bindings = append(next.Bindings, existing)
		}
	}
	next.Bindings = append(next.Bindings, binding)
	if err := ValidateConfiguration(next, roster, models); err != nil {
		return Configuration{}, err
	}
	return NormalizeConfiguration(next), nil
}

// Reset restores one configurable profile to its implicit unbound state. Reset
// deliberately does not require the current target to remain available, so it
// can repair a stale binding.
func Reset(current Configuration, profile Profile) (Configuration, error) {
	profile = NormalizeProfile(profile)
	if !isConfigurableProfile(profile) {
		if profile == ProfileSelf {
			return Configuration{}, fmt.Errorf("control/delegation: self is fixed and cannot be reset")
		}
		return Configuration{}, fmt.Errorf("control/delegation: unknown profile %q", profile)
	}
	next := Configuration{}
	for _, binding := range NormalizeConfiguration(current).Bindings {
		if binding.Profile != profile {
			next.Bindings = append(next.Bindings, binding)
		}
	}
	return NormalizeConfiguration(next), nil
}

// ResetAgentBindings restores every profile backed by agentID to its implicit
// unbound state. Roster deletion uses this before persisting the smaller roster,
// so removing a model or external ACP Agent cannot leave stale delegation
// references behind.
func ResetAgentBindings(current Configuration, agentID string) (Configuration, []Profile) {
	agentID = controlagents.NormalizeName(agentID)
	normalized := NormalizeConfiguration(current)
	if agentID == "" {
		return normalized, nil
	}
	next := Configuration{Bindings: make([]Binding, 0, len(normalized.Bindings))}
	reset := make([]Profile, 0, len(normalized.Bindings))
	for _, binding := range normalized.Bindings {
		if binding.Target == TargetAgent && binding.AgentID == agentID {
			reset = append(reset, binding.Profile)
			continue
		}
		next.Bindings = append(next.Bindings, binding)
	}
	return NormalizeConfiguration(next), reset
}

// Resolve returns one effective profile binding and its roster Agent when the
// target is Agent-backed.
func Resolve(
	configuration Configuration,
	profile Profile,
	roster controlagents.Configuration,
	models []modelconfig.Config,
) (Resolution, error) {
	if err := ValidateConfiguration(configuration, roster, models); err != nil {
		return Resolution{}, err
	}
	binding, ok := LookupBinding(configuration, profile)
	if !ok {
		return Resolution{}, fmt.Errorf("control/delegation: unknown profile %q", strings.TrimSpace(string(profile)))
	}
	resolution := Resolution{Binding: binding}
	if binding.Target == TargetSelf {
		return resolution, nil
	}
	agent, ok := controlagents.LookupAgent(roster, binding.AgentID)
	if !ok {
		return Resolution{}, fmt.Errorf("control/delegation: profile %q references unknown Agent %q", binding.Profile, binding.AgentID)
	}
	resolution.Agent = agent
	return resolution, nil
}

func validateReasoningEffort(binding Binding, agent controlagents.Agent, models map[string]modelconfig.Config) error {
	if agent.Backing.ModelAlias == "" {
		if binding.ReasoningEffort != "" {
			return fmt.Errorf("control/delegation: profile %q cannot override reasoning effort for external ACP Agent %q", binding.Profile, agent.ID)
		}
		return nil
	}
	modelID := strings.ToLower(strings.TrimSpace(agent.Backing.ModelAlias))
	configured, ok := models[modelID]
	if !ok {
		return fmt.Errorf("control/delegation: model-backed Agent %q references unknown configured model %q", agent.ID, agent.Backing.ModelAlias)
	}
	if binding.ReasoningEffort == "" {
		return nil
	}
	if !modelconfig.SupportsReasoningEffort(configured, binding.ReasoningEffort) {
		return fmt.Errorf(
			"control/delegation: reasoning effort %q is not supported by model-backed Agent %q",
			binding.ReasoningEffort,
			agent.ID,
		)
	}
	return nil
}

func configuredModelsByID(models []modelconfig.Config) map[string]modelconfig.Config {
	out := make(map[string]modelconfig.Config, len(models))
	for _, raw := range models {
		configured := modelconfig.NormalizeConfig(raw)
		if configured.ID == "" {
			continue
		}
		out[configured.ID] = configured
	}
	return out
}

func isKnownProfile(profile Profile) bool {
	for _, definition := range definitions {
		if definition.Profile == profile {
			return true
		}
	}
	return false
}

func isConfigurableProfile(profile Profile) bool {
	for _, definition := range definitions {
		if definition.Profile == profile {
			return definition.Configurable
		}
	}
	return false
}

func profileOrder(profile Profile) int {
	for i, definition := range definitions {
		if definition.Profile == profile {
			return i
		}
	}
	return len(definitions)
}
