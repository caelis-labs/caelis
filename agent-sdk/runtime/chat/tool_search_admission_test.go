package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestAdmitToolSearchResultBoundsMessageAndDurableEvent(t *testing.T) {
	t.Parallel()

	search := tool.NamedTool{Def: tool.Definition{
		Name:     tool.ToolSearchToolName,
		Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindToolSearch},
	}}
	tools := []tool.Tool{search}
	definitions := make([]tool.Definition, 0, tool.MaxDeferredToolsPerRun+6)
	for i := 0; i < tool.MaxDeferredToolsPerRun+6; i++ {
		def := tool.Definition{
			Name:        fmt.Sprintf("mcp__plugin__server__tool_%03d", i),
			Description: "Deferred external tool",
			InputSchema: map[string]any{"type": "object"},
			Metadata:    map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP},
		}
		definitions = append(definitions, def)
		tools = append(tools, tool.NamedTool{Def: def})
	}
	raw, err := json.Marshal(tool.NewToolSearchResult(definitions))
	if err != nil {
		t.Fatal(err)
	}
	call := model.ToolCall{ID: "call-search", Name: tool.ToolSearchToolName, Args: `{"query":"tool"}`}
	result := tool.Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewJSONPart(raw)}}
	visibility := tool.NewToolVisibility(tools)

	result = admitToolSearchResult(search.Definition(), call, result, &visibility)
	message := toolResultMessageFromCanonical(call, result)
	event := toolResultEvent(call, result, &message)
	payload := tool.ParseToolSearchOutput(event.Tool.Output)
	if got, want := len(payload.Tools), tool.MaxDeferredToolsPerRun; got != want {
		t.Fatalf("durable event tools = %d, want %d", got, want)
	}
	if !payload.Truncated || payload.OmittedCount != 6 {
		t.Fatalf("durable event payload = %#v, want bounded truncation", payload)
	}
	parts := message.Parts[0].ToolResult.Content
	if len(parts) != 1 || parts[0].JSON == nil {
		t.Fatalf("rewritten model message = %#v", message)
	}
	var modelOutput map[string]any
	if err := json.Unmarshal(parts[0].JSON.Value, &modelOutput); err != nil {
		t.Fatal(err)
	}
	if got := len(tool.ParseToolSearchOutput(modelOutput).Tools); got != tool.MaxDeferredToolsPerRun {
		t.Fatalf("model message tools = %d, want %d", got, tool.MaxDeferredToolsPerRun)
	}

	replay := tool.NewToolVisibility(tools)
	replay.ApplyToolResult(tool.ToolSearchToolName, event.Tool.Output)
	if got, want := len(replay.ModelSpecs()), 1+tool.MaxDeferredToolsPerRun; got != want {
		t.Fatalf("replay visible specs = %d, want %d", got, want)
	}
}

func TestChatAgentAdmitsToolSearchBeforeModelMessageAndDurableEvent(t *testing.T) {
	t.Parallel()

	definitions := make([]tool.Definition, 0, tool.MaxDeferredToolsPerRun+6)
	tools := make([]tool.Tool, 0, tool.MaxDeferredToolsPerRun+7)
	for i := 0; i < tool.MaxDeferredToolsPerRun+6; i++ {
		def := tool.Definition{
			Name:        fmt.Sprintf("mcp__plugin__server__tool_%03d", i),
			Description: "Deferred external tool",
			InputSchema: map[string]any{"type": "object"},
			Metadata:    map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP},
		}
		definitions = append(definitions, def)
		tools = append(tools, tool.NamedTool{Def: def})
	}
	search := tool.NamedTool{
		Def: tool.Definition{
			Name:     tool.ToolSearchToolName,
			Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindToolSearch},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			raw, err := json.Marshal(tool.NewToolSearchResult(definitions))
			if err != nil {
				return tool.Result{}, err
			}
			return tool.Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewJSONPart(raw)}}, nil
		},
	}
	tools = append([]tool.Tool{search}, tools...)
	llm := &boundedToolSearchModel{}
	chatAgent, err := NewWithTools("chat", llm, tools, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-bounded-search"}},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "find tool")),
			Text:    "find tool",
		}},
	})
	var searchEvent *session.Event
	for event, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatal(runErr)
		}
		if event != nil && event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Name == tool.ToolSearchToolName {
			searchEvent = event
		}
	}
	if len(llm.requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(llm.requests))
	}
	if got, want := len(llm.requests[1].Tools), 1+tool.MaxDeferredToolsPerRun; got != want {
		t.Fatalf("second request tools = %d, want %d", got, want)
	}
	var requestSearchResult tool.ToolSearchResult
	for _, message := range llm.requests[1].Messages {
		response := message.ToolResponse()
		if response == nil || response.Name != tool.ToolSearchToolName {
			continue
		}
		raw, err := json.Marshal(response.Result)
		if err != nil {
			t.Fatalf("marshal request ToolSearch result: %v", err)
		}
		if err := json.Unmarshal(raw, &requestSearchResult); err != nil {
			t.Fatalf("decode request ToolSearch result: %v", err)
		}
	}
	if len(requestSearchResult.Tools) != tool.MaxDeferredToolsPerRun {
		t.Fatalf("request ToolSearch result tools = %d, want %d", len(requestSearchResult.Tools), tool.MaxDeferredToolsPerRun)
	}
	if searchEvent == nil || searchEvent.Message == nil {
		t.Fatalf("durable ToolSearch event = %#v", searchEvent)
	}
	payload := tool.ParseToolSearchOutput(searchEvent.Tool.Output)
	if len(payload.Tools) != tool.MaxDeferredToolsPerRun || !payload.Truncated {
		t.Fatalf("durable ToolSearch payload = %#v", payload)
	}
}

func TestChatAgentToolSearchBudgetsFinalPayloadBeforeRevealAndReplaysExactly(t *testing.T) {
	t.Parallel()

	definitions := make([]tool.Definition, 0, tool.MaxDeferredToolsPerRun)
	tools := make([]tool.Tool, 0, tool.MaxDeferredToolsPerRun+1)
	for i := 0; i < tool.MaxDeferredToolsPerRun; i++ {
		def := tool.Definition{
			Name:        fmt.Sprintf("mcp__plugin__server__heavy_%03d", i),
			Description: strings.Repeat("heavy metadata ", 70),
			InputSchema: map[string]any{"type": "object"},
			Metadata: map[string]any{
				tool.MetadataToolKind:  tool.MetadataToolKindMCP,
				tool.MetadataPluginID:  strings.Repeat("plugin", 80),
				tool.MetadataMCPServer: strings.Repeat("server", 80),
				tool.MetadataMCPTool:   strings.Repeat("remote", 80),
			},
		}
		definitions = append(definitions, def)
		tools = append(tools, tool.NamedTool{Def: def})
	}
	search := tool.NamedTool{
		Def: tool.Definition{
			Name:     tool.ToolSearchToolName,
			Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindToolSearch},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			raw, err := json.Marshal(tool.NewToolSearchResult(definitions))
			if err != nil {
				return tool.Result{}, err
			}
			return tool.Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewJSONPart(raw)}}, nil
		},
	}
	tools = append([]tool.Tool{search}, tools...)
	llm := &boundedToolSearchModel{}
	chatAgent, err := NewWithTools("chat", llm, tools, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-final-search-budget"}},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "find heavy tool")),
		}},
	})
	var searchEvent *session.Event
	for event, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatal(runErr)
		}
		if event != nil && event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Name == tool.ToolSearchToolName {
			searchEvent = event
		}
	}
	if searchEvent == nil || searchEvent.Message == nil {
		t.Fatalf("durable ToolSearch event = %#v", searchEvent)
	}
	payload := tool.ParseToolSearchOutput(searchEvent.Tool.Output)
	if !payload.Truncated || len(payload.Tools) == 0 || len(payload.Tools) >= tool.MaxDeferredToolsPerRun {
		t.Fatalf("durable ToolSearch payload = %#v, want non-empty result bounded below run count", payload)
	}
	if tokens := tool.EstimateToolSearchResultPromptTokens(payload); tokens > tool.MaxToolSearchResultPromptTokens {
		t.Fatalf("durable ToolSearch result tokens = %d, want <= %d", tokens, tool.MaxToolSearchResultPromptTokens)
	}
	if truncation := nestedMap(searchEvent.Meta, "caelis", "runtime", "tool", "truncation"); len(truncation) != 0 {
		t.Fatalf("generic tool-result truncation = %#v, want admission to produce final canonical payload", truncation)
	}
	if len(llm.requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(llm.requests))
	}
	liveNames := functionToolNames(llm.requests[1].Tools)
	replay := tool.NewToolVisibility(tools)
	replay.ApplyToolResult(tool.ToolSearchToolName, searchEvent.Tool.Output)
	replayNames := functionToolNames(replay.ModelSpecs())
	if !slices.Equal(liveNames, replayNames) {
		t.Fatalf("live visible tools = %v, replay visible tools = %v", liveNames, replayNames)
	}
	if got, want := len(liveNames), 1+len(payload.Tools); got != want {
		t.Fatalf("live visible tools = %d, want %d", got, want)
	}
}

func functionToolNames(specs []model.ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.Function != nil {
			names = append(names, spec.Function.Name)
		}
	}
	return names
}

type boundedToolSearchModel struct {
	requests []model.Request
}

func (m *boundedToolSearchModel) Name() string { return "bounded-tool-search" }

func (m *boundedToolSearchModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if req != nil {
		cloned := *req
		cloned.Messages = model.CloneMessages(req.Messages)
		cloned.Tools = append([]model.ToolSpec(nil), req.Tools...)
		m.requests = append(m.requests, cloned)
	}
	index := len(m.requests)
	return func(yield func(*model.StreamEvent, error) bool) {
		message := model.NewTextMessage(model.RoleAssistant, "done")
		finishReason := model.FinishReasonStop
		if index == 1 {
			message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
				ID:   "call-search",
				Name: tool.ToolSearchToolName,
				Args: `{"query":"tool"}`,
			}}, "")
			finishReason = model.FinishReasonToolCalls
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      message,
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: finishReason,
			},
		}, nil)
	}
}
