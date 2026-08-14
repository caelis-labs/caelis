package acpagentbridge

import (
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func acpEnvelopeSessionID(env eventstream.Envelope, fallbackSessionID string) string {
	sessionID := strings.TrimSpace(env.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(fallbackSessionID)
	}
	return sessionID
}
