package runtime

import (
	"context"
	"iter"
	"slices"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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
			msg = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "late", Name: "docs__read", Args: `{}`}}, "")
			finish = model.FinishReasonToolCalls
		}
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: &model.Response{Message: msg, TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted, FinishReason: finish}}, nil)
	}
}

func TestRuntimeLateMCPUsesPolicyAndExecutionJournal(t *testing.T) {
	for _, deny := range []bool{false, true} {
		name := "allowed"
		if deny {
			name = "denied"
		}
		t.Run(name, func(t *testing.T) {
			service, active := newJournalTestSession(t, "late-mcp-"+name)
			source := &runtimeDeferredSource{}
			invoked, decisions := 0, 0
			late := tool.NamedTool{Def: tool.Definition{Name: "docs__read", Description: "Read docs", InputSchema: map[string]any{"type": "object"}, Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
				invoked++
				return tool.Result{Content: []model.Part{model.NewTextPart("read")}}, nil
			}}
			llm := &deferredRuntimeModel{source: source, late: late}
			registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "test", Decide: func(_ context.Context, req policy.ToolContext) (policy.Decision, error) {
				if req.Tool.Name == "docs__read" {
					decisions++
					if deny {
						return policy.Decision{Action: policy.ActionDeny}, nil
					}
				}
				return policy.Decision{Action: policy.ActionAllow}, nil
			}}}
			runtime, err := New(Config{Sessions: service, AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: "test"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := runtime.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "read docs", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{toolsearch.NewSource(source)}, DeferredTools: source}})
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
			if len(llm.requests) != 3 || len(llm.requests[0].Tools) != 1 || len(llm.requests[1].Tools) != 2 {
				t.Fatalf("tool declarations=%#v", llm.requests)
			}
			journaled := false
			events, err := service.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Journal != nil && event.Journal.ToolExecution != nil && event.Journal.ToolExecution.ToolName == "docs__read" {
					journaled = true
				}
			}
			if journaled == deny {
				t.Fatalf("execution journal present=%v deny=%v", journaled, deny)
			}
		})
	}
}
