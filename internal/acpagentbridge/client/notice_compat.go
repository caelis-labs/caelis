package client

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Notice is the draft ACP v1 session/update notice payload. Replace this local
// wire decoder with the SDK variant when session notices are supported there.
// Unknown severities remain display hints; optional malformed fields are ignored.
type Notice struct {
	SessionUpdate string         `json:"sessionUpdate"`
	Severity      string         `json:"severity"`
	Title         string         `json:"title"`
	Description   string         `json:"description,omitempty"`
	Meta          map[string]any `json:"_meta,omitempty"`
}

// This negotiated fallback carries the same SessionNotification payload as
// session/update. Remove it with the Codex sender once the SDK can encode Notice.
const sessionNoticeMethod = "_session/notice"
const sessionNoticeCapability = "session_notice"

func decodeNotice(raw json.RawMessage) (Notice, error) {
	var fields struct {
		Severity    *string         `json:"severity"`
		Title       *string         `json:"title"`
		Description json.RawMessage `json:"description"`
		Meta        json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Notice{}, err
	}
	if fields.Severity == nil || fields.Title == nil || strings.TrimSpace(*fields.Title) == "" {
		return Notice{}, fmt.Errorf("invalid ACP notice: severity and non-empty title required")
	}
	notice := Notice{SessionUpdate: "notice", Severity: *fields.Severity, Title: *fields.Title}
	_ = json.Unmarshal(fields.Description, &notice.Description)
	_ = json.Unmarshal(fields.Meta, &notice.Meta)
	return notice, nil
}

func (c *Client) handleNotice(params json.RawMessage) {
	var note SessionNotification
	if json.Unmarshal(params, &note) != nil {
		return
	}
	var probe struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if json.Unmarshal(note.Update, &probe) == nil && probe.SessionUpdate == "notice" {
		c.handleUpdate(params)
	}
}
