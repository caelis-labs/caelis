package modelprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/control/modelcatalog"
)

// BackendKind identifies the Control backend referenced by a ModelProfile.
type BackendKind string

const (
	BackendProvider BackendKind = "provider"
	BackendACP      BackendKind = "acp"
)

// ProviderBackend references one configured provider model.
type ProviderBackend struct {
	ModelConfigID string `json:"model_config_id,omitempty"`
}

// ACPBackend references one external Agent and one model advertised by it.
// SessionDefaults excludes the effort selector, which has a typed mapping in
// EffortCapability.
type ACPBackend struct {
	AgentID         string            `json:"agent_id,omitempty"`
	RemoteModelID   string            `json:"remote_model_id,omitempty"`
	SessionDefaults map[string]string `json:"session_defaults,omitempty"`
}

// Backend selects exactly one provider or ACP execution source.
type Backend struct {
	Provider *ProviderBackend `json:"provider,omitempty"`
	ACP      *ACPBackend      `json:"acp,omitempty"`
}

// EffortChoice maps one canonical Caelis effort to the exact backend wire
// value. Provider profiles normally use the canonical value as WireValue. An
// ACP profile without an effort selector has one "none" choice with an empty
// WireValue.
type EffortChoice struct {
	Canonical string `json:"canonical,omitempty"`
	WireValue string `json:"wire_value,omitempty"`
}

// EffortCapability describes the efforts selectable for one profile.
// ACPConfigID is the exact session config option ID advertised by an ACP Agent.
type EffortCapability struct {
	DefaultEffort string         `json:"default_effort,omitempty"`
	Choices       []EffortChoice `json:"choices,omitempty"`
	ACPConfigID   string         `json:"acp_config_id,omitempty"`
}

// ModelProfile is one stable product-level selectable model identity.
type ModelProfile struct {
	ID          string           `json:"id,omitempty"`
	DisplayName string           `json:"display_name,omitempty"`
	Backend     Backend          `json:"backend"`
	Effort      EffortCapability `json:"effort"`
}

// Configuration is the single Control-owned profile catalog.
type Configuration struct {
	DefaultProfileID string         `json:"default_profile_id,omitempty"`
	Profiles         []ModelProfile `json:"profiles,omitempty"`
}

// NormalizeID canonicalizes a product profile identity.
func NormalizeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// BuildProviderID returns the stable profile identity for one provider model.
func BuildProviderID(modelConfigID string) string {
	modelConfigID = strings.ToLower(strings.TrimSpace(modelConfigID))
	if modelConfigID == "" {
		return ""
	}
	return "provider:" + modelConfigID
}

// BuildACPID returns a stable profile identity for one external Agent model.
func BuildACPID(agentID, remoteModelID string) string {
	agentID = strings.ToLower(strings.TrimSpace(agentID))
	remoteModelID = strings.TrimSpace(remoteModelID)
	if agentID == "" || remoteModelID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(agentID + "\x00" + remoteModelID))
	return "acp:" + agentID + ":" + hex.EncodeToString(sum[:8])
}

// Normalize returns a canonical detached profile. Remote model identities and
// ACP wire values retain case.
func Normalize(in ModelProfile) ModelProfile {
	out := ModelProfile{
		ID:          NormalizeID(in.ID),
		DisplayName: strings.TrimSpace(in.DisplayName),
		Effort:      normalizeEffort(in.Effort),
	}
	if in.Backend.Provider != nil {
		out.Backend.Provider = &ProviderBackend{ModelConfigID: strings.ToLower(strings.TrimSpace(in.Backend.Provider.ModelConfigID))}
	}
	if in.Backend.ACP != nil {
		out.Backend.ACP = &ACPBackend{
			AgentID:         strings.ToLower(strings.TrimSpace(in.Backend.ACP.AgentID)),
			RemoteModelID:   strings.TrimSpace(in.Backend.ACP.RemoteModelID),
			SessionDefaults: placement.NormalizeSessionConfigValues(in.Backend.ACP.SessionDefaults),
		}
	}
	return out
}

// Kind reports the selected backend kind, or empty for an invalid union.
func (p ModelProfile) Kind() BackendKind {
	p = Normalize(p)
	switch {
	case p.Backend.Provider != nil && p.Backend.ACP == nil:
		return BackendProvider
	case p.Backend.ACP != nil && p.Backend.Provider == nil:
		return BackendACP
	default:
		return ""
	}
}

// SupportsEffort reports whether the profile declares one canonical effort.
func (p ModelProfile) SupportsEffort(effort string) bool {
	_, ok := p.WireEffort(effort)
	return ok
}

// WireEffort returns the exact backend value for one canonical effort. The
// empty value is valid for ACP's explicit no-selector "none" capability.
func (p ModelProfile) WireEffort(effort string) (string, bool) {
	effort = modelcatalog.NormalizeReasoningEffort(effort)
	for _, choice := range Normalize(p).Effort.Choices {
		if choice.Canonical == effort {
			return choice.WireValue, true
		}
	}
	return "", false
}

// Validate rejects an incomplete or contradictory profile.
func Validate(raw ModelProfile) error {
	if (raw.Backend.Provider == nil) == (raw.Backend.ACP == nil) {
		return fmt.Errorf("control/modelprofile: profile must select exactly one backend")
	}
	p := Normalize(raw)
	if p.ID == "" {
		return fmt.Errorf("control/modelprofile: profile ID is required")
	}
	if p.DisplayName == "" {
		return fmt.Errorf("control/modelprofile: profile %q requires a display name", p.ID)
	}
	switch p.Kind() {
	case BackendProvider:
		if p.Backend.Provider.ModelConfigID == "" {
			return fmt.Errorf("control/modelprofile: provider profile %q requires a model config ID", p.ID)
		}
		if p.Effort.ACPConfigID != "" {
			return fmt.Errorf("control/modelprofile: provider profile %q must not declare an ACP effort config ID", p.ID)
		}
	case BackendACP:
		if p.Backend.ACP.AgentID == "" || p.Backend.ACP.RemoteModelID == "" {
			return fmt.Errorf("control/modelprofile: ACP profile %q requires an Agent and remote model", p.ID)
		}
		if err := placement.ValidateSessionConfigValues(raw.Backend.ACP.SessionDefaults); err != nil {
			return fmt.Errorf("control/modelprofile: ACP profile %q: %w", p.ID, err)
		}
	}
	if err := validateEffort(p); err != nil {
		return err
	}
	return nil
}

// NormalizeConfiguration returns a detached deterministic profile catalog.
func NormalizeConfiguration(in Configuration) Configuration {
	out := Configuration{DefaultProfileID: NormalizeID(in.DefaultProfileID)}
	seen := make(map[string]struct{}, len(in.Profiles))
	for _, raw := range in.Profiles {
		profile := Normalize(raw)
		if profile.ID == "" {
			continue
		}
		if _, ok := seen[profile.ID]; ok {
			continue
		}
		seen[profile.ID] = struct{}{}
		out.Profiles = append(out.Profiles, profile)
	}
	sort.Slice(out.Profiles, func(i, j int) bool { return out.Profiles[i].ID < out.Profiles[j].ID })
	return out
}

// ValidateConfiguration validates every profile and the default reference.
func ValidateConfiguration(in Configuration) error {
	seen := make(map[string]struct{}, len(in.Profiles))
	for _, raw := range in.Profiles {
		if err := Validate(raw); err != nil {
			return err
		}
		id := NormalizeID(raw.ID)
		if _, ok := seen[id]; ok {
			return fmt.Errorf("control/modelprofile: duplicate profile %q", id)
		}
		seen[id] = struct{}{}
	}
	defaultID := NormalizeID(in.DefaultProfileID)
	if defaultID != "" {
		if _, ok := seen[defaultID]; !ok {
			return fmt.Errorf("control/modelprofile: default references unknown profile %q", defaultID)
		}
	}
	return nil
}

// Lookup returns one detached profile by ID.
func Lookup(in Configuration, id string) (ModelProfile, bool) {
	id = NormalizeID(id)
	for _, profile := range NormalizeConfiguration(in).Profiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return ModelProfile{}, false
}

// Upsert returns a validated catalog with the supplied profiles inserted or
// replaced by ID. The operation is pure and leaves current detached.
func Upsert(current Configuration, profiles ...ModelProfile) (Configuration, error) {
	next := NormalizeConfiguration(current)
	byID := make(map[string]ModelProfile, len(next.Profiles)+len(profiles))
	for _, profile := range next.Profiles {
		byID[profile.ID] = profile
	}
	for _, raw := range profiles {
		profile := Normalize(raw)
		if err := Validate(profile); err != nil {
			return Configuration{}, err
		}
		byID[profile.ID] = profile
	}
	next.Profiles = next.Profiles[:0]
	for _, profile := range byID {
		next.Profiles = append(next.Profiles, profile)
	}
	next = NormalizeConfiguration(next)
	if err := ValidateConfiguration(next); err != nil {
		return Configuration{}, err
	}
	return next, nil
}

// Remove returns a catalog without profileID. Removing the default also clears
// DefaultProfileID; binding policy is owned separately by agentbinding.
func Remove(current Configuration, profileID string) Configuration {
	profileID = NormalizeID(profileID)
	next := NormalizeConfiguration(current)
	filtered := next.Profiles[:0]
	for _, profile := range next.Profiles {
		if profile.ID != profileID {
			filtered = append(filtered, profile)
		}
	}
	next.Profiles = filtered
	if next.DefaultProfileID == profileID {
		next.DefaultProfileID = ""
	}
	return next
}

func normalizeEffort(in EffortCapability) EffortCapability {
	out := EffortCapability{
		DefaultEffort: modelcatalog.NormalizeReasoningEffort(in.DefaultEffort),
		ACPConfigID:   strings.TrimSpace(in.ACPConfigID),
	}
	for _, raw := range in.Choices {
		out.Choices = append(out.Choices, EffortChoice{
			Canonical: modelcatalog.NormalizeReasoningEffort(raw.Canonical),
			WireValue: strings.TrimSpace(raw.WireValue),
		})
	}
	sort.SliceStable(out.Choices, func(i, j int) bool {
		return modelcatalog.CompareReasoningEffort(out.Choices[i].Canonical, out.Choices[j].Canonical) < 0
	})
	return out
}

func validateEffort(p ModelProfile) error {
	if p.Effort.DefaultEffort == "" {
		return fmt.Errorf("control/modelprofile: profile %q requires a default effort", p.ID)
	}
	if len(p.Effort.Choices) == 0 {
		return fmt.Errorf("control/modelprofile: profile %q requires at least one effort choice", p.ID)
	}
	seen := make(map[string]struct{}, len(p.Effort.Choices))
	defaultFound := false
	for _, choice := range p.Effort.Choices {
		if choice.Canonical == "" {
			return fmt.Errorf("control/modelprofile: profile %q has an empty canonical effort", p.ID)
		}
		if _, ok := seen[choice.Canonical]; ok {
			return fmt.Errorf("control/modelprofile: profile %q has duplicate effort %q", p.ID, choice.Canonical)
		}
		seen[choice.Canonical] = struct{}{}
		defaultFound = defaultFound || choice.Canonical == p.Effort.DefaultEffort
	}
	if !defaultFound {
		return fmt.Errorf("control/modelprofile: profile %q default effort %q is not selectable", p.ID, p.Effort.DefaultEffort)
	}
	if p.Kind() != BackendACP {
		return nil
	}
	if p.Effort.ACPConfigID == "" {
		if len(p.Effort.Choices) != 1 || p.Effort.Choices[0].Canonical != "none" || p.Effort.Choices[0].WireValue != "" {
			return fmt.Errorf("control/modelprofile: ACP profile %q without an effort selector must expose only none", p.ID)
		}
		return nil
	}
	for _, choice := range p.Effort.Choices {
		if choice.WireValue == "" {
			return fmt.Errorf("control/modelprofile: ACP profile %q effort %q requires a wire value", p.ID, choice.Canonical)
		}
	}
	for id := range p.Backend.ACP.SessionDefaults {
		if strings.EqualFold(id, p.Effort.ACPConfigID) {
			return fmt.Errorf("control/modelprofile: ACP profile %q duplicates effort config %q in session defaults", p.ID, id)
		}
	}
	return nil
}
