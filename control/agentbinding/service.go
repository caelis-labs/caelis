package agentbinding

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/control/modelprofile"
)

// HandleStatus is the detached Control projection of one fixed or custom
// handle.
type HandleStatus struct {
	Definition Definition
	Binding    Binding
	Profile    modelprofile.ModelProfile
}

// IsBound reports whether a configurable handle has an explicit
// ModelProfile and effort binding.
func IsBound(status HandleStatus) bool {
	return status.Definition.Configurable &&
		strings.TrimSpace(status.Binding.ProfileID) != "" &&
		strings.TrimSpace(status.Binding.Effort) != ""
}

// BoundDirectHandles returns the bound directly runnable handles in the
// canonical order supplied by Status.
func BoundDirectHandles(status Status) []HandleStatus {
	out := make([]HandleStatus, 0, len(status.Handles))
	seen := make(map[Handle]struct{}, len(status.Handles))
	for _, item := range status.Handles {
		handle := NormalizeHandle(item.Definition.Handle)
		if handle == "" || !IsDirectRunDefinition(item.Definition) || !IsBound(item) {
			continue
		}
		if _, ok := seen[handle]; ok {
			continue
		}
		item.Definition.Handle = handle
		out = append(out, item)
		seen[handle] = struct{}{}
	}
	return out
}

// IsBoundDirectHandle reports whether handle is directly runnable and has an
// explicit profile and effort binding in Status.
func IsBoundDirectHandle(status Status, raw Handle) bool {
	handle := NormalizeHandle(raw)
	for _, item := range status.Handles {
		if NormalizeHandle(item.Definition.Handle) == handle {
			return IsDirectRunDefinition(item.Definition) && IsBound(item)
		}
	}
	return false
}

// ProjectBoundDirectNames filters unbound direct handles from base and appends
// all bound fixed and custom handles in Status order. Non-Agent commands are
// preserved.
func ProjectBoundDirectNames(base []string, status Status) []string {
	direct := make(map[Handle]struct{}, len(status.Handles)+len(DirectRunHandles()))
	for _, handle := range DirectRunHandles() {
		direct[handle] = struct{}{}
	}
	for _, item := range status.Handles {
		if IsDirectRunDefinition(item.Definition) {
			direct[NormalizeHandle(item.Definition.Handle)] = struct{}{}
		}
	}

	bound := BoundDirectHandles(status)
	boundNames := make(map[Handle]struct{}, len(bound))
	for _, item := range bound {
		boundNames[item.Definition.Handle] = struct{}{}
	}

	out := make([]string, 0, len(base)+len(bound))
	seen := make(map[string]struct{}, len(base)+len(bound))
	for _, raw := range base {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "/")))
		if name == "" {
			continue
		}
		handle := NormalizeHandle(Handle(name))
		if _, isDirect := direct[handle]; isDirect {
			if _, isBound := boundNames[handle]; !isBound {
				continue
			}
		}
		if _, ok := seen[name]; ok {
			continue
		}
		out = append(out, name)
		seen[name] = struct{}{}
	}
	for _, item := range bound {
		name := string(item.Definition.Handle)
		if _, ok := seen[name]; ok {
			continue
		}
		out = append(out, name)
		seen[name] = struct{}{}
	}
	return out
}

// Status is the complete handle-binding view used by Control surfaces.
// Targets contains standard ModelProfiles; SupportsProfile decides eligibility
// for a particular handle.
type Status struct {
	Handles []HandleStatus
	Targets []modelprofile.ModelProfile
	Sets    []BindingSetStatus
}

// BindingSetStatus is the detached availability view of one persisted binding
// snapshot.
type BindingSetStatus struct {
	Name      string
	Bindings  []Binding
	Active    bool
	Available bool
	Problem   string
}

// SupportsProfile reports whether a profile may back one persisted handle.
func SupportsProfile(handle Handle, profile modelprofile.ModelProfile) bool {
	handle = NormalizeHandle(handle)
	if !isPersistedHandle(handle) && ValidateCustomHandle(handle) != nil {
		return false
	}
	return !IsSystem(handle) || profile.Kind() == modelprofile.BackendProvider
}

// Service is the single Control-owned handle configuration capability.
// Surfaces render its detached state and submit explicit mutations; they do not
// own binding validation or persistence.
type Service interface {
	AgentBindingStatus(context.Context) (Status, error)
	BindAgentBinding(context.Context, Binding) (Status, error)
	ResetAgentBinding(context.Context, Handle) (Status, error)
}

// ConfigurationService extends binding mutation with user-created roles and
// named binding snapshots. It remains Control-owned; presentation surfaces
// render Status and submit explicit operations.
type ConfigurationService interface {
	Service
	CreateAgentRole(context.Context, Role, Binding) (Status, error)
	DeleteAgentRole(context.Context, Handle) (Status, error)
	SaveAgentBindingSet(context.Context, string) (Status, error)
	ApplyAgentBindingSet(context.Context, string) (Status, error)
	DeleteAgentBindingSet(context.Context, string) (Status, error)
}
