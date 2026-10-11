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
	notices []acp.SessionNotification
}

func (c *recordingNoticeClient) SessionUpdate(ctx context.Context, note acp.SessionNotification) error {
	if note.Update.Notice == nil {
		return c.recordingACPClient.SessionUpdate(ctx, note)
	}
	c.mu.Lock()
	c.notices = append(c.notices, note)
	c.mu.Unlock()
	return nil
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
		if notice == nil || notice.Title != tc.title || stringPointerValue(notice.Description) != tc.description || notice.SessionUpdate != "notice" || notice.Severity != "warning" {
			t.Fatalf("notice = %#v", notice)
		}
		route := &sessionRoute{agent: &agent{}, state: &sessionState{threadID: "child"}}
		if updates, _, err := route.translateNotification(notification); err != nil || len(updates) != 0 {
			t.Fatalf("notice leaked to text: %#v, %v", updates, err)
		}
		// Clients without standard notices capability receive no notice or fallback text.
		if err := route.publish(notification); err != nil {
			t.Fatal(err)
		}
	}
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
