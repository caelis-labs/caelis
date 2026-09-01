package memorybinding

import (
	"encoding/hex"
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

// DeploymentMode selects how Host-private composition reaches the appliance.
type DeploymentMode string

const (
	DeploymentModeManagedLocal DeploymentMode = "managed_local"
)

// APICompatibility pins the Memory protocol and exact sidecar identity. The
// SDK remains the authority that interprets protocol behavior.
type APICompatibility struct {
	Protocol       string `json:"protocol"`
	APIVersion     string `json:"api_version"`
	CoreProfile    string `json:"core_profile"`
	ServiceVersion string `json:"service_version"`
	BuildRevision  string `json:"build_revision"`
	ArtifactSHA256 string `json:"artifact_sha256"`
}

// EndpointConfig identifies one appliance deployment. Endpoint is reserved
// for a future external deployment; managed-local Socket paths are derived by
// Host-private composition and are not persisted here.
type EndpointConfig struct {
	ID            string           `json:"id"`
	Deployment    DeploymentMode   `json:"deployment"`
	Endpoint      string           `json:"endpoint,omitempty"`
	Compatibility APICompatibility `json:"compatibility"`
}

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

// Configuration is the complete persisted Memory host binding. Enabled is a
// product feature flag; an independent process kill switch may still disable
// activation without mutating this document or appliance data.
type Configuration struct {
	Enabled           bool            `json:"enabled,omitempty"`
	Endpoint          EndpointConfig  `json:"endpoint,omitempty"`
	DefaultBindingRef BindingRef      `json:"default_binding_ref,omitempty"`
	Bindings          []AccessBinding `json:"bindings,omitempty"`
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
	Endpoint            EndpointConfig
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
		Enabled: in.Enabled, Endpoint: normalizeEndpoint(in.Endpoint),
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
	if configuration.Endpoint.ID == "" && len(configuration.Bindings) == 0 {
		if configuration.Enabled {
			return fmt.Errorf("control/memorybinding: enabled configuration requires an endpoint and access binding")
		}
		return nil
	}
	if err := validateEndpoint(configuration.Endpoint); err != nil {
		return err
	}
	if len(configuration.Bindings) == 0 {
		return fmt.Errorf("control/memorybinding: configured endpoint requires at least one access binding")
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

// Resolve returns a detached Runtime snapshot. disabled is the independent
// process kill switch; false means Memory tools must be absent, not degraded.
func Resolve(configuration Configuration, selection RuntimeSelection, disabled bool) (RuntimeMemoryBindingSnapshot, bool, error) {
	if disabled || !configuration.Enabled {
		return RuntimeMemoryBindingSnapshot{}, false, nil
	}
	if err := Validate(configuration); err != nil {
		return RuntimeMemoryBindingSnapshot{}, false, err
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
		return RuntimeMemoryBindingSnapshot{
			Endpoint: configuration.Endpoint, BindingRef: binding.BindingRef,
			RuntimeActorRef: binding.RuntimeActorRef,
			PrincipalRef:    binding.PrincipalRef, IssuerCredentialRef: binding.IssuerCredentialRef,
			ViewRef: binding.ViewRef, GrantRef: binding.GrantRef,
			Audience: binding.Audience, BindingVersion: binding.BindingVersion,
		}, true, nil
	}
	return RuntimeMemoryBindingSnapshot{}, false, fmt.Errorf("control/memorybinding: binding reference %q does not exist", selection.BindingRef)
}

func normalizeEndpoint(in EndpointConfig) EndpointConfig {
	in.ID = strings.TrimSpace(in.ID)
	in.Deployment = DeploymentMode(strings.ToLower(strings.TrimSpace(string(in.Deployment))))
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	in.Compatibility.Protocol = strings.TrimSpace(in.Compatibility.Protocol)
	in.Compatibility.APIVersion = strings.TrimSpace(in.Compatibility.APIVersion)
	in.Compatibility.CoreProfile = strings.TrimSpace(in.Compatibility.CoreProfile)
	in.Compatibility.ServiceVersion = strings.TrimSpace(in.Compatibility.ServiceVersion)
	in.Compatibility.BuildRevision = strings.ToLower(strings.TrimSpace(in.Compatibility.BuildRevision))
	in.Compatibility.ArtifactSHA256 = strings.ToLower(strings.TrimSpace(in.Compatibility.ArtifactSHA256))
	return in
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

func validateEndpoint(endpoint EndpointConfig) error {
	if endpoint.ID == "" {
		return fmt.Errorf("control/memorybinding: endpoint ID is required")
	}
	if endpoint.Deployment != DeploymentModeManagedLocal {
		return fmt.Errorf("control/memorybinding: unsupported deployment mode %q", endpoint.Deployment)
	}
	if endpoint.Endpoint != "" {
		return fmt.Errorf("control/memorybinding: managed-local endpoint is Host-derived and must not be persisted")
	}
	compatibility := endpoint.Compatibility
	if compatibility.Protocol == "" || compatibility.APIVersion == "" || compatibility.CoreProfile == "" || compatibility.ServiceVersion == "" {
		return fmt.Errorf("control/memorybinding: complete API compatibility is required")
	}
	if !validDigest(compatibility.BuildRevision, 40, 64) {
		return fmt.Errorf("control/memorybinding: build revision must be a full hexadecimal Git object ID")
	}
	if !validDigest(compatibility.ArtifactSHA256, 64) {
		return fmt.Errorf("control/memorybinding: artifact SHA-256 is invalid")
	}
	return nil
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

func validDigest(value string, lengths ...int) bool {
	validLength := false
	for _, length := range lengths {
		if len(value) == length {
			validLength = true
			break
		}
	}
	if !validLength || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
