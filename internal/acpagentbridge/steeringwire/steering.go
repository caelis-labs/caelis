// Package steeringwire owns Host-private ACP extension wire values shared by the
// product Agent surface and external-Agent client adapters.
package steeringwire

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// SessionSteeringOutcome is the Agent-selected disposition of one steering
// request. Unknown values are retained so newer peer outcomes can cross the
// wire without requiring a schema update.
type SessionSteeringOutcome string

// SessionSteeringIdleBehavior controls how an Agent reports steering input
// received without a running Turn. It does not authorize the Agent to start a
// replacement Turn.
type SessionSteeringIdleBehavior string

const (
	SessionSteeringMetaKey = "steering"

	SessionSteeringInjected       SessionSteeringOutcome = "injected"
	SessionSteeringStartedNewTurn SessionSteeringOutcome = "startedNewTurn"
	SessionSteeringPromptRequired SessionSteeringOutcome = "promptRequired"
	SessionSteeringFailed         SessionSteeringOutcome = "failed"

	SessionSteeringIdlePromptRequired SessionSteeringIdleBehavior = "promptRequired"
)

// SessionSteeringCapability is advertised at initialize response
// _meta.steering when the Agent accepts _session/steering requests.
type SessionSteeringCapability struct {
	Supported bool `json:"supported"`
}

// DecodeSessionSteeringCapability validates the recognized initialize
// response _meta.steering capability. Missing fields mean unsupported; a
// present supported field must be a JSON boolean.
func DecodeSessionSteeringCapability(meta map[string]json.RawMessage) (SessionSteeringCapability, error) {
	raw, ok := meta[SessionSteeringMetaKey]
	if !ok {
		return SessionSteeringCapability{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err == nil {
			err = fmt.Errorf("must be an object")
		}
		return SessionSteeringCapability{}, fmt.Errorf("_meta.%s: %w", SessionSteeringMetaKey, err)
	}
	supportedRaw, ok := fields["supported"]
	if !ok {
		return SessionSteeringCapability{}, nil
	}
	if bytes.Equal(bytes.TrimSpace(supportedRaw), []byte("null")) {
		return SessionSteeringCapability{}, fmt.Errorf("_meta.%s.supported must be a boolean", SessionSteeringMetaKey)
	}
	var supported bool
	if err := json.Unmarshal(supportedRaw, &supported); err != nil {
		return SessionSteeringCapability{}, fmt.Errorf("_meta.%s.supported: %w", SessionSteeringMetaKey, err)
	}
	return SessionSteeringCapability{Supported: supported}, nil
}

// SessionSteeringOptions is the recognized _meta.steering request vocabulary.
// Unknown sibling fields remain available to newer peers through the original
// request Meta value.
type SessionSteeringOptions struct {
	IdleBehavior SessionSteeringIdleBehavior `json:"idleBehavior,omitempty"`
}

// SessionSteeringRequest carries one ACP prompt to a Session without assigning
// a prompt or Turn lifecycle to the request itself.
type SessionSteeringRequest struct {
	SessionID string                     `json:"sessionId"`
	Prompt    []json.RawMessage          `json:"prompt"`
	Meta      map[string]json.RawMessage `json:"_meta,omitempty"`
}

// SessionSteeringResponse reports how the Agent accepted a steering request.
// Reason is optional extension detail, including noRunningTurn for the
// promptRequired outcome.
type SessionSteeringResponse struct {
	Outcome SessionSteeringOutcome `json:"outcome"`
	Reason  string                 `json:"reason,omitempty"`
}

// DecodeSessionSteeringOptions validates and returns the steering options
// owned by this protocol version while ignoring unknown extension fields.
func DecodeSessionSteeringOptions(meta map[string]json.RawMessage) (SessionSteeringOptions, error) {
	raw, ok := meta[SessionSteeringMetaKey]
	if !ok {
		return SessionSteeringOptions{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err == nil {
			err = fmt.Errorf("must be an object")
		}
		return SessionSteeringOptions{}, fmt.Errorf("_meta.%s: %w", SessionSteeringMetaKey, err)
	}
	var options SessionSteeringOptions
	if idleRaw, ok := fields["idleBehavior"]; ok {
		if err := json.Unmarshal(idleRaw, &options.IdleBehavior); err != nil {
			return SessionSteeringOptions{}, fmt.Errorf("_meta.%s.idleBehavior: %w", SessionSteeringMetaKey, err)
		}
		if options.IdleBehavior != SessionSteeringIdlePromptRequired {
			return SessionSteeringOptions{}, fmt.Errorf(
				"_meta.%s.idleBehavior %q is unsupported",
				SessionSteeringMetaKey,
				options.IdleBehavior,
			)
		}
	}
	return options, nil
}
