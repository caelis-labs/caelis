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

// AudienceBinding names the View and Grant selected for exactly one output
// audience. These references do not themselves grant access.
type AudienceBinding struct {
	ViewRef  string `json:"view_ref"`
	GrantRef string `json:"grant_ref"`
}

// BotMemoryBinding maps one stable product Bot to cognitive continuity and
// Memory authorization references. IssuerCredentialRef is an opaque secret
// lookup reference; raw credentials never enter AppConfig.
type BotMemoryBinding struct {
	BotID               string          `json:"bot_id"`
	RuntimeActorRef     RuntimeActorRef `json:"runtime_actor_ref"`
	MemoryIdentityRef   string          `json:"memory_identity_ref"`
	PrincipalRef        string          `json:"principal_ref"`
	IssuerCredentialRef string          `json:"issuer_credential_ref"`
	Private             AudienceBinding `json:"private,omitzero"`
	Shared              AudienceBinding `json:"shared,omitzero"`
	BindingVersion      uint64          `json:"binding_version"`
}

// Configuration is the complete persisted Memory host binding. Enabled is a
// product feature flag; an independent process kill switch may still disable
// activation without mutating this document or appliance data.
type Configuration struct {
	Enabled  bool               `json:"enabled,omitempty"`
	Endpoint EndpointConfig     `json:"endpoint,omitempty"`
	Bots     []BotMemoryBinding `json:"bots,omitempty"`
}

// RuntimeSelection is process-owned input selecting one Bot and one audience
// for later Session Runtime activations. Neither value is model-controlled.
type RuntimeSelection struct {
	BotID    string
	Audience OutputAudience
}

// RuntimeMemoryBindingSnapshot is the detached immutable binding consumed by
// one activated Runtime. It contains only the selected View/Grant and cannot be
// retargeted to the other audience after activation.
type RuntimeMemoryBindingSnapshot struct {
	Endpoint            EndpointConfig
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
	out := Configuration{Enabled: in.Enabled, Endpoint: normalizeEndpoint(in.Endpoint)}
	if len(in.Bots) == 0 {
		return out
	}
	out.Bots = make([]BotMemoryBinding, 0, len(in.Bots))
	for _, binding := range in.Bots {
		out.Bots = append(out.Bots, normalizeBotBinding(binding))
	}
	sort.Slice(out.Bots, func(i, j int) bool {
		return out.Bots[i].BotID < out.Bots[j].BotID
	})
	return out
}

// ValidateIdentities rejects collisions before deterministic sorting or any
// future lossy normalization can hide them.
func ValidateIdentities(in Configuration) error {
	botIDs := make(map[string]struct{}, len(in.Bots))
	actorRefs := make(map[RuntimeActorRef]struct{}, len(in.Bots))
	for _, raw := range in.Bots {
		binding := normalizeBotBinding(raw)
		if binding.BotID != "" {
			if _, duplicate := botIDs[binding.BotID]; duplicate {
				return fmt.Errorf("control/memorybinding: duplicate Bot ID %q", binding.BotID)
			}
			botIDs[binding.BotID] = struct{}{}
		}
		if binding.RuntimeActorRef != "" {
			if _, duplicate := actorRefs[binding.RuntimeActorRef]; duplicate {
				return fmt.Errorf("control/memorybinding: duplicate Runtime actor %q", binding.RuntimeActorRef)
			}
			actorRefs[binding.RuntimeActorRef] = struct{}{}
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
	if configuration.Endpoint.ID == "" && len(configuration.Bots) == 0 {
		if configuration.Enabled {
			return fmt.Errorf("control/memorybinding: enabled configuration requires an endpoint and Bot binding")
		}
		return nil
	}
	if err := validateEndpoint(configuration.Endpoint); err != nil {
		return err
	}
	if len(configuration.Bots) == 0 {
		return fmt.Errorf("control/memorybinding: configured endpoint requires at least one Bot binding")
	}
	for _, binding := range configuration.Bots {
		if err := validateBotBinding(binding); err != nil {
			return err
		}
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
	selection.BotID = strings.TrimSpace(selection.BotID)
	selection.Audience = normalizeAudience(selection.Audience)
	if selection.BotID == "" || !validAudience(selection.Audience) {
		return RuntimeMemoryBindingSnapshot{}, false, fmt.Errorf("control/memorybinding: Runtime Bot and output audience are required")
	}
	configuration = Normalize(configuration)
	for _, binding := range configuration.Bots {
		if binding.BotID != selection.BotID {
			continue
		}
		audienceBinding := binding.Private
		if selection.Audience == OutputAudienceShared {
			audienceBinding = binding.Shared
		}
		if audienceBinding.ViewRef == "" || audienceBinding.GrantRef == "" {
			return RuntimeMemoryBindingSnapshot{}, false, fmt.Errorf(
				"control/memorybinding: Bot %q has no %s Memory binding",
				selection.BotID,
				selection.Audience,
			)
		}
		return RuntimeMemoryBindingSnapshot{
			Endpoint:        configuration.Endpoint,
			RuntimeActorRef: binding.RuntimeActorRef,
			PrincipalRef:    binding.PrincipalRef, IssuerCredentialRef: binding.IssuerCredentialRef,
			ViewRef: audienceBinding.ViewRef, GrantRef: audienceBinding.GrantRef,
			Audience: selection.Audience, BindingVersion: binding.BindingVersion,
		}, true, nil
	}
	return RuntimeMemoryBindingSnapshot{}, false, fmt.Errorf("control/memorybinding: Bot %q has no Memory binding", selection.BotID)
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

func normalizeBotBinding(in BotMemoryBinding) BotMemoryBinding {
	in.BotID = strings.TrimSpace(in.BotID)
	in.RuntimeActorRef = RuntimeActorRef(strings.TrimSpace(string(in.RuntimeActorRef)))
	in.MemoryIdentityRef = strings.TrimSpace(in.MemoryIdentityRef)
	in.PrincipalRef = strings.TrimSpace(in.PrincipalRef)
	in.IssuerCredentialRef = strings.TrimSpace(in.IssuerCredentialRef)
	in.Private = normalizeAudienceBinding(in.Private)
	in.Shared = normalizeAudienceBinding(in.Shared)
	return in
}

func normalizeAudienceBinding(in AudienceBinding) AudienceBinding {
	in.ViewRef = strings.TrimSpace(in.ViewRef)
	in.GrantRef = strings.TrimSpace(in.GrantRef)
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

func validateBotBinding(binding BotMemoryBinding) error {
	if binding.BotID == "" || binding.RuntimeActorRef == "" || binding.MemoryIdentityRef == "" ||
		binding.PrincipalRef == "" || binding.IssuerCredentialRef == "" || binding.BindingVersion == 0 {
		return fmt.Errorf("control/memorybinding: Bot identity, Runtime actor, Memory identity, principal, issuer credential, and binding version are required")
	}
	if (binding.Private.ViewRef == "") != (binding.Private.GrantRef == "") {
		return fmt.Errorf("control/memorybinding: Bot %q private View and Grant must be configured together", binding.BotID)
	}
	if (binding.Shared.ViewRef == "") != (binding.Shared.GrantRef == "") {
		return fmt.Errorf("control/memorybinding: Bot %q shared View and Grant must be configured together", binding.BotID)
	}
	if binding.Private.ViewRef == "" && binding.Shared.ViewRef == "" {
		return fmt.Errorf("control/memorybinding: Bot %q requires a private or shared binding", binding.BotID)
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
