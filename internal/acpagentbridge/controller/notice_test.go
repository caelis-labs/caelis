package controller

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestACPNoticeHasOneTransientProjectionAndNoModelMessage(t *testing.T) {
	notice := client.Notice{SessionUpdate: "notice", Severity: "warning", Title: "Review approved", Description: "Read-only request"}
	event := normalizeACPUpdateEvent(time.Now, session.ControllerBinding{ControllerID: "codex", Label: "codex"}, "remote", "turn-1", notice)
	if event == nil || event.Notice == nil || event.Notice.Text != "Review approved\nRead-only request" || event.Notice.Level != "warning" {
		t.Fatalf("event = %#v", event)
	}
	if native := acpEnvelopeFromUpdate(client.UpdateEnvelope{SessionID: "remote", Update: notice}, event, nil); native != nil {
		t.Fatalf("duplicate native notice = %#v", native)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var decoded session.Event
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, value := range []*session.Event{event, &decoded} {
		if _, ok := session.ModelMessageOf(value); ok || session.IsInvocationVisibleEvent(value) || session.IsCanonicalHistoryEvent(value) || session.EventMatchesPageVisibility(value, session.EventPageClientReplay) {
			t.Fatalf("notice became history: %#v", value)
		}
		base := projection.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "local"}, value, projection.SessionEventTransport{})
		projected := projection.ProjectSessionEventEnvelope(base, value)
		if len(projected) != 1 || projected[0].Kind != eventstream.KindNotice || projected[0].Notice != event.Notice.Text || projected[0].Permission != nil || projected[0].ApprovalReview != nil || projected[0].Update != nil {
			t.Fatalf("notice projection = %#v", projected)
		}
	}
}
