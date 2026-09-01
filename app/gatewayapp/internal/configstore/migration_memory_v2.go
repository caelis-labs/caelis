package configstore

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/control/memorybinding"
)

// These types exist only to decode the pre-BindingRef schema-v2 wire shape.
// Concrete product identity never enters the current AppConfig or Runtime API.
type legacyV2MemoryConfiguration struct {
	Enabled  bool                         `json:"enabled,omitempty"`
	Endpoint memorybinding.EndpointConfig `json:"endpoint,omitempty"`
	Bots     []legacyV2MemoryIdentity     `json:"bots,omitempty"`
}

type legacyV2MemoryIdentity struct {
	BotID               string                        `json:"bot_id"`
	RuntimeActorRef     memorybinding.RuntimeActorRef `json:"runtime_actor_ref"`
	MemoryIdentityRef   string                        `json:"memory_identity_ref"`
	PrincipalRef        string                        `json:"principal_ref"`
	IssuerCredentialRef string                        `json:"issuer_credential_ref"`
	Private             legacyV2AudienceBinding       `json:"private,omitzero"`
	Shared              legacyV2AudienceBinding       `json:"shared,omitzero"`
	BindingVersion      uint64                        `json:"binding_version"`
}

type legacyV2AudienceBinding struct {
	ViewRef  string `json:"view_ref"`
	GrantRef string `json:"grant_ref"`
}

func decodeCurrentAppConfigWithMemoryMigration(data []byte) (AppConfig, bool, error) {
	memory, migrated, err := migrateLegacyV2Memory(data)
	if err != nil {
		return AppConfig{}, false, wrapInvalidMemoryConfiguration(err)
	}
	if !migrated {
		if err := validateCurrentMemoryWire(data); err != nil {
			return AppConfig{}, false, wrapInvalidMemoryConfiguration(err)
		}
	}
	var doc AppConfig
	if err := json.Unmarshal(data, &doc); err != nil {
		return AppConfig{}, false, fmt.Errorf("gatewayapp: decode app config: %w", err)
	}
	if migrated {
		doc.Memory = memory
	}
	if err := validateCurrentRecordIdentities(doc); err != nil {
		return AppConfig{}, false, err
	}
	doc = Normalize(doc)
	if err := Validate(doc); err != nil {
		return AppConfig{}, false, err
	}
	return doc, migrated, nil
}

func validateCurrentMemoryWire(data []byte) error {
	var top struct {
		Memory json.RawMessage `json:"memory"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return fmt.Errorf("gatewayapp: decode app config: %w", err)
	}
	if len(top.Memory) == 0 || string(top.Memory) == "null" {
		return nil
	}
	var current memorybinding.Configuration
	if err := json.Unmarshal(top.Memory, &current); err != nil {
		return fmt.Errorf("decode current Memory configuration: %w", err)
	}
	return nil
}

func hasLegacyV2Memory(data []byte) (bool, error) {
	_, migrated, err := migrateLegacyV2Memory(data)
	return migrated, wrapInvalidMemoryConfiguration(err)
}

func migrateLegacyV2Memory(data []byte) (memorybinding.Configuration, bool, error) {
	var top struct {
		Memory json.RawMessage `json:"memory"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return memorybinding.Configuration{}, false, fmt.Errorf("gatewayapp: decode app config: %w", err)
	}
	if len(top.Memory) == 0 || string(top.Memory) == "null" {
		return memorybinding.Configuration{}, false, nil
	}
	var shape struct {
		Bots     json.RawMessage `json:"bots"`
		Bindings json.RawMessage `json:"bindings"`
	}
	if err := json.Unmarshal(top.Memory, &shape); err != nil {
		return memorybinding.Configuration{}, false, fmt.Errorf("gatewayapp: decode Memory configuration: %w", err)
	}
	if len(shape.Bots) == 0 || string(shape.Bots) == "null" {
		return memorybinding.Configuration{}, false, nil
	}
	if len(shape.Bindings) != 0 && string(shape.Bindings) != "null" {
		return memorybinding.Configuration{}, false, fmt.Errorf("gatewayapp: invalid Memory binding: legacy bots and current bindings cannot coexist")
	}
	var legacy legacyV2MemoryConfiguration
	if err := json.Unmarshal(top.Memory, &legacy); err != nil {
		return memorybinding.Configuration{}, false, fmt.Errorf("gatewayapp: decode legacy Memory configuration: %w", err)
	}
	converted := memorybinding.Configuration{Enabled: legacy.Enabled, Endpoint: legacy.Endpoint}
	seenIdentity := make(map[string]struct{}, len(legacy.Bots))
	for index, raw := range legacy.Bots {
		identity := normalizeLegacyV2MemoryIdentity(raw)
		if identity.BotID == "" || identity.RuntimeActorRef == "" || identity.MemoryIdentityRef == "" ||
			identity.PrincipalRef == "" || identity.IssuerCredentialRef == "" || identity.BindingVersion == 0 {
			return memorybinding.Configuration{}, false, fmt.Errorf("gatewayapp: invalid Memory binding: legacy identity, actor, principal, issuer reference, and version are required")
		}
		if _, duplicate := seenIdentity[identity.BotID]; duplicate {
			return memorybinding.Configuration{}, false, fmt.Errorf("gatewayapp: invalid Memory binding: duplicate legacy identity")
		}
		seenIdentity[identity.BotID] = struct{}{}
		private, err := convertLegacyV2Audience(identity, index, memorybinding.OutputAudiencePrivate, identity.Private)
		if err != nil {
			return memorybinding.Configuration{}, false, err
		}
		shared, err := convertLegacyV2Audience(identity, index, memorybinding.OutputAudienceShared, identity.Shared)
		if err != nil {
			return memorybinding.Configuration{}, false, err
		}
		if private.BindingRef == "" && shared.BindingRef == "" {
			return memorybinding.Configuration{}, false, fmt.Errorf("gatewayapp: invalid Memory binding: legacy identity has no audience binding")
		}
		if private.BindingRef != "" {
			converted.Bindings = append(converted.Bindings, private)
		}
		if shared.BindingRef != "" {
			converted.Bindings = append(converted.Bindings, shared)
		}
	}
	if len(converted.Bindings) == 0 {
		return memorybinding.Configuration{}, false, fmt.Errorf("gatewayapp: invalid Memory binding: legacy configuration has no access binding")
	}
	converted.DefaultBindingRef = converted.Bindings[0].BindingRef
	// The old wire selected a concrete identity outside AppConfig. With more
	// than one historical identity there is no safe implicit equivalent, so the
	// atomic wire migration preserves data but disables activation until a host
	// explicitly chooses a new opaque default and re-enables the feature.
	if len(legacy.Bots) > 1 {
		converted.Enabled = false
	}
	return converted, true, nil
}

func normalizeLegacyV2MemoryIdentity(in legacyV2MemoryIdentity) legacyV2MemoryIdentity {
	in.BotID = strings.TrimSpace(in.BotID)
	in.RuntimeActorRef = memorybinding.RuntimeActorRef(strings.TrimSpace(string(in.RuntimeActorRef)))
	in.MemoryIdentityRef = strings.TrimSpace(in.MemoryIdentityRef)
	in.PrincipalRef = strings.TrimSpace(in.PrincipalRef)
	in.IssuerCredentialRef = strings.TrimSpace(in.IssuerCredentialRef)
	in.Private.ViewRef = strings.TrimSpace(in.Private.ViewRef)
	in.Private.GrantRef = strings.TrimSpace(in.Private.GrantRef)
	in.Shared.ViewRef = strings.TrimSpace(in.Shared.ViewRef)
	in.Shared.GrantRef = strings.TrimSpace(in.Shared.GrantRef)
	return in
}

func convertLegacyV2Audience(
	identity legacyV2MemoryIdentity,
	index int,
	audience memorybinding.OutputAudience,
	binding legacyV2AudienceBinding,
) (memorybinding.AccessBinding, error) {
	if (binding.ViewRef == "") != (binding.GrantRef == "") {
		return memorybinding.AccessBinding{}, fmt.Errorf("gatewayapp: invalid Memory binding: legacy View and Grant must be configured together")
	}
	if binding.ViewRef == "" {
		return memorybinding.AccessBinding{}, nil
	}
	return memorybinding.AccessBinding{
		BindingRef:          memorybinding.BindingRef(fmt.Sprintf("legacy-%03d-%s", index+1, audience)),
		RuntimeActorRef:     identity.RuntimeActorRef,
		PrincipalRef:        identity.PrincipalRef,
		IssuerCredentialRef: identity.IssuerCredentialRef,
		ViewRef:             binding.ViewRef,
		GrantRef:            binding.GrantRef,
		Audience:            audience,
		BindingVersion:      identity.BindingVersion,
	}, nil
}
