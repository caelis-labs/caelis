package sessiontest

import (
	"context"
	"iter"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// ToolRunConformance exercises a native tool turn and compares the next
// Runtime's model request with the full conversation observed during execution.
// Supply an empty, dedicated Session. open is called once per turn and should
// reopen durable backends on the second call. No Task store or artifacts are used.
func ToolRunConformance(t *testing.T, ref session.SessionRef, open func(*testing.T) session.RunService) {
	t.Helper()
	effects := 0
	lookup := tool.NamedTool{
		Def: tool.Definition{Name: "lookup", InputSchema: map[string]any{"type": "object"}},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			effects++
			if call.Name != "lookup" || call.ID != "lookup-1" || string(call.Input) != `{"key":"one"}` {
				t.Errorf("unexpected tool call: %#v", call)
			}
			return tool.Result{Content: []tool.ContentBlock{tool.Text("stored value")}}, nil
		},
	}
	run := func(input string, probe *toolRunModel) session.RunService {
		t.Helper()
		store := open(t)
		core, err := runtime.New(runtime.Config{Sessions: store, AgentFactory: chat.Factory{}})
		if err != nil {
			t.Fatal(err)
		}
		turn, err := core.Run(t.Context(), agent.RunRequest{
			SessionRef: ref, Input: input,
			AgentSpec: agent.AgentSpec{Model: probe, Tools: []tool.Tool{lookup}},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer turn.Handle.Close()
		if err := turn.Handle.WaitCompletion(t.Context()); err != nil {
			t.Fatal(err)
		}
		return store
	}

	first := &toolRunModel{callTool: true}
	run("look up a value", first)
	if effects != 1 || len(first.requests) != 2 {
		t.Fatalf("first turn executed %d tools and made %d model requests", effects, len(first.requests))
	}
	live := first.requests[1]
	if len(live) != 3 || live[0].Role != model.RoleUser || live[0].TextContent() != "look up a value" || len(live[1].ToolCalls()) != 1 {
		t.Fatalf("expected the original input, tool call and reply: %#v", live)
	}
	last := live[2].ToolResults()
	if len(last) != 1 || last[0].Name != "lookup" || last[0].ToolUseID != "lookup-1" || last[0].IsError ||
		len(last[0].Content) != 1 || last[0].Content[0].Text == nil || last[0].Content[0].Text.Text != "stored value" {
		t.Fatalf("unexpected model-visible result: %#v", last)
	}
	second := &toolRunModel{}
	store := run("continue", second)
	want := append(model.CloneMessages(live), model.NewTextMessage(model.RoleAssistant, "done"), model.NewTextMessage(model.RoleUser, "continue"))
	if effects != 1 || len(second.requests) != 1 || !reflect.DeepEqual(second.requests[0], want) {
		t.Fatalf("reopened turn executed %d total effects and saw %#v; want %#v", effects, second.requests, want)
	}
	events, err := store.Events(t.Context(), session.EventsRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	var seq uint64
	for _, e := range events {
		if e == nil || e.Seq <= seq {
			t.Fatalf("event sequence did not advance after %d", seq)
		}
		seq = e.Seq
	}
	pairs, err := session.PairToolFacts(events)
	if err != nil || len(pairs) != 1 || pairs[0].Result == nil || pairs[0].Result.Tool.Result == nil ||
		!reflect.DeepEqual(pairs[0].Result.Tool.Result.Content, []tool.ContentBlock{tool.Text("stored value")}) {
		t.Fatalf("unexpected persisted tool facts: %#v, %v", pairs, err)
	}
}

type toolRunModel struct {
	callTool bool
	requests [][]model.Message
}

func (*toolRunModel) Name() string                     { return "session-conformance" }
func (*toolRunModel) Capabilities() model.Capabilities { return model.Capabilities{ToolCalls: true} }
func (m *toolRunModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.requests = append(m.requests, model.CloneMessages(req.Messages))
		message := model.NewTextMessage(model.RoleAssistant, "done")
		if m.callTool && len(m.requests) == 1 {
			message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "lookup-1", Name: "lookup", Args: `{"key":"one"}`}}, "")
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, StepComplete: true, TurnComplete: true}), nil)
	}
}
