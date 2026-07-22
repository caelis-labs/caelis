package controlclient

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestSuppressHistoricalChildStreamMirrorPreservesControlFacts(t *testing.T) {
	origin := &session.EventChildOrigin{
		Scope: session.EventChildScopeSubagent, ScopeID: "task-1", TaskID: "task-1",
		SourceEventID: "legacy:1", ParentTool: session.EventParentTool{CallID: "spawn-1", Name: "SPAWN"},
	}
	permission := &session.EventProtocol{Method: session.ProtocolMethodRequestPermission}
	participant := session.NewParticipantProtocol(session.ProtocolParticipant{Action: "attached"})
	for _, test := range []struct {
		name  string
		event *session.Event
		want  bool
	}{
		{
			name:  "child live frame",
			event: &session.Event{Type: session.EventTypeAssistant, Visibility: session.VisibilityMirror, ChildOrigin: origin},
			want:  true,
		},
		{
			name:  "approval",
			event: &session.Event{Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror, ChildOrigin: origin, ApprovalRequestID: "approval-1", Protocol: permission},
		},
		{
			name:  "participant lifecycle",
			event: &session.Event{Type: session.EventTypeParticipant, Visibility: session.VisibilityMirror, ChildOrigin: origin, Protocol: &participant},
		},
		{
			name:  "ordinary control mirror",
			event: &session.Event{Type: session.EventTypeLifecycle, Visibility: session.VisibilityMirror},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := suppressHistoricalChildStreamMirror(test.event); got != test.want {
				t.Fatalf("suppressHistoricalChildStreamMirror() = %t, want %t", got, test.want)
			}
		})
	}
}
