package placement

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// Purpose identifies the Control operation requesting a placement.
type Purpose string

const (
	PurposeSpawn    Purpose = "spawn"
	PurposeDelegate Purpose = "delegate"
	PurposeDirect   Purpose = "direct"
	PurposeGuardian Purpose = "guardian"
	PurposeReviewer Purpose = "reviewer"
)

// SessionContext supplies the current Session choice used by the synthetic
// self handle. It contains product identities only; the resolver freezes the
// same fields as every persisted handle.
type SessionContext struct {
	ProfileID string `json:"profile_id,omitempty"`
	Effort    string `json:"effort,omitempty"`
}

// HandleRequest asks Control to resolve one fixed handle for one operation.
// Participant attachment uses ResolveParticipant instead, so a request cannot
// mix handle-binding and explicit-profile semantics.
type HandleRequest struct {
	Handle  agentbinding.Handle `json:"handle,omitempty"`
	Purpose Purpose             `json:"purpose,omitempty"`
	Session SessionContext      `json:"session,omitempty"`
}

// ParticipantSelectionError reports that an explicit participant profile or
// effort cannot be selected. Snapshot, storage, and backend failures do not use
// this type and must remain internal failures at transport boundaries.
type ParticipantSelectionError struct {
	ProfileID string
	Effort    string
	Reason    string
}

func (e *ParticipantSelectionError) Error() string {
	if e == nil {
		return "control/placement: participant selection is invalid"
	}
	return "control/placement: participant " + strings.TrimSpace(e.Reason)
}

// DefaultProfileError reports that an unbound system Agent cannot resolve the
// Control-owned default provider profile.
type DefaultProfileError struct {
	Handle    agentbinding.Handle
	ProfileID string
	Reason    string
}

func (e *DefaultProfileError) Error() string {
	if e == nil {
		return "control/placement: system Agent default profile is unavailable"
	}
	if e.ProfileID == "" {
		return fmt.Sprintf("control/placement: %q has no default provider profile", e.Handle)
	}
	return fmt.Sprintf(
		"control/placement: default profile %q for %q %s",
		e.ProfileID,
		e.Handle,
		e.Reason,
	)
}

// Snapshot is the complete immutable configuration view needed for one
// resolution.
type Snapshot struct {
	Profiles modelprofile.Configuration  `json:"profiles"`
	Bindings agentbinding.Configuration  `json:"bindings"`
	Models   []modelconfig.Config        `json:"models,omitempty"`
	Agents   controlagents.Configuration `json:"agents,omitempty"`
}

// PurposeForHandle returns the operation used when a host directly invokes a
// user-addressable or system handle. Self remains Session-derived and callers
// resolving it for Spawn or Delegate must choose that purpose explicitly.
func PurposeForHandle(raw agentbinding.Handle) (Purpose, error) {
	handle := agentbinding.NormalizeHandle(raw)
	switch {
	case agentbinding.IsDirectRun(handle):
		return PurposeDirect, nil
	case handle == agentbinding.HandleGuardian:
		return PurposeGuardian, nil
	case handle == agentbinding.HandleReviewer:
		return PurposeReviewer, nil
	case handle == agentbinding.HandleSelf:
		return "", fmt.Errorf("control/placement: self is Session-derived and cannot run directly")
	default:
		return "", fmt.Errorf("control/placement: unknown Agent handle %q", handle)
	}
}

// ResolveHandle resolves one fixed handle from a caller-owned immutable
// snapshot and returns a sealed SDK Placement.
func ResolveHandle(snapshot Snapshot, req HandleRequest) (sdkplacement.Placement, error) {
	if err := ValidateSnapshot(snapshot); err != nil {
		return sdkplacement.Placement{}, err
	}
	purpose := Purpose(strings.ToLower(strings.TrimSpace(string(req.Purpose))))
	handle := agentbinding.NormalizeHandle(req.Handle)
	if err := validateHandlePurpose(handle, purpose); err != nil {
		return sdkplacement.Placement{}, err
	}

	profileID := ""
	effort := ""
	if handle == agentbinding.HandleSelf {
		profileID = modelprofile.NormalizeID(req.Session.ProfileID)
		effort = modelcatalog.NormalizeReasoningEffort(req.Session.Effort)
		if profileID == "" || effort == "" {
			return sdkplacement.Placement{}, fmt.Errorf("control/placement: self requires the current Session profile and effort")
		}
	} else {
		binding, ok := agentbinding.Lookup(snapshot.Bindings, handle)
		if ok {
			profileID, effort = binding.ProfileID, binding.Effort
		} else if purpose == PurposeGuardian || purpose == PurposeReviewer {
			profile, err := defaultSystemProfile(snapshot.Profiles, handle)
			if err != nil {
				return sdkplacement.Placement{}, err
			}
			profileID, effort = profile.ID, profile.Effort.DefaultEffort
		} else {
			return sdkplacement.Placement{}, fmt.Errorf("control/placement: handle %q is not bound", handle)
		}
	}
	profile, ok := modelprofile.Lookup(snapshot.Profiles, profileID)
	if !ok {
		return sdkplacement.Placement{}, fmt.Errorf("control/placement: handle %q references unknown profile %q", handle, profileID)
	}
	if !profile.SupportsEffort(effort) {
		return sdkplacement.Placement{}, fmt.Errorf("control/placement: effort %q is not supported by profile %q", effort, profile.ID)
	}
	if purpose == PurposeGuardian || purpose == PurposeReviewer {
		if profile.Kind() != modelprofile.BackendProvider {
			return sdkplacement.Placement{}, &agentbinding.UnsupportedBackendError{Handle: handle, ProfileID: profile.ID, Backend: profile.Kind()}
		}
	}

	switch profile.Kind() {
	case modelprofile.BackendProvider:
		return resolveProvider(snapshot, profile, effort)
	case modelprofile.BackendACP:
		return resolveACP(snapshot, profile, effort)
	default:
		return sdkplacement.Placement{}, fmt.Errorf("control/placement: profile %q has no valid backend", profile.ID)
	}
}

// ResolveParticipant selects one explicit ACP ModelProfile and effort for
// participant attachment. It never reads or synthesizes a fixed handle.
func ResolveParticipant(snapshot Snapshot, rawProfileID, rawEffort string) (sdkplacement.Placement, error) {
	if err := ValidateSnapshot(snapshot); err != nil {
		return sdkplacement.Placement{}, err
	}
	profileID := modelprofile.NormalizeID(rawProfileID)
	effort := modelcatalog.NormalizeReasoningEffort(rawEffort)
	if profileID == "" || effort == "" {
		return sdkplacement.Placement{}, &ParticipantSelectionError{
			ProfileID: profileID,
			Effort:    effort,
			Reason:    "requires an explicit profile and effort",
		}
	}
	profile, ok := modelprofile.Lookup(snapshot.Profiles, profileID)
	if !ok {
		return sdkplacement.Placement{}, &ParticipantSelectionError{
			ProfileID: profileID,
			Effort:    effort,
			Reason:    fmt.Sprintf("references unknown profile %q", profileID),
		}
	}
	if profile.Kind() != modelprofile.BackendACP {
		return sdkplacement.Placement{}, &ParticipantSelectionError{
			ProfileID: profile.ID,
			Effort:    effort,
			Reason:    fmt.Sprintf("profile %q is not ACP-backed", profile.ID),
		}
	}
	if !profile.SupportsEffort(effort) {
		return sdkplacement.Placement{}, &ParticipantSelectionError{
			ProfileID: profile.ID,
			Effort:    effort,
			Reason:    fmt.Sprintf("effort %q is not supported by profile %q", effort, profile.ID),
		}
	}
	return resolveACP(snapshot, profile, effort)
}

func defaultSystemProfile(profiles modelprofile.Configuration, handle agentbinding.Handle) (modelprofile.ModelProfile, error) {
	profileID := modelprofile.NormalizeID(profiles.DefaultProfileID)
	if profileID == "" {
		return modelprofile.ModelProfile{}, &DefaultProfileError{Handle: handle}
	}
	profile, ok := modelprofile.Lookup(profiles, profileID)
	if !ok {
		return modelprofile.ModelProfile{}, &DefaultProfileError{
			Handle: handle, ProfileID: profileID, Reason: "does not exist",
		}
	}
	if profile.Kind() != modelprofile.BackendProvider {
		return modelprofile.ModelProfile{}, &DefaultProfileError{
			Handle: handle, ProfileID: profile.ID, Reason: "is not provider-backed",
		}
	}
	return profile, nil
}

// ValidateFrozen verifies a durable placement without consulting the current
// handle binding. Rebinding a handle therefore cannot alter prepared work,
// while deletion or mutation of the referenced backend still fails closed.
func ValidateFrozen(snapshot Snapshot, frozen sdkplacement.Placement) error {
	frozen = sdkplacement.Normalize(frozen)
	if err := sdkplacement.ValidateSealed(frozen); err != nil {
		return err
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		return err
	}
	profile, ok := modelprofile.Lookup(snapshot.Profiles, frozen.ProfileID)
	if !ok {
		return fmt.Errorf("control/placement: frozen placement references unknown profile %q", frozen.ProfileID)
	}
	if !profile.SupportsEffort(frozen.ReasoningEffort) {
		return fmt.Errorf("control/placement: frozen effort %q is unavailable for profile %q", frozen.ReasoningEffort, profile.ID)
	}
	var current sdkplacement.Placement
	var err error
	switch profile.Kind() {
	case modelprofile.BackendProvider:
		current, err = resolveProvider(snapshot, profile, frozen.ReasoningEffort)
	case modelprofile.BackendACP:
		current, err = resolveACP(snapshot, profile, frozen.ReasoningEffort)
	default:
		err = fmt.Errorf("control/placement: profile %q has no valid backend", profile.ID)
	}
	if err != nil {
		return err
	}
	if current.ConfigFingerprint != frozen.ConfigFingerprint || current.Fingerprint != frozen.Fingerprint {
		return fmt.Errorf("control/placement: referenced configuration for profile %q changed after placement was frozen", profile.ID)
	}
	return nil
}

// ValidateSnapshot validates intrinsic profile/binding state and every backend
// reference needed for deterministic resolution.
func ValidateSnapshot(snapshot Snapshot) error {
	if err := modelprofile.ValidateConfiguration(snapshot.Profiles); err != nil {
		return fmt.Errorf("control/placement: invalid profiles: %w", err)
	}
	if err := agentbinding.ValidateConfiguration(snapshot.Bindings, snapshot.Profiles); err != nil {
		return fmt.Errorf("control/placement: invalid bindings: %w", err)
	}
	if err := controlagents.ValidateConfiguration(snapshot.Agents); err != nil {
		return fmt.Errorf("control/placement: invalid Agent roster: %w", err)
	}
	models := configuredModels(snapshot.Models)
	for _, profile := range modelprofile.NormalizeConfiguration(snapshot.Profiles).Profiles {
		switch profile.Kind() {
		case modelprofile.BackendProvider:
			configured, ok := models[profile.Backend.Provider.ModelConfigID]
			if !ok {
				return fmt.Errorf("control/placement: provider profile %q references unknown model config %q", profile.ID, profile.Backend.Provider.ModelConfigID)
			}
			for _, choice := range profile.Effort.Choices {
				if !modelconfig.SupportsReasoningEffort(configured, choice.Canonical) {
					return fmt.Errorf("control/placement: provider profile %q declares unsupported effort %q", profile.ID, choice.Canonical)
				}
			}
		case modelprofile.BackendACP:
			agent, _, err := controlagents.ResolveAgent(snapshot.Agents, profile.Backend.ACP.AgentID)
			if err != nil {
				return fmt.Errorf("control/placement: ACP profile %q: %w", profile.ID, err)
			}
			if strings.TrimSpace(agent.ConnectionID) == "" {
				return fmt.Errorf("control/placement: ACP profile %q requires an external Agent", profile.ID)
			}
		}
	}
	return nil
}

func resolveProvider(snapshot Snapshot, profile modelprofile.ModelProfile, effort string) (sdkplacement.Placement, error) {
	configured, ok := configuredModels(snapshot.Models)[profile.Backend.Provider.ModelConfigID]
	if !ok {
		return sdkplacement.Placement{}, fmt.Errorf("control/placement: provider profile %q model config is unavailable", profile.ID)
	}
	fingerprint, err := providerConfigFingerprint(configured)
	if err != nil {
		return sdkplacement.Placement{}, err
	}
	return sdkplacement.Seal(sdkplacement.Placement{
		Kind:              sdkplacement.KindModel,
		ProfileID:         profile.ID,
		Model:             configured.ID,
		ReasoningEffort:   effort,
		ConfigFingerprint: fingerprint,
	})
}

func resolveACP(snapshot Snapshot, profile modelprofile.ModelProfile, effort string) (sdkplacement.Placement, error) {
	agent, connection, err := controlagents.ResolveAgent(snapshot.Agents, profile.Backend.ACP.AgentID)
	if err != nil {
		return sdkplacement.Placement{}, fmt.Errorf("control/placement: resolve ACP profile %q: %w", profile.ID, err)
	}
	values := maps.Clone(profile.Backend.ACP.SessionDefaults)
	wireEffort, ok := profile.WireEffort(effort)
	if !ok {
		return sdkplacement.Placement{}, fmt.Errorf("control/placement: effort %q is unavailable for profile %q", effort, profile.ID)
	}
	if configID := strings.TrimSpace(profile.Effort.ACPConfigID); configID != "" {
		if values == nil {
			values = make(map[string]string, 1)
		}
		values[configID] = wireEffort
	}
	fingerprint, err := acpConfigFingerprint(profile, agent, connection)
	if err != nil {
		return sdkplacement.Placement{}, err
	}
	return sdkplacement.Seal(sdkplacement.Placement{
		Kind:                    sdkplacement.KindAgent,
		ProfileID:               profile.ID,
		Agent:                   agent.ID,
		Model:                   profile.Backend.ACP.RemoteModelID,
		ReasoningEffort:         effort,
		ReasoningEffortConfigID: strings.TrimSpace(profile.Effort.ACPConfigID),
		SessionConfigValues:     values,
		ConfigFingerprint:       fingerprint,
	})
}

func validateHandlePurpose(handle agentbinding.Handle, purpose Purpose) error {
	switch purpose {
	case PurposeSpawn, PurposeDelegate:
		if !agentbinding.IsDelegation(handle) {
			return fmt.Errorf("control/placement: handle %q is not valid for %s", handle, purpose)
		}
	case PurposeDirect:
		if !agentbinding.IsDirectRun(handle) {
			return fmt.Errorf("control/placement: handle %q is not directly runnable", handle)
		}
	case PurposeGuardian:
		if handle != agentbinding.HandleGuardian {
			return fmt.Errorf("control/placement: purpose guardian requires the guardian handle")
		}
	case PurposeReviewer:
		if handle != agentbinding.HandleReviewer {
			return fmt.Errorf("control/placement: purpose reviewer requires the reviewer handle")
		}
	default:
		return fmt.Errorf("control/placement: unsupported purpose %q", purpose)
	}
	return nil
}

func configuredModels(in []modelconfig.Config) map[string]modelconfig.Config {
	out := make(map[string]modelconfig.Config, len(in))
	for _, raw := range in {
		configured := modelconfig.NormalizeConfig(raw)
		if configured.ID != "" {
			out[configured.ID] = configured
		}
	}
	return out
}

func providerConfigFingerprint(raw modelconfig.Config) (string, error) {
	configured := modelconfig.NormalizeConfig(raw)
	configured.HTTPClient = nil
	configured.Token = ""
	payload, err := json.Marshal(configured)
	if err != nil {
		return "", fmt.Errorf("control/placement: encode provider config fingerprint: %w", err)
	}
	return hashPayload("provider-config-v1", payload), nil
}

func acpConfigFingerprint(profile modelprofile.ModelProfile, agent controlagents.Agent, connection controlagents.Connection) (string, error) {
	payload, err := json.Marshal(struct {
		Profile           modelprofile.ModelProfile `json:"profile"`
		AgentID           string                    `json:"agent_id"`
		ConnectionID      string                    `json:"connection_id"`
		LaunchFingerprint string                    `json:"launch_fingerprint"`
	}{
		Profile:           modelprofile.Normalize(profile),
		AgentID:           strings.TrimSpace(agent.ID),
		ConnectionID:      strings.TrimSpace(connection.ID),
		LaunchFingerprint: controlagents.LaunchFingerprint(connection.Launcher),
	})
	if err != nil {
		return "", fmt.Errorf("control/placement: encode ACP config fingerprint: %w", err)
	}
	return hashPayload("acp-config-v1", payload), nil
}

func hashPayload(domain string, payload []byte) string {
	sum := sha256.Sum256(append([]byte(domain+"\x00"), payload...))
	return "sha256:" + hex.EncodeToString(sum[:])
}
