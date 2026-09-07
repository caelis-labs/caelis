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
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

type invocationFaultModel struct {
	mode    string
	calls   int
	cancel  context.CancelFunc
	failure error
}

func (*invocationFaultModel) Name() string         { return "invocation-probe" }
func (*invocationFaultModel) ProviderName() string { return "probe" }
func (m *invocationFaultModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	return func(yield func(*model.StreamEvent, error) bool) {
		message := model.NewTextMessage(model.RoleAssistant, "CONTEXT CHECKPOINT\n\nUser objective remains inspect evidence. Tool outcomes not observed remain unknown. Next action: verify results.")
		if m.mode == "repair" && m.calls == 1 {
			message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{Name: "echo", Args: "{}"}}, "")
		}
		if m.cancel != nil {
			m.cancel()
		}
		response := &model.Response{Message: message, Provider: "probe", Model: m.Name(), Usage: model.Usage{PromptTokens: 10, CachedInputTokens: 3, CompletionTokens: 2, TotalTokens: 12}, TurnComplete: m.failure == nil}
		if !yield(&model.StreamEvent{Type: model.StreamEventPartDelta, Response: response}, nil) {
			return
		}
		if m.failure != nil {
			yield(nil, m.failure)
		}
	}
}

func TestRuntimeInvocationReceiptsSurviveRepairFailureCancellationAndReopen(t *testing.T) {
	for _, mode := range []string{"repair", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := store.StartSession(context.Background(), session.StartSessionRequest{AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "w", CWD: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			llm := &invocationFaultModel{mode: mode}
			if mode == "failure" {
				llm.failure = errors.New("provider stream failure")
			}
			if mode == "cancel" {
				llm.cancel = cancel
			}
			run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "inspect", AgentSpec: agent.AgentSpec{Name: "main", Model: llm}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = drainRunnerEvents(t, run.Handle)
			if mode == "repair" && err != nil {
				t.Fatal(err)
			}
			if mode == "failure" && !errors.Is(err, llm.failure) {
				t.Fatalf("provider cause lost: %v", err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			before, err := store.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			after, err := reopened.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Events, after.Events) {
				t.Fatal("reopen changed whole durable events")
			}
			count := 0
			for _, event := range after.Events {
				if session.IsModelInvocationReceipt(event) {
					count++
					if session.IsMainInvocationVisibleEvent(event) || session.IsClientReplayEvent(event) {
						t.Fatal("receipt entered context/replay")
					}
				}
			}
			if count != llm.calls || count == 0 {
				t.Fatalf("calls=%d durable receipts=%d", llm.calls, count)
			}
			total := 0
			for _, event := range session.InvocationAccountingEvents(after.Events) {
				if usage := session.UsageSnapshotFromSessionEvent(event); usage != nil {
					total += usage.TotalTokens
				}
			}
			if total != 12*llm.calls {
				t.Fatalf("tokens=%d calls=%d", total, llm.calls)
			}
		})
	}
}

func TestRuntimeCompactionPersistsEveryAttemptEvenWhenCheckpointFails(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			sessions, active := newTestSessionService(t, "compact-usage")
			for range 6 {
				if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: userTextEvent("inspect retained evidence and missing results")}); err != nil {
					t.Fatal(err)
				}
			}
			rt, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Compaction: CompactionConfig{Enabled: true, MaxRetryAttempts: 1}})
			if err != nil {
				t.Fatal(err)
			}
			llm := &invocationFaultModel{}
			if fail {
				llm.failure = errors.New("compaction provider failed")
			}
			result, err := rt.Compact(context.Background(), CompactRequest{SessionRef: active.SessionRef, Model: llm, Trigger: "manual"})
			if fail && !errors.Is(err, llm.failure) {
				t.Fatalf("cause lost: %v", err)
			}
			if !fail && (err != nil || !result.Compacted) {
				t.Fatalf("compact=%#v error=%v", result, err)
			}
			loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range loaded.Events {
				if session.IsModelInvocationReceipt(event) {
					count++
				}
			}
			if count != llm.calls || count == 0 {
				t.Fatalf("calls=%d receipts=%d", llm.calls, count)
			}
			if result.Session.Revision != loaded.Session.Revision {
				t.Fatalf("returned revision=%d stored=%d", result.Session.Revision, loaded.Session.Revision)
			}
		})
	}
}

type failingInvocationStore struct {
	session.Service
	failure error
}

func (s failingInvocationStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if session.IsModelInvocationReceipt(req.Event) {
		return nil, s.failure
	}
	return s.Service.AppendEvent(ctx, req)
}
func TestRuntimeReceiptPersistenceFailureCannotBecomeSuccessfulTurn(t *testing.T) {
	store, active := newTestSessionService(t, "receipt-failure")
	failure := errors.New("receipt write failed")
	rt, err := New(Config{Sessions: failingInvocationStore{Service: store, failure: failure}, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	llm := &invocationFaultModel{}
	run, err := rt.Run(context.Background(), agent.RunRequest{SessionRef: active.SessionRef, Input: "inspect", AgentSpec: agent.AgentSpec{Name: "main", Model: llm}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = drainRunnerEvents(t, run.Handle)
	if !errors.Is(err, failure) || llm.calls != 1 {
		t.Fatalf("error=%v calls=%d", err, llm.calls)
	}
	events, err := store.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == session.EventTypeAssistant {
			t.Fatal("receipt failure yielded durable success")
		}
	}
}
