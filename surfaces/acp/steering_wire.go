package acp

import (
	"encoding/json"

	"github.com/caelis-labs/caelis/internal/acpagentbridge/steeringwire"
)

// SessionSteeringRequest is the product ACP Surface request for the advertised
// _session/steering extension.
type SessionSteeringRequest = steeringwire.SessionSteeringRequest

// SessionSteeringResponse is the product ACP Surface response for the
// advertised _session/steering extension.
type SessionSteeringResponse = steeringwire.SessionSteeringResponse

// SessionSteeringOutcome retains known and future extension outcomes.
type SessionSteeringOutcome = steeringwire.SessionSteeringOutcome

const SessionSteeringPromptRequired = steeringwire.SessionSteeringPromptRequired

func decodeSessionSteeringOptions(meta map[string]json.RawMessage) (steeringwire.SessionSteeringOptions, error) {
	return steeringwire.DecodeSessionSteeringOptions(meta)
}
