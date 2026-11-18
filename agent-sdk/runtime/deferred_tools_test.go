package runtime

import (
	"context"
	"encoding/json"
	"iter"
	"slices"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
)

type runtimeDeferredSource struct {
	mu    sync.Mutex
	ready []tool.Tool
}

func (s *runtimeDeferredSource) Tools() []tool.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.ready)
}
func (s *runtimeDeferredSource) publish(t tool.Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = []tool.Tool{t}
}

type deferredRuntimeModel struct {
	source   *runtimeDeferredSource
	late     tool.Tool
	requests []model.Request
}

func (*deferredRuntimeModel) Name() string { return "deferred-test" }
func (*deferredRuntimeModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (m *deferredRuntimeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	snapshot := *req
	snapshot.Tools = slices.Clone(req.Tools)
	m.requests = append(m.requests, snapshot)
	index := len(m.requests)
	return func(yield func(*model.StreamEvent, error) bool) {
		msg := model.NewTextMessage(model.RoleAssistant, "done")
		finish := model.FinishReasonStop
		switch index {
		case 1:
			m.source.publish(m.late)
			msg = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "search", Name: tool.ToolSearchToolName, Args: `{"query":"docs"}`}}, "")
			finish = model.FinishReasonToolCalls
		case 2:
			msg = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "late", Name: tool.MCPCallToolName, Args: `{"tool_name":"docs__read","arguments":{}}`}}, "")
			finish = model.FinishReasonToolCalls
		}
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: &model.Response{Message: msg, TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted, FinishReason: finish}}, nil)
	}
}

func TestRuntimeLateMCPUsesEffectiveArgumentsForPolicyDispatchAndSession(t *testing.T) {
	for _, deny := range []bool{false, true} {
		name := "allowed"
		if deny {
			name = "denied"
		}
		t.Run(name, func(t *testing.T) {
			service := inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string { return "late-mcp-" + name }})
			active, err := service.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "user-1", Workspace: session.WorkspaceRef{Key: "fixture", CWD: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			source := &runtimeDeferredSource{}
			invoked, decisions := 0, 0
			late := tool.NamedTool{Def: tool.Definition{Name: "docs__read", Description: "Read docs", InputSchema: map[string]any{"type": "object"}, Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataPluginID: "fixture", tool.MetadataMCPServer: "docs", tool.MetadataMCPTool: "read", tool.MetadataMCPSourceFingerprint: "fixture-v1"}}, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
				invoked++
				if string(call.Input) != `{"edition":2}` {
					t.Errorf("dispatch input = %s", call.Input)
				}
				return tool.Result{Content: []model.Part{model.NewTextPart("read")}}, nil
			}}
			llm := &deferredRuntimeModel{source: source, late: late}
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "test", Decide: func(_ context.Context, req policy.ToolContext) (policy.Decision, error) {
				if req.Tool.Name == "docs__read" {
					decisions++
					if string(req.Call.Input) != `{"edition":2}` {
						t.Errorf("policy input = %s", req.Call.Input)
					}
					if deny {
						return policy.Decision{Action: policy.ActionDeny}, nil
					}
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			hooks := agent.Hooks{ToolPrepare: []agent.Hook[agent.ToolPreparation, agent.ToolPreparation]{{Name: "edition", Apply: func(_ context.Context, e agent.HookContext, p agent.ToolPreparation) (agent.ToolPreparation, error) {
				if e.ToolDefinition.Name == "docs__read" {
					p.Input = json.RawMessage(`{"edition":2}`)
				}
				return p, nil
			}}}}
			runtime, err := New(Config{Hooks: hooks, Sessions: service, AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: "test"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := runtime.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "read docs", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{toolsearch.NewSource(source, toolsearch.NewLexicalRanker())}, DeferredTools: source}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = drainRunnerEvents(t, run.Handle)
			if err != nil {
				t.Fatal(err)
			}
			if decisions != 1 {
				t.Fatalf("policy decisions=%d", decisions)
			}
			want := 1
			if deny {
				want = 0
			}
			if invoked != want {
				t.Fatalf("calls=%d want %d", invoked, want)
			}
			if len(llm.requests) != 3 || len(llm.requests[0].Tools) != 2 || len(llm.requests[1].Tools) != 2 {
				t.Fatalf("tool declarations=%#v", llm.requests)
			}
			events, err := service.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			seen := map[session.EventType]int{}
			for _, event := range events {
				fact := event.Tool
				if fact == nil || fact.ID != "late" {
					continue
				}
				seen[event.Type]++
				input, err := json.Marshal(fact.Input)
				if err != nil || fact.Name != "docs__read" || string(input) != `{"edition":2}` || fact.ModelCall == nil || fact.ProposedCall == nil {
					t.Fatalf("incorrect routed fact: %+v", fact)
				}
				effective, err := tool.ParseMCPCall(*fact.ModelCall)
				if err != nil || effective.Args != string(input) {
					t.Fatalf("effective model call: %+v, %v", effective, err)
				}
				proposal, err := tool.ParseMCPCall(*fact.ProposedCall)
				if err != nil || proposal.Args != "{}" {
					t.Fatalf("original proposal: %+v, %v", proposal, err)
				}
				if event.Type == session.EventTypeToolCall {
					calls := event.Message.ToolCalls()
					if len(calls) != 1 || calls[0] != *fact.ModelCall {
						t.Fatalf("assistant envelope diverged: %v", calls)
					}
				}
			}
			if seen[session.EventTypeToolCall] != 1 || seen[session.EventTypeToolResult] != 1 {
				t.Fatalf("routed facts: %v", seen)
			}
		})
	}
}
