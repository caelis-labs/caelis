package codex

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/adapters/codex/internal/appserver"
)

// The pinned ACP SDK cannot encode the draft session/update notice variant.
// Use a negotiated extension with the draft SessionNotification payload until
// the SDK supports it; then replace this sender with connection.SessionUpdate.
const sessionNoticeMethod = "_session/notice"
const sessionNoticeCapability = "session_notice"

type sessionNotice struct {
	SessionUpdate string `json:"sessionUpdate"`
	Severity      string `json:"severity"`
	Title         string `json:"title"`
	Description   string `json:"description,omitempty"`
}

func codexNotice(notification appserver.Notification) *sessionNotice {
	switch notification.Method {
	case "warning", "deprecationNotice", "configWarning", "guardianWarning":
	default:
		return nil
	}
	var value struct {
		Message string `json:"message"`
		Summary string `json:"summary"`
		Details string `json:"details"`
	}
	if json.Unmarshal(notification.Params, &value) != nil {
		return nil
	}
	title := firstNonEmpty(value.Summary, value.Message)
	if strings.TrimSpace(title) == "" {
		return nil
	}
	return &sessionNotice{SessionUpdate: "notice", Severity: "warning", Title: title, Description: value.Details}
}

func (r *sessionRoute) publishNotice(notice *sessionNotice) error {
	r.agent.mu.Lock()
	supported := r.agent.sessionNotices
	r.agent.mu.Unlock()
	if !supported {
		return nil
	}
	return r.agent.connection.NotifyExtension(context.Background(), sessionNoticeMethod, struct {
		SessionID string         `json:"sessionId"`
		Update    *sessionNotice `json:"update"`
	}{SessionID: r.state.threadID, Update: notice})
}
