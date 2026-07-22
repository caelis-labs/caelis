package controlclient

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// suppressHistoricalChildStreamMirror recognizes the durable child live-frame
// copies written by the retired ChildRecorder. They remain in old Session
// stores but are no longer part of the Session feed. Control facts that happen
// to use mirror visibility, especially approvals and participant lifecycle,
// stay visible.
func suppressHistoricalChildStreamMirror(event *session.Event) bool {
	if event == nil || !session.IsMirror(event) || event.ChildOrigin == nil {
		return false
	}
	if strings.TrimSpace(event.ApprovalRequestID) != "" {
		return false
	}
	if event.Protocol != nil && strings.TrimSpace(event.Protocol.Method) == session.ProtocolMethodRequestPermission {
		return false
	}
	return session.EventTypeOf(event) != session.EventTypeParticipant
}
