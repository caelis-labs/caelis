package controlplane

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

var _ controller.RecoveryCoordinator = (*Coordinator)(nil)
var _ agent.SessionControlPlane = (*SessionControl)(nil)

func TestContextRouterUsesOnlySharedCanonicalDialogue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sessions, activeSession := newControlTestSession(t, "context-shared")
	var err error
	activeSession, err = sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID: "participant-1", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
			Label: "@ella", AgentName: "codex", DelegationID: "task-1",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}
	toolMessage := model.Message{Role: model.RoleTool, Parts: []model.Part{
		model.NewToolResultJSONPart("call-1", "RUN_COMMAND", map[string]any{"result": "tool output"}, false),
	}}
	childEvent := controlAssistantEvent("child answer")
	childEvent.Actor = session.ActorRef{Kind: session.ActorKindParticipant, Name: "ella"}
	childEvent.Scope = &session.EventScope{Participant: session.ParticipantRef{ID: "participant-1", Kind: session.ParticipantKindSubagent}}
	for _, event := range []*session.Event{
		controlUserEvent("user prompt"),
		{
			Type: session.EventTypeToolResult, Visibility: session.VisibilityCanonical, Text: "tool output", Message: &toolMessage,
			Tool: &session.EventTool{ID: "call-1", Name: "RUN_COMMAND", Output: map[string]any{"result": "tool output"}},
		},
		childEvent,
		session.MarkUIOnly(&session.Event{Type: session.EventTypeAssistant, Text: "live chunk"}),
	} {
		if _, err := sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: activeSession.SessionRef, Event: event}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	router, err := NewContextRouter(sessions)
	if err != nil {
		t.Fatalf("NewContextRouter() error = %v", err)
	}
	route, err := router.ControllerContext(ctx, controller.ControllerContextRequest{
		SessionRef: activeSession.SessionRef,
		Session:    activeSession,
		Controller: session.ControllerBinding{Kind: session.ControllerKindACP, Label: "old", ContextSyncSeq: 4},
	})
	if err != nil {
		t.Fatalf("ControllerContext() error = %v", err)
	}
	if route.SyncSeq != 3 {
		t.Fatalf("SyncSeq = %d, want latest shared event sequence 3", route.SyncSeq)
	}
	for _, want := range []string{"shared_ledger_checkpoint: 3", "shared_dialogue_delta:", "[1] user:\nuser prompt", "[3] assistant(ella):\nchild answer", "- @ella agent=codex"} {
		if !strings.Contains(route.Prelude, want) {
			t.Fatalf("context missing %q:\n%s", want, route.Prelude)
		}
	}
	for _, forbidden := range []string{"canonical_tail", "tool output", "live chunk", "task-1"} {
		if strings.Contains(route.Prelude, forbidden) {
			t.Fatalf("context contains %q:\n%s", forbidden, route.Prelude)
		}
	}
}

func TestSharedDialogueDeltaUsesDurableSequenceAndCompactBoundary(t *testing.T) {
	t.Parallel()

	compactMessage := model.NewTextMessage(model.RoleUser, "CONTEXT CHECKPOINT\nObjective: compacted baseline")
	events := []*session.Event{
		controlUserEvent("old user"),
		controlAssistantEvent("old assistant"),
		{Seq: 7, Type: session.EventTypeCompact, Visibility: session.VisibilityCanonical, Message: &compactMessage, Text: compactMessage.TextContent()},
		{Seq: 8, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Text: "fresh user"},
		{Seq: 9, Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Text: "fresh assistant"},
	}
	events[0].Seq = 2
	events[1].Seq = 4

	first := sharedDialogueDeltaFromEvents(events, 0, "")
	want := sharedDialogueDelta{
		Checkpoint: 9,
		Entries: []sharedDialogueEntry{
			{Seq: 7, Role: "compact", Text: compactMessage.TextContent()},
			{Seq: 8, Role: "user", Text: "fresh user"},
			{Seq: 9, Role: "assistant", Text: "fresh assistant"},
		},
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("delta = %#v, want %#v", first, want)
	}
	if got := sharedDialogueDeltaFromEvents(events, 9, ""); !reflect.DeepEqual(got, sharedDialogueDelta{Checkpoint: 9}) {
		t.Fatalf("incremental delta = %#v, want empty checkpoint", got)
	}
}

func TestCoordinatorOwnsActivationAndAtomicHandoffCommit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sessions, activeSession := newControlTestSession(t, "handoff")
	router, err := NewContextRouter(sessions)
	if err != nil {
		t.Fatal(err)
	}
	backend := &recordingControllerBackend{activation: session.ControllerBinding{
		Kind: session.ControllerKindACP, ControllerID: "remote-1", AgentName: "codex", Label: "Codex",
		EpochID: "epoch-remote", RemoteSessionID: "acp-session-1", ContextSyncSeq: 0,
	}}
	traceSink := &controlTraceSink{}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Sessions: sessions, Controllers: backend, Context: router,
		Clock: func() time.Time { return time.Unix(20, 0) }, IDGenerator: func() string { return "epoch-kernel" },
		TraceSink: traceSink,
	})
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	updated, err := coordinator.HandoffController(ctx, agent.HandoffControllerRequest{
		SessionRef: activeSession.SessionRef, Kind: session.ControllerKindACP, Agent: "codex", Source: "user", Reason: "delegate",
	})
	if err != nil {
		t.Fatalf("HandoffController() error = %v", err)
	}
	if !reflect.DeepEqual(updated.Controller, backend.activation) {
		t.Fatalf("controller = %#v, want %#v", updated.Controller, backend.activation)
	}
	if backend.activate.Agent != "codex" || !strings.Contains(backend.activate.ContextPrelude, "Caelis controller handoff context") {
		t.Fatalf("activation request = %#v", backend.activate)
	}
	loaded, err := sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if len(loaded.Events) != 1 || session.ProtocolHandoffOf(loaded.Events[0]) == nil || loaded.Events[0].Actor.Name != "control" {
		t.Fatalf("handoff events = %#v", loaded.Events)
	}
	if !traceSink.saw(agent.LifecycleHandoff, agent.TraceStarted) || !traceSink.saw(agent.LifecycleHandoff, agent.TraceCompleted) {
		t.Fatalf("trace records = %#v, want handoff lifecycle", traceSink.records)
	}
}

type controlTraceSink struct{ records []agent.TraceRecord }

func (s *controlTraceSink) RecordTrace(record agent.TraceRecord) {
	s.records = append(s.records, record)
}

func (s *controlTraceSink) saw(operation agent.LifecycleOperation, status agent.TraceStatus) bool {
	for _, record := range s.records {
		if record.Event.Operation == operation && record.Status == status {
			return true
		}
	}
	return false
}

func TestCoordinatorDeactivatesNewEndpointWhenAtomicCommitFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base, activeSession := newControlTestSession(t, "handoff-failure")
	commitErr := errors.New("commit failed")
	sessions := failingHandoffService{Service: base, err: commitErr}
	router, err := NewContextRouter(sessions)
	if err != nil {
		t.Fatal(err)
	}
	backend := &recordingControllerBackend{activation: session.ControllerBinding{
		Kind: session.ControllerKindACP, ControllerID: "remote-1", AgentName: "codex", EpochID: "remote-epoch",
	}}
	coordinator, err := NewCoordinator(CoordinatorConfig{Sessions: sessions, Controllers: backend, Context: router})
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.HandoffController(ctx, agent.HandoffControllerRequest{
		SessionRef: activeSession.SessionRef, Kind: session.ControllerKindACP, Agent: "codex",
	})
	if !errors.Is(err, commitErr) {
		t.Fatalf("HandoffController() error = %v, want %v", err, commitErr)
	}
	if backend.deactivations != 1 {
		t.Fatalf("deactivations = %d, want cleanup of activated endpoint", backend.deactivations)
	}
	loaded, err := base.LoadSession(ctx, session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Session.Controller.Kind != session.ControllerKindKernel || len(loaded.Events) != 0 {
		t.Fatalf("failed handoff persisted partial state: %#v", loaded)
	}
}

func TestCoordinatorOwnsControllerProcessReattachAndBindingRefresh(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sessions, activeSession := newControlTestSession(t, "reattach")
	var err error
	activeSession, err = sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "old-controller", AgentName: "codex",
			EpochID: "old-epoch", RemoteSessionID: "old-session", ContextSyncSeq: 0,
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	router, err := NewContextRouter(sessions)
	if err != nil {
		t.Fatal(err)
	}
	backend := &recordingControllerBackend{activation: session.ControllerBinding{
		Kind: session.ControllerKindACP, ControllerID: "new-controller", AgentName: "codex",
		EpochID: "new-epoch", RemoteSessionID: "new-session", ContextSyncSeq: 0,
	}}
	coordinator, err := NewCoordinator(CoordinatorConfig{Sessions: sessions, Controllers: backend, Context: router})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coordinator.ReattachController(ctx, controller.RecoveryRequest{
		SessionRef: activeSession.SessionRef, Session: activeSession, ExcludeTurnID: "turn-current",
	})
	if err != nil {
		t.Fatalf("ReattachController() error = %v", err)
	}
	if !reflect.DeepEqual(updated.Controller, backend.activation) {
		t.Fatalf("controller = %#v, want %#v", updated.Controller, backend.activation)
	}
	if backend.activate.Source != "controller_rehydrate" || backend.activate.Agent != "codex" {
		t.Fatalf("reattach activation = %#v", backend.activate)
	}
}

type failingHandoffService struct {
	session.Service
	err error
}

func (s failingHandoffService) BindControllerWithEvent(context.Context, session.BindControllerWithEventRequest) (session.Session, *session.Event, error) {
	return session.Session{}, nil, s.err
}

type recordingControllerBackend struct {
	activation    session.ControllerBinding
	activate      controller.HandoffRequest
	deactivations int
}

func (b *recordingControllerBackend) Activate(_ context.Context, req controller.HandoffRequest) (session.ControllerBinding, error) {
	b.activate = controller.NormalizeHandoffRequest(req)
	return session.CloneControllerBinding(b.activation), nil
}

func (b *recordingControllerBackend) Deactivate(context.Context, session.SessionRef) error {
	b.deactivations++
	return nil
}

func (*recordingControllerBackend) RunTurn(context.Context, controller.TurnRequest) (controller.TurnResult, error) {
	return controller.TurnResult{}, nil
}

func (*recordingControllerBackend) Attach(context.Context, controller.AttachRequest) (session.ParticipantBinding, error) {
	return session.ParticipantBinding{}, nil
}

func (*recordingControllerBackend) PromptParticipant(context.Context, controller.ParticipantPromptRequest) (controller.TurnResult, error) {
	return controller.TurnResult{}, nil
}

func (*recordingControllerBackend) Detach(context.Context, controller.DetachRequest) error {
	return nil
}

func newControlTestSession(t *testing.T, id string) (session.Service, session.Session) {
	t.Helper()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string { return id }}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "test", Workspace: session.WorkspaceRef{Key: "ws", CWD: "/tmp/ws"},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return sessions, activeSession
}

func controlUserEvent(text string) *session.Event {
	message := model.NewTextMessage(model.RoleUser, text)
	return &session.Event{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &message, Text: text}
}

func controlAssistantEvent(text string) *session.Event {
	message := model.NewTextMessage(model.RoleAssistant, text)
	return &session.Event{Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Message: &message, Text: text}
}
