package chat

import (
	"context"
	"encoding/json"
	"iter"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
)

type mcpRoutingModel struct {
	step int
	next func(int) []model.ToolCall
}

func (*mcpRoutingModel) Name() string { return "mcp-routing" }
func (m *mcpRoutingModel) Generate(_ context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	i := m.step
	m.step++
	return func(yield func(*model.StreamEvent, error) bool) {
		calls := m.next(i)
		message := model.NewTextMessage(model.RoleAssistant, "done")
		if len(calls) > 0 {
			message = model.MessageFromToolCalls(model.RoleAssistant, calls, "")
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, Status: model.ResponseStatusCompleted, TurnComplete: true, StepComplete: true}), nil)
	}
}
func TestMCPRoutingRejectsLegacyDiscoveryGuessesAndInvalidArguments(t *testing.T) {
	calls := 0
	remote := tool.NamedTool{Def: tool.Definition{Name: "docs__read", Description: "Read one numbered document", InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"number"}, "properties": map[string]any{"number": map[string]any{"type": "integer"}}}, Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP, tool.MetadataMCPServer: "docs", tool.MetadataMCPTool: "read", tool.MetadataMCPSourceFingerprint: "version-1"}}, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
		calls++
		return tool.Result{ID: c.ID, Name: c.Name, Content: []model.Part{model.NewTextPart("read")}}, nil
	}}
	source := &readyMCPSource{tools: []tool.Tool{remote}}
	llm := &mcpRoutingModel{next: func(i int) []model.ToolCall {
		call := model.ToolCall{ID: "same-model-id", Name: tool.MCPCallToolName, Args: `{"tool_name":"docs__read","arguments":{"number":1}}`}
		switch i {
		case 0: // Legacy Exposure and result history cannot authorize this call.
		case 1:
			call.Name = tool.ToolSearchToolName
			call.Args = `{"query":"docs"}`
		case 2:
			call.Args = `{"tool_name":"docs__read","arguments":{"number":"wrong"}}`
		case 3:
			call.Args = `{"tool_name":"guessed__read","arguments":{}}`
		case 4:
			call.Name = "docs__read"
			call.Args = `{"number":1}`
		case 5:
		case 6:
			source.tools = []tool.Tool{tool.NamedTool{Def: func() tool.Definition {
				d := remote.Definition()
				d.Metadata[tool.MetadataMCPSourceFingerprint] = "version-2"
				return d
			}(), Invoke: remote.Invoke}}
		case 7:
			call.Name = tool.ToolSearchToolName
			call.Args = `{"query":"docs"}`
		case 8:
		default:
			return nil
		}
		return []model.ToolCall{call}
	}}
	a, err := NewWithTools("chat", llm, []tool.Tool{toolsearch.NewSource(source, fixtureSearchRanker{})}, "")
	if err != nil {
		t.Fatal(err)
	}
	a.deferredTools = source
	binding := tool.BindingForDefinition(remote.Definition())
	payload := map[string]any{"tools": []any{map[string]any{"name": remote.Definition().Name, "binding": binding}}}
	legacy := &session.Event{Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "old-search", Name: "ToolSearch", Status: "completed", Output: payload}, Meta: map[string]any{"caelis.tool_exposure.v1": map[string]any{"version": 1, "active": []tool.Binding{binding}}}}
	failures := 0
	ctx := agent.NewContext(agent.ContextSpec{Context: t.Context(), Session: session.Session{SessionRef: session.SessionRef{SessionID: "legacy"}}, Events: []*session.Event{legacy}})
	for e, err := range a.Run(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		if e.Type == session.EventTypeToolResult && e.Tool != nil && e.Tool.Status == "failed" {
			failures++
		}
	}
	if calls != 2 || failures != 5 {
		t.Fatalf("calls=%d failures=%d", calls, failures)
	}
}

func TestSameBatchSearchCannotAuthorizeUndiscoveredSibling(t *testing.T) {
	source := &readyMCPSource{tools: []tool.Tool{mcpToolForTest("docs__read")}}
	llm := &mcpRoutingModel{next: func(i int) []model.ToolCall {
		if i > 0 {
			return nil
		}
		return []model.ToolCall{{ID: "search", Name: tool.ToolSearchToolName, Args: `{"query":"docs"}`}, {ID: "call", Name: tool.MCPCallToolName, Args: `{"tool_name":"docs__read","arguments":{}}`}}
	}}
	a, err := NewWithTools("chat", llm, []tool.Tool{toolsearch.NewSource(source, fixtureSearchRanker{})}, "")
	if err != nil {
		t.Fatal(err)
	}
	a.deferredTools = source
	failed := false
	for e, err := range a.Run(agent.NewContext(agent.ContextSpec{Context: t.Context()})) {
		if err != nil {
			t.Fatal(err)
		}
		if e.Type == session.EventTypeToolResult && e.Tool != nil && e.Tool.ID == "call" {
			failed = e.Tool.Status == "failed"
			if e.Tool.ModelCall == nil {
				t.Fatal("display did not retain original call")
			}
			raw, _ := json.Marshal(e.Tool.Input)
			if string(raw) != "null" && string(raw) != "{}" {
				t.Fatalf("display input=%s", raw)
			}
		}
	}
	if !failed {
		t.Fatal("same-batch search authorized its sibling")
	}
}

func TestMCPRoutingRejectsDuplicateCallIDsBeforeDispatch(t *testing.T) {
	for _, secondName := range []string{tool.MCPCallToolName, "Read"} {
		t.Run(secondName, func(t *testing.T) {
			effects := map[string]int{}
			invoke := func(_ context.Context, call tool.Call) (tool.Result, error) {
				effects[call.Name]++
				return tool.Result{}, nil
			}
			first := tool.NamedTool{Def: mcpToolForTest("docs__first").Definition(), Invoke: invoke}
			second := tool.NamedTool{Def: mcpToolForTest("docs__second").Definition(), Invoke: invoke}
			source := &readyMCPSource{tools: []tool.Tool{first}}
			secondArgs := `{"tool_name":"docs__second","arguments":{}}`
			if secondName == "Read" {
				secondArgs = `{}`
			}
			llm := &mcpRoutingModel{next: func(i int) []model.ToolCall {
				switch i {
				case 0:
					return []model.ToolCall{{ID: "search", Name: tool.ToolSearchToolName, Args: `{"query":"docs"}`}}
				case 1:
					source.tools = []tool.Tool{second}
					return []model.ToolCall{{ID: "search-second", Name: tool.ToolSearchToolName, Args: `{"query":"docs"}`}}
				case 2:
					source.tools = []tool.Tool{first, second}
					return []model.ToolCall{
						{ID: "duplicate", Name: tool.MCPCallToolName, Args: `{"tool_name":"docs__first","arguments":{}}`},
						{ID: "duplicate", Name: secondName, Args: secondArgs},
					}
				default:
					return nil
				}
			}}
			local := tool.NamedTool{Def: tool.Definition{Name: "Read"}, Invoke: invoke}
			a, err := NewWithTools("chat", llm, []tool.Tool{toolsearch.NewSource(source, fixtureSearchRanker{}), local}, "")
			if err != nil {
				t.Fatal(err)
			}
			a.deferredTools = source
			for _, err := range a.Run(agent.NewContext(agent.ContextSpec{Context: t.Context()})) {
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(effects) != 0 {
				t.Fatalf("duplicate model call IDs dispatched effects: %v", effects)
			}
		})
	}
}
