package controlclient

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestProcessRestartRebuildsDurableClientStateFromSessionTruth(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	newService := func() *sessionfile.Service {
		return sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
			RootDir: root, SessionIDGenerator: func() string { return "session-1" },
		}))
	}
	beforeRestart := newService()
	active, err := beforeRestart.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "session-1",
		Workspace: session.WorkspaceRef{Key: "workspace-b", CWD: "/workspace/b"}, Title: "Restart state",
	})
	if err != nil {
		t.Fatal(err)
	}
	userMessage := model.NewTextMessage(model.RoleUser, "durable parent message")
	if _, err := beforeRestart.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
		ID: "parent-message", Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &userMessage,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := beforeRestart.BindController(ctx, session.BindControllerRequest{SessionRef: active.SessionRef, Binding: session.ControllerBinding{
		Kind: session.ControllerKindACP, ControllerID: "controller-1", EpochID: "epoch-9", AgentName: "external-main",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := beforeRestart.PutParticipant(ctx, session.PutParticipantRequest{SessionRef: active.SessionRef, Binding: session.ParticipantBinding{
		ID: "participant-1", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar, SessionID: "remote-session-1", Source: "control-client",
	}}); err != nil {
		t.Fatal(err)
	}
	recorder := NewChildRecorder(beforeRestart)
	origin := session.EventChildOrigin{
		Scope: session.EventChildScopeSubagent, ScopeID: "task-1", TaskID: "task-1", DelegationID: "task-1",
		SourceEventID: "child-message", ParentTool: session.EventParentTool{CallID: "spawn-1", Name: "Spawn"},
	}
	if _, err := recorder.Record(ctx, ChildRecordRequest{SessionRef: active.SessionRef, Origin: origin, Event: childProtocolUpdate(session.EventTypeAssistant, session.ProtocolUpdate{
		SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), MessageID: "child-message", Content: session.ProtocolTextContent("nested child"),
	})}); err != nil {
		t.Fatal(err)
	}
	origin.SourceEventID = "child-tool"
	if _, err := recorder.Record(ctx, ChildRecordRequest{SessionRef: active.SessionRef, Origin: origin, Event: childProtocolUpdate(session.EventTypeToolResult, session.ProtocolUpdate{
		SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate), ToolCallID: "child-call", Status: "completed", RawOutput: map[string]any{"result": "ok"},
	})}); err != nil {
		t.Fatal(err)
	}

	afterRestart := newService()
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := NewFeedRegistry(FeedRegistryConfig{Reader: afterRestart, CursorCodec: codec})
	if err != nil {
		t.Fatal(err)
	}
	stateService, err := NewStateService(StateServiceConfig{Sessions: afterRestart, Runtime: staticRuntimeStateReader{}, Feeds: feeds})
	if err != nil {
		t.Fatal(err)
	}
	state, err := stateService.State(ctx, controlport.StateRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if state.WorkspaceKey != "workspace-b" || state.Controller.EpochID != "epoch-9" || len(state.Participants) != 1 || state.Participants[0].ID != "participant-1" || state.BoundaryCursor == "" {
		t.Fatalf("restart SessionState = %#v", state)
	}
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := feed.Subscribe(ctx, controlport.SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Subscription.Close()
	events := receiveEnvelopes(t, replayed.Subscription.Backfill(), 3)
	if events[0].Delivery == nil || events[0].Delivery.Mode != eventstream.DeliveryCanonical ||
		events[1].Delivery == nil || events[1].Delivery.Mode != eventstream.DeliveryMirror || events[1].ScopeID != "task-1" ||
		events[2].Delivery == nil || events[2].Delivery.Mode != eventstream.DeliveryMirror || events[2].ScopeID != "task-1" {
		t.Fatalf("restart durable replay = %#v", events)
	}
}
