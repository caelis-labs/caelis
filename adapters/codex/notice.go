package codex

import (
	"context"
	"encoding/json"
	"strings"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/adapters/codex/internal/appserver"
)

func codexNotice(notification appserver.Notification) *acp.SessionUpdateNotice {
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
	notice := &acp.SessionUpdateNotice{SessionUpdate: "notice", Severity: acp.NoticeSeverityWarning, Title: title}
	if value.Details != "" {
		notice.Description = acp.Ptr(value.Details)
	}
	return notice
}

func (r *sessionRoute) publishNotice(notice *acp.SessionUpdateNotice) error {
	r.agent.mu.Lock()
	supported := r.agent.sessionNotices
	r.agent.mu.Unlock()
	if !supported {
		return nil
	}
	return r.agent.connection.SessionUpdate(context.Background(), acp.SessionNotification{
		SessionId: acp.SessionId(r.state.threadID), Update: acp.SessionUpdate{Notice: notice},
	})
}
