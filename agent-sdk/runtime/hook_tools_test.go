package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPolicyAndApprovalReceiveResolvedSessionIdentity(t *testing.T) {
	store, active := newTestSessionService(t, "by-id")
	var policyRef, approvalRef, approvalSession session.SessionRef
	registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "ask", Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
		policyRef = input.Session.SessionRef
		return policy.Decision{Action: policy.ActionAskApproval, Approval: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: input.Call.ID, Name: input.Call.Name}}}, nil
	}}}
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, DefaultPolicyMode: "ask", Hooks: hook.Set{ToolPolicy: registry}})
	if err != nil {
		t.Fatal(err)
	}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "call", Name: "lookup", Args: "{}"}}
		}
		return nil
	}}
	probe := tool.NamedTool{Def: tool.Definition{Name: "lookup", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { return tool.Result{}, nil }}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: session.SessionRef{SessionID: active.SessionID}, Input: "start", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{probe}},
		ApprovalRequester: approvalRequesterFunc(func(_ context.Context, req approval.Request) (approval.Response, error) {
			approvalRef, approvalSession = req.SessionRef, req.Session.SessionRef
			return approval.Response{Outcome: "selected", OptionID: "reject_once"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if policyRef != active.SessionRef || approvalRef != active.SessionRef || approvalSession != active.SessionRef {
		t.Fatalf("stored=%+v policy=%+v approval=%+v approval session=%+v", active.SessionRef, policyRef, approvalRef, approvalSession)
	}
}

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
					return tool.Result{Content: []tool.ContentBlock{tool.Text("full")}}, nil
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
				hooks := hook.Set{ToolPrepare: []hook.ToolPrepare{{Name: "target", Apply: func(_ context.Context, e hook.ToolContext, p hook.ToolPreparation) (hook.ToolPreparation, error) {
					prepares.Add(1)
					if e.ToolExecution.ItemID == "" || e.ToolDefinition.Name != "operate" {
						t.Error("missing frozen identity/definition")
					}
					p.Input = json.RawMessage(`{"app":"B"}`)
					if synthetic {
						p.Kind = hook.ToolPrepareRespond
						p.Result = &tool.Result{Content: []tool.ContentBlock{tool.Text("cached")}}
					}
					return p, nil
				}}}, ToolResult: []hook.ToolResult{{Name: "full", Apply: func(_ context.Context, _ hook.ToolContext, o hook.ToolOutcome) (tool.Result, error) {
					results.Add(1)
					return o.Result, nil
				}}}, ModelView: []hook.ModelView{{Name: "view", Apply: func(_ context.Context, _ hook.ToolContext, m model.Message) (model.Message, error) {
					m.Parts[0].ToolResult.Content = []model.Part{model.NewTextPart("model view")}
					return m, nil
				}}}}
				hooks.ToolPolicy = registry
				rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, DefaultPolicyMode: "test", Hooks: hooks})
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
					if string(p.Result.Tool.Result.Content[0].(tool.Text)) != expected || p.Result.Message.ToolResults()[0].Content[0].Text.Text != "model view" {
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
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPrepare: []hook.ToolPrepare{{Name: "invalid-second", Apply: func(_ context.Context, _ hook.ToolContext, p hook.ToolPreparation) (hook.ToolPreparation, error) {
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
		if r.Instructions[0].Text.Text != "hook instruction" || r.BaseInstructionCount != 0 {
			t.Errorf("hook instruction gained base provenance or was lost: %+v", r)
		}
		return textResponse("provider", model.Usage{PromptTokens: 5, CompletionTokens: 7, TotalTokens: 12})
	}}
	for _, cached := range []bool{false, true} {
		hooks := hook.Set{ModelPrepare: []hook.ModelPrepare{{Name: "prepare", Apply: func(_ context.Context, e hook.PrepareContext, p hook.ModelPreparation) (hook.ModelPreparation, error) {
			if e.LogicalRequestID == "" || e.AssemblyRevision != 1 {
				t.Error("missing logical/assembly identity")
			}
			p.Request.Instructions = []model.Part{model.NewTextPart("hook instruction")}
			p.Request.BaseInstructionCount = len(p.Request.Instructions)
			if cached {
				p.Response = textResponse("cache", model.Usage{TotalTokens: 999})
			}
			return p, nil
		}}}, ModelResult: []hook.ModelResult{{Name: "replace", Apply: func(_ context.Context, _ hook.OutputContext, r model.Response) (model.Response, error) {
			r.Message = model.NewTextMessage(model.RoleAssistant, "accepted")
			r.Usage = model.Usage{TotalTokens: 999}
			return r, nil
		}}}}
		rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{SystemPrompt: "configured base"}, Hooks: hooks})
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

func TestHooksCodeModeFullResultAndModelViewShareFact(t *testing.T) {
	for _, mode := range []string{"accepted", "result-failure", "view-failure", "invalid-structured", "invalid-output-schema", "forged-completion"} {
		t.Run(mode, func(t *testing.T) {
			store, active, root := codeModeStore(t)
			var effects, fullCalls, viewCalls atomic.Int64
			raw := json.RawMessage("{\"value\":\"secret\"}")
			accepted := json.RawMessage("{\"value\":\"accepted\"}")
			probe := tool.NamedTool{Def: tool.Definition{Name: "ReadFact", InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"}}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				effects.Add(1)
				return tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}}}, nil
			}}
			hooks := hook.Set{
				ToolResult: []hook.ToolResult{{Name: "redact-business", Apply: func(_ context.Context, e hook.ToolContext, o hook.ToolOutcome) (tool.Result, error) {
					if e.Tool.Name != "ReadFact" {
						return o.Result, nil
					}
					fullCalls.Add(1)
					if e.ToolParent == nil || e.ToolExecution.ItemID == "" {
						t.Error("nested identity absent")
					}
					if mode == "result-failure" {
						return tool.Result{}, errors.New("fixture redaction failed")
					}
					o.Result.Content = []tool.ContentBlock{tool.StructuredContent{Value: accepted}}
					if mode == "invalid-output-schema" {
						wrong := json.RawMessage(`{"value":42}`)
						o.Result.Content = []tool.ContentBlock{tool.StructuredContent{Value: wrong}}
					}
					if mode == "invalid-structured" {
						o.Result.Content = append(o.Result.Content, tool.StructuredContent{Value: accepted})
					}
					if mode == "forged-completion" {
						o.Result.Content = []tool.ContentBlock{tool.StructuredContent{Value: []byte("invalid")}}
					}
					return o.Result, nil
				}}},
				ModelView: []hook.ModelView{{Name: "summary", Apply: func(_ context.Context, e hook.ToolContext, m model.Message) (model.Message, error) {
					if e.Tool.Name != "ReadFact" {
						return m, nil
					}
					viewCalls.Add(1)
					if e.ToolDefinition.OutputSchema == nil {
						t.Error("nested projection lost its actual definition")
					}
					m.Parts[0].ToolResult.Content = []model.Part{model.NewTextPart("summary")}
					if mode == "view-failure" {
						m.Parts[0].ToolResult.Name = "forged"
					}
					return m, nil
				}}},
			}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: "{\"code\":\"const b=await tools.call('ReadFact',{});if(b.data.value!=='accepted')throw Error('wrong Body');return b.data;\"}"}}
				}
				return nil
			}}
			rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{}, Hooks: hooks})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "start", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{probe, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
			if err != nil {
				t.Fatal(err)
			}
			completionErr := run.Handle.WaitCompletion(t.Context())
			if mode == "accepted" && completionErr != nil {
				t.Fatal(completionErr)
			}
			live, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(live.Events, reopened.Events) {
				t.Fatal("complete facts changed on File reopen")
			}
			pairs, err := session.PairToolFacts(reopened.Events)
			if err != nil {
				t.Fatal(err)
			}
			var child, outer *session.Event
			for _, p := range pairs {
				switch p.Call.Tool.Name {
				case "ReadFact":
					child = p.Result
				case codemode.ToolName:
					outer = p.Result
				}
			}
			if child == nil || outer == nil || effects.Load() != 1 || fullCalls.Load() != 1 || viewCalls.Load() != 1 {
				t.Fatalf("facts child=%v outer=%v effects=%d full=%d view=%d completion=%v", child, outer, effects.Load(), fullCalls.Load(), viewCalls.Load(), completionErr)
			}
			if child.Tool.Outcome != session.ToolOutcomeSucceeded {
				t.Fatalf("presentation failure changed known effect outcome: %+v", child.Tool)
			}
			if mode == "accepted" {
				structured, _ := child.Tool.Result.Structured()
				if string(structured) != string(accepted) || child.Message.ToolResults()[0].Content[0].Text.Text != "summary" || !strings.Contains(string(mustMarshalHook(t, outer.Tool.Result)), "accepted") {
					t.Fatal("JS Body, full result and model view conflated")
				}
			} else {
				if child.Tool.ProcessingFailure == nil || child.Tool.ProcessingFailure.Name == "" {
					t.Fatal("typed processing failure absent")
				}
				if mode == "view-failure" {
					reply := child.Message.ToolResults()
					if len(reply) != 1 || reply[0].Name != child.Tool.Name || reply[0].ToolUseID != child.Tool.ID ||
						len(reply[0].Content) != 1 || reply[0].Content[0].Text == nil || !strings.Contains(reply[0].Content[0].Text.Text, "model projection failed") {
						t.Fatal("failed projection did not retain the child identity and safe reply")
					}
				}
				if mode != "view-failure" && strings.Contains(string(mustMarshalHook(t, reopened.Events)), "secret") {
					t.Fatal("failed result processing leaked original secret")
				}
			}
		})
	}
}

func mustMarshalHook(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type projectionArtifactRecorder struct {
	results []tool.Result
}

func (s *projectionArtifactRecorder) Save(_ context.Context, result tool.Result) (string, error) {
	stored, err := tool.CloneResult(result, nil)
	if err == nil {
		s.results = append(s.results, stored)
	}
	return "artifact://outer", err
}

func TestToolProjectionBoundsCodeModeViewsAndSavesOnlyOuterResult(t *testing.T) {
	store, active, root := codeModeStore(t)
	text := strings.Repeat("payload", 3000)
	probe := tool.NamedTool{Def: tool.Definition{Name: "lookup", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: []tool.ContentBlock{tool.Text(text)}}, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"const b = await tools.call('lookup', {}); if (b.text.length !== 21000) throw Error('incomplete body'); return b.text;"}`}}
		}
		return nil
	}}
	artifacts := &projectionArtifactRecorder{}
	rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolProjection: tool.ResultProjection{Budget: tool.TruncationPolicy{MaxTokens: 256}, Artifacts: artifacts}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "start", AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{probe, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	loaded, err := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var child, outer *session.Event
	for _, ev := range loaded.Events {
		if ev.Tool == nil || ev.Type != session.EventTypeToolResult {
			continue
		}
		switch ev.Tool.Name {
		case "lookup":
			child = ev
		case codemode.ToolName:
			outer = ev
		}
	}
	if child == nil || outer == nil || child.Tool.Result == nil || outer.Tool.Result == nil {
		t.Fatal("missing committed tool results")
	}
	if string(child.Tool.Result.Content[0].(tool.Text)) != text || outer.Tool.Outcome != session.ToolOutcomeSucceeded {
		t.Fatalf("incomplete child result or failed script: %s", outer.Tool.Error)
	}
	childView := string(mustMarshalHook(t, child.Message))
	outerView := string(mustMarshalHook(t, outer.Message))
	if len(childView) >= len(text) || strings.Contains(childView, "artifact://outer") || !strings.Contains(outerView, "artifact://outer") || len(outerView) >= len(text) || len(artifacts.results) != 1 || !reflect.DeepEqual(artifacts.results[0], *outer.Tool.Result) {
		t.Fatalf("projected bytes child=%d outer=%d saved=%d", len(childView), len(outerView), len(artifacts.results))
	}
}

func TestHooksInvokeFailureSanitizesEveryResultSurface(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, mode := range []string{"error", "panic", "timeout", "invalid", "unknown", "accepted"} {
			t.Run(fmt.Sprintf("nested=%v/%s", nested, mode), func(t *testing.T) {
				root := t.TempDir()
				store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
				active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "hooks", UserID: "safe-result"})
				if err != nil {
					t.Fatal(err)
				}
				var effects atomic.Int64

				raw := json.RawMessage(`{"value":"RAW-SECRET"}`)
				accepted := json.RawMessage(`{"value":"ACCEPTED"}`)
				target := tool.NamedTool{Def: tool.Definition{Name: "ReadSecret"}, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
					effects.Add(1)
					r := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}, tool.Resource{URI: "https://example.invalid/RAW-SECRET", MIMEType: "image/png", Name: "fixture"}}}
					if mode == "unknown" {
						return r, errorcode.New(errorcode.UnknownOutcome, "effect unknown")
					}
					return r, nil
				}}
				// The hook also wraps js_exec's full lifecycle; only the deliberate
				// timeout case needs a shorter budget than the normal hook default.
				var timeout time.Duration
				if mode == "timeout" {
					timeout = 500 * time.Millisecond
				}
				hooks := hook.Set{ToolInvoke: []hook.ToolInvoke{{Name: "redact", Timeout: timeout, Apply: func(ctx context.Context, e hook.ToolContext, next hook.ToolNext) (tool.Result, error) {
					r, err := next(ctx)
					if e.Tool.Name != "ReadSecret" {
						return r, err
					}

					switch mode {
					case "error", "unknown":
						return r, errors.New("redaction failed")
					case "panic":
						panic("redaction panic")
					case "timeout":
						<-ctx.Done()
						return r, nil
					case "invalid":
						r.Content = append(r.Content, tool.StructuredContent{Value: raw})
						return r, nil
					}
					r.Content = []tool.ContentBlock{tool.StructuredContent{Value: accepted}, tool.Resource{URI: "https://example.invalid/accepted", MIMEType: "image/png", Name: "fixture"}}
					return r, err
				}}}}
				hooks.ModelView = []hook.ModelView{{Name: "summary", Apply: func(_ context.Context, e hook.ToolContext, m model.Message) (model.Message, error) {
					if e.Tool.Name == "ReadSecret" && len(m.ToolResults()) > 0 {
						r := m.Parts[0].ToolResult
						r.Content = append([]model.Part(nil), r.Content[:1]...)
					}
					return m, nil
				}}}
				driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
					if i == 0 {
						call := model.ToolCall{ID: "one", Name: "ReadSecret", Args: `{}`}
						if nested {
							call.Name = codemode.ToolName
							call.Args = `{"code":"try{const r=await tools.call('ReadSecret',{});if(r.text.includes('RAW-'+'SECRET'))throw Error('unsafe Body');return r.data;}catch(e){if(JSON.stringify(e.result||{}).includes('RAW-'+'SECRET'))throw Error('unsafe error.result');throw e;}"}`
						}
						return []model.ToolCall{call}
					}
					return nil
				}}
				targets := []tool.Tool{target}
				if nested {
					targets = append(targets, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}})
				}
				hooks.ToolPolicy = mcpFixturePolicy()
				rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{}, Hooks: hooks, DefaultPolicyMode: "mcp-test"})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
				run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "request", AgentSpec: agent.AgentSpec{Model: driver, Tools: targets}})
				if err != nil {
					t.Fatal(err)
				}
				completion := run.Handle.WaitCompletion(t.Context())
				var hookErr *hook.Error
				if errors.As(completion, &hookErr) {
					t.Logf("hook %s/%s cause=%v deadline=%v", hookErr.Phase, hookErr.Name, hookErr.Cause, errors.Is(completion, context.DeadlineExceeded))
				}
				if mode == "accepted" && completion != nil {
					t.Fatal(completion)
				}
				if mode == "timeout" && !errors.Is(completion, context.DeadlineExceeded) {
					t.Fatalf("expected deliberate deadline: %v", completion)
				}
				if effects.Load() != 1 {
					events, _ := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
					raw, _ := json.Marshal(events)
					t.Fatalf("effects=%d completion=%v events=%s", effects.Load(), completion, raw)
				}
				live, err := store.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
				events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil || !reflect.DeepEqual(live, events) {
					t.Fatalf("full fact replay changed: %v", err)
				}
				encoded, err := json.Marshal(events)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), "RAW-SECRET") {
					t.Fatalf("raw content/structured/media/Body/error.result/view persisted: %s", encoded)
				}
				pairs, err := session.PairToolFacts(events)
				if err != nil {
					t.Fatal(err)
				}
				found := 0
				for _, p := range pairs {
					if p.Call.Tool.Name != "ReadSecret" {
						continue
					}
					found++
					if p.Result == nil {
						t.Fatal("missing paired fact")
					}
					r := p.Result.Tool
					outcome := session.ToolOutcomeSucceeded
					if mode == "unknown" {
						outcome = session.ToolOutcomeUnknown
					}
					if r.Outcome != outcome {
						t.Fatalf("effect relabeled: %s", r.Outcome)
					}
					if mode == "accepted" {
						structured, ok := r.Result.Structured()
						if r.ProcessingFailure != nil || !ok || string(structured) != string(accepted) || len(r.Result.Content) != 2 {
							t.Fatal("valid result was discarded")
						}
					} else if r.ProcessingFailure == nil || r.Result == nil || len(r.Result.Content) != 1 {
						t.Fatalf("unsafe failed result: %+v", r)
					}
				}
				if found != 1 {
					t.Fatalf("facts=%d", found)
				}
			})
		}
	}
}
