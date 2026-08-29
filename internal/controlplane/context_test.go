package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
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

func TestDefaultControllerEpochIdentitySurvivesCoordinatorRestart(t *testing.T) {
	t.Parallel()

	first := (&Coordinator{clock: time.Now}).kernelControllerBinding("test")
	second := (&Coordinator{clock: time.Now}).kernelControllerBinding("test")
	if first.EpochID == "" || second.EpochID == "" || first.EpochID == second.EpochID {
		t.Fatalf("controller epochs = %q and %q, want distinct durable identities", first.EpochID, second.EpochID)
	}
}

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
		model.NewToolResultJSONPart("call-1", "RunCommand", map[string]any{"result": "tool output"}, false),
	}}
	childEvent := controlAssistantEvent("child answer")
	childEvent.Actor = session.ActorRef{Kind: session.ActorKindParticipant, Name: "ella"}
	childEvent.Scope = &session.EventScope{Participant: session.ParticipantRef{ID: "participant-1", Kind: session.ParticipantKindSubagent}}
	for _, event := range []*session.Event{
		controlUserEvent("user prompt"),
		{
			Type: session.EventTypeToolResult, Visibility: session.VisibilityCanonical, Text: "tool output", Message: &toolMessage,
			Tool: &session.EventTool{ID: "call-1", Name: "RunCommand", Output: map[string]any{"result": "tool output"}},
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
	wantContext := agent.ContextTransfer{Turns: []agent.ContextTurn{{
		Executor:     session.ActorRef{Kind: session.ActorKindParticipant, Name: "ella"},
		UserMessages: []string{"user prompt"}, AssistantSummary: "child answer",
	}}}
	if !reflect.DeepEqual(route.Context, wantContext) {
		t.Fatalf("context = %#v, want %#v", route.Context, wantContext)
	}
	if !reflect.DeepEqual(route.FreshContext, wantContext) {
		t.Fatalf("fresh context = %#v, want complete route %#v", route.FreshContext, wantContext)
	}
	encoded, err := json.Marshal(route.Context)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"canonical_tail", "tool output", "live chunk", "task-1", "@ella"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("context contains %q: %s", forbidden, encoded)
		}
	}
}

func TestContextRouterAllowsEmptyCanonicalHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sessions, activeSession := newControlTestSession(t, "context-empty")
	router, err := NewContextRouter(sessions)
	if err != nil {
		t.Fatal(err)
	}
	route, err := router.ParticipantContext(ctx, controller.ParticipantContextRequest{
		SessionRef: activeSession.SessionRef,
		Session:    activeSession,
		Binding: session.ParticipantBinding{
			ID: "spawn", Label: "spawn", ContextSyncSeq: 0,
		},
	})
	if err != nil {
		t.Fatalf("ParticipantContext(empty history) error = %v", err)
	}
	if route.SyncSeq != 0 || !agent.ContextTransferEmpty(route.Context) || !agent.ContextTransferEmpty(route.FreshContext) {
		t.Fatalf("empty history route = %#v, want empty transfer without error", route)
	}
}

func TestSharedContextOffsetUsesDurableCompleteTurnsAndOpaqueCompactBoundary(t *testing.T) {
	t.Parallel()

	compactMessage := model.NewTextMessage(model.RoleUser, "CONTEXT CHECKPOINT\nObjective: compacted baseline")
	events := []*session.Event{
		controlUserEvent("old user"),
		controlAssistantEvent("old assistant"),
		{Seq: 7, Type: session.EventTypeCompact, Visibility: session.VisibilityCanonical, Message: &compactMessage, Text: compactMessage.TextContent()},
		{Seq: 8, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Text: "fresh user", Scope: &session.EventScope{TurnID: "turn-fresh", Executor: session.ActorRef{Kind: session.ActorKindController, Name: "codex"}}},
		{Seq: 9, Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Text: "fresh assistant", Scope: &session.EventScope{TurnID: "turn-fresh", Executor: session.ActorRef{Kind: session.ActorKindController, Name: "codex"}}},
	}
	events[0].Seq = 2
	events[1].Seq = 4

	first := sharedContextOffsetFromEvents(events, 0, "")
	want := sharedContextOffset{
		Checkpoint: 9,
		Transfer: agent.ContextTransfer{
			Summary: compactMessage.TextContent(),
			Turns: []agent.ContextTurn{{
				Executor:     session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
				UserMessages: []string{"fresh user"}, AssistantSummary: "fresh assistant",
			}},
		},
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("offset = %#v, want %#v", first, want)
	}
	if got := sharedContextOffsetFromEvents(events, 9, ""); !reflect.DeepEqual(got, sharedContextOffset{Checkpoint: 9}) {
		t.Fatalf("incremental offset = %#v, want empty checkpoint", got)
	}
}

func TestControllerContextIncludesFullBootstrapAlongsideResumeDelta(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sessions, activeSession := newControlTestSession(t, "controller-context-bootstrap")
	for _, event := range []*session.Event{
		controlUserEvent("first question"),
		controlAssistantEvent("first answer"),
		controlUserEvent("second question"),
		controlAssistantEvent("second answer"),
	} {
		if _, err := sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: activeSession.SessionRef,
			Event:      event,
		}); err != nil {
			t.Fatal(err)
		}
	}
	router, err := NewContextRouter(sessions)
	if err != nil {
		t.Fatal(err)
	}
	route, err := router.ControllerContext(ctx, controller.ControllerContextRequest{
		SessionRef: activeSession.SessionRef,
		Session:    activeSession,
		SinceSeq:   2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if route.SyncSeq != 4 {
		t.Fatalf("SyncSeq = %d, want 4", route.SyncSeq)
	}
	if len(route.Context.Turns) != 1 || route.Context.Turns[0].AssistantSummary != "second answer" {
		t.Fatalf("resume delta = %#v, want only second exchange", route.Context)
	}
	if len(route.FreshContext.Turns) != 2 || route.FreshContext.Turns[0].AssistantSummary != "first answer" || route.FreshContext.Turns[1].AssistantSummary != "second answer" {
		t.Fatalf("fresh context = %#v, want both exchanges", route.FreshContext)
	}
}

func TestSharedContextOffsetDefersIncompleteTurnsWithoutLosingTheirUserMessage(t *testing.T) {
	t.Parallel()

	events := []*session.Event{
		{Seq: 1, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Text: "first user", Scope: &session.EventScope{
			TurnID: "turn-first", Executor: session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
		}},
		{Seq: 2, Type: session.EventTypeToolResult, Visibility: session.VisibilityCanonical, Text: "private tool trace", Scope: &session.EventScope{TurnID: "turn-first"}},
		{Seq: 3, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Text: "second user", Scope: &session.EventScope{
			TurnID: "turn-second", Executor: session.ActorRef{Kind: session.ActorKindParticipant, Name: "claude(aria)"},
		}},
		{Seq: 4, Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Text: "second assistant", Scope: &session.EventScope{
			TurnID: "turn-second", Executor: session.ActorRef{Kind: session.ActorKindParticipant, Name: "claude(aria)"},
		}},
	}
	first := sharedContextOffsetFromEvents(events, 0, "")
	wantFirst := sharedContextOffset{Checkpoint: 4, Transfer: agent.ContextTransfer{Turns: []agent.ContextTurn{{
		Executor:     session.ActorRef{Kind: session.ActorKindParticipant, Name: "claude(aria)"},
		UserMessages: []string{"second user"}, AssistantSummary: "second assistant",
	}}}}
	if !reflect.DeepEqual(first, wantFirst) {
		t.Fatalf("first offset = %#v, want only complete turn %#v", first, wantFirst)
	}

	events = append(events, &session.Event{
		Seq: 5, Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Text: "first assistant", Scope: &session.EventScope{
			TurnID: "turn-first", Executor: session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
		},
	})
	second := sharedContextOffsetFromEvents(events, first.Checkpoint, "")
	wantSecond := sharedContextOffset{Checkpoint: 5, Transfer: agent.ContextTransfer{Turns: []agent.ContextTurn{{
		Executor:     session.ActorRef{Kind: session.ActorKindController, Name: "codex"},
		UserMessages: []string{"first user"}, AssistantSummary: "first assistant",
	}}}}
	if !reflect.DeepEqual(second, wantSecond) {
		t.Fatalf("second offset = %#v, want completed crossing turn %#v", second, wantSecond)
	}
	encoded, err := json.Marshal(second.Transfer)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private tool trace") {
		t.Fatalf("context leaked a tool trace: %s", encoded)
	}
}

func TestSharedContextOffsetPreservesMultipleExchangesWithinOneRuntimeTurn(t *testing.T) {
	t.Parallel()

	executor := session.ActorRef{Kind: session.ActorKindController, ID: "controller-1", Name: "codex"}
	events := []*session.Event{
		{Seq: 1, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Text: "initial request", Scope: &session.EventScope{TurnID: "turn-1", Executor: executor}},
		{Seq: 2, Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Text: "initial answer", Scope: &session.EventScope{TurnID: "turn-1", Executor: executor}},
		{Seq: 3, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Text: "follow-up guidance", Scope: &session.EventScope{TurnID: "turn-1", Executor: executor}},
		{Seq: 4, Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Text: "revised answer", Scope: &session.EventScope{TurnID: "turn-1", Executor: executor}},
	}
	want := sharedContextOffset{Checkpoint: 4, Transfer: agent.ContextTransfer{Turns: []agent.ContextTurn{
		{Executor: executor, UserMessages: []string{"initial request"}, AssistantSummary: "initial answer"},
		{Executor: executor, UserMessages: []string{"follow-up guidance"}, AssistantSummary: "revised answer"},
	}}}
	if got := sharedContextOffsetFromEvents(events, 0, ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("offset = %#v, want two ordered exchanges %#v", got, want)
	}
}

func TestSharedContextOffsetPreservesSteeringBeforeAssistantAnswer(t *testing.T) {
	t.Parallel()

	executor := session.ActorRef{Kind: session.ActorKindController, Name: "codex"}
	events := []*session.Event{
		{Seq: 1, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Text: "initial request", Scope: &session.EventScope{TurnID: "turn-1", Executor: executor}},
		{Seq: 2, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Text: "steer before answering", Scope: &session.EventScope{TurnID: "turn-1", Executor: executor}},
		{Seq: 3, Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Text: "guided answer", Scope: &session.EventScope{TurnID: "turn-1", Executor: executor}},
	}
	want := sharedContextOffset{Checkpoint: 3, Transfer: agent.ContextTransfer{Turns: []agent.ContextTurn{{
		Executor:         executor,
		UserMessages:     []string{"initial request", "steer before answering"},
		AssistantSummary: "guided answer",
	}}}}
	if got := sharedContextOffsetFromEvents(events, 0, ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("offset = %#v, want ordered user steering %#v", got, want)
	}
}

func TestSharedContextOffsetPreservesIDOnlyExecutor(t *testing.T) {
	t.Parallel()

	executor := session.ActorRef{ID: "custom-agent-1"}
	events := []*session.Event{
		{Seq: 1, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Text: "question", Scope: &session.EventScope{TurnID: "turn-1", Executor: executor}},
		{Seq: 2, Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Text: "answer", Scope: &session.EventScope{TurnID: "turn-1", Executor: executor}},
	}
	want := sharedContextOffset{Checkpoint: 2, Transfer: agent.ContextTransfer{Turns: []agent.ContextTurn{{
		Executor: executor, UserMessages: []string{"question"}, AssistantSummary: "answer",
	}}}}
	if got := sharedContextOffsetFromEvents(events, 0, ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("offset = %#v, want ID-only executor %#v", got, want)
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
		FenceOwnerID: "test-host",
		Clock:        func() time.Time { return time.Unix(20, 0) }, IDGenerator: func() string { return "epoch-kernel" },
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
	if backend.activate.Agent != "codex" || !agent.ContextTransferEmpty(backend.activate.Context) {
		t.Fatalf("activation request = %#v", backend.activate)
	}
	loaded, err := sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if len(loaded.Events) != 1 {
		t.Fatalf("handoff events = %#v, want one event", loaded.Events)
	}
	handoff := session.ProtocolHandoffOf(loaded.Events[0])
	if handoff == nil || handoff.Phase != "activation" || loaded.Events[0].Actor.Name != "control" {
		t.Fatalf("handoff events = %#v", loaded.Events)
	}
	if !traceSink.saw(agent.LifecycleHandoff, agent.TraceStarted) || !traceSink.saw(agent.LifecycleHandoff, agent.TraceCompleted) {
		t.Fatalf("trace records = %#v, want handoff lifecycle", traceSink.snapshot())
	}
}

func TestCoordinatorRequiresSessionFenceService(t *testing.T) {
	t.Parallel()

	sessions, _ := newControlTestSession(t, "handoff-requires-fence")
	unfenced := struct{ session.Service }{Service: sessions}
	router, err := NewContextRouter(unfenced)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCoordinator(CoordinatorConfig{Sessions: unfenced, Context: router}); err == nil || !strings.Contains(err.Error(), "must provide execution fences") {
		t.Fatalf("NewCoordinator() error = %v, want required execution fence service", err)
	}
	if _, err := NewCoordinator(CoordinatorConfig{Sessions: sessions, Context: router}); err == nil || !strings.Contains(err.Error(), "requires fence_owner_id") {
		t.Fatalf("NewCoordinator() error = %v, want explicit shared fence owner", err)
	}
}

func TestCoordinatorHandoffDoesNotBypassActiveTurnFence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sessions, activeSession := newControlTestSession(t, "handoff-active-turn")
	fence, err := sessions.(session.SessionFenceService).AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: activeSession.SessionRef,
		OwnerID:    "active-turn",
	})
	if err != nil {
		t.Fatalf("AcquireSessionFence() error = %v", err)
	}
	t.Cleanup(func() {
		_ = sessions.(session.SessionFenceService).ReleaseSessionFence(context.Background(), session.SessionFenceReleaseRequest(fence))
	})
	router, err := NewContextRouter(sessions)
	if err != nil {
		t.Fatal(err)
	}
	backend := &recordingControllerBackend{activation: session.ControllerBinding{
		Kind: session.ControllerKindACP, ControllerID: "remote-1", AgentName: "claude", EpochID: "remote-epoch",
	}}
	coordinator, err := NewCoordinator(CoordinatorConfig{Sessions: sessions, Controllers: backend, Context: router, FenceOwnerID: "test-host"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.HandoffController(ctx, agent.HandoffControllerRequest{
		SessionRef: activeSession.SessionRef, Kind: session.ControllerKindACP, Agent: "claude",
	})
	if !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("HandoffController() error = %v, want active Turn ErrFenceConflict", err)
	}
	if backend.activate.Agent != "" || backend.deactivations != 0 {
		t.Fatalf("backend activity = %#v/deactivations=%d, want no endpoint effect before fence ownership", backend.activate, backend.deactivations)
	}
	loaded, err := sessions.Session(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Controller, activeSession.Controller) {
		t.Fatalf("controller = %#v, want unchanged %#v", loaded.Controller, activeSession.Controller)
	}
}

type controlTraceSink struct {
	mu      sync.Mutex
	records []agent.TraceRecord
}

func (s *controlTraceSink) RecordTrace(record agent.TraceRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
}

func (s *controlTraceSink) snapshot() []agent.TraceRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agent.TraceRecord(nil), s.records...)
}

func (s *controlTraceSink) saw(operation agent.LifecycleOperation, status agent.TraceStatus) bool {
	deadline := time.Now().Add(time.Second)
	for {
		for _, record := range s.snapshot() {
			if record.Event.Operation == operation && record.Status == status {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCoordinatorDeactivatesNewEndpointWhenAtomicCommitFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base, activeSession := newControlTestSession(t, "handoff-failure")
	commitErr := errors.New("commit failed")
	sessions := failingHandoffService{
		Service: base, SessionFenceService: base.(session.SessionFenceService), err: commitErr,
	}
	router, err := NewContextRouter(sessions)
	if err != nil {
		t.Fatal(err)
	}
	backend := &recordingControllerBackend{activation: session.ControllerBinding{
		Kind: session.ControllerKindACP, ControllerID: "remote-1", AgentName: "codex", EpochID: "remote-epoch",
	}}
	coordinator, err := NewCoordinator(CoordinatorConfig{Sessions: sessions, Controllers: backend, Context: router, FenceOwnerID: "test-host"})
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

func TestCoordinatorKeepsActivationWhenCommitReportsAlreadyCommitted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base, activeSession := newControlTestSession(t, "handoff-committed")
	committedBinding := session.ControllerBinding{
		Kind: session.ControllerKindACP, ControllerID: "remote-1", AgentName: "codex", EpochID: "remote-epoch",
	}
	sessions := committedHandoffService{
		Service:             base,
		SessionFenceService: base.(session.SessionFenceService),
		binding:             committedBinding,
		err:                 &session.CommittedError{Err: errors.New("index write failed after commit")},
	}
	router, err := NewContextRouter(sessions)
	if err != nil {
		t.Fatal(err)
	}
	backend := &recordingControllerBackend{activation: committedBinding}
	coordinator, err := NewCoordinator(CoordinatorConfig{Sessions: sessions, Controllers: backend, Context: router, FenceOwnerID: "test-host"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coordinator.HandoffController(ctx, agent.HandoffControllerRequest{
		SessionRef: activeSession.SessionRef, Kind: session.ControllerKindACP, Agent: "codex",
	})
	if err != nil {
		t.Fatalf("HandoffController() error = %v, want nil for durable commit with reporting failure", err)
	}
	if !reflect.DeepEqual(updated.Controller, committedBinding) {
		t.Fatalf("controller = %#v, want durable binding %#v", updated.Controller, committedBinding)
	}
	if backend.deactivations != 0 {
		t.Fatalf("deactivations = %d, want 0 when commit already durable", backend.deactivations)
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
	coordinator, err := NewCoordinator(CoordinatorConfig{Sessions: sessions, Controllers: backend, Context: router, FenceOwnerID: "test-host"})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := sessions.(session.SessionFenceService).AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{
		SessionRef: activeSession.SessionRef, OwnerID: "runtime-owner",
	})
	if err != nil {
		t.Fatalf("AcquireSessionFence() error = %v", err)
	}
	ctx = session.ContextWithRuntimeFence(ctx, fence)
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
	session.SessionFenceService
	err error
}

func (s failingHandoffService) BindControllerWithEvent(context.Context, session.BindControllerWithEventRequest) (session.Session, *session.Event, error) {
	return session.Session{}, nil, s.err
}

type committedHandoffService struct {
	session.Service
	session.SessionFenceService
	binding session.ControllerBinding
	err     error
}

func (s committedHandoffService) BindControllerWithEvent(ctx context.Context, req session.BindControllerWithEventRequest) (session.Session, *session.Event, error) {
	updated, err := s.BindController(ctx, session.BindControllerRequest{
		SessionRef:    req.SessionRef,
		MutationGuard: req.MutationGuard,
		Binding:       s.binding,
	})
	if err != nil {
		return session.Session{}, nil, err
	}
	return updated, nil, s.err
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
	sessions := inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string { return id }})
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
