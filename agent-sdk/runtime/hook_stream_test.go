package runtime

import (
	"context"
	"iter"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

type hookStreamModel struct {
	calls atomic.Int64
	emit  func(int, func(*model.StreamEvent, error) bool)
}

func (*hookStreamModel) Name() string                     { return "hook-stream" }
func (*hookStreamModel) Capabilities() model.Capabilities { return model.Capabilities{Streaming: true} }
func (m *hookStreamModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) { m.emit(int(m.calls.Add(1)), yield) }
}

type hookRetryError struct{}

func (hookRetryError) Error() string   { return "fixture transport failure" }
func (hookRetryError) Retryable() bool { return true }

// Emits only text which cannot be a prefix of the protected token.
type tokenRedactor struct{ pending string }

func (r *tokenRedactor) Transform(_ context.Context, fragment string) (string, error) {
	r.pending += fragment
	var out strings.Builder
	for r.pending != "" {
		if strings.HasPrefix(r.pending, "secret") {
			out.WriteString("[redacted]")
			r.pending = r.pending[6:]
			continue
		}
		if strings.HasPrefix("secret", r.pending) {
			break
		}
		_, n := utf8.DecodeRuneInString(r.pending)
		out.WriteString(r.pending[:n])
		r.pending = r.pending[n:]
	}
	return out.String(), nil
}
func (r *tokenRedactor) Finish(context.Context) (string, error) {
	out := r.pending
	r.pending = ""
	return out, nil
}
func hookTextDelta(text string) *model.StreamEvent {
	return &model.StreamEvent{Type: model.StreamEventPartDelta, PartDelta: &model.PartDelta{Kind: model.PartKindText, TextDelta: text}}
}

func TestHooksStreamingAcceptedTextMatchesFileReopen(t *testing.T) {
	for _, incremental := range []bool{false, true} {
		t.Run(map[bool]string{false: "buffered-replacement", true: "incremental-unicode-empty-reset"}[incremental], func(t *testing.T) {
			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "hooks", UserID: "stream"})
			if err != nil {
				t.Fatal(err)
			}
			driver := &hookStreamModel{}
			driver.emit = func(attempt int, yield func(*model.StreamEvent, error) bool) {
				if incremental && attempt == 1 {
					if yield(hookTextDelta("sec"), nil) {
						yield(nil, hookRetryError{})
					}
					return
				}
				for _, text := range []string{"se", "", "cret ", string([]byte{0xe4}), string([]byte{0xbd, 0xa0}), "好"} {
					if !yield(hookTextDelta(text), nil) {
						return
					}
				}
				yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, "secret 你好"), Usage: model.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}, TurnComplete: true}), nil)
			}
			var factories atomic.Int64
			var identities []string
			hooks := agent.Hooks{Stream: []agent.Hook[struct{}, agent.StreamTransformer]{{Name: "redact", Apply: func(_ context.Context, e agent.HookContext, _ struct{}) (agent.StreamTransformer, error) {
				factories.Add(1)
				identities = append(identities, e.InvocationID)
				if e.InvocationID == "" || e.AttemptOrdinal != uint64(factories.Load()) {
					t.Error("missing per-attempt identity")
				}
				return &tokenRedactor{}, nil
			}}}}
			expected := "accepted final"
			if incremental {
				hooks.StreamMode = agent.StreamIncremental
				expected = "[redacted] 你好"
			} else {
				hooks.Completion = []agent.Hook[model.Message, agent.CompletionDecision]{{Name: "replace", Apply: func(_ context.Context, _ agent.HookContext, candidate model.Message) (agent.CompletionDecision, error) {
					if candidate.TextContent() != "[redacted] 你好" {
						t.Error("buffered transformer result missing")
					}
					m := model.NewTextMessage(model.RoleAssistant, expected)
					return agent.CompletionDecision{Kind: agent.CompletionReplace, Message: &m}, nil
				}}}
			}
			llm := model.WithRetry(driver, model.RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hooks})
			if err != nil {
				t.Fatal(err)
			}
			streaming := true
			var chunks strings.Builder
			var liveMu sync.Mutex
			observer := agent.SourceEventObserverFunc(func(_ context.Context, source agent.SourceEvent) error {
				e := source.Canonical
				if e != nil && e.Visibility == session.VisibilityUIOnly && e.Message != nil {
					liveMu.Lock()
					chunks.WriteString(e.Message.TextContent())
					liveMu.Unlock()
				}
				return nil
			})
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "start", SourceObserver: observer, AgentSpec: agent.AgentSpec{Model: llm, Request: agent.ModelRequestOptions{Stream: &streaming}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			live, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(live.Events, reopened.Events) {
				t.Fatal("complete canonical events changed on reopen")
			}
			finals := 0
			for _, e := range reopened.Events {
				if e.Type == session.EventTypeAssistant {
					finals++
					if e.Message.TextContent() != expected {
						t.Fatalf("final=%q", e.Message.TextContent())
					}
				}
			}
			if finals != 1 {
				t.Fatalf("finals=%d", finals)
			}
			if incremental && chunks.String() != expected || !incremental && chunks.Len() != 0 {
				t.Fatalf("published chunks=%q", chunks.String())
			}
			want := int64(1)
			if incremental {
				want = 2
			}
			if factories.Load() != want || driver.calls.Load() != want {
				t.Fatalf("factories=%d providers=%d", factories.Load(), driver.calls.Load())
			}
			if want == 2 && identities[0] == identities[1] {
				t.Fatal("retry reused actual invocation ID")
			}
			receipts, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, e := range receipts {
				if session.IsModelInvocationReceipt(e) {
					count++
				}
			}
			if int64(count) != want {
				t.Fatalf("actual receipts=%d want=%d", count, want)
			}
		})
	}
}

func TestHooksIncrementalIrreversibleOutputPreventsRetry(t *testing.T) {
	store, active := newTestSessionService(t, "irreversible")
	driver := &hookStreamModel{emit: func(_ int, yield func(*model.StreamEvent, error) bool) {
		if yield(hookTextDelta("accepted"), nil) {
			yield(nil, hookRetryError{})
		}
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: agent.Hooks{StreamMode: agent.StreamIncremental}})
	if err != nil {
		t.Fatal(err)
	}
	streaming := true
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, AgentSpec: agent.AgentSpec{Model: model.WithRetry(driver, model.RetryConfig{MaxRetries: 2, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond}), Request: agent.ModelRequestOptions{Stream: &streaming}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err == nil {
		t.Fatal("stream transport failure disappeared")
	}
	if driver.calls.Load() != 1 {
		t.Fatalf("irreversible stream retried: %d", driver.calls.Load())
	}
}
