package runtime

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

type interleavedCompactionModel struct {
	invocationFaultModel
	mutate func()
}

func TestCompactionReceiptAndCheckpointKeepOriginalFence(t *testing.T) {
	store := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: userTextEvent("objective")}); err != nil {
		t.Fatal(err)
	}
	fence, err := store.AcquireSessionFence(t.Context(), session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "host"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.ContextWithRuntimeFence(t.Context(), fence)
	llm := &interleavedCompactionModel{mutate: func() {
		if err := store.ReleaseSessionFence(t.Context(), session.SessionFenceReleaseRequest(fence)); err != nil {
			t.Fatal(err)
		}
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := rt.Compact(ctx, CompactRequest{SessionRef: active.SessionRef, Model: llm})
	if !errors.Is(err, session.ErrFenceConflict) || result.Compacted || llm.calls != 1 {
		t.Fatalf("compact=%+v calls=%d err=%v", result, llm.calls, err)
	}
	loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range loaded.Events {
		if session.IsModelInvocationReceipt(event) || compact.IsCompactEvent(event) {
			t.Fatal("stale fence committed accounting or checkpoint")
		}
	}
}

func (m *interleavedCompactionModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.mutate()
		m.invocationFaultModel.Generate(ctx, req)(yield)
	}
}

func TestCompactionAccountingSurvivesConcurrentJournalWithoutAdmittingStaleContext(t *testing.T) {
	for _, visible := range []bool{false, true} {
		t.Run(map[bool]string{false: "journal", true: "model_context"}[visible], func(t *testing.T) {
			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: userTextEvent("original objective")}); err != nil {
				t.Fatal(err)
			}
			other := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			llm := &interleavedCompactionModel{mutate: func() {
				event := session.NewModelInvocationReceipt(model.Invocation{ID: "concurrent-review", Outcome: "completed"}, "guardian")
				if visible {
					event = userTextEvent("new unsummarized instruction")
				}
				if _, err := other.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeApproval), Event: event}); err != nil {
					t.Fatal(err)
				}
			}}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := rt.Compact(t.Context(), CompactRequest{SessionRef: active.SessionRef, Model: llm})
			if visible {
				if !errors.Is(err, session.ErrRevisionConflict) || result.Compacted {
					t.Fatalf("stale compaction = %+v, %v", result, err)
				}
			} else if err != nil || !result.Compacted {
				t.Fatalf("journal-only compaction = %+v, %v", result, err)
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			loaded, err := reopened.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			receipts := 0
			for _, event := range loaded.Events {
				if session.IsModelInvocationReceipt(event) {
					receipts++
				}
				if visible && compact.IsCompactEvent(event) {
					t.Fatal("stale checkpoint committed")
				}
			}
			wantReceipts := 2
			if visible {
				wantReceipts = 1
			}
			if receipts != wantReceipts || llm.calls != 1 {
				t.Fatalf("receipts=%d calls=%d", receipts, llm.calls)
			}
			probe := &capturingContextModel{messages: make(chan []model.Message, 1)}
			rt, err = New(Config{Sessions: reopened, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "probe", AgentSpec: agent.AgentSpec{Name: "main", Model: probe}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := drainRunnerEvents(t, run.Handle); err != nil {
				t.Fatal(err)
			}
			want := []model.Message{model.NewTextMessage(model.RoleUser, "original objective"), model.NewTextMessage(model.RoleUser, "new unsummarized instruction"), model.NewTextMessage(model.RoleUser, "probe")}
			if !visible {
				want = []model.Message{*result.Event.Message, model.NewTextMessage(model.RoleUser, "probe")}
			}
			if got := <-probe.messages; !reflect.DeepEqual(got, want) {
				t.Fatalf("rebuilt context = %#v, want %#v", got, want)
			}
		})
	}
}
