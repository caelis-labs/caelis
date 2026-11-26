package session_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/internal/sdkctx"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestReferenceParticipantStoresConform(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		t.Parallel()

		participantLifecycleConformance(t, func(*testing.T) conformanceParticipantStore {
			return &committedFaultParticipantStore{participantBackend: sessionmemory.NewStore(sessionmemory.Config{})}
		})
	})
	t.Run("file", func(t *testing.T) {
		t.Parallel()

		participantLifecycleConformance(t, func(t *testing.T) conformanceParticipantStore {
			return &committedFaultParticipantStore{participantBackend: sdkctx.UseLogicalFileStore(t, sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()}))}
		})
	})
}

func TestParticipantEventMatchesRejectsUnrelatedAssistantEvent(t *testing.T) {
	t.Parallel()
	binding := session.ParticipantBinding{
		ID: "participant-a", Kind: session.ParticipantKindSubagent,
		Role: session.ParticipantRoleSidecar, DelegationID: "delegation-a",
	}
	requested := participantEvent("attach-a", binding, "attached")
	actual := &session.Event{
		ID: "event-a", IdempotencyKey: requested.IdempotencyKey,
		SessionID: "session-a", Seq: 1, Schema: session.EventSchemaVersion,
		Type: session.EventTypeAssistant, Visibility: requested.Visibility, Time: time.Now(),
	}
	if participantEventMatches(session.SessionRef{SessionID: "session-a"}, requested, actual) {
		t.Fatal("unrelated assistant event passed participant lifecycle validation")
	}
}

func TestParticipantJSONEqualRejectsCorruptedReturnedSessionTitle(t *testing.T) {
	t.Parallel()
	durable := session.Session{SessionRef: session.SessionRef{SessionID: "session-a"}, Revision: 2, Title: "durable title"}
	returned := session.CloneSession(durable)
	returned.Title = "corrupted title"
	if participantJSONEqual(returned, durable) {
		t.Fatal("corrupted returned Session.Title passed whole-object validation")
	}
}

type participantBackend interface {
	session.ManagedService
	session.ParticipantLifecycleService
}

type participantMutation string

const (
	participantPlainPut        participantMutation = "plain_put"
	participantPlainRemove     participantMutation = "plain_remove"
	participantLifecyclePut    participantMutation = "lifecycle_put"
	participantLifecycleRemove participantMutation = "lifecycle_remove"
)

type conformanceParticipantStore interface {
	session.ManagedService
	session.ParticipantLifecycleService
	session.SessionFenceService
	ArmParticipantCommittedFailure(participantMutation)
}

type committedFaultParticipantStore struct {
	participantBackend
	mu    sync.Mutex
	armed participantMutation
}

func (s *committedFaultParticipantStore) ArmParticipantCommittedFailure(mutation participantMutation) {
	s.mu.Lock()
	s.armed = mutation
	s.mu.Unlock()
}

func (s *committedFaultParticipantStore) consume(mutation participantMutation) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.armed != mutation {
		return false
	}
	s.armed = ""
	return true
}

func (s *committedFaultParticipantStore) PutParticipant(ctx context.Context, req session.PutParticipantRequest) (session.Session, error) {
	updated, err := s.participantBackend.PutParticipant(ctx, req)
	if err == nil && s.consume(participantPlainPut) {
		err = &session.CommittedError{Err: errors.New("forced post-commit report failure")}
	}
	return updated, err
}

func (s *committedFaultParticipantStore) RemoveParticipant(ctx context.Context, req session.RemoveParticipantRequest) (session.Session, error) {
	updated, err := s.participantBackend.RemoveParticipant(ctx, req)
	if err == nil && s.consume(participantPlainRemove) {
		err = &session.CommittedError{Err: errors.New("forced post-commit report failure")}
	}
	return updated, err
}

func (s *committedFaultParticipantStore) PutParticipantWithEvent(ctx context.Context, req session.PutParticipantWithEventRequest) (session.Session, *session.Event, error) {
	updated, event, err := s.participantBackend.PutParticipantWithEvent(ctx, req)
	if err == nil && s.consume(participantLifecyclePut) {
		err = &session.CommittedError{Err: errors.New("forced post-commit report failure")}
	}
	return updated, event, err
}

func (s *committedFaultParticipantStore) RemoveParticipantWithEvent(ctx context.Context, req session.RemoveParticipantWithEventRequest) (session.Session, *session.Event, error) {
	updated, event, err := s.participantBackend.RemoveParticipantWithEvent(ctx, req)
	if err == nil && s.consume(participantLifecycleRemove) {
		err = &session.CommittedError{Err: errors.New("forced post-commit report failure")}
	}
	return updated, event, err
}

func (s *committedFaultParticipantStore) AcquireSessionFence(ctx context.Context, req session.AcquireSessionFenceRequest) (session.SessionFence, error) {
	return s.participantBackend.(session.SessionFenceService).AcquireSessionFence(ctx, req)
}

func (s *committedFaultParticipantStore) SessionFence(ctx context.Context, ref session.SessionRef) (session.SessionFence, error) {
	return s.participantBackend.(session.SessionFenceReader).SessionFence(ctx, ref)
}

func (s *committedFaultParticipantStore) ReleaseSessionFence(ctx context.Context, req session.ReleaseSessionFenceRequest) error {
	return s.participantBackend.(session.SessionFenceService).ReleaseSessionFence(ctx, req)
}

func participantLifecycleConformance(t *testing.T, factory func(*testing.T) conformanceParticipantStore) {
	t.Helper()
	for _, test := range []struct {
		name string
		run  func(*testing.T, conformanceParticipantStore, session.Session, session.ParticipantBinding)
	}{
		{name: "plain delegation and revision CAS", run: checkPlainParticipantCAS},
		{name: "lifecycle conflict is atomic", run: checkLifecycleParticipantCAS},
		{name: "runtime mutation guard is fenced", run: checkParticipantMutationGuard},
		{name: "committed failures return exact results", run: checkParticipantCommittedResults},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := factory(t)
			active, binding := seedParticipant(t, store, test.name)
			test.run(t, store, active, binding)
		})
	}
}

func seedParticipant(t *testing.T, store conformanceParticipantStore, user string) (session.Session, session.ParticipantBinding) {
	t.Helper()
	ctx := context.Background()
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "sessiontest", UserID: user})
	if err != nil {
		t.Fatal(err)
	}
	binding := session.ParticipantBinding{
		ID: "shared-agent-id", Kind: session.ParticipantKindSubagent,
		Role: session.ParticipantRoleSidecar, DelegationID: "task-a",
	}
	before := active
	requestedEvent := participantEvent("attach-a", binding, "attached")
	active, persistedEvent, err := store.PutParticipantWithEvent(ctx, session.PutParticipantWithEventRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
		ExpectedDelegationID: stringPointer(binding.DelegationID), Binding: binding,
		Event: requestedEvent,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertParticipantSessionTransition(t, store, before, active, binding, "attached", persistedEvent)
	assertParticipantEvent(t, store, active.SessionRef, requestedEvent, persistedEvent)
	return active, binding
}

func checkPlainParticipantCAS(t *testing.T, store conformanceParticipantStore, active session.Session, binding session.ParticipantBinding) {
	t.Helper()
	ctx := context.Background()
	colliding := binding
	colliding.DelegationID = ""
	_, err := store.PutParticipant(ctx, session.PutParticipantRequest{SessionRef: active.SessionRef, Binding: colliding})
	assertDelegationConflict(t, err)

	wrongDelegation := "task-b"
	_, err = store.RemoveParticipant(ctx, session.RemoveParticipantRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
		ParticipantID: binding.ID, ExpectedDelegationID: &wrongDelegation,
	})
	assertDelegationConflict(t, err)

	staleRevision := active.Revision - 1
	_, err = store.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &staleRevision,
		ExpectedDelegationID: stringPointer(binding.DelegationID), Binding: binding,
	})
	var conflict *session.RevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale revision error = %v, want RevisionConflictError", err)
	}
	updatedBinding := binding
	updatedBinding.Label = "updated"
	updated, err := store.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
		ExpectedDelegationID: stringPointer(binding.DelegationID), Binding: updatedBinding,
	})
	if err != nil || updated.Revision != active.Revision+1 || len(updated.Participants) != 1 || updated.Participants[0].Label != "updated" {
		t.Fatalf("plain put result = %#v, %v", updated, err)
	}
	assertParticipantSessionTransition(t, store, active, updated, updatedBinding, "attached", nil)
	removed, err := store.RemoveParticipant(ctx, session.RemoveParticipantRequest{
		SessionRef: updated.SessionRef, ExpectedRevision: &updated.Revision,
		ParticipantID: binding.ID, ExpectedDelegationID: stringPointer(binding.DelegationID),
	})
	if err != nil || removed.Revision != updated.Revision+1 || len(removed.Participants) != 0 {
		t.Fatalf("plain remove result = %#v, %v", removed, err)
	}
	assertParticipantSessionTransition(t, store, updated, removed, binding, "detached", nil)
}

func checkLifecycleParticipantCAS(t *testing.T, store conformanceParticipantStore, active session.Session, binding session.ParticipantBinding) {
	t.Helper()
	ctx := context.Background()
	beforeEvents, err := store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	wrongDelegation := "task-b"
	_, _, err = store.RemoveParticipantWithEvent(ctx, session.RemoveParticipantWithEventRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
		ParticipantID: binding.ID, ExpectedDelegationID: &wrongDelegation,
		Event: participantEvent("detach-b", binding, "detached"),
	})
	assertDelegationConflict(t, err)
	after, err := store.Session(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	afterEvents, err := store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != active.Revision || len(after.Participants) != 1 || after.Participants[0].DelegationID != binding.DelegationID {
		t.Fatalf("failed lifecycle CAS mutated session: before=%#v after=%#v", active, after)
	}
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("failed lifecycle CAS appended event: before=%d after=%d", len(beforeEvents), len(afterEvents))
	}
	colliding := binding
	colliding.DelegationID = "task-b"
	_, _, err = store.PutParticipantWithEvent(ctx, session.PutParticipantWithEventRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
		ExpectedDelegationID: stringPointer(colliding.DelegationID), Binding: colliding,
		Event: participantEvent("attach-b", colliding, "attached"),
	})
	assertDelegationConflict(t, err)
	requestedEvent := participantEvent("detach-a", binding, "detached")
	removed, removedEvent, err := store.RemoveParticipantWithEvent(ctx, session.RemoveParticipantWithEventRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
		ParticipantID: binding.ID, ExpectedDelegationID: stringPointer(binding.DelegationID),
		Event: requestedEvent,
	})
	if err != nil || removedEvent == nil || removed.Revision != active.Revision+1 || len(removed.Participants) != 0 {
		t.Fatalf("lifecycle remove result = %#v, %#v, %v", removed, removedEvent, err)
	}
	assertParticipantSessionTransition(t, store, active, removed, binding, "detached", removedEvent)
	assertParticipantEvent(t, store, active.SessionRef, requestedEvent, removedEvent)
}

func checkParticipantMutationGuard(t *testing.T, store conformanceParticipantStore, active session.Session, binding session.ParticipantBinding) {
	t.Helper()
	ctx := context.Background()
	fence, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "owner-a"})
	if err != nil {
		t.Fatal(err)
	}
	if fence.FencingToken == 0 {
		t.Fatal("acquired participant mutation fence has no fencing token")
	}
	baseline, err := store.Session(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	baselineEvents, err := store.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	updatedBinding := binding
	updatedBinding.Label = "must-not-commit"
	guards := []struct {
		name  string
		guard session.MutationGuard
	}{
		{
			name: "wrong owner",
			guard: session.MutationGuard{
				Authority: session.MutationAuthorityRuntime, FenceID: fence.FenceID,
				OwnerID: "owner-b", FencingToken: fence.FencingToken,
			},
		},
		{
			name: "stale fencing token",
			guard: session.MutationGuard{
				Authority: session.MutationAuthorityRuntime, FenceID: fence.FenceID,
				OwnerID: "owner-a", FencingToken: fence.FencingToken - 1,
			},
		},
	}
	for _, item := range guards {
		t.Run(item.name, func(t *testing.T) {
			checkParticipantMutationGuardShape(t, store, baseline, baselineEvents, binding, updatedBinding, item.guard)
		})
	}
}

func checkParticipantMutationGuardShape(
	t *testing.T,
	store conformanceParticipantStore,
	baseline session.Session,
	baselineEvents []*session.Event,
	binding session.ParticipantBinding,
	updatedBinding session.ParticipantBinding,
	guard session.MutationGuard,
) {
	t.Helper()
	ctx := context.Background()

	_, err := store.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: baseline.SessionRef, ExpectedRevision: &baseline.Revision, MutationGuard: guard,
		ExpectedDelegationID: stringPointer(binding.DelegationID), Binding: updatedBinding,
	})
	assertParticipantMutationGuardRejected(t, store, baseline, baselineEvents, "plain put", err)

	_, err = store.RemoveParticipant(ctx, session.RemoveParticipantRequest{
		SessionRef: baseline.SessionRef, ExpectedRevision: &baseline.Revision, MutationGuard: guard,
		ParticipantID: binding.ID, ExpectedDelegationID: stringPointer(binding.DelegationID),
	})
	assertParticipantMutationGuardRejected(t, store, baseline, baselineEvents, "plain remove", err)

	_, _, err = store.PutParticipantWithEvent(ctx, session.PutParticipantWithEventRequest{
		SessionRef: baseline.SessionRef, ExpectedRevision: &baseline.Revision, MutationGuard: guard,
		ExpectedDelegationID: stringPointer(binding.DelegationID), Binding: updatedBinding,
		Event: participantEvent("guarded-put-must-not-commit", updatedBinding, "attached"),
	})
	assertParticipantMutationGuardRejected(t, store, baseline, baselineEvents, "lifecycle put", err)

	_, _, err = store.RemoveParticipantWithEvent(ctx, session.RemoveParticipantWithEventRequest{
		SessionRef: baseline.SessionRef, ExpectedRevision: &baseline.Revision, MutationGuard: guard,
		ParticipantID: binding.ID, ExpectedDelegationID: stringPointer(binding.DelegationID),
		Event: participantEvent("guarded-remove-must-not-commit", binding, "detached"),
	})
	assertParticipantMutationGuardRejected(t, store, baseline, baselineEvents, "lifecycle remove", err)
}

func assertParticipantMutationGuardRejected(
	t *testing.T,
	store conformanceParticipantStore,
	baseline session.Session,
	baselineEvents []*session.Event,
	operation string,
	err error,
) {
	t.Helper()
	var conflict *session.FenceConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("%s mismatched mutation guard error = %v, want FenceConflictError", operation, err)
	}
	durable, loadErr := store.Session(context.Background(), baseline.SessionRef)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !participantJSONEqual(session.CloneSession(durable), session.CloneSession(baseline)) {
		t.Fatalf("%s mutated durable session despite guard rejection: before=%#v after=%#v", operation, baseline, durable)
	}
	events, eventsErr := store.Events(context.Background(), session.EventsRequest{SessionRef: baseline.SessionRef, IncludeTransient: true})
	if eventsErr != nil {
		t.Fatal(eventsErr)
	}
	if !participantJSONEqual(session.CloneEvents(events), session.CloneEvents(baselineEvents)) {
		t.Fatalf("%s mutated durable events despite guard rejection: before=%#v after=%#v", operation, baselineEvents, events)
	}
}

func checkParticipantCommittedResults(t *testing.T, store conformanceParticipantStore, active session.Session, binding session.ParticipantBinding) {
	t.Helper()
	ctx := context.Background()
	updatedBinding := binding
	updatedBinding.Label = "committed"
	store.ArmParticipantCommittedFailure(participantPlainPut)
	updated, err := store.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &active.Revision,
		ExpectedDelegationID: stringPointer(binding.DelegationID), Binding: updatedBinding,
	})
	assertCommitted(t, err)
	if updated.Revision != active.Revision+1 || len(updated.Participants) != 1 || updated.Participants[0].Label != "committed" {
		t.Fatalf("committed plain put result = %#v", updated)
	}
	assertParticipantSessionTransition(t, store, active, updated, updatedBinding, "attached", nil)

	store.ArmParticipantCommittedFailure(participantPlainRemove)
	removed, err := store.RemoveParticipant(ctx, session.RemoveParticipantRequest{
		SessionRef: updated.SessionRef, ExpectedRevision: &updated.Revision,
		ParticipantID: binding.ID, ExpectedDelegationID: stringPointer(binding.DelegationID),
	})
	assertCommitted(t, err)
	if removed.Revision != updated.Revision+1 || len(removed.Participants) != 0 {
		t.Fatalf("committed plain remove result = %#v", removed)
	}
	assertParticipantSessionTransition(t, store, updated, removed, binding, "detached", nil)

	store.ArmParticipantCommittedFailure(participantLifecyclePut)
	putRequestedEvent := participantEvent("committed-attach", binding, "attached")
	restored, putEvent, err := store.PutParticipantWithEvent(ctx, session.PutParticipantWithEventRequest{
		SessionRef: removed.SessionRef, ExpectedRevision: &removed.Revision,
		ExpectedDelegationID: stringPointer(binding.DelegationID), Binding: binding,
		Event: putRequestedEvent,
	})
	assertCommitted(t, err)
	if putEvent == nil || restored.Revision != removed.Revision+1 || len(restored.Participants) != 1 {
		t.Fatalf("committed lifecycle put result = %#v, %#v", restored, putEvent)
	}
	assertParticipantSessionTransition(t, store, removed, restored, binding, "attached", putEvent)
	assertParticipantEvent(t, store, restored.SessionRef, putRequestedEvent, putEvent)

	store.ArmParticipantCommittedFailure(participantLifecycleRemove)
	removeRequestedEvent := participantEvent("committed-detach", binding, "detached")
	final, removeEvent, err := store.RemoveParticipantWithEvent(ctx, session.RemoveParticipantWithEventRequest{
		SessionRef: restored.SessionRef, ExpectedRevision: &restored.Revision,
		ParticipantID: binding.ID, ExpectedDelegationID: stringPointer(binding.DelegationID),
		Event: removeRequestedEvent,
	})
	assertCommitted(t, err)
	if removeEvent == nil || final.Revision != restored.Revision+1 || len(final.Participants) != 0 {
		t.Fatalf("committed lifecycle remove result = %#v, %#v", final, removeEvent)
	}
	assertParticipantSessionTransition(t, store, restored, final, binding, "detached", removeEvent)
	assertParticipantEvent(t, store, final.SessionRef, removeRequestedEvent, removeEvent)
}

func assertParticipantSessionTransition(
	t *testing.T,
	store conformanceParticipantStore,
	before session.Session,
	actual session.Session,
	binding session.ParticipantBinding,
	action string,
	lifecycleEvent *session.Event,
) {
	t.Helper()
	expected := session.CloneSession(before)
	switch strings.TrimSpace(action) {
	case "attached":
		session.PutParticipantBinding(&expected, binding)
	case "detached":
		session.RemoveParticipantBinding(&expected, binding.ID)
	default:
		t.Fatalf("unsupported participant transition %q", action)
	}
	expected.Revision = before.Revision + 1
	expected.UpdatedAt = actual.UpdatedAt
	if lifecycleEvent != nil {
		expected.UpdatedAt = lifecycleEvent.Time
	}
	if !participantJSONEqual(session.CloneSession(actual), expected) {
		t.Fatalf("participant mutation returned a non-exact session: expected=%#v actual=%#v", expected, actual)
	}
	durable, err := store.Session(context.Background(), actual.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if !participantJSONEqual(session.CloneSession(actual), session.CloneSession(durable)) {
		t.Fatalf("participant mutation returned session differs from durable session: returned=%#v durable=%#v", actual, durable)
	}
}

func assertParticipantEvent(t *testing.T, store conformanceParticipantStore, ref session.SessionRef, requested, actual *session.Event) {
	t.Helper()
	if !participantEventMatches(ref, requested, actual) {
		t.Fatalf("participant lifecycle event is not the exact normalized request: requested=%#v actual=%#v", requested, actual)
	}
	events, err := store.Events(context.Background(), session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, durable := range events {
		if durable == nil || strings.TrimSpace(durable.ID) != strings.TrimSpace(actual.ID) {
			continue
		}
		if actual.Text != durable.Text || !participantJSONEqual(session.CloneEvent(actual), session.CloneEvent(durable)) {
			t.Fatalf("returned participant event differs from durable event: returned=%#v durable=%#v", actual, durable)
		}
		return
	}
	t.Fatalf("returned participant event %q is absent from durable history", strings.TrimSpace(actual.ID))
}

func participantEventMatches(ref session.SessionRef, requested, actual *session.Event) bool {
	if requested == nil || actual == nil || strings.TrimSpace(actual.ID) == "" || actual.Seq == 0 {
		return false
	}
	expected := session.CanonicalizeEvent(requested)
	if expected == nil {
		return false
	}
	expected.ID = strings.TrimSpace(actual.ID)
	expected.SessionID = strings.TrimSpace(ref.SessionID)
	expected.Seq = actual.Seq
	if expected.Schema == 0 {
		expected.Schema = session.EventSchemaVersion
	}
	if expected.Time.IsZero() {
		expected.Time = actual.Time
	}
	if expected.Type == "" {
		expected.Type = session.EventTypeOf(expected)
	}
	if expected.Visibility == "" {
		expected.Visibility = session.VisibilityCanonical
	}
	return expected.Text == actual.Text && participantJSONEqual(expected, session.CloneEvent(actual))
}

func participantJSONEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func assertCommitted(t *testing.T, err error) {
	t.Helper()
	if !session.IsCommitted(err) {
		t.Fatalf("error = %v, want CommittedError", err)
	}
}

func assertDelegationConflict(t *testing.T, err error) {
	t.Helper()
	var conflict *session.ParticipantBindingConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %v, want ParticipantBindingConflictError", err)
	}
}

func participantEvent(key string, binding session.ParticipantBinding, action string) *session.Event {
	protocol := session.NewParticipantProtocol(session.ProtocolParticipant{Action: action})
	return &session.Event{
		IdempotencyKey: key, Type: session.EventTypeParticipant, Visibility: session.VisibilityMirror,
		Protocol: &protocol,
		Scope: &session.EventScope{Participant: session.ParticipantRef{
			ID: binding.ID, Kind: binding.Kind, Role: binding.Role, DelegationID: binding.DelegationID,
		}},
	}
}

func stringPointer(value string) *string { return &value }
