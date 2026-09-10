package codex

import (
	"context"
	"encoding/json"
	"testing"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/adapters/codex/internal/appserver"
)

type recordingNoticeClient struct {
	recordingACPClient
	notices []struct {
		SessionID string        `json:"sessionId"`
		Update    sessionNotice `json:"update"`
	}
}

func (c *recordingNoticeClient) HandleExtensionMethod(_ context.Context, method string, raw json.RawMessage) (any, error) {
	if method != sessionNoticeMethod {
		return nil, acp.NewMethodNotFound(method)
	}
	var note struct {
		SessionID string        `json:"sessionId"`
		Update    sessionNotice `json:"update"`
	}
	if err := json.Unmarshal(raw, &note); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.notices = append(c.notices, note)
	c.mu.Unlock()
	return nil, nil
}

func TestCodexNoticesNeverBecomeReasoning(t *testing.T) {
	for _, tc := range []struct{ method, params, title, description string }{
		{"warning", `{"message":"Warning"}`, "Warning", ""},
		{"guardianWarning", `{"message":"Review approved"}`, "Review approved", ""},
		{"configWarning", `{"summary":"Configuration warning","details":"Check config"}`, "Configuration warning", "Check config"},
		{"deprecationNotice", `{"summary":"Deprecated","details":"Use replacement"}`, "Deprecated", "Use replacement"},
	} {
		notification := appserver.Notification{Method: tc.method, Params: json.RawMessage(tc.params)}
		notice := codexNotice(notification)
		if notice == nil || notice.Title != tc.title || notice.Description != tc.description || notice.SessionUpdate != "notice" || notice.Severity != "warning" {
			t.Fatalf("notice = %#v", notice)
		}
		route := &sessionRoute{agent: &agent{}, state: &sessionState{threadID: "child"}}
		if updates, _, err := route.translateNotification(notification); err != nil || len(updates) != 0 {
			t.Fatalf("notice leaked to text: %#v, %v", updates, err)
		}
		// An unnegotiated client may ignore notices; no fallback thought or text.
		if err := route.publish(notification); err != nil {
			t.Fatal(err)
		}
	}
}
