package acpagentbridge

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
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

func acpFilterSourceFromSessionEvent(ref session.SessionRef, event *session.Event) acpFilterSource {
	base := projector.EnvelopeBaseFromSessionEvent(ref, event, projector.SessionEventTransport{})
	return acpFilterSourceFromEnvelope(base, ref.SessionID)
}
