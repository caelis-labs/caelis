package agentbinding

import (
	"fmt"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// Handle is one stable orchestration or system-Agent selector.
type Handle string

const (
	HandleSelf     Handle = "self"
	HandleBreeze   Handle = "breeze"
	HandleOrbit    Handle = "orbit"
	HandleZenith   Handle = "zenith"
	HandleGuardian Handle = "guardian"
	HandleReviewer Handle = "reviewer"
	HandleSteward  Handle = "steward"
)

// Binding maps one persisted handle to exactly one profile and canonical
// effort. Self is synthesized from current Session context and is never stored.
type Binding struct {
	Handle    Handle `json:"handle,omitempty"`
	ProfileID string `json:"profile_id,omitempty"`
	Effort    string `json:"effort,omitempty"`
}

// Role defines one user-created delegation handle. Custom roles deliberately
// carry only the stable handle and the description presented to Agents and
// users; their display name is the handle itself.
type Role struct {
	Handle      Handle `json:"handle,omitempty"`
	Description string `json:"description,omitempty"`
}

// BindingSet stores one named snapshot of explicit handle bindings. Snapshot
// entries are allowed to become unavailable when a referenced role or
// ModelProfile is later removed; applying the set validates it atomically
// against current configuration.
type BindingSet struct {
	Name     string    `json:"name,omitempty"`
	Bindings []Binding `json:"bindings,omitempty"`
}

// Configuration is the single persisted role, handle-binding, and binding-set
// document. Roles and Sets are optional additive fields so schema-v2 documents
// written before their introduction remain valid without migration.
type Configuration struct {
	Roles    []Role       `json:"roles,omitempty"`
	Bindings []Binding    `json:"bindings,omitempty"`
	Sets     []BindingSet `json:"sets,omitempty"`
}

// UnsupportedBackendError reports a handle bound to an execution backend that
// its fixed scene cannot safely execute.
type UnsupportedBackendError struct {
	Handle    Handle
	ProfileID string
	Backend   modelprofile.BackendKind
}

// ProfileInUseError reports a system binding that must be explicitly rebound
// or reset before its profile may be deleted.
type ProfileInUseError struct {
	ProfileID string
	Handle    Handle
}

func (e *ProfileInUseError) Error() string {
	if e == nil {
		return "control/agentbinding: profile is still in use"
	}
	return fmt.Sprintf(
		"control/agentbinding: profile %q is bound to system Agent %q; rebind or reset it before deletion",
		e.ProfileID,
		e.Handle,
	)
}

func (e *UnsupportedBackendError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("control/agentbinding: handle %q cannot use %s profile %q", e.Handle, e.Backend, e.ProfileID)
}

// NormalizeHandle canonicalizes one fixed or custom handle.
func NormalizeHandle(handle Handle) Handle {
	return Handle(strings.ToLower(strings.TrimSpace(string(handle))))
}

// Normalize returns one canonical binding.
func Normalize(in Binding) Binding {
	return Binding{
		Handle:    NormalizeHandle(in.Handle),
		ProfileID: modelprofile.NormalizeID(in.ProfileID),
		Effort:    modelcatalog.NormalizeReasoningEffort(in.Effort),
	}
}

// NormalizeRole returns one canonical custom role.
func NormalizeRole(in Role) Role {
	return Role{
		Handle:      NormalizeHandle(in.Handle),
		Description: strings.TrimSpace(in.Description),
	}
}

func normalizedRoles(roles []Role) []Role {
	var out []Role
	seen := make(map[Handle]struct{}, len(roles))
	for _, raw := range roles {
		role := NormalizeRole(raw)
		if role.Handle == "" || role.Description == "" {
			continue
		}
		if _, ok := seen[role.Handle]; ok {
			continue
		}
		seen[role.Handle] = struct{}{}
		out = append(out, role)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Handle < out[j].Handle
	})
	return out
}

// NormalizeBindingSet returns one detached canonical binding snapshot.
func NormalizeBindingSet(in BindingSet) BindingSet {
	out := BindingSet{Name: NormalizeSetName(in.Name)}
	seen := make(map[Handle]struct{}, len(in.Bindings))
	for _, raw := range in.Bindings {
		binding := Normalize(raw)
		if binding.Handle == "" || binding.ProfileID == "" || binding.Effort == "" {
			continue
		}
		if _, ok := seen[binding.Handle]; ok {
			continue
		}
		seen[binding.Handle] = struct{}{}
		out.Bindings = append(out.Bindings, binding)
	}
	sortBindings(out.Bindings, Configuration{})
	return out
}

// NormalizeConfiguration returns one detached deterministic configuration.
func NormalizeConfiguration(in Configuration) Configuration {
	out := Configuration{Roles: normalizedRoles(in.Roles)}

	seen := make(map[Handle]struct{}, len(in.Bindings))
	for _, raw := range in.Bindings {
		binding := Normalize(raw)
		if binding.Handle == "" || binding.ProfileID == "" {
			continue
		}
		if _, ok := seen[binding.Handle]; ok {
			continue
		}
		seen[binding.Handle] = struct{}{}
		out.Bindings = append(out.Bindings, binding)
	}
	sortBindings(out.Bindings, out)

	seenSets := make(map[string]struct{}, len(in.Sets))
	for _, raw := range in.Sets {
		set := NormalizeBindingSet(raw)
		if set.Name == "" {
			continue
		}
		if _, ok := seenSets[set.Name]; ok {
			continue
		}
		seenSets[set.Name] = struct{}{}
		sortBindings(set.Bindings, out)
		out.Sets = append(out.Sets, set)
	}
	sort.Slice(out.Sets, func(i, j int) bool {
		return out.Sets[i].Name < out.Sets[j].Name
	})
	return out
}

// ValidateConfiguration validates all handle/profile/effort references.
func ValidateConfiguration(in Configuration, profiles modelprofile.Configuration) error {
	if err := modelprofile.ValidateConfiguration(profiles); err != nil {
		return fmt.Errorf("control/agentbinding: invalid model profiles: %w", err)
	}
	if err := ValidateRoles(in.Roles); err != nil {
		return err
	}
	seen := make(map[Handle]struct{}, len(in.Bindings))
	for _, raw := range in.Bindings {
		binding := Normalize(raw)
		if !isPersistedHandleIn(in, binding.Handle) {
			if binding.Handle == HandleSelf {
				return fmt.Errorf("control/agentbinding: self is Session-derived and cannot be persisted")
			}
			return fmt.Errorf("control/agentbinding: unknown handle %q", strings.TrimSpace(string(raw.Handle)))
		}
		if _, ok := seen[binding.Handle]; ok {
			return fmt.Errorf("control/agentbinding: duplicate binding for handle %q", binding.Handle)
		}
		seen[binding.Handle] = struct{}{}
		profile, ok := modelprofile.Lookup(profiles, binding.ProfileID)
		if !ok {
			return fmt.Errorf("control/agentbinding: handle %q references unknown profile %q", binding.Handle, binding.ProfileID)
		}
		if binding.Effort == "" {
			return fmt.Errorf("control/agentbinding: handle %q requires an explicit effort", binding.Handle)
		}
		if !profile.SupportsEffort(binding.Effort) {
			return fmt.Errorf("control/agentbinding: effort %q is not supported by profile %q", binding.Effort, profile.ID)
		}
		if !SupportsProfile(binding.Handle, profile) {
			return &UnsupportedBackendError{Handle: binding.Handle, ProfileID: profile.ID, Backend: profile.Kind()}
		}
	}
	if err := ValidateBindingSets(in.Sets); err != nil {
		return err
	}
	return nil
}

// Bind validates and replaces one persisted handle binding. Failure returns no
// candidate configuration, preventing callers from accidentally saving a
// partially normalized mutation.
func Bind(current Configuration, raw Binding, profiles modelprofile.Configuration) (Configuration, error) {
	binding := Normalize(raw)
	if !isPersistedHandleIn(current, binding.Handle) {
		if binding.Handle == HandleSelf {
			return Configuration{}, fmt.Errorf("control/agentbinding: self is Session-derived and cannot be bound")
		}
		return Configuration{}, fmt.Errorf("control/agentbinding: unknown handle %q", binding.Handle)
	}
	next := Configuration{}
	for _, rawExisting := range current.Bindings {
		existing := Normalize(rawExisting)
		if existing.Handle != binding.Handle {
			next.Bindings = append(next.Bindings, existing)
		}
	}
	next.Bindings = append(next.Bindings, binding)
	next.Roles = append(next.Roles, current.Roles...)
	next.Sets = append(next.Sets, current.Sets...)
	if err := ValidateConfiguration(next, profiles); err != nil {
		return Configuration{}, err
	}
	return NormalizeConfiguration(next), nil
}

// Reset removes one explicit binding.
func Reset(current Configuration, handle Handle) (Configuration, error) {
	handle = NormalizeHandle(handle)
	if !isPersistedHandleIn(current, handle) {
		return Configuration{}, fmt.Errorf("control/agentbinding: handle %q cannot be reset", handle)
	}
	next := Configuration{
		Roles: append([]Role(nil), current.Roles...),
		Sets:  append([]BindingSet(nil), current.Sets...),
	}
	for _, binding := range NormalizeConfiguration(current).Bindings {
		if binding.Handle != handle {
			next.Bindings = append(next.Bindings, binding)
		}
	}
	return next, nil
}

// Lookup returns one explicit binding.
func Lookup(in Configuration, handle Handle) (Binding, bool) {
	handle = NormalizeHandle(handle)
	for _, binding := range NormalizeConfiguration(in).Bindings {
		if binding.Handle == handle {
			return binding, true
		}
	}
	return Binding{}, false
}

// RemoveProfileBindings atomically derives the bindings that remain after one
// profile is removed and reports the affected handles.
func RemoveProfileBindings(in Configuration, profileID string) (Configuration, []Handle) {
	profileID = modelprofile.NormalizeID(profileID)
	next := Configuration{
		Roles: append([]Role(nil), in.Roles...),
		Sets:  append([]BindingSet(nil), in.Sets...),
	}
	var removed []Handle
	for _, binding := range NormalizeConfiguration(in).Bindings {
		if binding.ProfileID == profileID {
			removed = append(removed, binding.Handle)
			continue
		}
		next.Bindings = append(next.Bindings, binding)
	}
	return next, removed
}

// PrepareProfileRemoval removes ordinary bindings that reference profileID and
// rejects deletion while a system Agent still depends on that profile.
func PrepareProfileRemoval(in Configuration, profileID string) (Configuration, error) {
	profileID = modelprofile.NormalizeID(profileID)
	next, removed := RemoveProfileBindings(in, profileID)
	for _, handle := range removed {
		if IsSystem(handle) {
			return Configuration{}, &ProfileInUseError{ProfileID: profileID, Handle: handle}
		}
	}
	return next, nil
}

func sortBindings(bindings []Binding, configuration Configuration) {
	sort.Slice(bindings, func(i, j int) bool {
		left, right := orderIn(configuration, bindings[i].Handle), orderIn(configuration, bindings[j].Handle)
		if left != right {
			return left < right
		}
		return bindings[i].Handle < bindings[j].Handle
	})
}
