package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestHooksFreezeFinalArgumentsBeforePolicyAndBatch(t *testing.T) {
	for _, synthetic := range []bool{false, true} {
		for _, deny := range []bool{false, true} {
			t.Run(map[bool]string{false: "dispatch", true: "synthetic"}[synthetic]+map[bool]string{false: "/allow", true: "/deny"}[deny], func(t *testing.T) {
				store, active := newTestSessionService(t, "hook-params")
				var effects, prepares, results atomic.Int64
				var policyInput, actualInput string
				probe := tool.NamedTool{Def: tool.Definition{Name: "operate", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"app": map[string]any{"type": "string"}}, "required": []string{"app"}}}, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
					effects.Add(1)
					actualInput = string(c.Input)
					return tool.Result{ID: c.ID, Name: c.Name, Content: []model.Part{model.NewTextPart("full")}}, nil
				}}
				driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
					if i == 0 {
						return []model.ToolCall{{ID: "call", Name: "operate", Args: `{"app":"A"}`}}
					}
					return nil
				}}
				registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "test", Decide: func(_ context.Context, c policy.ToolContext) (policy.Decision, error) {
					policyInput = string(c.Call.Input)
					action := policy.ActionAllow
					if deny {
						action = policy.ActionDeny
					}
					return policy.Decision{Action: action}, nil
				}}}
				hooks := agent.Hooks{ToolPrepare: []agent.Hook[agent.ToolPreparation, agent.ToolPreparation]{{Name: "target", Apply: func(_ context.Context, e agent.HookContext, p agent.ToolPreparation) (agent.ToolPreparation, error) {
					prepares.Add(1)
					if e.Tool == nil || e.Tool.Execution.ItemID == "" || e.ToolDefinition.Name != "operate" {
						t.Error("missing frozen identity/definition")
					}
					p.Input = json.RawMessage(`{"app":"B"}`)
					if synthetic {
						p.Kind = agent.ToolPrepareRespond
						p.Result = &tool.Result{Content: []model.Part{model.NewTextPart("cached")}}
					}
					return p, nil
				}}}, ToolResult: []agent.Hook[agent.ToolOutcome, tool.Result]{{Name: "full", Apply: func(_ context.Context, _ agent.HookContext, o agent.ToolOutcome) (tool.Result, error) {
					results.Add(1)
					return o.Result, nil
				}}}, ModelView: []agent.Hook[model.Message, model.Message]{{Name: "view", Apply: func(_ context.Context, _ agent.HookContext, m model.Message) (model.Message, error) {
					m.Parts[0].ToolResult.Content = []model.Part{model.NewTextPart("model view")}
					return m, nil
				}}}}
				rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: "test", Hooks: hooks})
				if err != nil {
					t.Fatal(err)
				}
				run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{probe}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := run.Handle.WaitCompletion(t.Context()); err != nil {
					t.Fatal(err)
				}
				if policyInput != `{"app":"B"}` || prepares.Load() != 1 || results.Load() != 1 {
					t.Fatalf("policy=%s prepares=%d results=%d", policyInput, prepares.Load(), results.Load())
				}
				want := int64(1)
				if deny || synthetic {
					want = 0
				}
				if effects.Load() != want || want == 1 && actualInput != policyInput {
					t.Fatalf("effects=%d actual=%s", effects.Load(), actualInput)
				}
				events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
				if err != nil {
					t.Fatal(err)
				}
				pairs, err := session.PairToolFacts(events)
				if err != nil || len(pairs) != 1 {
					t.Fatalf("pairs=%v error=%v", pairs, err)
				}
				p := pairs[0]
				if p.Call.Tool.Input["app"] != "B" || p.Call.Message.ToolCalls()[0].Args != policyInput || p.Result == nil {
					t.Fatalf("effective fact=%+v", p)
				}
				if !deny {
					expected := "full"
					if synthetic {
						expected = "cached"
					}
					if p.Result.Tool.Result.Content[0].Text.Text != expected || p.Result.Message.ToolResults()[0].Content[0].Text.Text != "model view" {
						t.Fatal("full result and model view conflated")
					}
				}
				assertSubagentSagaModelRoundTrip(t, store, active.SessionRef)
			})
		}
	}
}

func TestHooksPrepareInvalidSchemaNeverCommitsOrDispatches(t *testing.T) {
	store, active := newTestSessionService(t, "hook-schema")
	var effects atomic.Int64
	probe := tool.NamedTool{Def: tool.Definition{Name: "operate", InputSchema: map[string]any{"type": "object", "required": []string{"app"}}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { effects.Add(1); return tool.Result{}, nil }}
	driver := &mcpScriptModel{steps: func(int, *model.Request) []model.ToolCall {
		return []model.ToolCall{{ID: "one", Name: "operate", Args: `{"app":"A"}`}, {ID: "two", Name: "operate", Args: `{"app":"A"}`}}
	}}
	calls := 0
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: agent.Hooks{ToolPrepare: []agent.Hook[agent.ToolPreparation, agent.ToolPreparation]{{Name: "invalid-second", Apply: func(_ context.Context, _ agent.HookContext, p agent.ToolPreparation) (agent.ToolPreparation, error) {
		calls++
		if calls == 2 {
			p.Input = json.RawMessage(`{}`)
		}
		return p, nil
	}}}}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{probe}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err == nil {
		t.Fatal("invalid final schema accepted")
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Tool != nil {
			t.Fatal("partially prepared batch committed")
		}
	}
	if effects.Load() != 0 {
		t.Fatal("invalid final schema dispatched")
	}
}

func TestHooksModelCacheAndResultKeepActualAccounting(t *testing.T) {
	store, active := newTestSessionService(t, "hook-cache")
	var providers atomic.Int64
	llm := scriptedTestModel{generate: func(_ context.Context, r *model.Request) *model.Response {
		providers.Add(1)
		if r.Instructions[0].Text.Text != "hook instruction" {
			t.Error("request transformation missing")
		}
		return textResponse("provider", model.Usage{PromptTokens: 5, CompletionTokens: 7, TotalTokens: 12})
	}}
	for _, cached := range []bool{false, true} {
		hooks := agent.Hooks{ModelPrepare: []agent.Hook[agent.ModelPreparation, agent.ModelPreparation]{{Name: "prepare", Apply: func(_ context.Context, e agent.HookContext, p agent.ModelPreparation) (agent.ModelPreparation, error) {
			if e.LogicalRequestID == "" || e.AssemblyRevision != 1 {
				t.Error("missing logical/assembly identity")
			}
			p.Request.Instructions = []model.Part{model.NewTextPart("hook instruction")}
			if cached {
				p.Response = textResponse("cache", model.Usage{TotalTokens: 999})
			}
			return p, nil
		}}}, ModelResult: []agent.Hook[model.Response, model.Response]{{Name: "replace", Apply: func(_ context.Context, _ agent.HookContext, r model.Response) (model.Response, error) {
			r.Message = model.NewTextMessage(model.RoleAssistant, "accepted")
			r.Usage = model.Usage{TotalTokens: 999}
			return r, nil
		}}}}
		rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hooks})
		if err != nil {
			t.Fatal(err)
		}
		run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, AgentSpec: agent.AgentSpec{Model: llm}})
		if err != nil {
			t.Fatal(err)
		}
		if err := run.Handle.WaitCompletion(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if providers.Load() != 1 {
		t.Fatalf("provider attempts=%d", providers.Load())
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	receipts := 0
	for _, e := range events {
		if session.IsModelInvocationReceipt(e) {
			receipts++
		}
		if e.Type == session.EventTypeAssistant && !reflect.DeepEqual(e.Message, hookMessage("accepted")) {
			t.Fatal("accepted response missing")
		}
	}
	if receipts != 1 {
		t.Fatalf("actual receipts=%d", receipts)
	}
}

func hookMessage(text string) *model.Message {
	m := model.NewTextMessage(model.RoleAssistant, text)
	return &m
}
