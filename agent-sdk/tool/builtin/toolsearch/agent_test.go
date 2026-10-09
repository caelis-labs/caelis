package toolsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"
	"testing/synctest"
	"time"

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
		if !req.Stream {
			t.Fatal("private ToolSearch model request must stream")
		}
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

func TestAgentSearchAnswersParallelSchemaInspections(t *testing.T) {
	definitions := []tool.Definition{
		{Name: "drive__search", Description: "Find a document", InputSchema: map[string]any{"type": "object", "title": "drive schema"}},
		{Name: "issues__create", Description: "Create an issue", InputSchema: map[string]any{"type": "object", "title": "issues schema"}},
	}
	reads := []string{}
	llm := &selectionModel{respond: func(index int, req *model.Request) (*model.Response, error) {
		if index == 0 {
			if len(req.Instructions) != 1 || req.Instructions[0].Text == nil || !strings.Contains(req.Instructions[0].Text.Text, "at most 4 candidate schemas") {
				t.Fatalf("first step did not communicate schema budget: %#v", req.Instructions)
			}
			return &model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{
				{ID: "drive-call", Name: inspectSchemaToolName, Args: `{"name":"drive__search"}`},
				{ID: "issues-call", Name: inspectSchemaToolName, Args: `{"name":"issues__create"}`},
			}, ""), TurnComplete: true, Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonToolCalls}, nil
		}
		if index != 1 || len(req.Messages) != 4 {
			t.Fatalf("follow-up step=%d messages=%d, want two schema results", index, len(req.Messages))
		}
		if len(req.Instructions) != 1 || req.Instructions[0].Text == nil || !strings.Contains(req.Instructions[0].Text.Text, "at most 2 candidate schemas") {
			t.Fatalf("follow-up step did not communicate remaining schema budget: %#v", req.Instructions)
		}
		for i, wantID := range []string{"drive-call", "issues-call"} {
			results := req.Messages[i+2].ToolResults()
			if len(results) != 1 || results[0].ToolUseID != wantID {
				t.Fatalf("result %d = %#v, want call %q", i, results, wantID)
			}
		}
		return &model.Response{Message: model.NewTextMessage(model.RoleAssistant, `{"tools":["drive__search","issues__create"]}`), TurnComplete: true, Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonStop}, nil
	}}
	got, err := NewAgentRanker().Rank(t.Context(), "find a document and open an issue", definitions, 2, SearchModel{
		Model: llm, ReadSchema: func(_ context.Context, name string) (tool.Definition, error) {
			reads = append(reads, name)
			for _, def := range definitions {
				if def.Name == name {
					return def, nil
				}
			}
			return tool.Definition{}, fmt.Errorf("unexpected schema name %q", name)
		},
	})
	if err != nil || len(got) != 2 || got[0] != definitions[0].Name || got[1] != definitions[1].Name || len(reads) != 2 || len(llm.requests) != 2 {
		t.Fatalf("result=%v error=%v reads=%v requests=%d", got, err, reads, len(llm.requests))
	}
}

func TestAgentSearchValidatesParallelSchemaCallsBeforeReading(t *testing.T) {
	for _, tc := range []struct {
		name, wantError string
		calls           []model.ToolCall
	}{
		{"over budget", "schema read budget exceeded", []model.ToolCall{
			{ID: "1", Name: inspectSchemaToolName, Args: `{"name":"drive__search"}`},
			{ID: "2", Name: inspectSchemaToolName, Args: `{"name":"issues__create"}`},
			{ID: "3", Name: inspectSchemaToolName, Args: `{"name":"drive__search"}`},
			{ID: "4", Name: inspectSchemaToolName, Args: `{"name":"issues__create"}`},
			{ID: "5", Name: inspectSchemaToolName, Args: `{"name":"drive__search"}`},
		}},
		{"foreign candidate", "outside the current scope", []model.ToolCall{
			{ID: "1", Name: inspectSchemaToolName, Args: `{"name":"drive__search"}`},
			{ID: "2", Name: inspectSchemaToolName, Args: `{"name":"foreign__search"}`},
		}},
		{"invalid args", "invalid schema request", []model.ToolCall{
			{ID: "1", Name: inspectSchemaToolName, Args: `{"name":"drive__search"}`},
			{ID: "2", Name: inspectSchemaToolName, Args: `{"name":42}`},
		}},
		{"duplicate call ID", "duplicate tool call", []model.ToolCall{
			{ID: "1", Name: inspectSchemaToolName, Args: `{"name":"drive__search"}`},
			{ID: "1", Name: inspectSchemaToolName, Args: `{"name":"issues__create"}`},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			llm := &selectionModel{respond: func(_ int, _ *model.Request) (*model.Response, error) {
				return &model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, tc.calls, ""), TurnComplete: true, Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonToolCalls}, nil
			}}
			reads := 0
			_, err := NewAgentRanker().Rank(t.Context(), "find and create", []tool.Definition{{Name: "drive__search"}, {Name: "issues__create"}}, 2, SearchModel{
				Model: llm, ReadSchema: func(context.Context, string) (tool.Definition, error) {
					reads++
					return tool.Definition{}, nil
				},
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) || reads != 0 {
				t.Fatalf("error=%v reads=%d", err, reads)
			}
		})
	}
}

type streamingSelectionModel struct{ calls int }

func (*streamingSelectionModel) Name() string { return "streaming-selector" }
func (*streamingSelectionModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, Streaming: true}
}
func (m *streamingSelectionModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.calls++
		if !req.Stream {
			yield(nil, errors.New("selector did not stream"))
			return
		}
		// Deltas and non-final responses may contain an apparently valid choice
		// or tool call. Only the provider's final semantic result is authoritative.
		yield(&model.StreamEvent{Type: model.StreamEventPartDelta, PartDelta: &model.PartDelta{Kind: model.PartKindText, TextDelta: `{"tools":["other"]}`}}, nil)
		if m.calls == 1 {
			yield(model.StreamEventFromResponse(&model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "early", Name: "other", Args: `{}`}}, ""), StepComplete: true}), nil)
			yield(model.StreamEventFromResponse(&model.Response{Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "inspect", Name: inspectSchemaToolName, Args: `{"name":"docs__lookup"}`}}, ""), TurnComplete: true, Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonToolCalls}), nil)
			return
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, `{"tools":["docs__lookup"]}`), TurnComplete: true, Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonStop}), nil)
	}
}

func TestAgentSearchUsesOnlyCompletedStreamResponses(t *testing.T) {
	called := 0
	candidate := mcpCandidate("docs__lookup", "Find a synthetic record", "", "docs", "lookup", map[string]any{"type": "object"})
	source := &mutableSearchSource{tools: []tool.Tool{tool.NamedTool{Def: candidate.Definition(), Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		called++
		return tool.Result{}, nil
	}}}}
	llm := &streamingSelectionModel{}
	result, err := NewSource(source).Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"find a record"}`), RuntimeModel: llm})
	if err != nil {
		t.Fatal(err)
	}
	var output tool.ToolSearchResult
	if err := json.Unmarshal(result.Content[0].JSON.Value, &output); err != nil || output.Count != 1 || output.Tools[0].Name != "docs__lookup" || llm.calls != 2 || called != 0 {
		t.Fatalf("stream result=%+v decode=%v model_calls=%d business_calls=%d", output, err, llm.calls, called)
	}
}

func TestAgentSearchRejectsIncompleteFinalStream(t *testing.T) {
	candidate := mcpCandidate("docs__lookup", "Find a synthetic record", "", "docs", "lookup", nil)
	for _, tc := range []struct {
		name     string
		response model.Response
	}{
		{"no final response", model.Response{Message: model.NewTextMessage(model.RoleAssistant, `{"tools":["docs__lookup"]}`), StepComplete: true}},
		{"truncated final", model.Response{Message: model.NewTextMessage(model.RoleAssistant, `{"tools":["docs__lookup"]}`), TurnComplete: true, FinishReason: model.FinishReasonLength}},
		{"failed final", model.Response{Message: model.NewTextMessage(model.RoleAssistant, `{"tools":["docs__lookup"]}`), TurnComplete: true, Status: model.ResponseStatusFailed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			llm := &selectionModel{respond: func(_ int, _ *model.Request) (*model.Response, error) { return &tc.response, nil }}
			if _, err := NewSource(&mutableSearchSource{tools: []tool.Tool{candidate}}).Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"record"}`), RuntimeModel: llm}); err == nil {
				t.Fatal("incomplete stream was accepted")
			}
		})
	}
}

func TestAgentSearchEmptyCatalogDoesNotCallModel(t *testing.T) {
	llm := &selectionModel{}
	result, err := NewSource(&mutableSearchSource{}).Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"record"}`), RuntimeModel: llm})
	if err != nil || len(llm.requests) != 0 {
		t.Fatalf("empty catalog: result=%+v error=%v model_calls=%d", result, err, len(llm.requests))
	}
}

type delayedSelectionModel struct {
	delays    []time.Duration
	calls     int
	remaining []time.Duration
}

func (*delayedSelectionModel) Name() string { return "delayed-selector" }
func (*delayedSelectionModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, Streaming: true}
}
func (m *delayedSelectionModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.calls++
		if !req.Stream {
			yield(nil, errors.New("selector did not stream"))
			return
		}
		if deadline, ok := ctx.Deadline(); ok {
			m.remaining = append(m.remaining, time.Until(deadline))
		}
		select {
		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		case <-time.After(m.delays[min(m.calls-1, len(m.delays)-1)]):
		}
		response := &model.Response{Message: model.NewTextMessage(model.RoleAssistant, `{"tools":["docs__lookup"]}`), TurnComplete: true}
		if m.calls == 1 && len(m.delays) > 1 {
			response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "inspect", Name: inspectSchemaToolName, Args: `{"name":"docs__lookup"}`}}, "")
		}
		yield(model.StreamEventFromResponse(response), nil)
	}
}

func TestAgentSearchRespectsOnlyCallerDeadline(t *testing.T) {
	for _, tc := range []struct {
		name        string
		delays      []time.Duration
		callerLimit time.Duration
		wantElapsed time.Duration
		wantErr     error
	}{
		{"slow valid final", []time.Duration{35 * time.Second}, 0, 35 * time.Second, nil},
		{"two model steps", []time.Duration{35 * time.Second, 35 * time.Second}, 0, 70 * time.Second, nil},
		{"slow valid final beyond old budget", []time.Duration{91 * time.Second}, 0, 91 * time.Second, nil},
		{"shorter caller deadline", []time.Duration{35 * time.Second}, 12 * time.Second, 12 * time.Second, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				if tc.callerLimit > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tc.callerLimit)
					defer cancel()
				}
				llm := &delayedSelectionModel{delays: tc.delays}
				candidate := mcpCandidate("docs__lookup", "Find a synthetic record", "", "docs", "lookup", nil)
				started := time.Now()
				_, err := NewSource(&mutableSearchSource{tools: []tool.Tool{candidate}}).Call(ctx, tool.Call{Input: json.RawMessage(`{"query":"record"}`), RuntimeModel: llm})
				if time.Since(started) != tc.wantElapsed || !errors.Is(err, tc.wantErr) {
					t.Fatalf("elapsed=%s error=%v want elapsed=%s error=%v", time.Since(started), err, tc.wantElapsed, tc.wantErr)
				}
				if tc.callerLimit == 0 && len(llm.remaining) != 0 {
					t.Fatalf("unexpected private selector deadline: %v", llm.remaining)
				}
				if tc.callerLimit > 0 && (len(llm.remaining) != 1 || llm.remaining[0] != tc.callerLimit) {
					t.Fatalf("caller deadline not propagated: %v", llm.remaining)
				}
			})
		})
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

func TestAgentSearchCoverageBudgetFailsBeforeDispatch(t *testing.T) {
	source := &mutableSearchSource{}
	for i := range 200 {
		source.tools = append(source.tools, mcpCandidate(fmt.Sprintf("tool_%03d", i), strings.Repeat("wide", 5500), "", "", "", nil))
	}
	llm := &selectionModel{}
	if _, err := NewSource(source).Call(t.Context(), tool.Call{Input: json.RawMessage(`{"query":"anything"}`), RuntimeModel: llm}); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("budget err=%v", err)
	}
	if len(llm.requests) != 0 {
		t.Fatalf("dispatched %d requests", len(llm.requests))
	}
}
