package chat

import (
	"context"
	"encoding/json"
	"iter"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestChatAgentLoadsDeferredMCPToolsAfterToolSearch(t *testing.T) {
	t.Parallel()

	const mcpToolName = "mcp__calendar__demo__create_event"
	testModel := &toolSearchLoopModel{mcpToolName: mcpToolName}
	searchTool := toolSearchToolForTest(t, mcpToolName)
	mcpTool := mcpToolForTest(mcpToolName)
	chatAgent, err := NewWithTools("chat", testModel, []tool.Tool{searchTool, mcpTool}, "")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-tool-search"}},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "create calendar event")),
			Text:    "create calendar event",
		}},
	})

	var events []*session.Event
	for event, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		events = append(events, event)
	}

	if got, want := len(testModel.requests), 3; got != want {
		t.Fatalf("len(requests) = %d, want %d", got, want)
	}
	if got, want := requestToolNames(testModel.requests[0]), []string{tool.ToolSearchToolName}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first request tools = %v, want %v", got, want)
	}
	if got, want := requestToolNames(testModel.requests[1]), []string{tool.ToolSearchToolName, mcpToolName}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second request tools = %v, want %v", got, want)
	}
	if got := events[len(events)-1].Text; got != "done" {
		t.Fatalf("final text = %q, want done", got)
	}
}

func TestChatAgentRestoresDeferredMCPVisibilityFromToolSearchHistory(t *testing.T) {
	t.Parallel()

	const mcpToolName = "mcp__calendar__demo__create_event"
	testModel := &recordingModel{}
	searchTool := toolSearchToolForTest(t, mcpToolName)
	mcpTool := mcpToolForTest(mcpToolName)
	chatAgent, err := NewWithTools("chat", testModel, []tool.Tool{searchTool, mcpTool}, "")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}
	searchCall := model.ToolCall{
		ID:   "call-search",
		Name: tool.ToolSearchToolName,
		Args: `{"query":"calendar"}`,
	}
	searchResult := tool.Result{
		ID:      searchCall.ID,
		Name:    searchCall.Name,
		Content: []model.Part{model.NewJSONPart(toolSearchResultJSON(t, mcpToolName))},
	}
	searchMessage := toolResultMessage(searchCall, searchResult)

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-tool-search-resume"}},
		Events: []*session.Event{
			{
				Type:    session.EventTypeUser,
				Message: ptrMessage(model.NewTextMessage(model.RoleUser, "find calendar tool")),
				Text:    "find calendar tool",
			},
			{
				Type:    session.EventTypeToolCall,
				Message: ptrMessage(model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{searchCall}, "")),
				Tool:    toolEventPayload(searchCall, "pending", mustObject(searchCall.Args), nil, nil),
				Meta:    mergeEventMeta(toolMeta(searchCall.Name)),
			},
			toolResultEvent(searchCall, searchResult, &searchMessage),
			{
				Type:    session.EventTypeUser,
				Message: ptrMessage(model.NewTextMessage(model.RoleUser, "continue")),
				Text:    "continue",
			},
		},
	})

	for _, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
	}
	if got, want := requestToolNames(testModel.last), []string{tool.ToolSearchToolName, mcpToolName}; !reflect.DeepEqual(got, want) {
		t.Fatalf("request tools = %v, want %v", got, want)
	}
}

func TestChatAgentRestoresDeferredMCPVisibilityAfterSessionStoreRoundTrip(t *testing.T) {
	t.Parallel()

	const mcpToolName = "mcp__calendar__demo__create_event"
	liveModel := &toolSearchLoopModel{mcpToolName: mcpToolName}
	searchTool := toolSearchToolForTest(t, mcpToolName)
	mcpTool := mcpToolForTest(mcpToolName)
	liveAgent, err := NewWithTools("chat", liveModel, []tool.Tool{searchTool, mcpTool}, "")
	if err != nil {
		t.Fatalf("NewWithTools(live) error = %v", err)
	}

	initialUser := &session.Event{
		Type:    session.EventTypeUser,
		Message: ptrMessage(model.NewTextMessage(model.RoleUser, "create calendar event")),
		Text:    "create calendar event",
	}
	liveCtx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-tool-search-store"}},
		Events:  []*session.Event{initialUser},
	})
	var liveEvents []*session.Event
	for event, runErr := range liveAgent.Run(liveCtx) {
		if runErr != nil {
			t.Fatalf("Run(live) error = %v", runErr)
		}
		if event != nil {
			liveEvents = append(liveEvents, event)
		}
	}
	if got, want := len(liveModel.requests), 3; got != want {
		t.Fatalf("len(live requests) = %d, want %d", got, want)
	}
	livePostSearchTools := requestToolNames(liveModel.requests[1])

	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-tool-search-store" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-tool-search-store",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	appendEvent := func(event *session.Event) {
		t.Helper()
		if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
			SessionRef: activeSession.SessionRef,
			Event:      event,
		}); err != nil {
			t.Fatalf("AppendEvent(%s) error = %v", event.Type, err)
		}
	}
	appendEvent(initialUser)
	for _, event := range liveEvents {
		appendEvent(event)
	}
	appendEvent(&session.Event{
		Type:    session.EventTypeUser,
		Message: ptrMessage(model.NewTextMessage(model.RoleUser, "continue")),
		Text:    "continue",
	})

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	reloadModel := &recordingModel{}
	reloadAgent, err := NewWithTools("chat", reloadModel, []tool.Tool{searchTool, mcpTool}, "")
	if err != nil {
		t.Fatalf("NewWithTools(reload) error = %v", err)
	}
	reloadCtx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: activeSession,
		Events:  loaded.Events,
	})
	for _, runErr := range reloadAgent.Run(reloadCtx) {
		if runErr != nil {
			t.Fatalf("Run(reload) error = %v", runErr)
		}
	}
	if got := requestToolNames(reloadModel.last); !reflect.DeepEqual(got, livePostSearchTools) {
		t.Fatalf("reloaded request tools = %v, want runtime post-search tools %v", got, livePostSearchTools)
	}
}

func requestToolNames(req model.Request) []string {
	out := make([]string, 0, len(req.Tools))
	for _, spec := range req.Tools {
		if spec.Function != nil {
			out = append(out, spec.Function.Name)
		}
	}
	return out
}

func toolSearchToolForTest(t *testing.T, mcpToolName string) tool.Tool {
	t.Helper()
	return tool.NamedTool{
		Def: tool.Definition{
			Name:        tool.ToolSearchToolName,
			Description: "Search deferred tools",
			InputSchema: map[string]any{"type": "object"},
			Metadata:    map[string]any{tool.MetadataToolKind: tool.MetadataToolKindToolSearch},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ID:      call.ID,
				Name:    call.Name,
				Content: []model.Part{model.NewJSONPart(toolSearchResultJSON(t, mcpToolName))},
			}, nil
		},
	}
}

func mcpToolForTest(name string) tool.Tool {
	return tool.NamedTool{
		Def: tool.Definition{
			Name:        name,
			Description: "Create calendar events",
			InputSchema: map[string]any{"type": "object"},
			Metadata: map[string]any{
				tool.MetadataToolKind:  tool.MetadataToolKindMCP,
				tool.MetadataPluginID:  "calendar",
				tool.MetadataMCPServer: "demo",
				tool.MetadataMCPTool:   "create_event",
			},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ID:      call.ID,
				Name:    call.Name,
				Content: []model.Part{model.NewJSONPart([]byte(`{"value":"created"}`))},
			}, nil
		},
	}
}

func toolSearchResultJSON(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(tool.NewToolSearchResult([]tool.Definition{{
		Name:        name,
		Description: "Create calendar events",
		InputSchema: map[string]any{"type": "object"},
		Metadata: map[string]any{
			tool.MetadataToolKind:  tool.MetadataToolKindMCP,
			tool.MetadataPluginID:  "calendar",
			tool.MetadataMCPServer: "demo",
			tool.MetadataMCPTool:   "create_event",
		},
	}}))
	if err != nil {
		t.Fatalf("marshal tool_search result: %v", err)
	}
	return raw
}

type toolSearchLoopModel struct {
	requests    []model.Request
	mcpToolName string
}

func (m *toolSearchLoopModel) Name() string { return "tool-search-loop" }

func (m *toolSearchLoopModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if req != nil {
		cp := *req
		cp.Messages = model.CloneMessages(req.Messages)
		cp.Instructions = model.CloneParts(req.Instructions)
		cp.Tools = append([]model.ToolSpec(nil), req.Tools...)
		m.requests = append(m.requests, cp)
	}
	index := len(m.requests)
	return func(yield func(*model.StreamEvent, error) bool) {
		switch index {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "call-search",
						Name: tool.ToolSearchToolName,
						Args: `{"query":"calendar event"}`,
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "call-mcp",
						Name: m.mcpToolName,
						Args: `{}`,
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "done"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}
