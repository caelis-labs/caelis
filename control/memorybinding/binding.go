package memorybinding

import (
	"fmt"
	"sort"
	"strings"
)

// RuntimeActorRef is the stable Control-owned actor presented to Memory
// authorization. It is not a Session, Workspace, model, or MemoryIdentity.
type RuntimeActorRef string

// OutputAudience is the single disclosure class of one canonical Runtime and
// Session in the Alpha profile.
type OutputAudience string

const (
	OutputAudiencePrivate OutputAudience = "private"
	OutputAudienceShared  OutputAudience = "shared"
)

// BindingRef is an opaque host-selected reference. Future product concepts
// such as an Agent, user, tenant, or workspace may resolve to this reference,
// but none of those concepts enter the Memory integration contract.
type BindingRef string

// AccessBinding is one complete, single-audience Memory delegation.
// IssuerCredentialRef is an opaque secret lookup reference; raw credentials
// never enter AppConfig. View and Grant references do not themselves grant
// access.
type AccessBinding struct {
	BindingRef          BindingRef      `json:"binding_ref"`
	RuntimeActorRef     RuntimeActorRef `json:"runtime_actor_ref"`
	PrincipalRef        string          `json:"principal_ref"`
	IssuerCredentialRef string          `json:"issuer_credential_ref"`
	ViewRef             string          `json:"view_ref"`
	GrantRef            string          `json:"grant_ref"`
	Audience            OutputAudience  `json:"audience"`
	BindingVersion      uint64          `json:"binding_version"`
}

// Configuration is the complete persisted logical Memory binding. Memory is
// a built-in Host capability; this document selects authority and has no
// independent lifecycle switch.
type Configuration struct {
	DefaultBindingRef BindingRef      `json:"default_binding_ref,omitempty"`
	Bindings          []AccessBinding `json:"bindings,omitempty"`
}

// IsConfigured reports whether any persistent appliance topology has been
// selected. Partial topology is configured and therefore fails validation;
// only the complete zero value is eligible for automatic Host provisioning.
func IsConfigured(configuration Configuration) bool {
	configuration = Normalize(configuration)
	return configuration.DefaultBindingRef != "" || len(configuration.Bindings) > 0
}

// RuntimeSelection is process-owned input selecting one opaque binding for
// later Session Runtime activations. An empty value selects the explicit
// configuration default. It is never model-controlled.
type RuntimeSelection struct {
	BindingRef BindingRef
}

// RuntimeMemoryBindingSnapshot is the detached immutable binding consumed by
// one activated Runtime. It contains only the selected View/Grant and cannot be
// retargeted to the other audience after activation.
type RuntimeMemoryBindingSnapshot struct {
	BindingRef          BindingRef
	RuntimeActorRef     RuntimeActorRef
	PrincipalRef        string
	IssuerCredentialRef string
	ViewRef             string
	GrantRef            string
	Audience            OutputAudience
	BindingVersion      uint64
}

// Normalize returns a detached deterministic configuration. Validation must
// run on the raw document first when duplicate identity rejection matters.
func Normalize(in Configuration) Configuration {
	out := Configuration{
		DefaultBindingRef: BindingRef(strings.TrimSpace(string(in.DefaultBindingRef))),
	}
	if len(in.Bindings) == 0 {
		return out
	}
	out.Bindings = make([]AccessBinding, 0, len(in.Bindings))
	for _, binding := range in.Bindings {
		out.Bindings = append(out.Bindings, normalizeAccessBinding(binding))
	}
	sort.Slice(out.Bindings, func(i, j int) bool {
		return out.Bindings[i].BindingRef < out.Bindings[j].BindingRef
	})
	return out
}

// ValidateIdentities rejects collisions before deterministic sorting or any
// future lossy normalization can hide them.
func ValidateIdentities(in Configuration) error {
	bindingRefs := make(map[BindingRef]struct{}, len(in.Bindings))
	for _, raw := range in.Bindings {
		binding := normalizeAccessBinding(raw)
		if binding.BindingRef != "" {
			if _, duplicate := bindingRefs[binding.BindingRef]; duplicate {
				return fmt.Errorf("control/memorybinding: duplicate binding reference %q", binding.BindingRef)
			}
			bindingRefs[binding.BindingRef] = struct{}{}
		}
	}
	return nil
}

// Validate enforces the Core Alpha profile without interpreting any appliance
// domain reference as authority.
func Validate(in Configuration) error {
	if err := ValidateIdentities(in); err != nil {
		return err
	}
	configuration := Normalize(in)
	if len(configuration.Bindings) == 0 {
		if configuration.DefaultBindingRef != "" {
			return fmt.Errorf("control/memorybinding: default binding requires an access binding")
		}
		return nil
	}
	if configuration.DefaultBindingRef == "" {
		return fmt.Errorf("control/memorybinding: configured bindings require an explicit default binding reference")
	}
	foundDefault := false
	for _, binding := range configuration.Bindings {
		if err := validateAccessBinding(binding); err != nil {
			return err
		}
		foundDefault = foundDefault || binding.BindingRef == configuration.DefaultBindingRef
	}
	if !foundDefault {
		return fmt.Errorf("control/memorybinding: default binding reference %q does not exist", configuration.DefaultBindingRef)
	}
	return nil
}

// Resolve returns a detached Runtime snapshot and reports whether the selected
// logical binding exists.
func Resolve(configuration Configuration, selection RuntimeSelection) (RuntimeMemoryBindingSnapshot, bool, error) {
	if err := Validate(configuration); err != nil {
		return RuntimeMemoryBindingSnapshot{}, false, err
	}
	if !IsConfigured(configuration) {
		return RuntimeMemoryBindingSnapshot{}, false, nil
	}
	selection.BindingRef = BindingRef(strings.TrimSpace(string(selection.BindingRef)))
	if selection.BindingRef == "" {
		selection.BindingRef = configuration.DefaultBindingRef
	}
	configuration = Normalize(configuration)
	for _, binding := range configuration.Bindings {
		if binding.BindingRef != selection.BindingRef {
			continue
		}
		return RuntimeMemoryBindingSnapshot(binding), true, nil
	}
	return RuntimeMemoryBindingSnapshot{}, false, fmt.Errorf("control/memorybinding: binding reference %q does not exist", selection.BindingRef)
}

func normalizeAccessBinding(in AccessBinding) AccessBinding {
	in.BindingRef = BindingRef(strings.TrimSpace(string(in.BindingRef)))
	in.RuntimeActorRef = RuntimeActorRef(strings.TrimSpace(string(in.RuntimeActorRef)))
	in.PrincipalRef = strings.TrimSpace(in.PrincipalRef)
	in.IssuerCredentialRef = strings.TrimSpace(in.IssuerCredentialRef)
	in.ViewRef = strings.TrimSpace(in.ViewRef)
	in.GrantRef = strings.TrimSpace(in.GrantRef)
	in.Audience = normalizeAudience(in.Audience)
	return in
}

func normalizeAudience(in OutputAudience) OutputAudience {
	return OutputAudience(strings.ToLower(strings.TrimSpace(string(in))))
}

func validateAccessBinding(binding AccessBinding) error {
	if binding.BindingRef == "" || binding.RuntimeActorRef == "" || binding.PrincipalRef == "" ||
		binding.IssuerCredentialRef == "" || binding.ViewRef == "" || binding.GrantRef == "" ||
		!validAudience(binding.Audience) || binding.BindingVersion == 0 {
		return fmt.Errorf("control/memorybinding: binding reference, Runtime actor, principal, issuer credential, View, Grant, audience, and version are required")
	}
	return nil
}

func validAudience(audience OutputAudience) bool {
	return audience == OutputAudiencePrivate || audience == OutputAudienceShared
}
