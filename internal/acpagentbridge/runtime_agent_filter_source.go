package acpagentbridge

import (
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// acpFilterSource identifies one independently replay-deduplicated outbound
// ACP stream. It is bridge-local presentation state, derived from the
// canonical Envelope scope rather than added to an ACP update payload.
type acpFilterSource struct {
	SessionID string
	Scope     string
	ScopeID   string
}

func acpFilterSourceFromEnvelope(env eventstream.Envelope, fallbackSessionID string) acpFilterSource {
	sessionID := strings.TrimSpace(env.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(fallbackSessionID)
	}
	return acpFilterSource{
		SessionID: sessionID,
		Scope:     strings.TrimSpace(string(env.Scope)),
		ScopeID:   strings.TrimSpace(env.ScopeID),
	}
}
