package consumer

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type sdkHookModel struct{ calls atomic.Int64 }

func (*sdkHookModel) Name() string                     { return "configured-model" }
func (*sdkHookModel) Capabilities() model.Capabilities { return model.Capabilities{ToolCalls: true} }
func (m *sdkHookModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.calls.Add(1)
		yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, "provider candidate"), Usage: model.Usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}, TurnComplete: true}), nil)
	}
}

func TestSDKHooksToolArgumentsFullResultAndModelView(t *testing.T) {
	store := file.NewStore(file.Config{RootDir: t.TempDir()})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "sdk", UserID: "tools"})
	if err != nil {
		t.Fatal(err)
	}
	var effects, prepares atomic.Int64
	probe := tool.NamedTool{Def: tool.Definition{Name: "lookup", EffectClass: tool.EffectReadOnly, InputSchema: map[string]any{"type": "object"}}, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
		effects.Add(1)
		if string(c.Input) != "{\"app\":\"B\"}" {
			t.Error("dispatch did not consume final arguments")
		}
		return tool.Result{ID: c.ID, Name: c.Name, Content: []model.Part{model.NewTextPart("original business")}}, nil
	}}
	hooks := agent.Hooks{
		ModelPrepare: []agent.Hook[agent.ModelPreparation, agent.ModelPreparation]{{Name: "cache-tool-proposal", Apply: func(_ context.Context, _ agent.HookContext, p agent.ModelPreparation) (agent.ModelPreparation, error) {
			if prepares.Add(1) == 1 {
				p.Response = &model.Response{Message: model.MessageFromAssistantParts("", "", []model.ToolCall{{ID: "lookup-1", Name: "lookup", Args: "{\"app\":\"A\"}"}})}
			}
			return p, nil
		}}},
		ToolPrepare: []agent.Hook[agent.ToolPreparation, agent.ToolPreparation]{{Name: "final-arguments", Apply: func(_ context.Context, _ agent.HookContext, p agent.ToolPreparation) (agent.ToolPreparation, error) {
			p.Input = json.RawMessage("{\"app\":\"B\"}")
			return p, nil
		}}},
		ToolResult: []agent.Hook[agent.ToolOutcome, tool.Result]{{Name: "business", Apply: func(_ context.Context, _ agent.HookContext, o agent.ToolOutcome) (tool.Result, error) {
			o.Result.Content = []model.Part{model.NewTextPart("full accepted business")}
			return o.Result, nil
		}}},
		ModelView: []agent.Hook[model.Message, model.Message]{{Name: "summary", Apply: func(_ context.Context, _ agent.HookContext, m model.Message) (model.Message, error) {
			m.Parts[0].ToolResult.Content = []model.Part{model.NewTextPart("model summary")}
			return m, nil
		}}},
	}
	core, err := runtime.New(runtime.Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	driver := &sdkHookModel{}
	run, err := core.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "lookup", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{probe}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := session.PairToolFacts(events)
	if err != nil || len(pairs) != 1 || pairs[0].Result == nil {
		t.Fatalf("facts=%v err=%v", pairs, err)
	}
	p := pairs[0]
	if effects.Load() != 1 || driver.calls.Load() != 1 || p.Call.Tool.Input["app"] != "B" || p.Call.Tool.ProposedCall == nil || p.Call.Tool.ProposedCall.Args != "{\"app\":\"A\"}" || p.Result.Tool.Result.Content[0].Text.Text != "full accepted business" || p.Result.Message.ToolResults()[0].Content[0].Text.Text != "model summary" {
		t.Fatal("full result and model projection did not share the effective fact")
	}
}

// Buffered output is transformed after semantic replacement, including early answers.
type sdkOutputRedactor struct{}

func (*sdkOutputRedactor) Transform(_ context.Context, text string) (string, error) {
	return strings.ReplaceAll(text, "sensitive", "[redacted]"), nil
}
func (*sdkOutputRedactor) Finish(context.Context) (string, error) { return "", nil }

// This source is copied into a separate Go module by sdk-boundary-check.
// It intentionally uses only supported SDK imports.
func TestSDKHooksRetrievalCacheEarlyAnswerAndBoundedFeedback(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{false: "retrieval-cache-feedback", true: "early-answer"}[early], func(t *testing.T) {
			store := file.NewStore(file.Config{RootDir: t.TempDir()})
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "sdk", UserID: "consumer"})
			if err != nil {
				t.Fatal(err)
			}
			primary, preferred := &sdkHookModel{}, &sdkHookModel{}
			var prepares, completions atomic.Int64
			hooks := agent.Hooks{
				StreamMode: agent.StreamBuffered,
				Stream: []agent.Hook[struct{}, agent.StreamTransformer]{{Name: "redact-final", Apply: func(_ context.Context, e agent.HookContext, _ struct{}) (agent.StreamTransformer, error) {
					if e.OutputSource != agent.HookOutputProvider && (e.InvocationID != "" || e.AttemptOrdinal != 0) {
						t.Error("synthetic output claimed provider identity")
					}
					return &sdkOutputRedactor{}, nil
				}}},
				Input: []agent.Hook[agent.InputView, agent.InputDecision]{{Name: "normalize", Apply: func(_ context.Context, _ agent.HookContext, v agent.InputView) (agent.InputDecision, error) {
					v.Message.Parts[0].Text.Text = strings.ToUpper(v.Message.Parts[0].Text.Text)
					d := agent.InputDecision{Message: v.Message}
					if early {
						m := model.NewTextMessage(model.RoleAssistant, "local answer")
						d.Response = &m
					}
					return d, nil
				}}},
				ModelPrepare: []agent.Hook[agent.ModelPreparation, agent.ModelPreparation]{{Name: "retrieve", Apply: func(_ context.Context, e agent.HookContext, p agent.ModelPreparation) (agent.ModelPreparation, error) {
					p.ModelName = "preferred"
					p.Request.Instructions = []model.Part{model.NewTextPart("Use the retrieved context.")}
					if prepares.Add(1) == 1 {
						p.ContextEdit = &agent.ContextEdit{Source: e.ContextSource, Append: []model.Part{model.NewTextPart("Retrieved source: version 4.")}}
						p.Response = &model.Response{Message: model.NewTextMessage(model.RoleAssistant, "cached candidate"), Usage: model.Usage{TotalTokens: 999}}
					}
					return p, nil
				}}},
				Completion: []agent.Hook[model.Message, agent.CompletionDecision]{{Name: "review", Apply: func(context.Context, agent.HookContext, model.Message) (agent.CompletionDecision, error) {
					if completions.Add(1) == 1 && !early {
						return agent.CompletionDecision{Kind: agent.CompletionContinue, Feedback: []model.Part{model.NewTextPart("Include the source version.")}}, nil
					}
					m := model.NewTextMessage(model.RoleAssistant, "Accepted sensitive answer, source version 4.")
					return agent.CompletionDecision{Kind: agent.CompletionReplace, Message: &m}, nil
				}}},
			}
			core, err := runtime.New(runtime.Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hooks})
			if err != nil {
				t.Fatal(err)
			}
			run, err := core.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "original input", AgentSpec: agent.AgentSpec{Model: primary, Models: map[string]model.LLM{"preferred": preferred}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := run.Handle.WaitCompletion(t.Context()); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			var receipts, edits, finals int
			for _, e := range loaded.Events {
				if session.IsModelInvocationReceipt(e) {
					receipts++
					if usage := session.UsageSnapshotFromSessionEvent(e); usage == nil || usage.TotalTokens != 7 {
						t.Fatal("cache invented or changed provider usage")
					}

				}
				if e.ContextEdit != nil {
					edits++
				}
				if e.Type == session.EventTypeUser && session.OriginalInputMessage(e).TextContent() != "original input" {
					t.Fatal("input transformation overwrote original evidence")
				}
				if e.Type == session.EventTypeAssistant {
					finals++
					if e.Message.TextContent() != "Accepted [redacted] answer, source version 4." {
						t.Fatal("unaccepted candidate committed")
					}
				}
			}
			want := 1
			if early {
				want = 0
			}
			if receipts != want || preferred.calls.Load() != int64(want) || primary.calls.Load() != 0 || edits != want || finals != 1 {
				t.Fatalf("receipts=%d preferred=%d primary=%d edits=%d finals=%d", receipts, preferred.calls.Load(), primary.calls.Load(), edits, finals)
			}
		})
	}
}
