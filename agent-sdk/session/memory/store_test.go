package inmemory

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestStoreAppendAndListCanonicalEvents(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		SessionIDGenerator: func() string { return "sess-1" },
		EventIDGenerator:   func() string { return "evt-1" },
	})
	ctx := context.Background()

	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-1",
			CWD: "/tmp/ws",
		},
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	_, err = store.AppendEvent(ctx, createdSession.SessionRef, &session.Event{
		Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "hello")),
		Text:    "hello",
	})
	if err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	_, err = store.AppendEvent(ctx, createdSession.SessionRef, session.MarkNotice(&session.Event{
		Message: ptrMessage(model.NewTextMessage(model.RoleSystem, "warn: retrying")),
	}, "warn", "retrying"))
	if err != nil {
		t.Fatalf("AppendEvent(notice) error = %v", err)
	}

	events, err := store.Events(ctx, session.EventsRequest{
		SessionRef: createdSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got, want := len(events), 1; got != want {
		t.Fatalf("len(events) = %d, want %d", got, want)
	}
	if got := events[0].Text; got != "hello" {
		t.Fatalf("event text = %q, want %q", got, "hello")
	}

	allEvents, err := store.Events(ctx, session.EventsRequest{
		SessionRef:       createdSession.SessionRef,
		IncludeTransient: true,
	})
	if err != nil {
		t.Fatalf("Events(include transient) error = %v", err)
	}
	if got, want := len(allEvents), 1; got != want {
		t.Fatalf("len(allEvents) = %d, want %d; memory store must not persist transient notices", got, want)
	}
}

func TestStoreUpdateState(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		SessionIDGenerator: func() string { return "sess-1" },
	})
	ctx := context.Background()
	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	err = store.UpdateState(ctx, createdSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		state["mode"] = "chat"
		return state, nil
	})
	if err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}

	state, err := store.SnapshotState(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if got := state["mode"]; got != "chat" {
		t.Fatalf("state[mode] = %v, want %q", got, "chat")
	}
}

func TestCompoundTransactionIdentityDoesNotReapplyStateOnRetry(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{SessionIDGenerator: func() string { return "sess-compound-retry" }})
	service := NewService(store)
	created, err := service.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	message := model.NewTextMessage(model.RoleUser, "compound retry")
	event := &session.Event{ID: "event-compound-retry", IdempotencyKey: "fact:compound-retry", Type: session.EventTypeUser, Message: &message}
	stateCalls := 0
	req := session.AppendEventsAndUpdateStateRequest{
		SessionRef:    created.SessionRef,
		TransactionID: "transaction-compound-retry",
		Events:        []*session.Event{event},
		UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
			stateCalls++
			state["count"] = float64(stateCalls)
			return state, nil
		},
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := service.AppendEventsAndUpdateState(context.Background(), req); err != nil {
			t.Fatalf("AppendEventsAndUpdateState(attempt %d) error = %v", attempt+1, err)
		}
	}
	loaded, err := service.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: created.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if stateCalls != 1 || loaded.State["count"] != float64(1) || len(loaded.Events) != 1 || loaded.Session.Revision != 1 {
		t.Fatalf("retry outcome = calls %d revision %d state %#v events %#v, want one complete transaction", stateCalls, loaded.Session.Revision, loaded.State, loaded.Events)
	}
	changedMessage := model.NewTextMessage(model.RoleUser, "changed transaction payload")
	changed := req
	changed.Events = []*session.Event{{ID: "event-compound-changed", IdempotencyKey: "fact:compound-changed", Type: session.EventTypeUser, Message: &changedMessage}}
	if _, err := service.AppendEventsAndUpdateState(context.Background(), changed); err == nil {
		t.Fatal("changed payload with reused TransactionID succeeded, want conflict")
	} else {
		var conflict *session.EventConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("changed transaction error = %v, want *EventConflictError", err)
		}
	}
	pureStateCalls := 0
	pureState := session.AppendEventsAndUpdateStateRequest{
		SessionRef: created.SessionRef, TransactionID: "transaction-pure-state",
		UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
			pureStateCalls++
			state["pure"] = "applied"
			return state, nil
		},
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := service.AppendEventsAndUpdateState(context.Background(), pureState); err != nil {
			t.Fatalf("pure-state AppendEventsAndUpdateState(attempt %d) error = %v", attempt+1, err)
		}
	}
	loaded, err = service.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: created.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession(after pure state) error = %v", err)
	}
	if pureStateCalls != 1 || loaded.State["pure"] != "applied" || loaded.Session.Revision != 2 {
		t.Fatalf("pure-state retry = calls %d revision %d state %#v, want one separately identified commit", pureStateCalls, loaded.Session.Revision, loaded.State)
	}
}

func TestStoreIsolatesNestedMetadataEventsAndState(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{SessionIDGenerator: func() string { return "sess-isolation" }})
	ctx := context.Background()
	metadata := map[string]any{"nested": map[string]any{"value": "created"}}
	created, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName:  "caelis",
		UserID:   "user-1",
		Metadata: metadata,
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	metadata["nested"].(map[string]any)["value"] = "caller-mutated"
	loaded, err := store.Get(ctx, created.SessionRef)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got := loaded.Metadata["nested"].(map[string]any)["value"]; got != "created" {
		t.Fatalf("stored metadata = %v, want created", got)
	}

	event := &session.Event{
		Type: session.EventTypeToolCall,
		Tool: &session.EventTool{
			ID:    "call-1",
			Name:  "READ",
			Input: map[string]any{"nested": map[string]any{"path": "a"}},
		},
		Meta: map[string]any{"nested": map[string]any{"trace": "one"}},
	}
	if _, err := store.AppendEvent(ctx, created.SessionRef, event); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	event.Tool.Input["nested"].(map[string]any)["path"] = "b"
	event.Meta["nested"].(map[string]any)["trace"] = "two"
	events, err := store.Events(ctx, session.EventsRequest{SessionRef: created.SessionRef})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if got := events[0].Tool.Input["nested"].(map[string]any)["path"]; got != "a" {
		t.Fatalf("stored tool input = %v, want a", got)
	}
	if got := events[0].Meta["nested"].(map[string]any)["trace"]; got != "one" {
		t.Fatalf("stored event meta = %v, want one", got)
	}

	state := map[string]any{"nested": map[string]any{"items": []any{"original"}}}
	if err := store.ReplaceState(ctx, created.SessionRef, state); err != nil {
		t.Fatalf("ReplaceState() error = %v", err)
	}
	state["nested"].(map[string]any)["items"].([]any)[0] = "caller-mutated"
	snapshot, err := store.SnapshotState(ctx, created.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	snapshot["nested"].(map[string]any)["items"].([]any)[0] = "snapshot-mutated"
	stable, err := store.SnapshotState(ctx, created.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState(stable) error = %v", err)
	}
	if got := stable["nested"].(map[string]any)["items"].([]any)[0]; got != "original" {
		t.Fatalf("stored nested state = %v, want original", got)
	}
}

func TestStoreUpdateStateErrorRollsBackNestedMutation(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{SessionIDGenerator: func() string { return "sess-rollback" }})
	ctx := context.Background()
	created, err := store.GetOrCreate(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	if err := store.ReplaceState(ctx, created.SessionRef, map[string]any{
		"nested": map[string]any{"value": "before"},
	}); err != nil {
		t.Fatalf("ReplaceState() error = %v", err)
	}
	wantErr := errors.New("reject update")
	err = store.UpdateState(ctx, created.SessionRef, func(state map[string]any) (map[string]any, error) {
		state["nested"].(map[string]any)["value"] = "after"
		return state, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("UpdateState() error = %v, want %v", err, wantErr)
	}
	state, err := store.SnapshotState(ctx, created.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if got := state["nested"].(map[string]any)["value"]; got != "before" {
		t.Fatalf("state after failed update = %v, want before", got)
	}
}

func TestStoreRejectsInvalidJSONStateWithoutMutation(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{SessionIDGenerator: func() string { return "sess-invalid-state" }})
	ctx := context.Background()
	created, err := store.GetOrCreate(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}
	if err := store.ReplaceState(ctx, created.SessionRef, map[string]any{"value": "before"}); err != nil {
		t.Fatalf("ReplaceState() error = %v", err)
	}
	if err := store.ReplaceState(ctx, created.SessionRef, map[string]any{"value": math.NaN()}); err == nil {
		t.Fatal("ReplaceState(invalid) error = nil, want rejection")
	}
	state, err := store.SnapshotState(ctx, created.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if got := state["value"]; got != "before" {
		t.Fatalf("state after invalid replacement = %v, want before", got)
	}
}

func TestServiceAppendEventCASAndIdempotentRetry(t *testing.T) {
	t.Parallel()

	service := NewService(NewStore(Config{SessionIDGenerator: func() string { return "sess-cas" }}))
	ctx := context.Background()
	created, err := service.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	message := model.NewTextMessage(model.RoleUser, "stable retry")
	event := &session.Event{ID: "event-stable", Type: session.EventTypeUser, Message: &message}
	zero := uint64(0)
	first, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:       created.SessionRef,
		ExpectedRevision: &zero,
		Event:            event,
	})
	if err != nil {
		t.Fatalf("AppendEvent(first) error = %v", err)
	}
	if first.Seq != 1 {
		t.Fatalf("first.Seq = %d, want 1", first.Seq)
	}
	_, err = service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:       created.SessionRef,
		ExpectedRevision: &zero,
		Event:            &session.Event{ID: "event-other", Type: session.EventTypeUser, Message: &message},
	})
	if !errors.Is(err, session.ErrRevisionConflict) {
		t.Fatalf("AppendEvent(stale) error = %v, want ErrRevisionConflict", err)
	}
	one := uint64(1)
	retried, err := service.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:       created.SessionRef,
		ExpectedRevision: &one,
		Event:            event,
	})
	if err != nil {
		t.Fatalf("AppendEvent(retry) error = %v", err)
	}
	if retried.Seq != first.Seq {
		t.Fatalf("retry seq = %d, want existing %d", retried.Seq, first.Seq)
	}
	loaded, err := service.LoadSession(ctx, session.LoadSessionRequest{SessionRef: created.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if loaded.Session.Revision != 1 || len(loaded.Events) != 1 {
		t.Fatalf("loaded after retry = revision %d events %d, want 1/1", loaded.Session.Revision, len(loaded.Events))
	}
}

func TestStoreStateOperationsRepairNilState(t *testing.T) {
	t.Parallel()

	store := NewStore(Config{
		SessionIDGenerator: func() string { return "sess-1" },
	})
	ctx := context.Background()
	createdSession, err := store.GetOrCreate(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("GetOrCreate() error = %v", err)
	}

	store.mu.Lock()
	store.sessions[createdSession.SessionID].state = nil
	store.mu.Unlock()

	state, err := store.SnapshotState(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if state == nil || len(state) != 0 {
		t.Fatalf("SnapshotState() = %#v, want repaired empty state", state)
	}

	store.mu.Lock()
	store.sessions[createdSession.SessionID].state = nil
	store.mu.Unlock()
	if err := store.UpdateState(ctx, createdSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		if state == nil {
			t.Fatal("UpdateState() received nil state, want empty map")
		}
		state["mode"] = "chat"
		return state, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	state, err = store.SnapshotState(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState(after update) error = %v", err)
	}
	if got := state["mode"]; got != "chat" {
		t.Fatalf("state[mode] = %v, want chat", got)
	}

	if err := store.ReplaceState(ctx, createdSession.SessionRef, nil); err != nil {
		t.Fatalf("ReplaceState(nil) error = %v", err)
	}
	state, err = store.SnapshotState(ctx, createdSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState(after replace nil) error = %v", err)
	}
	if state == nil || len(state) != 0 {
		t.Fatalf("state after ReplaceState(nil) = %#v, want empty map", state)
	}
}

func TestStoreControllerAndParticipantBindings(t *testing.T) {
	t.Parallel()

	service := NewService(NewStore(Config{
		SessionIDGenerator: func() string { return "sess-1" },
	}))
	ctx := context.Background()
	createdSession, err := service.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	createdSession, err = service.BindController(ctx, session.BindControllerRequest{
		SessionRef: createdSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind:         session.ControllerKindACP,
			ControllerID: "copilot",
			EpochID:      "ep-1",
			Source:       "user_select",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	if got := createdSession.Controller.ControllerID; got != "copilot" {
		t.Fatalf("controller id = %q, want %q", got, "copilot")
	}

	createdSession, err = service.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: createdSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:            "part-1",
			Kind:          session.ParticipantKindACP,
			Role:          session.ParticipantRoleSidecar,
			Source:        "user_attach",
			ControllerRef: "ep-1",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}
	if got, want := len(createdSession.Participants), 1; got != want {
		t.Fatalf("len(participants) = %d, want %d", got, want)
	}

	createdSession, err = service.RemoveParticipant(ctx, session.RemoveParticipantRequest{
		SessionRef:    createdSession.SessionRef,
		ParticipantID: "part-1",
	})
	if err != nil {
		t.Fatalf("RemoveParticipant() error = %v", err)
	}
	if got := len(createdSession.Participants); got != 0 {
		t.Fatalf("len(participants) = %d, want 0", got)
	}
}

func ptrMessage(message model.Message) *model.Message {
	return &message
}
