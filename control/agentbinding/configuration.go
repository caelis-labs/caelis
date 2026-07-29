package agentbinding

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/caelis-labs/caelis/control/modelprofile"
)

const (
	maxCustomHandleLength = 32
	maxBindingSetName     = 32
)

var (
	customHandlePattern   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	bindingSetPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	reservedCustomHandles = map[Handle]struct{}{
		"compact":  {},
		"connect":  {},
		"doctor":   {},
		"exit":     {},
		"help":     {},
		"lead":     {},
		"model":    {},
		"new":      {},
		"plugin":   {},
		"quit":     {},
		"resume":   {},
		"review":   {},
		"sandbox":  {},
		"status":   {},
		"subagent": {},
	}
)

// NormalizeSetName canonicalizes one binding-set identifier.
func NormalizeSetName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ValidateCustomHandle validates the stable identifier of one user-created
// delegation role. Fixed handles remain reserved even when they are not
// configurable.
func ValidateCustomHandle(raw Handle) error {
	handle := NormalizeHandle(raw)
	if handle == "" {
		return fmt.Errorf("control/agentbinding: custom role handle is required")
	}
	if len(handle) > maxCustomHandleLength || !customHandlePattern.MatchString(string(handle)) {
		return fmt.Errorf(
			"control/agentbinding: custom role handle %q must start with a letter and use at most %d lowercase letters, numbers, or hyphens",
			handle,
			maxCustomHandleLength,
		)
	}
	if _, fixed := lookupDefinition(handle); fixed {
		return fmt.Errorf("control/agentbinding: handle %q is reserved", handle)
	}
	if _, reserved := reservedCustomHandles[handle]; reserved {
		return fmt.Errorf("control/agentbinding: handle %q is reserved", handle)
	}
	return nil
}

// ValidateSetName validates one user-visible binding-set identifier.
func ValidateSetName(raw string) error {
	name := NormalizeSetName(raw)
	if name == "" {
		return fmt.Errorf("control/agentbinding: binding set name is required")
	}
	if len(name) > maxBindingSetName || !bindingSetPattern.MatchString(name) {
		return fmt.Errorf(
			"control/agentbinding: binding set name %q must start with a letter and use at most %d lowercase letters, numbers, or hyphens",
			name,
			maxBindingSetName,
		)
	}
	return nil
}

// ValidateRoles validates custom role identity without consulting bindings.
func ValidateRoles(roles []Role) error {
	seen := make(map[Handle]struct{}, len(roles))
	for _, raw := range roles {
		role := NormalizeRole(raw)
		if err := ValidateCustomHandle(role.Handle); err != nil {
			return err
		}
		if role.Description == "" {
			return fmt.Errorf("control/agentbinding: custom role %q requires a description", role.Handle)
		}
		if _, ok := seen[role.Handle]; ok {
			return fmt.Errorf("control/agentbinding: duplicate custom role %q", role.Handle)
		}
		seen[role.Handle] = struct{}{}
	}
	return nil
}

// CreateRole atomically adds one custom delegation role and its optional
// initial binding.
func CreateRole(
	current Configuration,
	rawRole Role,
	initial Binding,
	profiles modelprofile.Configuration,
) (Configuration, error) {
	role := NormalizeRole(rawRole)
	if err := ValidateCustomHandle(role.Handle); err != nil {
		return Configuration{}, err
	}
	if role.Description == "" {
		return Configuration{}, fmt.Errorf("control/agentbinding: custom role %q requires a description", role.Handle)
	}
	if _, ok := LookupRole(current, role.Handle); ok {
		return Configuration{}, fmt.Errorf("control/agentbinding: custom role %q already exists", role.Handle)
	}
	next := NormalizeConfiguration(current)
	next.Roles = append(next.Roles, role)
	next = NormalizeConfiguration(next)

	initial = Normalize(initial)
	if initial.ProfileID != "" || initial.Effort != "" {
		initial.Handle = role.Handle
		var err error
		next, err = Bind(next, initial, profiles)
		if err != nil {
			return Configuration{}, err
		}
	}
	if err := ValidateConfiguration(next, profiles); err != nil {
		return Configuration{}, err
	}
	return NormalizeConfiguration(next), nil
}

// DeleteRole removes one custom role, its active binding, and its entries from
// saved sets. Fixed roles cannot be deleted.
func DeleteRole(current Configuration, raw Handle) (Configuration, error) {
	handle := NormalizeHandle(raw)
	if _, fixed := lookupDefinition(handle); fixed {
		return Configuration{}, fmt.Errorf("control/agentbinding: fixed role %q cannot be deleted", handle)
	}
	if _, ok := LookupRole(current, handle); !ok {
		return Configuration{}, fmt.Errorf("control/agentbinding: custom role %q does not exist", handle)
	}
	next := Configuration{}
	for _, role := range NormalizeConfiguration(current).Roles {
		if role.Handle != handle {
			next.Roles = append(next.Roles, role)
		}
	}
	for _, binding := range NormalizeConfiguration(current).Bindings {
		if binding.Handle != handle {
			next.Bindings = append(next.Bindings, binding)
		}
	}
	for _, set := range NormalizeConfiguration(current).Sets {
		cleaned := BindingSet{Name: set.Name}
		for _, binding := range set.Bindings {
			if binding.Handle != handle {
				cleaned.Bindings = append(cleaned.Bindings, binding)
			}
		}
		next.Sets = append(next.Sets, cleaned)
	}
	return NormalizeConfiguration(next), nil
}

// LookupRole returns one detached custom role.
func LookupRole(in Configuration, raw Handle) (Role, bool) {
	handle := NormalizeHandle(raw)
	for _, rawRole := range in.Roles {
		role := NormalizeRole(rawRole)
		if role.Handle == handle {
			return role, true
		}
	}
	return Role{}, false
}

// SaveBindingSet creates or replaces one named snapshot of all current
// explicit bindings.
func SaveBindingSet(current Configuration, rawName string) (Configuration, error) {
	name := NormalizeSetName(rawName)
	if err := ValidateSetName(name); err != nil {
		return Configuration{}, err
	}
	next := NormalizeConfiguration(current)
	snapshot := BindingSet{
		Name:     name,
		Bindings: append([]Binding(nil), next.Bindings...),
	}
	replaced := false
	for i := range next.Sets {
		if next.Sets[i].Name == name {
			next.Sets[i] = snapshot
			replaced = true
			break
		}
	}
	if !replaced {
		next.Sets = append(next.Sets, snapshot)
	}
	return NormalizeConfiguration(next), nil
}

// DeleteBindingSet removes one saved snapshot.
func DeleteBindingSet(current Configuration, rawName string) (Configuration, error) {
	name := NormalizeSetName(rawName)
	next := NormalizeConfiguration(current)
	found := false
	filtered := next.Sets[:0]
	for _, set := range next.Sets {
		if set.Name == name {
			found = true
			continue
		}
		filtered = append(filtered, set)
	}
	if !found {
		return Configuration{}, fmt.Errorf("control/agentbinding: binding set %q does not exist", name)
	}
	next.Sets = filtered
	return NormalizeConfiguration(next), nil
}

// ApplyBindingSet atomically replaces every explicit binding with the named
// snapshot after validating it against current roles and ModelProfiles.
func ApplyBindingSet(
	current Configuration,
	rawName string,
	profiles modelprofile.Configuration,
) (Configuration, error) {
	name := NormalizeSetName(rawName)
	set, ok := LookupBindingSet(current, name)
	if !ok {
		return Configuration{}, fmt.Errorf("control/agentbinding: binding set %q does not exist", name)
	}
	next := NormalizeConfiguration(current)
	next.Bindings = append([]Binding(nil), set.Bindings...)
	if err := ValidateConfiguration(next, profiles); err != nil {
		return Configuration{}, fmt.Errorf("control/agentbinding: binding set %q is unavailable: %w", name, err)
	}
	return NormalizeConfiguration(next), nil
}

// LookupBindingSet returns one detached named snapshot.
func LookupBindingSet(in Configuration, rawName string) (BindingSet, bool) {
	name := NormalizeSetName(rawName)
	for _, set := range NormalizeConfiguration(in).Sets {
		if set.Name == name {
			return BindingSet{
				Name:     set.Name,
				Bindings: append([]Binding(nil), set.Bindings...),
			}, true
		}
	}
	return BindingSet{}, false
}

// BindingSetStatuses projects availability and active state without mutating
// configuration. Unavailable snapshots remain visible so users can delete or
// replace them.
func BindingSetStatuses(
	current Configuration,
	profiles modelprofile.Configuration,
) []BindingSetStatus {
	normalized := NormalizeConfiguration(current)
	out := make([]BindingSetStatus, 0, len(normalized.Sets))
	for _, set := range normalized.Sets {
		item := BindingSetStatus{
			Name:     set.Name,
			Bindings: append([]Binding(nil), set.Bindings...),
		}
		candidate := normalized
		candidate.Bindings = append([]Binding(nil), set.Bindings...)
		if err := ValidateConfiguration(candidate, profiles); err != nil {
			item.Problem = err.Error()
		} else {
			item.Available = true
			item.Active = sameBindingTable(normalized.Bindings, set.Bindings)
		}
		out = append(out, item)
	}
	return out
}

// ValidateBindingSets validates raw binding snapshot identities and record
// completeness before deterministic normalization can discard conflicts.
func ValidateBindingSets(sets []BindingSet) error {
	seenNames := make(map[string]struct{}, len(sets))
	for _, raw := range sets {
		set := NormalizeBindingSet(raw)
		if err := ValidateSetName(set.Name); err != nil {
			return err
		}
		if _, ok := seenNames[set.Name]; ok {
			return fmt.Errorf("control/agentbinding: duplicate binding set %q", set.Name)
		}
		seenNames[set.Name] = struct{}{}
		seenHandles := make(map[Handle]struct{}, len(raw.Bindings))
		for _, rawBinding := range raw.Bindings {
			binding := Normalize(rawBinding)
			if binding.Handle == "" || binding.ProfileID == "" || binding.Effort == "" {
				return fmt.Errorf("control/agentbinding: binding set %q contains an incomplete binding", set.Name)
			}
			if binding.Handle == HandleSelf {
				return fmt.Errorf("control/agentbinding: binding set %q cannot persist self", set.Name)
			}
			if !isPersistedHandle(binding.Handle) && ValidateCustomHandle(binding.Handle) != nil {
				return fmt.Errorf("control/agentbinding: binding set %q contains invalid handle %q", set.Name, binding.Handle)
			}
			if _, ok := seenHandles[binding.Handle]; ok {
				return fmt.Errorf("control/agentbinding: binding set %q contains duplicate handle %q", set.Name, binding.Handle)
			}
			seenHandles[binding.Handle] = struct{}{}
		}
	}
	return nil
}

func sameBindingTable(left, right []Binding) bool {
	left = NormalizeBindingSet(BindingSet{Bindings: left}).Bindings
	right = NormalizeBindingSet(BindingSet{Bindings: right}).Bindings
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func isPersistedHandleIn(configuration Configuration, handle Handle) bool {
	handle = NormalizeHandle(handle)
	if isPersistedHandle(handle) {
		return true
	}
	_, ok := LookupRole(configuration, handle)
	return ok
}

func orderIn(configuration Configuration, handle Handle) int {
	handle = NormalizeHandle(handle)
	catalog := CatalogFor(configuration)
	definitions := catalog.Definitions()
	for i, definition := range definitions {
		if definition.Handle == handle {
			return i
		}
	}
	return len(definitions)
}
