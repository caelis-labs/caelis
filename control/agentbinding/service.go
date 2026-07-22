package agentbinding

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/control/modelprofile"
)

// HandleStatus is the detached Control projection of one fixed handle.
type HandleStatus struct {
	Definition Definition
	Binding    Binding
	Profile    modelprofile.ModelProfile
}

// IsBound reports whether a fixed configurable handle has an explicit
// ModelProfile and effort binding.
func IsBound(status HandleStatus) bool {
	return status.Definition.Configurable &&
		strings.TrimSpace(status.Binding.ProfileID) != "" &&
		strings.TrimSpace(status.Binding.Effort) != ""
}

// Status is the complete fixed-handle binding view used by Control surfaces.
// Targets contains standard ModelProfiles; SupportsProfile decides eligibility
// for a particular handle.
type Status struct {
	Handles []HandleStatus
	Targets []modelprofile.ModelProfile
}

// SupportsProfile reports whether a profile may back one persisted handle.
func SupportsProfile(handle Handle, profile modelprofile.ModelProfile) bool {
	handle = NormalizeHandle(handle)
	if !isPersistedHandle(handle) {
		return false
	}
	return !IsSystem(handle) || profile.Kind() == modelprofile.BackendProvider
}

// Service is the single Control-owned fixed-handle configuration capability.
// Surfaces render its detached state and submit explicit mutations; they do not
// own binding validation or persistence.
type Service interface {
	AgentBindingStatus(context.Context) (Status, error)
	BindAgentBinding(context.Context, Binding) (Status, error)
	ResetAgentBinding(context.Context, Handle) (Status, error)
}
