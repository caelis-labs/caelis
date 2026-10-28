package sessiontest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

type idleBackend interface {
	session.Service
	session.SessionFenceService
	session.IdleCompactionStore
	session.CompactionReceiptStore
}

type idleFixture struct {
	store      idleBackend
	now        *time.Time
	active     session.Session
	fence      session.SessionFence
	release    session.ReleaseForIdleCompactionRequest
	window     session.IdleCompactionWindow
	event      *session.Event
	reopen     func() idleBackend
	recovery   session.PriorHostCompactionReceiptRecovery
	authorized bool
}

func newIdleFixture(t *testing.T, backend string) *idleFixture {
	t.Helper()
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	f := &idleFixture{now: &now, authorized: true}
	authorize := func(context.Context) (func(), bool) { return func() {}, f.authorized }
	if backend == "memory" {
		store, capability := sessionmemory.NewStoreWithPriorHostFences(sessionmemory.Config{Clock: clock}, authorize)
		f.store = store
		f.recovery = capability.(session.PriorHostCompactionReceiptRecovery)
		f.reopen = func() idleBackend { return f.store }
	} else {
		root := t.TempDir()
		f.reopen = func() idleBackend {
			store, capability := sessionfile.NewStoreWithPriorHostFences(sessionfile.Config{RootDir: root, Clock: clock}, authorize)
			f.recovery = capability.(session.PriorHostCompactionReceiptRecovery)
			return store
		}
		f.store = f.reopen()
	}
	var err error
	f.active, err = f.store.StartSession(t.Context(), session.StartSessionRequest{AppName: "idle", UserID: "fixture", Workspace: session.WorkspaceRef{Key: "fixture", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	f.fence, err = f.store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: f.active.SessionRef, OwnerID: "host"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.ContextWithRuntimeFence(t.Context(), f.fence)
	for _, role := range []model.Role{model.RoleUser, model.RoleAssistant} {
		message := model.NewTextMessage(role, "canonical source evidence for the checkpoint")
		event := &session.Event{Type: session.EventTypeUser, Message: &message}
		if role == model.RoleAssistant {
			event.Type = session.EventTypeAssistant
		}
		if _, err = f.store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: f.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(ctx), Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := f.store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: f.active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := session.CheckpointSourceStamp(loaded)
	if err != nil {
		t.Fatal(err)
	}
	through := loaded.Events[len(loaded.Events)-1]
	f.release = session.NewIdleCompactionRelease(f.fence, session.IdleCompactionSource{JobID: "job", HostID: "host", ActivationID: "activation", TurnID: "turn", Stamp: stamp, ThroughSeq: through.Seq, Coverage: session.IdleCompactionCoverage{Revision: 1, EventCount: len(loaded.Events), EndSeq: through.Seq, EndID: through.ID}})
	f.window, err = f.store.ReleaseSessionFenceForIdleCompaction(t.Context(), f.release)
	if err != nil {
		t.Fatal(err)
	}
	summary := model.NewTextMessage(model.RoleUser, "CONTEXT CHECKPOINT: complete source evidence")
	f.event = &session.Event{ID: "candidate", IdempotencyKey: "candidate", SessionID: f.active.SessionID, Type: session.EventTypeCompact, Visibility: session.VisibilityCanonical, Message: &summary, Meta: map[string]any{session.CheckpointMetadataKey: session.CheckpointDataValue(session.CheckpointData{ContractVersion: 1, Revision: 1, SummarizedThroughSeq: through.Seq, SummarizedThroughID: through.ID, SourceEventCount: len(loaded.Events)})}}
	return f
}

func (f *idleFixture) grant(t *testing.T) session.IdleCompactionGrant {
	t.Helper()
	grant, err := f.store.IssueIdleCompactionGrant(t.Context(), session.IssueIdleCompactionGrantRequest{Window: f.window, Event: f.event})
	if err != nil {
		t.Fatal(err)
	}
	return grant
}
func (f *idleFixture) revision(t *testing.T) uint64 {
	t.Helper()
	active, err := f.store.Session(t.Context(), f.active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	return active.Revision
}

func TestIdleCompactionStoreContract(t *testing.T) {
	for _, backend := range []string{"memory", "file"} {
		t.Run(backend, func(t *testing.T) {
			t.Run("commit_and_reopen_original_identity", func(t *testing.T) {
				f := newIdleFixture(t, backend)
				*f.now = f.now.Add(179 * time.Second)
				grant := f.grant(t)
				req := session.AppendIdleCompactionCheckpointRequest{Grant: grant, Event: f.event, ExpectedRevision: f.revision(t)}
				persisted, err := f.store.AppendIdleCompactionCheckpoint(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				if persisted.Seq <= 2 {
					t.Fatalf("coverage overlaps checkpoint: %+v", persisted)
				}
				f.store = f.reopen()
				*f.now = f.now.Add(time.Minute)
				again, err := f.store.AppendIdleCompactionCheckpoint(t.Context(), req)
				if err != nil || again.Seq != persisted.Seq {
					t.Fatalf("replay=%+v err=%v", again, err)
				}
				altered := session.CloneEvent(f.event)
				altered.Message = &model.Message{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("changed")}}
				req.Event = altered
				if _, err = f.store.AppendIdleCompactionCheckpoint(t.Context(), req); err == nil {
					t.Fatal("same ID accepted different payload")
				}
			})
			t.Run("grant_is_two_seconds_not_window", func(t *testing.T) {
				f := newIdleFixture(t, backend)
				grant := f.grant(t)
				*f.now = f.now.Add(2 * time.Second)
				_, err := f.store.AppendIdleCompactionCheckpoint(t.Context(), session.AppendIdleCompactionCheckpointRequest{Grant: grant, Event: f.event, ExpectedRevision: f.revision(t)})
				if !errors.Is(err, session.ErrIdleCompactionRevoked) {
					t.Fatalf("expired grant=%v", err)
				}
				if _, err = f.store.IssueIdleCompactionGrant(t.Context(), session.IssueIdleCompactionGrantRequest{Window: f.window, Event: f.event}); err == nil {
					t.Fatal("grant renewed")
				}
			})
			t.Run("window_expires_independently", func(t *testing.T) {
				f := newIdleFixture(t, backend)
				*f.now = f.now.Add(180 * time.Second)
				if _, err := f.store.IssueIdleCompactionGrant(t.Context(), session.IssueIdleCompactionGrantRequest{Window: f.window, Event: f.event}); !errors.Is(err, session.ErrIdleCompactionRevoked) {
					t.Fatalf("window=%v", err)
				}
			})
			t.Run("foreground_permanently_revokes", func(t *testing.T) {
				f := newIdleFixture(t, backend)
				grant := f.grant(t)
				fence, err := f.store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: f.active.SessionRef, OwnerID: "host"})
				if err != nil {
					t.Fatal(err)
				}
				if err = f.store.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(fence)); err != nil {
					t.Fatal(err)
				}
				if _, err = f.store.ReleaseSessionFenceForIdleCompaction(t.Context(), f.release); err == nil {
					t.Fatal("old transition recreated a window")
				}
				if _, err = f.store.AppendIdleCompactionCheckpoint(t.Context(), session.AppendIdleCompactionCheckpointRequest{Grant: grant, Event: f.event, ExpectedRevision: f.revision(t)}); !errors.Is(err, session.ErrIdleCompactionRevoked) {
					t.Fatalf("foreground revocation=%v", err)
				}
			})
			t.Run("narrow_grant_and_receipt_after_foreground", func(t *testing.T) {
				f := newIdleFixture(t, backend)
				bad := session.CloneEvent(f.event)
				bad.Type = session.EventTypeUser
				if _, err := f.store.IssueIdleCompactionGrant(t.Context(), session.IssueIdleCompactionGrantRequest{Window: f.window, Event: bad}); err == nil {
					t.Fatal("window authorized arbitrary user append")
				}
				ticket, err := f.store.BeginCompactionAttempt(t.Context(), session.BeginCompactionAttemptRequest{Window: f.window, Invocation: model.Invocation{ID: "attempt", Provider: "anthropic", Model: "fixture", StartedAt: *f.now}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: f.active.SessionRef, OwnerID: "foreground"}); err != nil {
					t.Fatal(err)
				}
				result := session.CompactionAttemptResult{Outcome: "cancelled", FinishedAt: f.now.Add(time.Second), Usage: model.Usage{CachedInputTokens: 6000, CacheWriteTokensReported: true, CacheWrite5mTokensReported: true}}
				if err = f.store.StageCompactionReceipt(t.Context(), ticket, result); err != nil {
					t.Fatal(err)
				}
				f.store = f.reopen()
				event, err := f.store.FinalizeCompactionReceipt(t.Context(), ticket)
				if err != nil {
					t.Fatal(err)
				}
				if session.IsMainInvocationVisibleEvent(event) || session.IsClientReplayEvent(event) || !session.IsModelInvocationReceipt(event) {
					t.Fatalf("receipt escaped journal: %+v", event)
				}
				again, err := f.store.FinalizeCompactionReceipt(t.Context(), ticket)
				if err != nil || again.Seq != event.Seq {
					t.Fatalf("receipt replay=%+v %v", again, err)
				}
				loaded, err := f.store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: f.active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, e := range loaded.Events {
					if session.IsModelInvocationReceipt(e) {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("receipt count=%d", count)
				}
			})
		})
	}
}

func TestIdleCompactionNarrowClaimsAndRecovery(t *testing.T) {
	for _, backend := range []string{"memory", "file"} {
		t.Run(backend, func(t *testing.T) {
			t.Run("coverage_and_protocol", func(t *testing.T) {
				f := newIdleFixture(t, backend)
				for _, kind := range []string{"count", "revision", "id", "seq", "protocol"} {
					event := session.CloneEvent(f.event)
					data, _ := session.CheckpointDataFromEvent(event)
					switch kind {
					case "count":
						data.SourceEventCount++
					case "revision":
						data.Revision++
					case "id":
						data.SummarizedThroughID = "not-the-source"
					case "seq":
						data.SummarizedThroughSeq--
					case "protocol":
						event.Protocol = &session.EventProtocol{Method: "arbitrary"}
					}
					event.Meta[session.CheckpointMetadataKey] = session.CheckpointDataValue(data)
					if _, err := f.store.IssueIdleCompactionGrant(t.Context(), session.IssueIdleCompactionGrantRequest{Window: f.window, Event: event}); !errors.Is(err, session.ErrIdleCompactionRevoked) {
						t.Fatalf("%s accepted: %v", kind, err)
					}
				}
				grant := f.grant(t)
				for _, claim := range []any{f.window, grant} {
					raw, err := json.Marshal(claim)
					if err != nil || string(raw) != "{}" {
						t.Fatalf("bearer leaked: %s %v", raw, err)
					}
				}
			})
			t.Run("bounded_reservations_never_fabricate_attempts", func(t *testing.T) {
				f := newIdleFixture(t, backend)
				var first session.BeginCompactionAttemptRequest
				for i := range 9 {
					req := session.BeginCompactionAttemptRequest{Window: f.window, Invocation: model.Invocation{ID: fmt.Sprintf("attempt-%d", i), Provider: "anthropic", Model: "fixture", StartedAt: *f.now}}
					ticket, err := f.store.BeginCompactionAttempt(t.Context(), req)
					if i == 0 {
						first = req
					}
					if i < 8 && err != nil {
						t.Fatal(err)
					}
					if i == 8 && !errors.Is(err, session.ErrIdleCompactionRevoked) {
						t.Fatalf("unbounded pending attempts: %v", err)
					}
					if i == 0 {
						raw, _ := json.Marshal(ticket) //nolint:staticcheck // SA9005: verify opaque claims do not serialize bearer fields.
						if string(raw) != "{}" {
							t.Fatal("receipt bearer leaked")
						}
					}
				}
				if err := f.store.CancelCompactionReservation(t.Context(), first); err != nil {
					t.Fatal(err)
				}
				if err := f.store.CancelCompactionReservation(t.Context(), first); err != nil {
					t.Fatal(err)
				}
				loaded, err := f.store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: f.active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				for _, event := range loaded.Events {
					if session.IsModelInvocationReceipt(event) {
						t.Fatal("reservation invented a billable attempt")
					}
				}
			})
			t.Run("prior_host_only_unknown_or_sealed_no_replay", func(t *testing.T) {
				f := newIdleFixture(t, backend)
				if _, ok := any(f.store).(session.PriorHostCompactionReceiptRecovery); ok {
					t.Fatal("ordinary Store exposed recovery authority")
				}
				for i := range 2 {
					ticket, err := f.store.BeginCompactionAttempt(t.Context(), session.BeginCompactionAttemptRequest{Window: f.window, Invocation: model.Invocation{ID: fmt.Sprintf("original-%d", i), Provider: "anthropic", Model: "fixture", StartedAt: *f.now}})
					if err != nil {
						t.Fatal(err)
					}
					if i == 0 {
						result := session.CompactionAttemptResult{Outcome: "completed", FinishedAt: f.now.Add(-time.Minute), Usage: model.Usage{CachedInputTokens: 6000, CacheWriteTokens: 200, CachedInputTokensReported: true, CacheWriteTokensReported: true}}
						if err = f.store.StageCompactionReceipt(t.Context(), ticket, result); err != nil {
							t.Fatal(err)
						}
					}
				}
				f.store = f.reopen()
				f.authorized = false
				if err := f.recovery.RecoverCompactionReceipts(t.Context(), f.active.SessionRef, "next-host"); err == nil {
					t.Fatal("unproven Host recovered accounting")
				}
				f.authorized = true
				if err := f.recovery.RecoverCompactionReceipts(t.Context(), f.active.SessionRef, "host"); err != nil {
					t.Fatal(err)
				}
				if err := f.recovery.RecoverCompactionReceipts(t.Context(), f.active.SessionRef, "next-host"); err != nil {
					t.Fatal(err)
				}
				if err := f.recovery.RecoverCompactionReceipts(t.Context(), f.active.SessionRef, "next-host"); err != nil {
					t.Fatal(err)
				}
				loaded, err := f.store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: f.active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, event := range loaded.Events {
					if !session.IsModelInvocationReceipt(event) {
						continue
					}
					count++
					if session.IsMainInvocationVisibleEvent(event) {
						t.Fatal("accounting entered model context")
					}
					sdk := event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)
					usage := session.UsageSnapshotFromSessionEvent(event)
					switch event.ID {
					case "original-0":
						if sdk["outcome"] != "completed" || usage == nil || usage.CacheWriteTokens != 200 {
							t.Fatalf("sealed result lost: %+v", event.Invocation)
						}
					case "original-1":
						if sdk["outcome"] != "unknown" || sdk["usage_status"] != "unknown" || usage != nil {
							t.Fatalf("fabricated accounting: %+v", event.Invocation)
						}
					default:
						t.Fatalf("identity replaced: %s", event.ID)
					}
				}
				if count != 2 {
					t.Fatalf("receipts=%d", count)
				}
			})
		})
	}
}
