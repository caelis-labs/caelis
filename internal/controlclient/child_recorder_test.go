package controlclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
)

func TestChildRecorderUsesParticipantControlAuthorityDuringActiveRuntimeLease(t *testing.T) {
	t.Parallel()

	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()}))
	parent, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "parent-leased",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: parent.SessionRef, OwnerID: "runtime-a", TTL: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	origin := session.EventChildOrigin{
		Scope: session.EventChildScopeSubagent, ScopeID: "task-1", TaskID: "task-1",
		DelegationID: "task-1", SourceEventID: "child-source-1",
		ParentTool: session.EventParentTool{CallID: "spawn-1", Name: "Spawn"},
	}
	stored, err := NewChildRecorder(sessions).Record(context.Background(), ChildRecordRequest{
		SessionRef: parent.SessionRef,
		Event:      &session.Event{Type: session.EventTypeLifecycle, Lifecycle: &session.EventLifecycle{Status: "running"}},
		Origin:     origin,
	})
	if err != nil {
		t.Fatalf("Record() with active runtime lease = %v", err)
	}
	if !session.IsMirror(stored) {
		t.Fatalf("stored child = %#v, want durable mirror", stored)
	}
}

func TestChildRecorderDeduplicatesStableSourceAndConflictsOnChangedPayload(t *testing.T) {
	ctx := context.Background()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string { return "parent-1" }}))
	parent, err := sessions.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := NewChildRecorder(sessions)
	origin := session.EventChildOrigin{
		Scope:         session.EventChildScopeSubagent,
		ScopeID:       "task-1",
		TaskID:        "task-1",
		DelegationID:  "task-1",
		ParticipantID: "child-1",
		ACPSessionID:  "acp-child-1",
		SourceEventID: "task-1:4",
		ParentTool:    session.EventParentTool{CallID: "spawn-1", Name: "Spawn"},
	}
	event := &session.Event{
		Type:       session.EventTypeLifecycle,
		Visibility: session.VisibilityUIOnly,
		Lifecycle:  &session.EventLifecycle{Status: "running"},
	}
	first, err := recorder.Record(ctx, ChildRecordRequest{SessionRef: parent.SessionRef, Event: event, Origin: origin})
	if err != nil {
		t.Fatalf("Record(first) error = %v", err)
	}
	retry, err := recorder.Record(ctx, ChildRecordRequest{SessionRef: parent.SessionRef, Event: event, Origin: origin})
	if err != nil {
		t.Fatalf("Record(retry) error = %v", err)
	}
	if first.ID == "" || retry.ID != first.ID || retry.Seq != first.Seq || !session.IsMirror(first) {
		t.Fatalf("retry = %#v first = %#v, want one durable mirror identity", retry, first)
	}

	changed := session.CloneEvent(event)
	changed.Lifecycle.Reason = "changed"
	_, err = recorder.Record(ctx, ChildRecordRequest{SessionRef: parent.SessionRef, Event: changed, Origin: origin})
	if !errors.Is(err, session.ErrEventConflict) {
		t.Fatalf("Record(changed) error = %v, want ErrEventConflict", err)
	}

	page, err := sessions.EventsPage(ctx, session.EventPageRequest{
		SessionRef: parent.SessionRef,
		Visibility: session.EventPageClientReplay,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].ChildOrigin == nil || page.Events[0].ChildOrigin.ParentTool.CallID != "spawn-1" {
		t.Fatalf("durable child page = %#v", page)
	}
}

func TestChildRecorderDurablyReplaysEveryScopedSemanticFamily(t *testing.T) {
	ctx := context.Background()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string { return "parent-1" }}))
	parent, err := sessions.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	line := 7
	oldText := "before"
	tests := []struct {
		name  string
		event *session.Event
	}{
		{name: "message", event: childProtocolUpdate(session.EventTypeAssistant, session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), MessageID: "shared-message", Content: session.ProtocolTextContent("child message")})},
		{name: "thought", event: childProtocolUpdate(session.EventTypeAssistant, session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentThought), MessageID: "shared-thought", Content: session.ProtocolTextContent("child thought")})},
		{name: "tool_call", event: childProtocolUpdate(session.EventTypeToolCall, session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeToolCall), ToolCallID: "shared-tool", Title: "Read", Kind: "read", Status: "pending", RawInput: map[string]any{"path": "child.txt"}})},
		{name: "diff", event: childProtocolUpdate(session.EventTypeToolResult, session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate), ToolCallID: "shared-tool", Status: "in_progress", Content: []session.ProtocolToolCallContent{{Type: "diff", Path: "child.txt", OldText: &oldText, NewText: "after"}}})},
		{name: "location", event: childProtocolUpdate(session.EventTypeToolResult, session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate), ToolCallID: "shared-tool", Status: "completed", Locations: []session.ProtocolToolCallLocation{{Path: "child.txt", Line: &line}}})},
		{name: "plan", event: childProtocolUpdate(session.EventTypePlan, session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypePlan), Entries: []session.ProtocolPlanEntry{{Content: "verify child", Status: "in_progress", Priority: "high"}}})},
		{name: "permission", event: &session.Event{Type: session.EventTypeCustom, ApprovalRequestID: "approval-child", Protocol: &session.EventProtocol{Method: session.ProtocolMethodRequestPermission, Permission: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "shared-tool", Name: "WRITE", RawInput: map[string]any{"path": "child.txt"}}, Options: []session.ProtocolApprovalOption{{ID: "allow_once", Name: "Allow once", Kind: "allow_once"}}}}}},
		{name: "lifecycle", event: &session.Event{Type: session.EventTypeLifecycle, Lifecycle: &session.EventLifecycle{Status: eventstream.LifecycleStateCompleted, Reason: "child complete"}}},
	}
	recorder := NewChildRecorder(sessions)
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			origin := session.EventChildOrigin{
				Scope: session.EventChildScopeSubagent, ScopeID: "task-1", TaskID: "task-1", DelegationID: "task-1",
				SourceEventID: "child-source-" + test.name, ParentTool: session.EventParentTool{CallID: "spawn-1", Name: "Spawn"},
			}
			stored, err := recorder.Record(ctx, ChildRecordRequest{SessionRef: parent.SessionRef, Event: test.event, Origin: origin})
			if err != nil {
				t.Fatalf("Record() error = %v", err)
			}
			if !session.IsMirror(stored) || stored.Seq != uint64(index+1) {
				t.Fatalf("stored event = %#v, want ordered durable mirror", stored)
			}
			base := acpprojector.EnvelopeBaseFromSessionEvent(parent.SessionRef, stored, acpprojector.SessionEventTransport{})
			projected := acpprojector.ProjectSessionEventEnvelope(base, stored)
			if len(projected) == 0 {
				t.Fatalf("ProjectSessionEventEnvelope() = nil for %#v", stored)
			}
			for _, envelope := range projected {
				if envelope.Delivery == nil || envelope.Delivery.Mode != eventstream.DeliveryMirror || envelope.Scope != eventstream.ScopeSubagent || envelope.ScopeID != "task-1" || envelope.ParentTool == nil || envelope.ParentTool.ToolCallID != "spawn-1" {
					t.Fatalf("projected Envelope = %#v, want durable child relation", envelope)
				}
			}
		})
	}
	page, err := sessions.EventsPage(ctx, session.EventPageRequest{SessionRef: parent.SessionRef, Visibility: session.EventPageClientReplay})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != len(tests) {
		t.Fatalf("durable replay count = %d, want %d", len(page.Events), len(tests))
	}
}

func childProtocolUpdate(eventType session.EventType, update session.ProtocolUpdate) *session.Event {
	return &session.Event{Type: eventType, Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &update}}
}
