package runtime

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
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
	for _, mode := range []agent.StreamMode{agent.StreamBuffered, agent.StreamIncremental} {
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
				var evidence []agent.HookContext
				var chunks strings.Builder
				h := agent.Hooks{StreamMode: mode, Stream: []agent.Hook[struct{}, agent.StreamTransformer]{{Name: "final-redact", Apply: func(_ context.Context, e agent.HookContext, _ struct{}) (agent.StreamTransformer, error) {
					mu.Lock()
					evidence = append(evidence, e)
					mu.Unlock()
					return &finalOutputRedactor{}, nil
				}}}}
				secret := model.NewTextMessage(model.RoleAssistant, "secret 你好")
				switch branch {
				case "cache":
					h.ModelPrepare = []agent.Hook[agent.ModelPreparation, agent.ModelPreparation]{{Name: "cache", Apply: func(_ context.Context, _ agent.HookContext, p agent.ModelPreparation) (agent.ModelPreparation, error) {
						p.Response = textResponse(secret.TextContent(), model.Usage{TotalTokens: 999})
						return p, nil
					}}}
				case "input-early":
					h.Input = []agent.Hook[agent.InputView, agent.InputDecision]{{Name: "early", Apply: func(_ context.Context, _ agent.HookContext, v agent.InputView) (agent.InputDecision, error) {
						return agent.InputDecision{Message: v.Message, Response: &secret}, nil
					}}}
				case "turn-early":
					h.TurnStart = []agent.Hook[struct{}, agent.TurnStartDecision]{{Name: "early", Apply: func(context.Context, agent.HookContext, struct{}) (agent.TurnStartDecision, error) {
						return agent.TurnStartDecision{Response: &secret}, nil
					}}}
				case "model-result":
					h.ModelResult = []agent.Hook[model.Response, model.Response]{{Name: "replace", Apply: func(_ context.Context, _ agent.HookContext, r model.Response) (model.Response, error) {
						r.Message = secret
						return r, nil
					}}}
				case "completion-replace":
					h.Completion = []agent.Hook[model.Message, agent.CompletionDecision]{{Name: "replace", Apply: func(context.Context, agent.HookContext, model.Message) (agent.CompletionDecision, error) {
						return agent.CompletionDecision{Kind: agent.CompletionReplace, Message: &secret}, nil
					}}}
				}
				rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: h})
				if mode == agent.StreamIncremental && (branch == "model-result" || branch == "completion-replace") {
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
				expectedSource := agent.HookOutputProvider
				switch branch {
				case "cache":
					expectedSource = agent.HookOutputCache
				case "input-early":
					expectedSource = agent.HookOutputInputEarly
				case "turn-early":
					expectedSource = agent.HookOutputTurnEarly
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
				if mode == agent.StreamIncremental && !synthetic {
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
