package runtime

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

type finalOutputRedactor struct{ tokenRedactor }

func (r *finalOutputRedactor) Finish(ctx context.Context) (string, error) {
	s, e := r.tokenRedactor.Finish(ctx)
	return s + "[once]", e
}

func TestHooksFinalOutputCoversEverySourceAndReplacement(t *testing.T) {
	for _, mode := range []hook.StreamMode{hook.StreamBuffered, hook.StreamIncremental} {
		for _, branch := range []string{"provider", "cache", "input-early", "turn-early", "model-result", "completion-replace"} {
			t.Run(string(mode)+"/"+branch, func(t *testing.T) {
				root := t.TempDir()
				store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
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
				reopened, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
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
