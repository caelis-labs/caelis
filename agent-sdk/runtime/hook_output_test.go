package runtime

import (
	"context"
	"errors"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"iter"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

type hookStreamModel struct {
	calls   atomic.Int64
	emit    func(int, func(*model.StreamEvent, error) bool)
	observe func(*model.Request)
}

func (*hookStreamModel) Name() string { return "hook-stream" }
func (*hookStreamModel) Capabilities() model.Capabilities {
	return model.Capabilities{Streaming: true, ToolCalls: true}
}
func (m *hookStreamModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if m.observe != nil {
			m.observe(req)
		}
		m.emit(int(m.calls.Add(1)), yield)
	}
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
	t.Parallel()
	for _, mode := range []string{"buffered-replacement", "incremental-unicode-empty-reset", "incremental-repeated-chunks-final-snapshot"} {
		incremental := mode != "buffered-replacement"
		repeated := mode == "incremental-repeated-chunks-final-snapshot"
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			store := newLogicalFileStore(t, sessionfile.Config{RootDir: root})
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
				fragments := []string{"se", "", "cret ", string([]byte{0xe4}), string([]byte{0xbd, 0xa0}), "好"}
				if repeated {
					fragments = append(fragments, fragments...)
				}
				for _, text := range fragments {
					if !yield(hookTextDelta(text), nil) {
						return
					}
				}
				yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, "secret 你好"), Usage: model.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}, TurnComplete: true}), nil)
			}
			var factories atomic.Int64
			var identities []string
			hooks := hook.Set{Stream: []hook.Stream{{Name: "redact", Apply: func(_ context.Context, e hook.OutputContext) (hook.StreamTransformer, error) {
				factories.Add(1)
				identities = append(identities, e.InvocationID)
				if e.InvocationID == "" || e.AttemptOrdinal != uint64(factories.Load()) {
					t.Error("missing per-attempt identity")
				}
				return &tokenRedactor{}, nil
			}}}}
			expected := "accepted final"
			if incremental {
				hooks.StreamMode = hook.StreamIncremental
				expected = "[redacted] 你好"
				if repeated {
					expected += expected
				}
			} else {
				hooks.Completion = []hook.Completion{{Name: "replace", Apply: func(_ context.Context, _ hook.OutputContext, candidate model.Message) (hook.CompletionDecision, error) {
					if candidate.TextContent() != "secret 你好" {
						t.Error("buffered semantic candidate transformed before replacement")
					}
					m := model.NewTextMessage(model.RoleAssistant, expected)
					return hook.CompletionDecision{Kind: hook.CompletionReplace, Message: &m}, nil
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
			reopened, err := newLogicalFileStore(t, sessionfile.Config{RootDir: root}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
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

func TestHooksIncrementalRetryPreservesCompletedToolStep(t *testing.T) {
	t.Parallel()
	store, active := newTestSessionService(t, "incremental-retry")
	var requests [][]model.Message
	driver := &hookStreamModel{
		observe: func(req *model.Request) { requests = append(requests, model.CloneMessages(req.Messages)) },
		emit: func(attempt int, yield func(*model.StreamEvent, error) bool) {
			switch attempt {
			case 1:
				message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "effect", Name: "ECHO", Args: `{}`}}, "")
				yield(model.StreamEventFromResponse(&model.Response{Message: message, TurnComplete: true, FinishReason: model.FinishReasonToolCalls}), nil)
			case 2:
				if yield(hookTextDelta("partial"), nil) {
					yield(nil, errorcode.New(errorcode.RateLimited, "rate limited"))
				}
			default:
				if yield(hookTextDelta("accepted"), nil) {
					yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, "accepted"), TurnComplete: true}), nil)
				}
			}
		},
	}
	var executions int
	target := tool.NamedTool{
		Def: tool.Definition{Name: "ECHO", InputSchema: map[string]any{"type": "object"}},
		Invoke: func(context.Context, tool.Call) (tool.Result, error) {
			executions++
			return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: []byte(`{"value":"effect result"}`)}}}, nil
		},
	}
	allow := staticPolicyRegistry{mode: policy.NamedMode{ID: "allow", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, DefaultPolicyMode: "allow", Hooks: hook.Set{StreamMode: hook.StreamIncrementalRetry, ToolPolicy: allow}})
	if err != nil {
		t.Fatal(err)
	}
	stream := true
	var partialID, resetID, acceptedID, canonicalID string
	observer := agent.SourceEventObserverFunc(func(_ context.Context, in agent.SourceEvent) error {
		event := in.Canonical
		if in.Err != nil {
			t.Errorf("recovered error reached the observer: %v", in.Err)
		}
		if event == nil {
			return nil
		}
		if event.Lifecycle != nil && event.Lifecycle.Status == session.LifecycleStatusModelAttemptReset {
			resetID = session.ModelAttemptResetMessageID(event.Meta)
			if partialID == "" || resetID != partialID || acceptedID != "" {
				t.Errorf("reset out of sequence: partial=%q reset=%q accepted=%q", partialID, resetID, acceptedID)
			}
		}
		if event.Type == session.EventTypeAssistant && event.Message != nil {
			switch {
			case session.IsUIOnly(event) && event.Text == "partial":
				partialID = event.MessageID
			case session.IsUIOnly(event) && event.Text == "accepted":
				acceptedID = event.MessageID
			case session.IsCanonicalHistoryEvent(event):
				canonicalID = event.MessageID
				if event.Text != "accepted" || !in.CanonicalContentAlreadyPublished.Has(agent.PublishedAssistantMessage) {
					t.Errorf("canonical text=%q, live ownership=%v", event.Text, in.CanonicalContentAlreadyPublished)
				}
			}
		}
		return nil
	})
	llm := model.WithRetry(driver, model.RetryConfig{RateLimitMaxRetries: 1, RateLimitBaseDelay: time.Nanosecond, RateLimitMaxDelay: time.Nanosecond})
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "start", SourceObserver: observer,
		AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{target}, Request: agent.ModelRequestOptions{Stream: &stream}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if executions != 1 || len(requests) != 3 || !reflect.DeepEqual(requests[1], requests[2]) {
		t.Fatalf("tool executions = %d, requests = %d, retry must reuse the completed tool step", executions, len(requests))
	}
	if partialID == "" || resetID != partialID || acceptedID == "" || acceptedID == partialID || canonicalID != acceptedID {
		t.Fatalf("message IDs: partial=%q reset=%q accepted=%q canonical=%q", partialID, resetID, acceptedID, canonicalID)
	}
}

func TestHooksIncrementalIrreversibleOutputPreventsRetry(t *testing.T) {
	t.Parallel()
	store, active := newTestSessionService(t, "irreversible")
	driver := &hookStreamModel{emit: func(_ int, yield func(*model.StreamEvent, error) bool) {
		if yield(hookTextDelta("accepted"), nil) {
			yield(nil, hookRetryError{})
		}
	}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{StreamMode: hook.StreamIncremental}})
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

type finalOutputRedactor struct{ tokenRedactor }

func (r *finalOutputRedactor) Finish(ctx context.Context) (string, error) {
	s, e := r.tokenRedactor.Finish(ctx)
	return s + "[once]", e
}

func TestHooksFinalOutputCoversEverySourceAndReplacement(t *testing.T) {
	t.Parallel()
	for _, mode := range []hook.StreamMode{hook.StreamBuffered, hook.StreamIncremental} {
		for _, branch := range []string{"provider", "cache", "input-early", "turn-early", "model-result", "completion-replace"} {
			t.Run(string(mode)+"/"+branch, func(t *testing.T) {
				root := t.TempDir()
				store := newLogicalFileStore(t, sessionfile.Config{RootDir: root})
				active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "hooks", UserID: "sources"})
				if err != nil {
					t.Fatal(err)
				}
				synthetic := branch == "cache" || branch == "input-early" || branch == "turn-early"
				var providers atomic.Int64
				var mu sync.Mutex
				var evidence []hook.OutputContext
				var chunks strings.Builder
				h := hook.Set{StreamMode: mode, Stream: []hook.Stream{{Name: "final-redact", Apply: func(_ context.Context, e hook.OutputContext) (hook.StreamTransformer, error) {
					mu.Lock()
					evidence = append(evidence, e)
					mu.Unlock()
					return &finalOutputRedactor{}, nil
				}}}}
				secret := model.NewTextMessage(model.RoleAssistant, "secret 你好")
				switch branch {
				case "cache":
					h.ModelPrepare = []hook.ModelPrepare{{Name: "cache", Apply: func(_ context.Context, _ hook.PrepareContext, p hook.ModelPreparation) (hook.ModelPreparation, error) {
						p.Response = textResponse(secret.TextContent(), model.Usage{TotalTokens: 999})
						return p, nil
					}}}
				case "input-early":
					h.Input = []hook.Input{{Name: "early", Apply: func(_ context.Context, _ hook.InputContext, v hook.InputView) (hook.InputDecision, error) {
						return hook.InputDecision{Message: v.Message, Response: &secret}, nil
					}}}
				case "turn-early":
					h.TurnStart = []hook.TurnStart{{Name: "early", Apply: func(context.Context, hook.TurnContext) (hook.TurnStartDecision, error) {
						return hook.TurnStartDecision{Response: &secret}, nil
					}}}
				case "model-result":
					h.ModelResult = []hook.ModelResult{{Name: "replace", Apply: func(_ context.Context, _ hook.OutputContext, r model.Response) (model.Response, error) {
						r.Message = secret
						return r, nil
					}}}
				case "completion-replace":
					h.Completion = []hook.Completion{{Name: "replace", Apply: func(context.Context, hook.OutputContext, model.Message) (hook.CompletionDecision, error) {
						return hook.CompletionDecision{Kind: hook.CompletionReplace, Message: &secret}, nil
					}}}
				}
				rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: h})
				if mode == hook.StreamIncremental && (branch == "model-result" || branch == "completion-replace") {
					if err == nil {
						t.Fatal("unsupported incremental mutator assembled")
					}
					events, e := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
					if e != nil || len(events) != 0 {
						t.Fatal("invalid assembly committed input")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				driver := &hookStreamModel{emit: func(_ int, yield func(*model.StreamEvent, error) bool) {
					providers.Add(1)
					text := "public"
					if branch == "provider" {
						text = secret.TextContent()
					}
					for _, part := range []string{text[:2], "", text[2:]} {
						if !yield(hookTextDelta(part), nil) {
							return
						}
					}
					yield(model.StreamEventFromResponse(textResponse(text, model.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5})), nil)
				}}
				streaming := true
				observer := agent.SourceEventObserverFunc(func(_ context.Context, e agent.SourceEvent) error {
					if e.Canonical != nil && e.Canonical.Visibility == session.VisibilityUIOnly && e.Canonical.Message != nil {
						mu.Lock()
						chunks.WriteString(e.Canonical.Message.TextContent())
						mu.Unlock()
					}
					return nil
				})
				run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "request", SourceObserver: observer, AgentSpec: agent.AgentSpec{Model: driver, Request: agent.ModelRequestOptions{Stream: &streaming}}})
				if err != nil {
					t.Fatal(err)
				}
				if err = run.Handle.WaitCompletion(t.Context()); err != nil {
					t.Fatal(err)
				}
				live, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				reopened, err := newLogicalFileStore(t, sessionfile.Config{RootDir: root}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil || !reflect.DeepEqual(live.Events, reopened.Events) {
					t.Fatalf("full replay changed: %v", err)
				}
				want := "[redacted] 你好[once]"
				finals, receipts := 0, 0
				for _, e := range reopened.Events {
					if session.IsModelInvocationReceipt(e) {
						receipts++
					}
					if e.Type == session.EventTypeAssistant {
						finals++
						if e.Message.TextContent() != want {
							t.Fatalf("final=%q", e.Message.TextContent())
						}
						if synthetic {
							sdk := e.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)
							if sdk["usage"] != nil || sdk["usage_receipt_id"] != nil || sdk["provider"] != "" {
								t.Fatalf("synthetic output claimed provider accounting: %+v", sdk)
							}
						}
					}
				}
				if finals != 1 || len(evidence) != 1 {
					t.Fatalf("finals=%d factories=%d", finals, len(evidence))
				}
				expectedSource := hook.OutputProvider
				switch branch {
				case "cache":
					expectedSource = hook.OutputCache
				case "input-early":
					expectedSource = hook.OutputInputEarly
				case "turn-early":
					expectedSource = hook.OutputTurnEarly
				}
				e := evidence[0]
				if e.OutputSource != expectedSource {
					t.Fatalf("source=%q", e.OutputSource)
				}
				if synthetic {
					if providers.Load() != 0 || receipts != 0 || e.InvocationID != "" || e.AttemptOrdinal != 0 {
						t.Fatalf("synthetic provider evidence: %+v providers=%d receipts=%d", e, providers.Load(), receipts)
					}
				} else if providers.Load() != 1 || receipts != 1 || e.InvocationID == "" || e.AttemptOrdinal != 1 {
					t.Fatalf("actual evidence: %+v providers=%d receipts=%d", e, providers.Load(), receipts)
				}
				if mode == hook.StreamIncremental && !synthetic {
					if chunks.String() != want {
						t.Fatalf("live=%q final=%q", chunks.String(), want)
					}
				} else if chunks.Len() != 0 {
					t.Fatalf("buffered or synthetic raw chunks=%q", chunks.String())
				}
			})
		}
	}
}

type feedbackAckStore struct {
	*sessionfile.Store
	mode   string
	writes atomic.Int64
}

func (s *feedbackAckStore) AppendEvent(ctx context.Context, r session.AppendEventRequest) (*session.Event, error) {
	if r.Event != nil && strings.HasPrefix(r.Event.ID, "hook-feedback:") {
		s.writes.Add(1)
		if s.mode == "failed" {
			return nil, errors.New("feedback not committed")
		}
		if s.mode == "conflict" {
			r.Event = session.CloneEvent(r.Event)
			r.Event.Message.Parts[0].Text.Text = "different payload"
		}
		if _, err := s.Store.AppendEvent(ctx, r); err != nil {
			return nil, err
		}
		return nil, errors.New("feedback ACK lost")
	}
	return s.Store.AppendEvent(ctx, r)
}
func TestHooksFeedbackBudgetConsumesOnlyConfirmedCommits(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"committed", "failed", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			store := &feedbackAckStore{Store: newLogicalFileStore(t, sessionfile.Config{RootDir: t.TempDir()}), mode: mode}
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "hooks", UserID: "feedback"})
			if err != nil {
				t.Fatal(err)
			}
			var providers atomic.Int64
			llm := scriptedTestModel{generate: func(context.Context, *model.Request) *model.Response {
				providers.Add(1)
				return textResponse("candidate", model.Usage{})
			}}
			h := hook.Set{CompletionLimit: 2, Completion: []hook.Completion{{Name: "continue", Apply: func(context.Context, hook.OutputContext, model.Message) (hook.CompletionDecision, error) {
				return hook.CompletionDecision{Kind: hook.CompletionContinue, Feedback: []model.Part{model.NewTextPart("feedback")}}, nil
			}}}}
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: h})
			if err != nil {
				t.Fatal(err)
			}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "start", AgentSpec: agent.AgentSpec{Model: llm}})
			if err != nil {
				t.Fatal(err)
			}
			completion := run.Handle.WaitCompletion(t.Context())
			if completion == nil {
				t.Fatal("failure or bounded rejection lost")
			}
			events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]bool{}
			for _, e := range events {
				if strings.HasPrefix(e.ID, "hook-feedback:") {
					if ids[e.ID] {
						t.Fatal("duplicate feedback ID")
					}
					ids[e.ID] = true
				}
			}
			if mode == "committed" {
				if len(ids) != 2 || providers.Load() != 3 || store.writes.Load() != 2 || !strings.Contains(completion.Error(), "completion_limit") {
					t.Fatalf("feedback budget bypass/replay: ids=%d provider=%d writes=%d completion=%v", len(ids), providers.Load(), store.writes.Load(), completion)
				}
			} else {
				want := 0
				if mode == "conflict" {
					want = 1
				}
				if len(ids) != want || providers.Load() != 1 || store.writes.Load() != 1 {
					t.Fatalf("unconfirmed commit continued/replayed: ids=%d provider=%d writes=%d", len(ids), providers.Load(), store.writes.Load())
				}
			}
		})
	}
}
