package controladapter

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/file"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/internal/kernel"
)

type usageTestStore interface {
	session.Service
	session.EventBatchService
	session.EventBatchStateService
	session.PagedReader
	session.EventCheckpointReader
}

type usageProbeStore struct {
	usageTestStore
	pages   atomic.Int64
	reads   atomic.Int64
	hook    func(session.EventPageRequest) error
	cutHook func(int64)
	cuts    atomic.Int64
}

func (s *usageProbeStore) EventCheckpoint(ctx context.Context, ref session.SessionRef) (session.EventCheckpoint, error) {
	cut, err := s.usageTestStore.(session.EventCheckpointReader).EventCheckpoint(ctx, ref)
	ordinal := s.cuts.Add(1)
	if s.cutHook != nil {
		s.cutHook(ordinal)
	}
	return cut, err
}

func (s *usageProbeStore) Events(ctx context.Context, req session.EventsRequest) ([]*session.Event, error) {
	s.reads.Add(1)
	return s.usageTestStore.Events(ctx, req)
}

func (s *usageProbeStore) EventsPage(ctx context.Context, req session.EventPageRequest) (session.EventPage, error) {
	s.pages.Add(1)
	if s.hook != nil {
		if err := s.hook(req); err != nil {
			return session.EventPage{}, err
		}
	}
	return s.usageTestStore.(session.PagedReader).EventsPage(ctx, req)
}

func usageTestEvent(id string, tokens int) *session.Event {
	message := model.NewTextMessage(model.RoleAssistant, "result payload")
	return &session.Event{ID: id, Type: session.EventTypeAssistant, Message: &message,
		Invocation: &session.EventInvocation{Provider: "fixture", Model: "main"},
		Meta:       map[string]any{"prompt_tokens": tokens, "total_tokens": tokens}}
}

func usageTestDriver(t *testing.T, store *usageProbeStore, active session.Session, cache *UsageReadModel) *assembler {
	t.Helper()
	d, err := newAssemblerForSession(t.Context(), &runtimeDeps{
		Session: SessionRuntimeDeps{Store: store}, Status: StatusRuntimeDeps{UsageReadModel: cache},
	}, active, "tui", "")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func appendUsageTestEvents(t *testing.T, store usageTestStore, ref session.SessionRef, events ...*session.Event) {
	t.Helper()
	if _, err := store.AppendEvents(t.Context(), session.AppendEventsRequest{SessionRef: ref, Events: events}); err != nil {
		t.Fatal(err)
	}
}

func TestUsageReadModelRebuildRestartAndCatchUp(t *testing.T) {
	root := t.TempDir()
	store := &usageProbeStore{usageTestStore: file.NewStore(file.Config{RootDir: root})}
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	var events []*session.Event
	for n := range 250 {
		events = append(events, usageTestEvent(fmt.Sprint(n), 10))
	}
	appendUsageTestEvents(t, store, active.SessionRef, events...)
	cache := NewUsageReadModel()
	d := usageTestDriver(t, store, active, cache)
	read := func(want int) sessionUsageView {
		t.Helper()
		view, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true)
		if err != nil || view.breakdown.Total.TotalTokens != want {
			t.Fatalf("usage=%+v err=%v want=%d", view.breakdown, err, want)
		}
		return view
	}
	read(2500)
	if store.pages.Load() != 2 || store.reads.Load() != 0 {
		t.Fatalf("cold reads: pages=%d full=%d", store.pages.Load(), store.reads.Load())
	}
	read(2500)
	if store.pages.Load() != 2 {
		t.Fatal("unchanged query replayed history")
	}
	appendUsageTestEvents(t, store, active.SessionRef, usageTestEvent("added", 20))
	view := read(2520)
	if store.pages.Load() != 3 {
		t.Fatal("increment did not read exactly one tail page")
	}
	for _, event := range view.events {
		if event.Message != nil || event.Tool != nil {
			t.Fatal("usage cache retained transcript payload")
		}
	}
	// A new Host/Store rebuilds from canonical truth; no usage snapshot is a
	// second authority, and the first subsequent increment still reads a tail.
	store = &usageProbeStore{usageTestStore: file.NewStore(file.Config{RootDir: root})}
	cache = NewUsageReadModel()
	d = usageTestDriver(t, store, active, cache)
	read(2520)
	appendUsageTestEvents(t, store, active.SessionRef, usageTestEvent("after-restart", 30))
	read(2550)
	if store.pages.Load() != 3 || store.reads.Load() != 0 {
		t.Fatalf("restart/catch-up pages=%d full=%d", store.pages.Load(), store.reads.Load())
	}
}

func TestUsageReadModelLateReceiptRetractsLegacyUsageAndKeepsBarriers(t *testing.T) {
	store := &usageProbeStore{usageTestStore: inmemory.NewStore(inmemory.Config{})}
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	first := usageTestEvent("legacy", 10)
	first.Meta["caelis"] = map[string]any{"sdk": map[string]any{"usage_receipt_id": "receipt"}}
	appendUsageTestEvents(t, store, active.SessionRef, first)
	cache := NewUsageReadModel()
	d := usageTestDriver(t, store, active, cache)
	check := func(want int) {
		t.Helper()
		view, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true)
		if err != nil || view.breakdown.Total.TotalTokens != want {
			t.Fatalf("total=%d err=%v want=%d", view.breakdown.Total.TotalTokens, err, want)
		}
	}
	check(10)
	receipt := session.NewModelInvocationReceipt(model.Invocation{ID: "receipt", Provider: "fixture", Model: "main", Usage: model.Usage{PromptTokens: 10, TotalTokens: 10}}, "main")
	appendUsageTestEvents(t, store, active.SessionRef, receipt)
	check(10)
	pages := store.pages.Load()
	appendUsageTestEvents(t, store, active.SessionRef, receipt)
	check(10)
	if store.pages.Load() != pages {
		t.Fatal("idempotent receipt advanced accounting")
	}
	call := func(id string) *session.Event {
		event := usageTestEvent(id, 7)
		event.Type, event.MessageID = session.EventTypeToolCall, "split-response"
		event.Message = nil
		event.Tool = &session.EventTool{ID: id, Name: "read", Input: map[string]any{"path": "fixture"}}
		return event
	}
	appendUsageTestEvents(t, store, active.SessionRef, call("call-1"), call("call-2"))
	check(17)
	appendUsageTestEvents(t, store, active.SessionRef, &session.Event{ID: "barrier", Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "call-1", Name: "read", Output: map[string]any{"ok": true}}}, call("call-3"))
	check(24)
}

func TestUsageReadModelTracksChildMembershipAndStateOnlyUsage(t *testing.T) {
	store := &usageProbeStore{usageTestStore: inmemory.NewStore(inmemory.Config{})}
	parent, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	appendUsageTestEvents(t, store, parent.SessionRef, usageTestEvent("parent", 10))
	appendUsageTestEvents(t, store, child.SessionRef, usageTestEvent("child", 20))
	cache := NewUsageReadModel()
	d := usageTestDriver(t, store, parent, cache)
	check := func(want int) {
		t.Helper()
		view, err := cache.read(t.Context(), d, parent.SessionRef, tokenUsageCategoryMain, true)
		if err != nil {
			t.Fatal(err)
		}
		breakdown, err := d.sessionTokenUsageBreakdownFromView(t.Context(), view)
		if err != nil || breakdown.Total.TotalTokens != want {
			t.Fatalf("total=%d err=%v want=%d", breakdown.Total.TotalTokens, err, want)
		}
	}
	check(10)
	if _, err := store.PutParticipant(t.Context(), session.PutParticipantRequest{SessionRef: parent.SessionRef, Binding: session.ParticipantBinding{ID: "child", Kind: session.ParticipantKindSubagent, SessionID: child.SessionID}}); err != nil {
		t.Fatal(err)
	}
	check(30)
	appendUsageTestEvents(t, store, child.SessionRef, usageTestEvent("child-next", 5))
	check(35)
	check(35)
	if _, err := store.UpdateState(t.Context(), session.UpdateStateRequest{SessionRef: child.SessionRef, Update: func(state map[string]any) (map[string]any, error) {
		state[kernel.StateUsageAccounting] = map[string]any{"auto_review": map[string]any{"total_tokens": 3}}
		return state, nil
	}}); err != nil {
		t.Fatal(err)
	}
	check(38)
	if _, err := store.RemoveParticipant(t.Context(), session.RemoveParticipantRequest{SessionRef: parent.SessionRef, ParticipantID: "child"}); err != nil {
		t.Fatal(err)
	}
	check(10)
}

func TestUsageReadModelEmptyGaugeRetractsPriorUsage(t *testing.T) {
	store := &usageProbeStore{usageTestStore: inmemory.NewStore(inmemory.Config{})}
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	active, err = store.BindController(t.Context(), session.BindControllerRequest{SessionRef: active.SessionRef, Binding: session.ControllerBinding{Kind: session.ControllerKindACP, ControllerID: "fixture", AgentName: "fixture", EpochID: "epoch"}})
	if err != nil {
		t.Fatal(err)
	}
	cache := NewUsageReadModel()
	d := usageTestDriver(t, store, active, cache)
	for index, gauge := range []session.ContextUsageSnapshot{{Size: 200000, Used: 42000}, {}} {
		appendUsageTestEvents(t, store, active.SessionRef, &session.Event{Type: session.EventTypeCustom, Visibility: session.VisibilityMirror,
			Scope:      &session.EventScope{Controller: session.ControllerRef{Kind: session.ControllerKindACP, ID: "fixture", EpochID: "epoch"}},
			Invocation: &session.EventInvocation{Provider: "fixture"}, ContextUsage: &gauge})
		view, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, false)
		want := 42000
		if index == 1 {
			want = 0
		}
		if err != nil || view.breakdown.Total.TotalTokens != want {
			t.Fatalf("total=%d err=%v want=%d", view.breakdown.Total.TotalTokens, err, want)
		}
	}
}

func TestStatusRejectsIncompleteUsageReads(t *testing.T) {
	for _, failure := range []string{"parent", "child", "tasks"} {
		t.Run(failure, func(t *testing.T) {
			store := &usageProbeStore{usageTestStore: inmemory.NewStore(inmemory.Config{})}
			parent, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "user"})
			if err != nil {
				t.Fatal(err)
			}
			child, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "user"})
			if err != nil {
				t.Fatal(err)
			}
			appendUsageTestEvents(t, store, parent.SessionRef, usageTestEvent("parent", 10))
			appendUsageTestEvents(t, store, child.SessionRef, usageTestEvent("child", 20))
			if _, err := store.PutParticipant(t.Context(), session.PutParticipantRequest{SessionRef: parent.SessionRef, Binding: session.ParticipantBinding{ID: "child", Kind: session.ParticipantKindSubagent, SessionID: child.SessionID}}); err != nil {
				t.Fatal(err)
			}
			unavailable := errors.New("fixture accounting unavailable")
			store.hook = func(req session.EventPageRequest) error {
				if failure == "parent" && req.SessionRef == parent.SessionRef || failure == "child" && req.SessionRef == child.SessionRef {
					return unavailable
				}
				return nil
			}
			d := usageTestDriver(t, store, parent, NewUsageReadModel())
			d.deps.Status.TaskEntriesFn = func(context.Context, session.SessionRef) ([]*taskapi.Entry, error) {
				if failure == "tasks" {
					return nil, unavailable
				}
				return nil, nil
			}
			if result, err := d.Status(t.Context()); !errors.Is(err, unavailable) {
				t.Fatalf("status presented a partial total: usage=%+v err=%v", result.Usage, err)
			}
			store.hook, d.deps.Status.TaskEntriesFn = nil, nil
			if result, err := d.Status(t.Context()); err != nil || result.Usage.SessionUsageTotal.TotalTokens != 30 {
				t.Fatalf("recovery usage=%+v err=%v", result.Usage, err)
			}
		})
	}
}

func TestUsageReadModelKeepsPublishedContextImmutableDuringCatchUp(t *testing.T) {
	store := &usageProbeStore{usageTestStore: inmemory.NewStore(inmemory.Config{})}
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	appendUsageTestEvents(t, store, active.SessionRef, usageTestEvent("first", 10))
	cache := NewUsageReadModel()
	d := usageTestDriver(t, store, active, cache)
	view, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true)
	if err != nil {
		t.Fatal(err)
	}
	want := view.context.Snapshot(128000, runtime.CompactionConfig{}, "fixture", "main")
	appendUsageTestEvents(t, store, active.SessionRef, usageTestEvent("next", 20))
	done := make(chan error, 1)
	go func() {
		_, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true)
		done <- err
	}()
	for range 100 {
		if got := view.context.Snapshot(128000, runtime.CompactionConfig{}, "fixture", "main"); got != want {
			t.Fatalf("published snapshot mutated: got=%+v want=%+v", got, want)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUsageReadModelAccountingCatchUpPreservesContext(t *testing.T) {
	store := &usageProbeStore{usageTestStore: file.NewStore(file.Config{RootDir: t.TempDir()})}
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	var events []*session.Event
	for n := range 250 {
		events = append(events, usageTestEvent(fmt.Sprint(n), 10))
	}
	appendUsageTestEvents(t, store, active.SessionRef, events...)
	cache := NewUsageReadModel()
	d := usageTestDriver(t, store, active, cache)
	view, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true)
	if err != nil {
		t.Fatal(err)
	}
	if store.pages.Load() != 2 || store.reads.Load() != 0 {
		t.Fatalf("initial build pages=%d full=%d", store.pages.Load(), store.reads.Load())
	}
	for index, event := range []*session.Event{
		usageTestEvent("canonical-tail", 20),
		session.NewModelInvocationReceipt(model.Invocation{ID: "receipt-tail", Provider: "fixture", Model: "main", Usage: model.Usage{PromptTokens: 30, TotalTokens: 30}}, "main"),
	} {
		previous := view.context.Snapshot(128000, runtime.CompactionConfig{}, "fixture", "main")
		previousProjection := view.context
		seq, pages := cache.entries[active.SessionRef].seq, store.pages.Load()
		appendUsageTestEvents(t, store, active.SessionRef, event)
		var requests []session.EventPageRequest
		store.hook = func(req session.EventPageRequest) error {
			requests = append(requests, req)
			return nil
		}
		accounting, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(requests) != 1 || requests[0].AfterSeq != seq || store.pages.Load() != pages+1 {
			t.Fatalf("accounting did not read only the tail: requests=%+v pages=%d", requests, store.pages.Load())
		}
		if accounting.context == nil {
			t.Error("accounting-only catch-up discarded an existing context projection")
		}
		view, err = cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true)
		if err != nil {
			t.Fatal(err)
		}
		if store.pages.Load() != pages+1 || store.reads.Load() != 0 {
			t.Fatalf("context query reread history after accounting catch-up: pages=%d want=%d full=%d", store.pages.Load(), pages+1, store.reads.Load())
		}
		all, err := sessionUsageEvents(t.Context(), store.usageTestStore, active.SessionRef)
		if err != nil {
			t.Fatal(err)
		}
		var canonical []*session.Event
		for _, event := range all {
			if session.IsCanonicalHistoryEvent(event) {
				canonical = append(canonical, event)
			}
		}
		want := runtime.ComputeUsageSnapshotForModel(canonical, nil, 128000, runtime.CompactionConfig{}, "fixture", "main")
		if got := view.context.Snapshot(128000, runtime.CompactionConfig{}, "fixture", "main"); got != want {
			t.Fatalf("context=%+v want full reconstruction=%+v", got, want)
		}
		if want := sessionTokenUsageBreakdownFromEvents(all, tokenUsageCategoryMain); view.breakdown.Total != want.Total || accounting.breakdown.Total != want.Total || !maps.Equal(view.breakdown.ByModel, want.ByModel) || !maps.Equal(accounting.breakdown.ByModel, want.ByModel) {
			t.Fatalf("accounting=%+v context query=%+v want=%+v", accounting.breakdown.Total, view.breakdown.Total, want.Total)
		}
		if want := []int{2520, 2550}[index]; accounting.breakdown.Total.TotalTokens != want {
			t.Fatalf("accounting total=%d want=%d", accounting.breakdown.Total.TotalTokens, want)
		}
		if got := previousProjection.Snapshot(128000, runtime.CompactionConfig{}, "fixture", "main"); got != previous {
			t.Fatalf("published context mutated: got=%+v want=%+v", got, previous)
		}
	}
}

func TestUsageReadModelPublishesOnlyCompleteCutsAndCancelsWaiters(t *testing.T) {
	store := &usageProbeStore{usageTestStore: inmemory.NewStore(inmemory.Config{})}
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	appendUsageTestEvents(t, store, active.SessionRef, usageTestEvent("first", 10))
	cache := NewUsageReadModel()
	d := usageTestDriver(t, store, active, cache)
	if _, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true); err != nil {
		t.Fatal(err)
	}
	var events []*session.Event
	for n := range 250 {
		events = append(events, usageTestEvent(fmt.Sprintf("tail-%d", n), 10))
	}
	appendUsageTestEvents(t, store, active.SessionRef, events...)
	nextPage := 0
	store.hook = func(req session.EventPageRequest) error {
		nextPage++
		if nextPage == 2 {
			return context.Canceled
		}
		return nil
	}
	if _, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("partial catch-up error=%v", err)
	}
	if cache.entries[session.NormalizeSessionRef(active.SessionRef)].seq != 1 {
		t.Fatal("partial source prefix was published")
	}
	store.hook = nil
	view, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true)
	if err != nil || view.breakdown.Total.TotalTokens != 2510 {
		t.Fatalf("recovered total=%d err=%v", view.breakdown.Total.TotalTokens, err)
	}
	cache.gate <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := cache.read(ctx, d, active.SessionRef, tokenUsageCategoryMain, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked waiter=%v", err)
	}
	<-cache.gate
}

func TestUsageReadModelConcurrentReadersShareOneSourceBuild(t *testing.T) {
	store := &usageProbeStore{usageTestStore: inmemory.NewStore(inmemory.Config{})}
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	appendUsageTestEvents(t, store, active.SessionRef, usageTestEvent("first", 10))
	cache := NewUsageReadModel()
	d := usageTestDriver(t, store, active, cache)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			view, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true)
			if err != nil || view.breakdown.Total.TotalTokens != 10 {
				t.Errorf("concurrent snapshot total=%d err=%v", view.breakdown.Total.TotalTokens, err)
			}
		})
	}
	wg.Wait()
	if store.pages.Load() != 1 {
		t.Fatalf("concurrent source builds=%d", store.pages.Load())
	}
}

func TestUsageReadModelEmptyCutDoesNotReadFutureEvents(t *testing.T) {
	store := &usageProbeStore{usageTestStore: inmemory.NewStore(inmemory.Config{})}
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	cache := NewUsageReadModel()
	d := usageTestDriver(t, store, active, cache)
	store.cutHook = func(ordinal int64) {
		if ordinal == 2 {
			appendUsageTestEvents(t, store, active.SessionRef, usageTestEvent("future", 10))
		}
	}
	view, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true)
	if err != nil || view.breakdown.Total.TotalTokens != 0 || store.pages.Load() != 0 {
		t.Fatalf("empty cut read future data: total=%d pages=%d err=%v", view.breakdown.Total.TotalTokens, store.pages.Load(), err)
	}
	store.cutHook = nil
	view, err = cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true)
	if err != nil || view.breakdown.Total.TotalTokens != 10 || store.pages.Load() != 1 {
		t.Fatalf("next cut total=%d pages=%d err=%v", view.breakdown.Total.TotalTokens, store.pages.Load(), err)
	}
}

func TestUsageReadModelEventAndStateCutsStayConsistentDuringWrites(t *testing.T) {
	store := &usageProbeStore{usageTestStore: inmemory.NewStore(inmemory.Config{})}
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "fixture", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	cache := NewUsageReadModel()
	d := usageTestDriver(t, store, active, cache)
	done := make(chan error, 1)
	go func() {
		for n := 1; n <= 100; n++ {
			_, err := store.AppendEventsAndUpdateState(t.Context(), session.AppendEventsAndUpdateStateRequest{SessionRef: active.SessionRef, TransactionID: fmt.Sprintf("usage-%d", n), MutationDigest: "usage-v1", Events: []*session.Event{usageTestEvent(fmt.Sprint(n), 10)}, UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
				state[kernel.StateUsageAccounting] = map[string]any{"auto_review": map[string]any{"total_tokens": n * 10}}
				return state, nil
			}})
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for range 100 {
		view, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true)
		if err != nil {
			t.Fatal(err)
		}
		if view.breakdown.Main.TotalTokens != view.breakdown.AutoReview.TotalTokens {
			t.Fatalf("mixed event/state cuts: main=%d review=%d", view.breakdown.Main.TotalTokens, view.breakdown.AutoReview.TotalTokens)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	view, err := cache.read(t.Context(), d, active.SessionRef, tokenUsageCategoryMain, true)
	if err != nil || view.breakdown.Total.TotalTokens != 2000 {
		t.Fatalf("final total=%d err=%v", view.breakdown.Total.TotalTokens, err)
	}
}
