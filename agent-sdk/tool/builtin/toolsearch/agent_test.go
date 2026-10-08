package toolsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type selectionModel struct {
	requests []model.Request
	respond  func(int, *model.Request) (*model.Response, error)
}

func (*selectionModel) Name() string                     { return "synthetic-selector" }
func (*selectionModel) Capabilities() model.Capabilities { return model.Capabilities{ToolCalls: true} }
func (m *selectionModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	copy := *model.CloneRequest(req)
	m.requests = append(m.requests, copy)
	index := len(m.requests) - 1
	return func(yield func(*model.StreamEvent, error) bool) {
		response, err := m.respond(index, req)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(model.StreamEventFromResponse(response), nil)
	}
}

type mutableSearchSource struct{ tools []tool.Tool }

func (s *mutableSearchSource) Tools() []tool.Tool { return append([]tool.Tool(nil), s.tools...) }

type checkedSearchSource struct {
	mutableSearchSource
	stale bool
}

func (s *checkedSearchSource) CheckSearchScope(context.Context) error {
	if s.stale {
		return errors.New("configuration revision changed")
	}
	return nil
}

func TestAgentSearchProgressiveSchemaAndNoBusinessExecution(t *testing.T) {
	called := 0
	source := &mutableSearchSource{}
	for i := range 21 {
		name := fmt.Sprintf("other_%02d", i)
		description := "Direct lexical appointment match"
		if i == 20 {
			name, description = "oblique", "Coordinate attendance across time zones"
		}
		source.tools = append(source.tools, tool.NamedTool{Def: tool.Definition{
			Name: name, Description: description,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"private_schema_marker": map[string]any{"type": "string"}}},
			Metadata:    map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP},
		}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { called++; return tool.Result{}, nil }})
	}
	llm := &selectionModel{respond: func(index int, req *model.Request) (*model.Response, error) {
		if len(req.Tools) != 1 || req.Tools[0].Function == nil || req.Tools[0].Function.Name != inspectSchemaToolName {
			t.Fatalf("selector tools=%#v", req.Tools)
		}
		if index == 0 {
			if len(req.Messages) != 1 || strings.Contains(req.Messages[0].TextContent(), "private_schema_marker") {
				t.Fatalf("initial request leaked schemas: %#v", req.Messages)
			}
			var input struct {
				Candidates []struct{ Name, Description string } `json:"candidates"`
			}
			if err := json.Unmarshal([]byte(req.Messages[0].TextContent()), &input); err != nil || len(input.Candidates) != 21 || input.Candidates[0].Name != "oblique" {
				t.Fatalf("complete initial catalog=%#v err=%v", input, err)
			}
			return &model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "read-1", Name: inspectSchemaToolName, Args: `{"name":"oblique"}`}}, ""), TurnComplete: true}, nil
		}
		encoded, _ := json.Marshal(req.Messages)
		if index != 1 || len(req.Messages) != 3 || !strings.Contains(string(encoded), "private_schema_marker") {
			t.Fatalf("schema was not read in second request: %#v", req.Messages)
		}
		return &model.Response{Message: model.NewTextMessage(model.RoleAssistant, `{"tools":["oblique"]}`), TurnComplete: true}, nil
	}}
	result, err := NewSource(source).Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"appointments"}`), RuntimeModel: llm, RuntimeReasoning: model.ReasoningConfig{Effort: "high"}, RuntimeServiceTier: model.ServiceTierPriority})
	if err != nil {
		t.Fatal(err)
	}
	var output tool.ToolSearchResult
	if err := json.Unmarshal(result.Content[0].JSON.Value, &output); err != nil || len(output.Tools) != 1 || output.Tools[0].Name != "oblique" {
		t.Fatalf("result=%#v err=%v", output, err)
	}
	if called != 0 || len(llm.requests) != 2 || llm.requests[0].Reasoning.Effort != "high" || llm.requests[0].ServiceTier != model.ServiceTierPriority {
		t.Fatalf("business calls=%d requests=%d settings=%#v", called, len(llm.requests), llm.requests[0])
	}
}

func TestAgentSearchFailureNoFallbackAndNoMatch(t *testing.T) {
	candidate := mcpCandidate("calendar", "Calendar appointments", "", "calendar", "list", map[string]any{"type": "object"})
	for _, tc := range []struct {
		name      string
		response  string
		failure   error
		wantError bool
	}{
		{"model error", "", errors.New("provider unavailable"), true},
		{"unknown name", `{"tools":["private"]}`, nil, true},
		{"duplicate", `{"tools":["calendar","calendar"]}`, nil, true},
		{"no match", `{"tools":[]}`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			llm := &selectionModel{respond: func(_ int, _ *model.Request) (*model.Response, error) {
				if tc.failure != nil {
					return nil, tc.failure
				}
				return &model.Response{Message: model.NewTextMessage(model.RoleAssistant, tc.response), TurnComplete: true}, nil
			}}
			result, err := NewSource(&mutableSearchSource{tools: []tool.Tool{candidate}}).Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"calendar"}`), RuntimeModel: llm})
			if (err != nil) != tc.wantError {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if !tc.wantError {
				var output tool.ToolSearchResult
				if err := json.Unmarshal(result.Content[0].JSON.Value, &output); err != nil || output.Count != 0 {
					t.Fatalf("no match=%#v err=%v", output, err)
				}
			}
		})
	}
}

func TestAgentSearchChangedSchemaFails(t *testing.T) {
	source := &mutableSearchSource{tools: []tool.Tool{mcpCandidate("calendar", "List events", "", "calendar", "list", map[string]any{"type": "object"})}}
	llm := &selectionModel{respond: func(_ int, _ *model.Request) (*model.Response, error) {
		source.tools = []tool.Tool{mcpCandidate("calendar", "List events", "", "calendar", "list", map[string]any{"type": "string"})}
		return &model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "read", Name: inspectSchemaToolName, Args: `{"name":"calendar"}`}}, ""), TurnComplete: true}, nil
	}}
	if _, err := NewSource(source).Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"events"}`), RuntimeModel: llm}); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale schema err=%v", err)
	}
}

func TestAgentSearchAllowsReplayAliasRefresh(t *testing.T) {
	definition := mcpCandidate("docs__echo", "Echo docs", "preferred", "docs", "echo", map[string]any{"type": "object"}).Definition()
	definition.Metadata[tool.MetadataReplayAliases] = []string{"mcp__preferred__docs__echo"}
	source := &mutableSearchSource{tools: []tool.Tool{tool.NamedTool{Def: definition}}}
	llm := &selectionModel{respond: func(index int, _ *model.Request) (*model.Response, error) {
		if index == 0 {
			updated := tool.CloneDefinition(definition)
			updated.Metadata[tool.MetadataReplayAliases] = []string{"mcp__preferred__docs__echo", "mcp__fallback__docs__echo"}
			source.tools = []tool.Tool{tool.NamedTool{Def: updated}}
			return &model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "read", Name: inspectSchemaToolName, Args: `{"name":"docs__echo"}`}}, ""), TurnComplete: true}, nil
		}
		return &model.Response{Message: model.NewTextMessage(model.RoleAssistant, `{"tools":["docs__echo"]}`), TurnComplete: true}, nil
	}}
	result, err := NewSource(source).Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"echo docs"}`), RuntimeModel: llm})
	if err != nil {
		t.Fatal(err)
	}
	var output tool.ToolSearchResult
	if err := json.Unmarshal(result.Content[0].JSON.Value, &output); err != nil || output.Count != 1 || output.Tools[0].Name != "docs__echo" {
		t.Fatalf("replay alias refresh result=%#v err=%v", output, err)
	}
}

func TestAgentSearchRejectsRevisionChangeBeforePublishing(t *testing.T) {
	source := &checkedSearchSource{mutableSearchSource: mutableSearchSource{tools: []tool.Tool{mcpCandidate("calendar", "Calendar", "", "calendar", "list", nil)}}}
	llm := &selectionModel{respond: func(_ int, _ *model.Request) (*model.Response, error) {
		source.stale = true
		return &model.Response{Message: model.NewTextMessage(model.RoleAssistant, `{"tools":["calendar"]}`), TurnComplete: true}, nil
	}}
	if _, err := NewSource(source).Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"calendar"}`), RuntimeModel: llm}); err == nil || !strings.Contains(err.Error(), "revision changed") {
		t.Fatalf("stale revision err=%v", err)
	}
}

func TestAgentSearchCannotInspectForeignScope(t *testing.T) {
	private := mcpCandidate("private__lookup", "Private lookup", "", "private", "lookup", map[string]any{"type": "object"})
	source := &mutableSearchSource{tools: []tool.Tool{mcpCandidate("public__lookup", "Public lookup", "", "public", "lookup", map[string]any{"type": "object"})}}
	llm := &selectionModel{respond: func(_ int, req *model.Request) (*model.Response, error) {
		if strings.Contains(req.Messages[0].TextContent(), private.Definition().Name) {
			t.Fatal("foreign scope leaked into selector catalog")
		}
		return &model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "foreign", Name: inspectSchemaToolName, Args: `{"name":"private__lookup"}`}}, ""), TurnComplete: true}, nil
	}}
	if _, err := NewSource(source).Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"private lookup"}`), RuntimeModel: llm}); err == nil || !strings.Contains(err.Error(), "outside the current scope") {
		t.Fatalf("foreign schema read err=%v", err)
	}
}

func TestAgentSearchCatalogBudgetFailsBeforeDispatch(t *testing.T) {
	source := &mutableSearchSource{}
	for i := range 200 {
		source.tools = append(source.tools, mcpCandidate(fmt.Sprintf("tool_%03d", i), strings.Repeat("wide", 250), "", "", "", nil))
	}
	llm := &selectionModel{}
	if _, err := NewSource(source).Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"anything"}`), RuntimeModel: llm}); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("budget err=%v", err)
	}
	if len(llm.requests) != 0 {
		t.Fatalf("dispatched %d requests", len(llm.requests))
	}
}
