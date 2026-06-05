// Package agentprofile defines editable agent identities and runtime bindings.
package agentprofile

import (
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultAgentsDirName = "agents"
	DefaultSelfTarget    = "self"
)

type BindingTargetKind string

const (
	BindingTargetSelf    BindingTargetKind = "self"
	BindingTargetBuiltIn BindingTargetKind = "built_in"
	BindingTargetACP     BindingTargetKind = "acp"
)

type BindingStatus string

const (
	BindingStatusOK      BindingStatus = "ok"
	BindingStatusWarning BindingStatus = "warning"
	BindingStatusStale   BindingStatus = "stale"
)

type Profile struct {
	ID           string         `json:"id,omitempty"`
	Name         string         `json:"name,omitempty"`
	Description  string         `json:"description,omitempty"`
	Capabilities []string       `json:"capabilities,omitempty"`
	Instructions string         `json:"instructions,omitempty"`
	Path         string         `json:"path,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type BindingSet struct {
	Bindings []Binding `json:"bindings,omitempty"`
}

type Binding struct {
	ProfileID       string            `json:"profile_id,omitempty"`
	Enabled         *bool             `json:"enabled,omitempty"`
	Target          BindingTargetKind `json:"target,omitempty"`
	Model           string            `json:"model,omitempty"`
	ACPAgent        string            `json:"acp_agent,omitempty"`
	ACPModel        string            `json:"acp_model,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	Status          BindingStatus     `json:"status,omitempty"`
	Warning         string            `json:"warning,omitempty"`
	UpdatedAt       time.Time         `json:"updated_at,omitempty"`
	ValidatedAt     time.Time         `json:"validated_at,omitempty"`
}

type Snapshot struct {
	Profile Profile `json:"profile,omitempty"`
	Binding Binding `json:"binding,omitempty"`
}

func NormalizeProfile(in Profile) Profile {
	out := Profile{
		ID:           normalizeID(in.ID),
		Name:         strings.TrimSpace(in.Name),
		Description:  strings.TrimSpace(in.Description),
		Capabilities: normalizeStringList(in.Capabilities),
		Instructions: strings.TrimSpace(in.Instructions),
		Path:         strings.TrimSpace(in.Path),
		Metadata:     maps.Clone(in.Metadata),
	}
	if out.ID == "" {
		out.ID = profileIDFromPath(out.Path)
	}
	if out.Name == "" {
		out.Name = out.ID
	}
	return out
}

func ValidateProfile(in Profile) error {
	profile := NormalizeProfile(in)
	if profile.ID == "" {
		return fmt.Errorf("ports/agentprofile: profile id is required")
	}
	if profile.Instructions == "" && profile.Description == "" {
		return fmt.Errorf("ports/agentprofile: profile %q requires instructions or description", profile.ID)
	}
	return nil
}

func NormalizeBindingSet(in BindingSet) BindingSet {
	out := BindingSet{}
	seen := map[string]struct{}{}
	for _, binding := range in.Bindings {
		normalized := NormalizeBinding(binding)
		if normalized.ProfileID == "" {
			continue
		}
		if _, ok := seen[normalized.ProfileID]; ok {
			continue
		}
		seen[normalized.ProfileID] = struct{}{}
		out.Bindings = append(out.Bindings, normalized)
	}
	return out
}

func NormalizeBinding(in Binding) Binding {
	target := NormalizeBindingTarget(in.Target)
	out := Binding{
		ProfileID:       normalizeID(in.ProfileID),
		Enabled:         cloneBoolPtr(in.Enabled),
		Target:          target,
		Model:           strings.TrimSpace(in.Model),
		ACPAgent:        strings.TrimSpace(in.ACPAgent),
		ACPModel:        strings.TrimSpace(in.ACPModel),
		ReasoningEffort: strings.ToLower(strings.TrimSpace(in.ReasoningEffort)),
		Status:          NormalizeBindingStatus(in.Status),
		Warning:         strings.TrimSpace(in.Warning),
		UpdatedAt:       in.UpdatedAt,
		ValidatedAt:     in.ValidatedAt,
	}
	if out.Target == "" {
		out.Target = BindingTargetSelf
	}
	if out.Status == "" {
		out.Status = BindingStatusOK
	}
	if out.Target != BindingTargetBuiltIn {
		out.Model = ""
	}
	if out.Target != BindingTargetACP {
		out.ACPAgent = ""
		out.ACPModel = ""
	}
	if out.Target == BindingTargetSelf {
		out.ReasoningEffort = ""
	}
	return out
}

func ValidateBinding(in Binding) error {
	binding := NormalizeBinding(in)
	if binding.ProfileID == "" {
		return fmt.Errorf("ports/agentprofile: binding profile id is required")
	}
	switch binding.Target {
	case BindingTargetSelf:
		return nil
	case BindingTargetBuiltIn:
		return nil
	case BindingTargetACP:
		if binding.ACPAgent == "" {
			return fmt.Errorf("ports/agentprofile: binding %q acp agent is required", binding.ProfileID)
		}
	default:
		return fmt.Errorf("ports/agentprofile: binding %q unsupported target %q", binding.ProfileID, binding.Target)
	}
	return nil
}

func LookupBinding(set BindingSet, profileID string) (Binding, bool) {
	profileID = normalizeID(profileID)
	for _, binding := range NormalizeBindingSet(set).Bindings {
		if binding.ProfileID == profileID {
			return binding, true
		}
	}
	return Binding{}, false
}

func UpsertBinding(set BindingSet, binding Binding, now time.Time) (BindingSet, error) {
	binding = NormalizeBinding(binding)
	if err := ValidateBinding(binding); err != nil {
		return BindingSet{}, err
	}
	if binding.UpdatedAt.IsZero() {
		binding.UpdatedAt = now
	}
	next := NormalizeBindingSet(set)
	for i := range next.Bindings {
		if next.Bindings[i].ProfileID == binding.ProfileID {
			next.Bindings[i] = binding
			return NormalizeBindingSet(next), nil
		}
	}
	next.Bindings = append(next.Bindings, binding)
	return NormalizeBindingSet(next), nil
}

func RemoveBinding(set BindingSet, profileID string) BindingSet {
	profileID = normalizeID(profileID)
	next := BindingSet{}
	for _, binding := range NormalizeBindingSet(set).Bindings {
		if binding.ProfileID == profileID {
			continue
		}
		next.Bindings = append(next.Bindings, binding)
	}
	return next
}

func NormalizeBindingTarget(target BindingTargetKind) BindingTargetKind {
	switch strings.ToLower(strings.TrimSpace(string(target))) {
	case "", string(BindingTargetSelf), "local", "default":
		return BindingTargetSelf
	case string(BindingTargetBuiltIn), "builtin", "built-in", "model":
		return BindingTargetBuiltIn
	case string(BindingTargetACP):
		return BindingTargetACP
	default:
		return BindingTargetKind(strings.ToLower(strings.TrimSpace(string(target))))
	}
}

func NormalizeBindingStatus(status BindingStatus) BindingStatus {
	switch strings.ToLower(strings.TrimSpace(string(status))) {
	case "":
		return ""
	case string(BindingStatusOK):
		return BindingStatusOK
	case string(BindingStatusWarning), "warn":
		return BindingStatusWarning
	case string(BindingStatusStale):
		return BindingStatusStale
	default:
		return BindingStatus(strings.ToLower(strings.TrimSpace(string(status))))
	}
}

func normalizeID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !ok {
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
			continue
		}
		b.WriteRune(r)
		lastDash = false
	}
	return strings.Trim(b.String(), "-")
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func profileIDFromPath(path string) string {
	base := strings.TrimSuffix(filepath.Base(strings.TrimSpace(path)), filepath.Ext(strings.TrimSpace(path)))
	return normalizeID(base)
}

func cloneBoolPtr(in *bool) *bool {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
