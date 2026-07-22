package placement

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Kind identifies the reusable execution backend captured by a Placement.
type Kind string

const (
	// KindAgent runs one already configured Agent endpoint. Agent placements may
	// also select one remote model and exact session configuration values.
	KindAgent Kind = "agent"
	// KindModel runs one configured local model through a host endpoint factory.
	KindModel Kind = "model"
)

// Placement is one immutable execution decision resolved by a host Control
// layer before durable work is prepared.
//
// ProfileID is an opaque audit identity and is never a Runtime lookup key.
// SessionConfigValues contains exact remote option IDs and wire values; those
// strings are case-preserving because ACP endpoints may treat them as
// case-sensitive. ReasoningEffortConfigID identifies which option must be
// applied after the non-effort defaults. ConfigFingerprint identifies the referenced host
// configuration. Fingerprint seals the remaining normalized fields.
type Placement struct {
	Kind                    Kind              `json:"kind,omitempty"`
	ProfileID               string            `json:"profile_id,omitempty"`
	Agent                   string            `json:"agent,omitempty"`
	Model                   string            `json:"model,omitempty"`
	ReasoningEffort         string            `json:"reasoning_effort,omitempty"`
	ReasoningEffortConfigID string            `json:"reasoning_effort_config_id,omitempty"`
	SessionConfigValues     map[string]string `json:"session_config_values,omitempty"`
	ConfigFingerprint       string            `json:"config_fingerprint,omitempty"`
	Fingerprint             string            `json:"fingerprint,omitempty"`
}

const fingerprintDomain = "caelis-agent-sdk-placement-v1\x00"

// Normalize returns a canonical detached Placement. Backend identities and
// remote wire values retain case; only Kind, canonical effort, and hash text
// are case-normalized.
func Normalize(in Placement) Placement {
	return Placement{
		Kind:                    Kind(strings.ToLower(strings.TrimSpace(string(in.Kind)))),
		ProfileID:               strings.TrimSpace(in.ProfileID),
		Agent:                   strings.TrimSpace(in.Agent),
		Model:                   strings.TrimSpace(in.Model),
		ReasoningEffort:         strings.ToLower(strings.TrimSpace(in.ReasoningEffort)),
		ReasoningEffortConfigID: strings.TrimSpace(in.ReasoningEffortConfigID),
		SessionConfigValues:     NormalizeSessionConfigValues(in.SessionConfigValues),
		ConfigFingerprint:       strings.ToLower(strings.TrimSpace(in.ConfigFingerprint)),
		Fingerprint:             strings.ToLower(strings.TrimSpace(in.Fingerprint)),
	}
}

// Validate rejects incomplete, contradictory, or tampered Placement values.
// An unsealed structurally valid value is allowed for SDK callers that do not
// require durable recovery. Use ValidateSealed at a durable execution boundary.
func Validate(raw Placement) error {
	value := Normalize(raw)
	switch value.Kind {
	case KindAgent:
		if value.Agent == "" {
			return fmt.Errorf("agent-sdk/placement: Agent placement requires an Agent")
		}
	case KindModel:
		if value.Model == "" {
			return fmt.Errorf("agent-sdk/placement: model placement requires a model")
		}
		if value.Agent != "" {
			return fmt.Errorf("agent-sdk/placement: model placement must not select an Agent")
		}
		if len(value.SessionConfigValues) != 0 {
			return fmt.Errorf("agent-sdk/placement: model placement must not carry remote session configuration")
		}
		if value.ReasoningEffortConfigID != "" {
			return fmt.Errorf("agent-sdk/placement: model placement must not carry a remote effort configuration ID")
		}
	default:
		return fmt.Errorf("agent-sdk/placement: unsupported placement kind %q", value.Kind)
	}
	if err := ValidateSessionConfigValues(raw.SessionConfigValues); err != nil {
		return err
	}
	if value.ReasoningEffortConfigID != "" {
		if _, ok := lookupSessionConfigValue(value.SessionConfigValues, value.ReasoningEffortConfigID); !ok {
			return fmt.Errorf("agent-sdk/placement: effort configuration %q has no session value", value.ReasoningEffortConfigID)
		}
	}
	if value.Fingerprint == "" {
		return nil
	}
	if value.ConfigFingerprint == "" {
		return fmt.Errorf("agent-sdk/placement: sealed placement requires a configuration fingerprint")
	}
	want, err := ComputeFingerprint(value)
	if err != nil {
		return err
	}
	if value.Fingerprint != want {
		return fmt.Errorf("agent-sdk/placement: placement fingerprint is invalid")
	}
	return nil
}

func lookupSessionConfigValue(values map[string]string, id string) (string, bool) {
	for key, value := range values {
		if strings.EqualFold(strings.TrimSpace(key), strings.TrimSpace(id)) {
			return value, true
		}
	}
	return "", false
}

// ValidateSealed validates a durable Placement and requires both its host
// configuration fingerprint and its canonical placement fingerprint.
func ValidateSealed(raw Placement) error {
	value := Normalize(raw)
	if value.ConfigFingerprint == "" {
		return fmt.Errorf("agent-sdk/placement: durable placement requires a configuration fingerprint")
	}
	if value.Fingerprint == "" {
		return fmt.Errorf("agent-sdk/placement: durable placement requires a placement fingerprint")
	}
	return Validate(value)
}

// Seal normalizes, validates, and fingerprints one durable Placement. The
// caller must first supply ConfigFingerprint from the referenced host config.
func Seal(raw Placement) (Placement, error) {
	value := Normalize(raw)
	value.Fingerprint = ""
	if value.ConfigFingerprint == "" {
		return Placement{}, fmt.Errorf("agent-sdk/placement: durable placement requires a configuration fingerprint")
	}
	if err := Validate(value); err != nil {
		return Placement{}, err
	}
	fingerprint, err := ComputeFingerprint(value)
	if err != nil {
		return Placement{}, err
	}
	value.Fingerprint = fingerprint
	return value, nil
}

// ComputeFingerprint returns the versioned canonical fingerprint for a
// Placement. Any supplied Fingerprint field is deliberately ignored.
func ComputeFingerprint(raw Placement) (string, error) {
	value := Normalize(raw)
	value.Fingerprint = ""
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("agent-sdk/placement: encode placement fingerprint: %w", err)
	}
	sum := sha256.Sum256(append([]byte(fingerprintDomain), payload...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// NormalizeSessionConfigValues returns a canonical detached copy of exact ACP
// session configuration values. Configuration IDs and values retain case; only
// surrounding whitespace is removed.
func NormalizeSessionConfigValues(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make(map[string]string, len(in))
	for _, key := range keys {
		normalizedID := strings.TrimSpace(key)
		if _, exists := out[normalizedID]; exists {
			continue
		}
		out[normalizedID] = strings.TrimSpace(in[key])
	}
	return out
}

// ValidateSessionConfigValues rejects empty values and configuration IDs that
// collide after case-insensitive normalization. Callers should validate the raw
// map before normalizing it so collisions cannot be silently collapsed.
func ValidateSessionConfigValues(in map[string]string) error {
	seen := make(map[string]string, len(in))
	for rawKey, rawValue := range in {
		key := strings.TrimSpace(rawKey)
		value := strings.TrimSpace(rawValue)
		if key == "" {
			return fmt.Errorf("agent-sdk/placement: session configuration ID is required")
		}
		if value == "" {
			return fmt.Errorf("agent-sdk/placement: session configuration %q requires a value", key)
		}
		folded := strings.ToLower(key)
		if previous, ok := seen[folded]; ok {
			return fmt.Errorf("agent-sdk/placement: session configuration IDs %q and %q normalize to the same ID", previous, rawKey)
		}
		seen[folded] = rawKey
	}
	return nil
}
