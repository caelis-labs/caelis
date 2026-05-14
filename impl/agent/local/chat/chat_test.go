package chat

import (
	"context"
	"encoding/json"
	"iter"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestChatAgentUsesSessionMessages(t *testing.T) {
	t.Parallel()

	testModel := &recordingModel{}
	chatAgent, err := New("chat", testModel, "Be terse.")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{
			SessionRef: session.SessionRef{
				AppName:      "caelis",
				UserID:       "user-1",
				SessionID:    "sess-1",
				WorkspaceKey: "ws-1",
			},
		},
		Events: []*session.Event{
			{
				Type:    session.EventTypeUser,
				Message: ptrMessage(model.NewTextMessage(model.RoleUser, "hello")),
				Text:    "hello",
			},
		},
	})

	var final *session.Event
	for event, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		final = event
	}

	if got := len(testModel.last.Messages); got != 1 {
		t.Fatalf("len(Messages) = %d, want 1", got)
	}
	if got := testModel.last.Messages[0].TextContent(); got != "hello" {
		t.Fatalf("user text = %q, want %q", got, "hello")
	}
	if got := len(testModel.last.Instructions); got != 1 {
		t.Fatalf("len(Instructions) = %d, want 1", got)
	}
	if final == nil || final.Text != "world" {
		t.Fatalf("final event = %+v, want assistant world", final)
	}
}

func TestChatAgentIncludesSideDialogueAndExcludesDelegatedSubagents(t *testing.T) {
	t.Parallel()

	testModel := &recordingModel{}
	chatAgent, err := New("chat", testModel, "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{
			SessionRef: session.SessionRef{SessionID: "sess-1"},
			Participants: []session.ParticipantBinding{{
				ID:        "side-acp",
				Kind:      session.ParticipantKindACP,
				Role:      session.ParticipantRoleSidecar,
				Label:     "@codex",
				AgentName: "codex",
			}, {
				ID:        "child-1",
				Kind:      session.ParticipantKindSubagent,
				Role:      session.ParticipantRoleDelegated,
				Label:     "@ella",
				AgentName: "self",
			}},
		},
		Events: []*session.Event{
			{
				Type:    session.EventTypeUser,
				Message: ptrMessage(model.NewTextMessage(model.RoleUser, "main user")),
				Text:    "main user",
			},
			{
				Type:    session.EventTypeAssistant,
				Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "side acp")),
				Text:    "side acp",
				Actor:   session.ActorRef{Kind: session.ActorKindParticipant, ID: "side-acp", Name: "@codex"},
				Scope: &session.EventScope{
					Source: "acp_participant",
					Participant: session.ParticipantRef{
						ID:   "side-acp",
						Kind: session.ParticipantKindACP,
						Role: session.ParticipantRoleSidecar,
					},
				},
			},
			{
				Type:    session.EventTypeAssistant,
				Message: ptrMessage(model.NewTextMessage(model.RoleAssistant, "spawn child")),
				Text:    "spawn child",
				Scope: &session.EventScope{
					Source: "agent_spawn",
					Participant: session.ParticipantRef{
						ID:   "child-1",
						Kind: session.ParticipantKindSubagent,
						Role: session.ParticipantRoleDelegated,
					},
				},
			},
		},
	})

	for _, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
	}

	if got := len(testModel.last.Messages); got != 2 {
		t.Fatalf("len(Messages) = %d, want main plus side final", got)
	}
	if got := testModel.last.Messages[0].TextContent(); got != "main user" {
		t.Fatalf("message text = %q, want main user", got)
	}
	if got := testModel.last.Messages[1].TextContent(); got != "[agent_source agent=codex handle=@codex]\nside acp" {
		t.Fatalf("side message text = %q", got)
	}
}

func TestFactoryMetadataSystemPromptOverridesFactoryDefault(t *testing.T) {
	t.Parallel()

	testModel := &recordingModel{}
	chatAgent, err := (Factory{SystemPrompt: "factory-default"}).NewAgent(context.Background(), agent.AgentSpec{
		Name:  "chat",
		Model: testModel,
		Metadata: map[string]any{
			"system_prompt": "assembly-override",
		},
	})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{
			SessionRef: session.SessionRef{SessionID: "sess-override"},
		},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "hello")),
			Text:    "hello",
		}},
	})

	for _, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
	}
	if got, want := len(testModel.last.Instructions), 1; got != want {
		t.Fatalf("len(Instructions) = %d, want %d", got, want)
	}
	if testModel.last.Instructions[0].Kind != model.PartKindText || testModel.last.Instructions[0].Text == nil {
		t.Fatalf("instruction[0] = %+v, want text part", testModel.last.Instructions[0])
	}
	if got := testModel.last.Instructions[0].Text.Text; got != "assembly-override" {
		t.Fatalf("instruction text = %q, want %q", got, "assembly-override")
	}
}

func TestFactoryPassesOutputSpecToModelRequest(t *testing.T) {
	t.Parallel()

	testModel := &recordingModel{}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"outcome": map[string]any{"type": "string"},
		},
		"required": []any{"outcome"},
	}
	chatAgent, err := (Factory{SystemPrompt: "Return JSON."}).NewAgent(context.Background(), agent.AgentSpec{
		Name:  "chat",
		Model: testModel,
		Request: agent.ModelRequestOptions{
			Output: &model.OutputSpec{
				Mode:            model.OutputModeSchema,
				JSONSchema:      schema,
				MaxOutputTokens: 128,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	schema["properties"].(map[string]any)["outcome"] = map[string]any{"type": "integer"}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{
			SessionRef: session.SessionRef{SessionID: "sess-output"},
		},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "hello")),
			Text:    "hello",
		}},
	})

	for _, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
	}

	if testModel.last.Output == nil {
		t.Fatal("model request Output = nil, want schema")
	}
	if testModel.last.Output.Mode != model.OutputModeSchema {
		t.Fatalf("Output.Mode = %q, want schema", testModel.last.Output.Mode)
	}
	if testModel.last.Output.MaxOutputTokens != 128 {
		t.Fatalf("Output.MaxOutputTokens = %d, want 128", testModel.last.Output.MaxOutputTokens)
	}
	properties, _ := testModel.last.Output.JSONSchema["properties"].(map[string]any)
	outcome, _ := properties["outcome"].(map[string]any)
	if got := outcome["type"]; got != "string" {
		t.Fatalf("schema properties.outcome.type = %v, want string", got)
	}
	if got := len(testModel.last.Tools); got != 0 {
		t.Fatalf("len(Tools) = %d, want 0", got)
	}
}

func TestModelRequestOptionsOutputSpecReturnsClone(t *testing.T) {
	t.Parallel()

	options := agent.ModelRequestOptions{
		Output: &model.OutputSpec{
			Mode: model.OutputModeSchema,
			JSONSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ok": map[string]any{"type": "boolean"},
				},
			},
		},
	}
	first := options.OutputSpec()
	first.JSONSchema["type"] = "array"
	first.JSONSchema["properties"].(map[string]any)["ok"].(map[string]any)["type"] = "string"

	second := options.OutputSpec()
	if got := second.JSONSchema["type"]; got != "object" {
		t.Fatalf("schema type = %v, want object", got)
	}
	properties, _ := second.JSONSchema["properties"].(map[string]any)
	okSchema, _ := properties["ok"].(map[string]any)
	if got := okSchema["type"]; got != "boolean" {
		t.Fatalf("nested schema type = %v, want boolean", got)
	}
}

func TestChatAgentRunsMinimalToolLoop(t *testing.T) {
	t.Parallel()

	testModel := &toolLoopModel{}
	echoTool := tool.NamedTool{
		Def: tool.Definition{
			Name:        "ECHO",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			var payload map[string]any
			_ = json.Unmarshal(call.Input, &payload)
			return tool.Result{
				ID:   call.ID,
				Name: call.Name,
				Content: []model.Part{
					model.NewJSONPart([]byte(`{"value":"pong"}`)),
				},
			}, nil
		},
	}
	chatAgent, err := NewWithTools("chat", testModel, []tool.Tool{echoTool}, "Use tools when needed.")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{
			SessionRef: session.SessionRef{SessionID: "sess-1"},
		},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "say pong")),
			Text:    "say pong",
		}},
	})

	var events []*session.Event
	for event, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		events = append(events, event)
	}

	if got, want := len(testModel.requests), 2; got != want {
		t.Fatalf("len(testModel.requests) = %d, want %d", got, want)
	}
	if got, want := len(testModel.requests[0].Tools), 1; got != want {
		t.Fatalf("len(first request tools) = %d, want %d", got, want)
	}
	if got, want := len(events), 3; got != want {
		t.Fatalf("len(events) = %d, want %d", got, want)
	}
	if events[0].Type != session.EventTypeToolCall {
		t.Fatalf("events[0].Type = %q, want tool_call", events[0].Type)
	}
	if events[0].Protocol == nil || events[0].Protocol.ToolCall == nil || events[0].Protocol.UpdateType != string(session.ProtocolUpdateTypeToolCall) {
		t.Fatalf("events[0].Protocol = %+v, want tool_call protocol payload", events[0].Protocol)
	}
	if events[1].Type != session.EventTypeToolResult {
		t.Fatalf("events[1].Type = %q, want tool_result", events[1].Type)
	}
	if events[1].Protocol == nil || events[1].Protocol.ToolCall == nil || events[1].Protocol.UpdateType != string(session.ProtocolUpdateTypeToolUpdate) {
		t.Fatalf("events[1].Protocol = %+v, want tool_call_update protocol payload", events[1].Protocol)
	}
	caelis, ok := events[1].Meta["caelis"].(map[string]any)
	if !ok {
		t.Fatalf("events[1].Meta = %#v, want caelis extension", events[1].Meta)
	}
	runtimeMeta, ok := caelis["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("events[1].Meta[caelis] = %#v, want runtime extension", caelis)
	}
	toolRuntime, ok := runtimeMeta["tool"].(map[string]any)
	if !ok || toolRuntime["name"] != "ECHO" {
		t.Fatalf("runtime.tool = %#v, want ECHO tool runtime name", runtimeMeta["tool"])
	}
	if events[2].Type != session.EventTypeAssistant || events[2].Text != "pong" {
		t.Fatalf("events[2] = %+v, want final assistant pong", events[2])
	}
	if events[2].Protocol == nil || events[2].Protocol.UpdateType != string(session.ProtocolUpdateTypeAgentMessage) {
		t.Fatalf("events[2].Protocol = %+v, want agent_message protocol payload", events[2].Protocol)
	}
}

func TestChatAgentDrainsPendingUserSubmissionAfterToolResults(t *testing.T) {
	t.Parallel()

	testModel := &toolLoopModel{}
	echoTool := tool.NamedTool{
		Def: tool.Definition{
			Name:        "ECHO",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ID:      call.ID,
				Name:    call.Name,
				Content: []model.Part{model.NewJSONPart([]byte(`{"value":"pong"}`))},
			}, nil
		},
	}
	chatAgent, err := NewWithTools("chat", testModel, []tool.Tool{echoTool}, "Use tools when needed.")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}

	drained := false
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{
			SessionRef: session.SessionRef{SessionID: "sess-steer"},
		},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "say pong")),
			Text:    "say pong",
		}},
		DrainSubmissions: func() []agent.Submission {
			if drained {
				return nil
			}
			drained = true
			return []agent.Submission{{
				Kind: agent.SubmissionKindConversation,
				Text: "focus on the follow-up",
			}}
		},
	})

	var userEvents []*session.Event
	for event, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		if event != nil && event.Type == session.EventTypeUser {
			userEvents = append(userEvents, event)
		}
	}

	if got, want := len(testModel.requests), 2; got != want {
		t.Fatalf("len(testModel.requests) = %d, want %d", got, want)
	}
	second := testModel.requests[1].Messages
	if got, want := len(second), 4; got != want {
		t.Fatalf("len(second.Messages) = %d, want %d (%#v)", got, want, second)
	}
	if got := second[3].Role; got != model.RoleUser {
		t.Fatalf("second.Messages[3].Role = %q, want user", got)
	}
	if got := second[3].TextContent(); got != "focus on the follow-up" {
		t.Fatalf("second.Messages[3] text = %q", got)
	}
	if len(userEvents) != 1 || userEvents[0].Text != "focus on the follow-up" {
		t.Fatalf("emitted user events = %#v, want queued guidance echoed once", userEvents)
	}
}

func TestToolCallEventsPersistAssistantTextInProtocolContent(t *testing.T) {
	t.Parallel()

	text := "I will inspect the files first."
	message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
		ID:   "call-1",
		Name: "ECHO",
		Args: `{"value":"one"}`,
	}, {
		ID:   "call-2",
		Name: "ECHO",
		Args: `{"value":"two"}`,
	}}, text)
	resp := &model.Response{
		Message: message,
		Usage: model.Usage{
			PromptTokens:     10,
			CompletionTokens: 2,
			TotalTokens:      12,
		},
	}

	events := modelToolCallEvents(message, resp)
	if got, want := len(events), 2; got != want {
		t.Fatalf("len(events) = %d, want %d", got, want)
	}
	if got := session.EventText(events[0]); got != text {
		t.Fatalf("first tool-call text = %q, want %q", got, text)
	}
	if got := session.EventText(events[1]); got != "" {
		t.Fatalf("second tool-call text = %q, want empty", got)
	}
	if usage := nestedMap(events[0].Meta, "caelis", "sdk", "usage"); usage == nil {
		t.Fatalf("first tool-call meta = %#v, want usage", events[0].Meta)
	}
	if usage := nestedMap(events[1].Meta, "caelis", "sdk", "usage"); usage != nil {
		t.Fatalf("second tool-call meta usage = %#v, want nil", usage)
	}

	raw, err := json.Marshal(events[0])
	if err != nil {
		t.Fatalf("Marshal(event) error = %v", err)
	}
	var decoded session.Event
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal(event) error = %v", err)
	}
	if got := session.EventText(&decoded); got != text {
		t.Fatalf("round-tripped tool-call text = %q, want %q", got, text)
	}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-tool-text"}},
		Events: []*session.Event{
			&decoded,
			persistedToolResultEvent("call-1", "ECHO", map[string]any{"value": "one"}, map[string]any{"value": "ok"}),
		},
	})
	messages := messagesFromContext(ctx)
	if got, want := len(messages), 2; got != want {
		t.Fatalf("len(messages) = %d, want %d: %#v", got, want, messages)
	}
	if got := messages[0].TextContent(); got != text {
		t.Fatalf("tool-call message text = %q, want %q", got, text)
	}
}

func TestToolMetaCarriesOnlyRuntimeToolName(t *testing.T) {
	t.Parallel()

	meta := toolMeta("TASK")
	caelis, ok := meta["caelis"].(map[string]any)
	if !ok {
		t.Fatalf("meta = %#v, want caelis wrapper", meta)
	}
	if _, ok := caelis["display"]; ok {
		t.Fatalf("caelis = %#v, should not carry display semantics", caelis)
	}
	runtimeMeta, ok := caelis["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("caelis = %#v, want runtime map", caelis)
	}
	toolRuntime, ok := runtimeMeta["tool"].(map[string]any)
	if !ok || toolRuntime["name"] != "TASK" {
		t.Fatalf("runtime.tool = %#v, want TASK tool name", runtimeMeta["tool"])
	}
}

func TestMessagesFromContextGroupsConsecutiveToolCalls(t *testing.T) {
	t.Parallel()

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-tools"}},
		Events: []*session.Event{
			{
				Type:    session.EventTypeUser,
				Message: ptrMessage(model.NewTextMessage(model.RoleUser, "demo async tools")),
				Text:    "demo async tools",
			},
			persistedToolCallEvent("bash-1", "BASH", map[string]any{"command": "sleep 1", "yield_time_ms": 5}),
			persistedToolCallEvent("spawn-1", "SPAWN", map[string]any{"agent": "self", "prompt": "check"}),
			persistedToolResultEvent("bash-1", "BASH", map[string]any{"command": "sleep 1", "yield_time_ms": 5}, map[string]any{"task_id": "bash-task", "state": "running"}),
			persistedToolResultEvent("spawn-1", "SPAWN", map[string]any{"agent": "self", "prompt": "check"}, map[string]any{"task_id": "spawn-task", "state": "running"}),
			{
				Type:    session.EventTypeUser,
				Message: ptrMessage(model.NewTextMessage(model.RoleUser, "next turn")),
				Text:    "next turn",
			},
		},
	})

	messages := messagesFromContext(ctx)
	if got, want := len(messages), 5; got != want {
		t.Fatalf("len(messages) = %d, want %d (%#v)", got, want, messages)
	}
	calls := messages[1].ToolCalls()
	if got, want := len(calls), 2; got != want {
		t.Fatalf("len(tool calls) = %d, want %d: %#v", got, want, calls)
	}
	if calls[0].ID != "bash-1" || calls[1].ID != "spawn-1" {
		t.Fatalf("tool call order = %#v, want bash then spawn", calls)
	}
	if got := messages[2].ToolResults()[0].ToolUseID; got != "bash-1" {
		t.Fatalf("first tool result id = %q, want bash-1", got)
	}
	if got := messages[3].ToolResults()[0].ToolUseID; got != "spawn-1" {
		t.Fatalf("second tool result id = %q, want spawn-1", got)
	}
	if got := messages[4].TextContent(); got != "next turn" {
		t.Fatalf("final user text = %q, want next turn", got)
	}
}

func TestMessagesFromContextDropsIncompleteToolCallRun(t *testing.T) {
	t.Parallel()

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-incomplete"}},
		Events: []*session.Event{
			{
				Type:    session.EventTypeUser,
				Message: ptrMessage(model.NewTextMessage(model.RoleUser, "demo async tools")),
				Text:    "demo async tools",
			},
			persistedToolCallEvent("bash-1", "BASH", map[string]any{"command": "sleep 1"}),
			persistedToolCallEvent("spawn-1", "SPAWN", map[string]any{"agent": "self", "prompt": "check"}),
			persistedToolResultEvent("bash-1", "BASH", map[string]any{"command": "sleep 1"}, map[string]any{"task_id": "bash-task", "state": "running"}),
			{
				Type:    session.EventTypeUser,
				Message: ptrMessage(model.NewTextMessage(model.RoleUser, "next turn")),
				Text:    "next turn",
			},
		},
	})

	messages := messagesFromContext(ctx)
	if got, want := len(messages), 2; got != want {
		t.Fatalf("len(messages) = %d, want %d (%#v)", got, want, messages)
	}
	if len(messages[0].ToolCalls()) != 0 || len(messages[1].ToolCalls()) != 0 {
		t.Fatalf("messages include unresolved tool call run: %#v", messages)
	}
	if got := messages[1].TextContent(); got != "next turn" {
		t.Fatalf("final user text = %q, want next turn", got)
	}
}

func TestMessagesFromContextUsesEventLocalParticipantLabel(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{SessionID: "sess-side-label"},
		Participants: []session.ParticipantBinding{{
			ID:    "emma",
			Kind:  session.ParticipantKindACP,
			Role:  session.ParticipantRoleSidecar,
			Label: "@renamed",
		}},
	}
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: activeSession,
		Events: []*session.Event{{
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Message:    ptrMessage(model.NewTextMessage(model.RoleUser, "刚才都做了什么？总结一下")),
			Text:       "刚才都做了什么？总结一下",
			Scope: &session.EventScope{
				Participant: session.ParticipantRef{
					ID:   "emma",
					Kind: session.ParticipantKindACP,
					Role: session.ParticipantRoleSidecar,
				},
			},
			Meta: map[string]any{"mention": "@emma", "agent": "claude"},
		}, {
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    ptrMessage(model.NewTextMessage(model.RoleAssistant, "会话总结")),
			Text:       "会话总结",
			Actor:      session.ActorRef{Kind: session.ActorKindParticipant, ID: "emma", Name: "@emma"},
			Scope: &session.EventScope{
				Participant: session.ParticipantRef{
					ID:   "emma",
					Kind: session.ParticipantKindACP,
					Role: session.ParticipantRoleSidecar,
				},
			},
			Meta: map[string]any{"mention": "@emma", "agent": "claude"},
		}},
	})

	messages := messagesFromContext(ctx)
	if got, want := len(messages), 2; got != want {
		t.Fatalf("len(messages) = %d, want %d (%#v)", got, want, messages)
	}
	if got := messages[0].TextContent(); got != "User to @emma: 刚才都做了什么？总结一下" {
		t.Fatalf("side user message = %q", got)
	}
	if got := messages[1].TextContent(); got != "[agent_source agent=claude handle=@emma]\n会话总结" {
		t.Fatalf("side assistant message = %q", got)
	}
}

func TestMessagesFromContextSkipsDelegatedACPToolRawOutput(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("permission denied\n", tool.DefaultTruncationPolicy().ByteBudget()/2)
	delegatedScope := &session.EventScope{
		Source: "acp_subagent",
		Participant: session.ParticipantRef{
			ID:           "agent-1",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			DelegationID: "task-1",
		},
	}
	resultEvent := &session.Event{
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Scope:      delegatedScope,
		Protocol: &session.EventProtocol{
			ToolCall: &session.ProtocolToolCall{
				ID:        "call-1",
				Name:      "execute",
				Status:    "failed",
				RawOutput: map[string]any{"stderr": large, "exit_code": 1},
			},
			Update: &session.ProtocolUpdate{
				SessionUpdate: "tool_call_update",
				ToolCallID:    "call-1",
				Kind:          "execute",
				Status:        "failed",
				RawOutput:     map[string]any{"stderr": large, "exit_code": 1},
			},
		},
	}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-delegated-acp"}},
		Events: []*session.Event{
			{
				Type:       session.EventTypeUser,
				Visibility: session.VisibilityCanonical,
				Message:    ptrMessage(model.NewTextMessage(model.RoleUser, "continue")),
				Text:       "continue",
			},
			{
				Type:       session.EventTypeToolCall,
				Visibility: session.VisibilityCanonical,
				Scope:      delegatedScope,
				Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
					SessionUpdate: "tool_call",
					ToolCallID:    "call-1",
					Kind:          "execute",
					RawInput:      map[string]any{"command": "find /tmp"},
				}},
			},
			resultEvent,
		},
	})

	messages := messagesFromContext(ctx)
	if got, want := len(messages), 1; got != want {
		t.Fatalf("len(messages) = %d, want %d (%#v)", got, want, messages)
	}
	if got := messages[0].TextContent(); got != "continue" {
		t.Fatalf("message text = %q, want continue", got)
	}
	if got, _ := resultEvent.Protocol.Update.RawOutput["stderr"].(string); got != large {
		t.Fatalf("delegated ACP raw output was mutated before display projection")
	}
}

func TestToolResultMessagePreservesCanonicalBashPayloadForModel(t *testing.T) {
	t.Parallel()

	const deniedPath = "/home/test/go/pkg/mod/cache/download/work.ctyun.cn/git/ctstack_cmp_v2/system/@v/v0.0.0.tmp"
	message := toolResultMessage(model.ToolCall{
		ID:   "call-1",
		Name: "BASH",
	}, tool.Result{
		ID:      "call-1",
		Name:    "BASH",
		Content: []model.Part{model.NewJSONPart([]byte(`{"result":"go: writing stat cache: open /home/test/go/pkg/mod/cache/download/work.ctyun.cn/git/ctstack_cmp_v2/system/@v/v0.0.0.tmp: read-only file system\n","exit_code":1,"error":"Sandbox permission denied. Use a writable workspace path or request elevated permissions."}`))},
	})

	results := message.ToolResults()
	if len(results) != 1 {
		t.Fatalf("ToolResults() len = %d, want 1", len(results))
	}
	if results[0].IsError {
		t.Fatal("tool result IsError = true for bash exit status, want false")
	}
	var payload map[string]any
	if len(results[0].Content) == 0 || results[0].Content[0].JSON == nil {
		t.Fatalf("tool result content = %#v, want JSON payload", results[0].Content)
	}
	if err := json.Unmarshal(results[0].Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(tool result payload) error = %v", err)
	}
	if got, _ := payload["error"].(string); got == "" {
		t.Fatalf("error = %q, want concise sandbox permission hint", got)
	}
	if resultText, _ := payload["result"].(string); !strings.Contains(resultText, deniedPath) {
		t.Fatalf("result = %q, want original denied path", resultText)
	}
}

func TestToolResultEventFallsBackToJSONContentForRawOutput(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "call-1",
		Name: "BASH",
		Args: `{"command":"echo hello"}`,
	}, tool.Result{
		ID:      "call-1",
		Name:    "BASH",
		IsError: true,
		Content: []model.Part{model.NewJSONPart([]byte(`{"error":"terminal session failed"}`))},
	}, nil)

	if event.Protocol == nil || event.Protocol.Update == nil {
		t.Fatalf("event.Protocol = %#v, want tool result protocol", event.Protocol)
	}
	if got, _ := event.Protocol.Update.RawOutput["error"].(string); got != "terminal session failed" {
		t.Fatalf("raw output error = %q, want terminal session failed", got)
	}
	if got := event.Protocol.Update.Status; got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
}

func TestToolResultEventPreservesRunningTaskOutputPreviewAsACPContent(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "task-wait-1",
		Name: "TASK",
		Args: `{"action":"wait","task_id":"jack","yield_time_ms":5000}`,
	}, tool.Result{
		ID:   "task-wait-1",
		Name: "TASK",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"action":         "wait",
			"running":        true,
			"state":          "running",
			"task_id":        "jack",
			"target_kind":    "subagent",
			"output_preview": "正在读取 hello_from_spawn.txt\n",
		}))},
	}, nil)

	update := session.ProtocolUpdateOf(event)
	if update == nil {
		t.Fatalf("event protocol = %#v, want tool update", event.Protocol)
	}
	if got := update.Status; got != "running" {
		t.Fatalf("status = %q, want running", got)
	}
	content := session.ProtocolToolCallContentOf(update)
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one ACP terminal content item", content)
	}
	if content[0].Type != "terminal" {
		t.Fatalf("content type = %q, want terminal", content[0].Type)
	}
	textPayload, _ := content[0].Content.(map[string]any)
	if got, _ := textPayload["text"].(string); got != "正在读取 hello_from_spawn.txt" {
		t.Fatalf("content text = %q, want running output_preview", got)
	}
}

func TestToolResultEventPreservesTaskFinalMessageAsACPContent(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "task-wait-1",
		Name: "TASK",
		Args: `{"action":"wait","task_id":"jeff"}`,
	}, tool.Result{
		ID:   "task-wait-1",
		Name: "TASK",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"task_id":       "jeff",
			"state":         "completed",
			"final_message": "child final answer\n",
		}))},
	}, nil)

	update := session.ProtocolUpdateOf(event)
	if update == nil {
		t.Fatalf("event protocol = %#v, want tool update", event.Protocol)
	}
	content := session.ProtocolToolCallContentOf(update)
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one ACP terminal content item", content)
	}
	if content[0].Type != "terminal" {
		t.Fatalf("content type = %q, want terminal", content[0].Type)
	}
	textPayload, _ := content[0].Content.(map[string]any)
	if got, _ := textPayload["text"].(string); got != "child final answer" {
		t.Fatalf("content text = %q, want final message", got)
	}
}

func TestToolResultEventPreservesFailedTaskResultBeforeError(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "task-wait-1",
		Name: "TASK",
		Args: `{"action":"wait","task_id":"bash-task"}`,
	}, tool.Result{
		ID:   "task-wait-1",
		Name: "TASK",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"state":     "failed",
			"result":    "go: module internal registry: network unreachable\n",
			"error":     "Sandbox permission denied. Use a writable workspace path or request elevated permissions.",
			"exit_code": 1,
		}))},
	}, nil)

	update := session.ProtocolUpdateOf(event)
	if update == nil {
		t.Fatalf("event protocol = %#v, want tool update", event.Protocol)
	}
	if got := update.Status; got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	content := session.ProtocolToolCallContentOf(update)
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one ACP terminal content item", content)
	}
	if content[0].Type != "terminal" {
		t.Fatalf("content type = %q, want terminal", content[0].Type)
	}
	textPayload, _ := content[0].Content.(map[string]any)
	if got, _ := textPayload["text"].(string); got != "go: module internal registry: network unreachable" {
		t.Fatalf("content text = %q, want failed task result", got)
	}
	if got, _ := update.RawOutput["error"].(string); got == "" {
		t.Fatalf("raw output error = %q, want preserved error for model context", got)
	}
}

func TestToolResultEventPreservesBashResultFieldAsACPContent(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "bash-status-1",
		Name: "BASH",
		Args: `{"command":"git status"}`,
	}, tool.Result{
		ID:   "bash-status-1",
		Name: "BASH",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"result":    "On branch dev\nYour branch is behind 'origin/dev' by 3 commits.\n",
			"exit_code": 0,
		}))},
	}, nil)

	update := session.ProtocolUpdateOf(event)
	if update == nil {
		t.Fatalf("event protocol = %#v, want tool update", event.Protocol)
	}
	content := session.ProtocolToolCallContentOf(update)
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one ACP terminal content item", content)
	}
	if content[0].Type != "terminal" {
		t.Fatalf("content type = %q, want terminal", content[0].Type)
	}
	textPayload, _ := content[0].Content.(map[string]any)
	got, _ := textPayload["text"].(string)
	if got != "On branch dev\nYour branch is behind 'origin/dev' by 3 commits." {
		t.Fatalf("content text = %q, want result field", got)
	}
}

func TestToolResultEventPreservesFailedBashOutputBeforeExitSummary(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "bash-tidy-1",
		Name: "BASH",
		Args: `{"command":"go mod tidy"}`,
	}, tool.Result{
		ID:   "bash-tidy-1",
		Name: "BASH",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"result":    "go: module internal registry: network unreachable\n",
			"exit_code": 1,
		}))},
	}, nil)

	update := session.ProtocolUpdateOf(event)
	if update == nil {
		t.Fatalf("event protocol = %#v, want tool update", event.Protocol)
	}
	if got := update.Status; got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	content := session.ProtocolToolCallContentOf(update)
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one ACP terminal content item", content)
	}
	textPayload, _ := content[0].Content.(map[string]any)
	got, _ := textPayload["text"].(string)
	if got != "go: module internal registry: network unreachable" {
		t.Fatalf("content text = %q, want failed result field", got)
	}
}

func TestToolResultEventUsesNoOutputPlaceholderForSilentBashFailure(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "bash-silent-failure-1",
		Name: "BASH",
		Args: `{"command":"false"}`,
	}, tool.Result{
		ID:   "bash-silent-failure-1",
		Name: "BASH",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"exit_code": 1,
		}))},
	}, nil)

	update := session.ProtocolUpdateOf(event)
	if update == nil {
		t.Fatalf("event protocol = %#v, want tool update", event.Protocol)
	}
	if got := update.Status; got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	content := session.ProtocolToolCallContentOf(update)
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one ACP terminal content item", content)
	}
	textPayload, _ := content[0].Content.(map[string]any)
	got, _ := textPayload["text"].(string)
	if got != "(no output)" {
		t.Fatalf("content text = %q, want no-output placeholder", got)
	}
}

func TestToolResultEventUsesCanonicalTruncatedOutputForDisplayAndMessage(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("permission denied\n", tool.DefaultTruncationPolicy().ByteBudget()/2)
	result := tool.Result{
		ID:   "call-1",
		Name: "BASH",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"result":    large,
			"exit_code": 1,
		}))},
	}
	call := model.ToolCall{
		ID:   "call-1",
		Name: "BASH",
		Args: `{"command":"find /tmp -delete"}`,
	}
	canonical, truncationMeta := canonicalToolResult(result)
	message := toolResultMessageFromCanonical(call, canonical)
	event := toolResultEvent(model.ToolCall{
		ID:   "call-1",
		Name: "BASH",
		Args: `{"command":"find /tmp -delete"}`,
	}, canonical, &message, truncationMeta)

	rawOutput := event.Protocol.Update.RawOutput
	resultText, _ := rawOutput["result"].(string)
	if resultText == large {
		t.Fatalf("raw result kept original huge output, want canonical truncated rawOutput")
	}
	if !strings.Contains(resultText, "tokens truncated") {
		t.Fatalf("raw result = %q, want truncation marker", resultText)
	}
	if rawOutput["_tool_truncation"] != nil {
		t.Fatalf("raw output = %#v, should not carry model truncation metadata", rawOutput)
	}
	if meta := nestedMap(event.Meta, "caelis", "runtime", "tool", "truncation"); meta == nil || meta["truncated"] != true {
		t.Fatalf("event.Meta = %#v, want truncation metadata under caelis.runtime.tool.truncation", event.Meta)
	}
	results := event.Message.ToolResults()
	if len(results) != 1 || len(results[0].Content) == 0 || results[0].Content[0].JSON == nil {
		t.Fatalf("event message = %#v, want one tool result", event.Message)
	}
	if encoded := results[0].Content[0].JSON.Value; len(encoded) > tool.DefaultTruncationPolicy().ByteBudget()+4096 {
		t.Fatalf("model-visible message len = %d, want bounded", len(encoded))
	}
	var payload map[string]any
	if err := json.Unmarshal(results[0].Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(tool result payload) error = %v", err)
	}
	modelResult, _ := payload["result"].(string)
	if modelResult != resultText {
		t.Fatalf("model result != rawOutput result; model=%q raw=%q", modelResult, resultText)
	}
	if payload["_tool_truncation"] != nil || payload["output_meta"] != nil {
		t.Fatalf("payload = %#v, should not expose truncation metadata to model", payload)
	}
}

func TestToolResultMessageCompactsLargeJSONPayloadForModel(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("permission denied\n", tool.DefaultTruncationPolicy().ByteBudget()/2)
	message := toolResultMessage(model.ToolCall{
		ID:   "call-1",
		Name: "BASH",
	}, tool.Result{
		ID:   "call-1",
		Name: "BASH",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"result": large,
		}))},
	})

	results := message.ToolResults()
	if len(results) != 1 || len(results[0].Content) == 0 || results[0].Content[0].JSON == nil {
		t.Fatalf("ToolResults() = %#v, want one JSON tool result", results)
	}
	var payload map[string]any
	if err := json.Unmarshal(results[0].Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(tool result payload) error = %v", err)
	}
	limit := tool.DefaultTruncationPolicy().ByteBudget() + 4096
	if encoded := results[0].Content[0].JSON.Value; len(encoded) > limit {
		t.Fatalf("encoded payload len = %d, want <= %d", len(encoded), limit)
	}
	resultText, _ := payload["result"].(string)
	if !strings.Contains(resultText, "tokens truncated") {
		t.Fatalf("result = %q, want truncation marker", resultText)
	}
	if payload["_tool_truncation"] != nil || payload["output_meta"] != nil {
		t.Fatalf("payload = %#v, should not expose truncation metadata to model", payload)
	}
}

func TestProtocolToolResultContextCompactsRawOutput(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("x", tool.DefaultTruncationPolicy().ByteBudget()*2)
	message, ok := messageFromProtocolEvent(&session.Event{
		Type: session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			ToolCall: &session.ProtocolToolCall{ID: "call-1", Name: "BASH"},
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "call-1",
				Title:         "BASH echo",
				Status:        "completed",
				RawOutput:     map[string]any{"result": large},
			},
		},
	})
	if !ok {
		t.Fatal("messageFromProtocolEvent() ok = false, want true")
	}
	results := message.ToolResults()
	if len(results) != 1 || len(results[0].Content) == 0 || results[0].Content[0].JSON == nil {
		t.Fatalf("ToolResults() = %#v, want one JSON tool result", results)
	}
	var payload map[string]any
	if err := json.Unmarshal(results[0].Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(tool result payload) error = %v", err)
	}
	resultText, _ := payload["result"].(string)
	if !strings.Contains(resultText, "tokens truncated") {
		t.Fatalf("result = %q, want truncation marker", resultText)
	}
	if payload["_tool_truncation"] != nil || payload["output_meta"] != nil {
		t.Fatalf("payload = %#v, should not expose truncation metadata to model", payload)
	}
}

func TestProtocolToolResultContextUsesACPContentWhenRawOutputAbsent(t *testing.T) {
	t.Parallel()

	message, ok := messageFromProtocolEvent(&session.Event{
		Type: session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			ToolCall: &session.ProtocolToolCall{ID: "call-1", Name: "BASH"},
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "call-1",
				Title:         "BASH printf",
				Status:        "completed",
				Content: []session.ProtocolToolCallContent{{
					Type:       "terminal",
					TerminalID: "call-1",
					Content:    session.ProtocolTextContent("  output\n"),
				}},
			},
		},
	})
	if !ok {
		t.Fatal("messageFromProtocolEvent() ok = false, want true")
	}
	results := message.ToolResults()
	if len(results) != 1 || len(results[0].Content) == 0 || results[0].Content[0].JSON == nil {
		t.Fatalf("ToolResults() = %#v, want one JSON tool result", results)
	}
	var payload map[string]any
	if err := json.Unmarshal(results[0].Content[0].JSON.Value, &payload); err != nil {
		t.Fatalf("json.Unmarshal(tool result payload) error = %v", err)
	}
	if got, _ := payload["result"].(string); got != "  output\n" {
		t.Fatalf("payload[result] = %q, want ACP terminal content text", got)
	}
}

func TestProtocolToolResultContextTruncatesPersistedBashFailureShape(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("find: cannot delete /tmp/gomod/pkg: permission denied\n", tool.DefaultTruncationPolicy().ByteBudget()/4)
	message, ok := messageFromProtocolEvent(&session.Event{
		Type: session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			ToolCall: &session.ProtocolToolCall{ID: "call-1", Name: "BASH"},
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "call-1",
				Title:         "BASH find /tmp/gomod -delete",
				Status:        "failed",
				RawOutput: map[string]any{
					"result":    "stderr:\n" + large,
					"exit_code": 1,
					"task_id":   "task-11",
					"state":     "failed",
				},
			},
		},
	})
	if !ok {
		t.Fatal("messageFromProtocolEvent() ok = false, want true")
	}
	results := message.ToolResults()
	if len(results) != 1 || len(results[0].Content) == 0 || results[0].Content[0].JSON == nil {
		t.Fatalf("ToolResults() = %#v, want one JSON tool result", results)
	}
	raw := results[0].Content[0].JSON.Value
	if limit := tool.DefaultTruncationPolicy().ByteBudget() + 4096; len(raw) > limit {
		t.Fatalf("replayed tool result len = %d, want <= %d", len(raw), limit)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("json.Unmarshal(tool result payload) error = %v", err)
	}
	if payload["task_id"] != "task-11" || payload["exit_code"] != float64(1) {
		t.Fatalf("payload lost task identity or exit code: %#v", payload)
	}
	resultText, _ := payload["result"].(string)
	if !strings.Contains(resultText, "tokens truncated") {
		t.Fatalf("result = %q, want truncation marker", resultText)
	}
	if payload["_tool_truncation"] != nil || payload["output_meta"] != nil {
		t.Fatalf("payload = %#v, should not expose truncation metadata to model", payload)
	}
}

func TestChatAgentEmitsToolProgressWhileCallIsRunning(t *testing.T) {
	t.Parallel()

	testModel := &toolLoopModel{}
	release := make(chan struct{})
	progressReported := make(chan struct{})
	echoTool := tool.NamedTool{
		Def: tool.Definition{
			Name:        "ECHO",
			Description: "fake tool with progress",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			if call.Observer == nil {
				t.Fatal("tool observer missing from call")
			}
			call.Observer.ObserveToolResult(tool.Result{
				ID:   call.ID,
				Name: call.Name,
				Meta: map[string]any{
					"task_id": "task-1",
					"state":   "running",
					"running": true,
				},
				Content: []model.Part{model.NewJSONPart([]byte(`{"task_id":"task-1","state":"running","running":true}`))},
			})
			close(progressReported)
			<-release
			return tool.Result{
				ID:      call.ID,
				Name:    call.Name,
				Content: []model.Part{model.NewJSONPart([]byte(`{"result":"done","state":"completed"}`))},
				Meta: map[string]any{
					"result": "done",
					"state":  "completed",
				},
			}, nil
		},
	}
	chatAgent, err := NewWithTools("chat", testModel, []tool.Tool{echoTool}, "Use tools when needed.")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{
			SessionRef: session.SessionRef{SessionID: "sess-progress"},
		},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "run bash")),
			Text:    "run bash",
		}},
	})

	eventsCh := make(chan *session.Event, 8)
	errCh := make(chan error, 1)
	go func() {
		defer close(eventsCh)
		for event, runErr := range chatAgent.Run(ctx) {
			if runErr != nil {
				errCh <- runErr
				return
			}
			eventsCh <- event
		}
	}()

	var progress *session.Event
	deadline := time.After(2 * time.Second)
	for progress == nil {
		select {
		case err := <-errCh:
			t.Fatalf("Run() error before progress = %v", err)
		case <-progressReported:
		case event := <-eventsCh:
			if event == nil {
				t.Fatal("Run() ended before tool progress")
			}
			if event.Type == session.EventTypeToolResult && event.Protocol != nil && event.Protocol.ToolCall != nil && event.Protocol.ToolCall.Status == "running" {
				progress = event
			}
		case <-deadline:
			t.Fatal("timed out waiting for running tool progress")
		}
	}
	if progress.Visibility != session.VisibilityUIOnly {
		t.Fatalf("progress visibility = %q, want ui_only", progress.Visibility)
	}
	if progress.Message != nil {
		t.Fatalf("progress message = %+v, want nil so it is not appended to model history", progress.Message)
	}
	update := session.ProtocolUpdateOf(progress)
	if update == nil {
		t.Fatalf("progress protocol = %#v, want tool update", progress.Protocol)
	}
	if got, _ := update.RawOutput["task_id"].(string); got != "task-1" {
		t.Fatalf("progress task_id = %q, want task-1", got)
	}

	close(release)
	var finalText string
	for event := range eventsCh {
		if event != nil && event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(event.Text)
		}
	}
	select {
	case err := <-errCh:
		t.Fatalf("Run() error = %v", err)
	default:
	}
	if finalText != "pong" {
		t.Fatalf("finalText = %q, want pong", finalText)
	}
}

func TestChatAgentStreamsAssistantChunksBeforeFinalMessage(t *testing.T) {
	t.Parallel()

	testModel := &streamingModel{}
	chatAgent, err := (Factory{SystemPrompt: "Be terse."}).NewAgent(context.Background(), agent.AgentSpec{
		Name:  "chat",
		Model: testModel,
		Request: agent.ModelRequestOptions{
			Stream: boolPtr(true),
		},
	})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{
			SessionRef: session.SessionRef{SessionID: "sess-stream"},
		},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "hello")),
			Text:    "hello",
		}},
	})

	var events []*session.Event
	for event, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		events = append(events, event)
	}

	if !testModel.last.Stream {
		t.Fatal("model request Stream = false, want true")
	}
	if got, want := len(events), 4; got != want {
		t.Fatalf("len(events) = %d, want %d", got, want)
	}
	if events[0].Visibility != session.VisibilityUIOnly || events[0].Protocol == nil || events[0].Protocol.UpdateType != string(session.ProtocolUpdateTypeAgentThought) || events[0].Text != "thinking..." {
		t.Fatalf("events[0] = %+v, want ui-only reasoning chunk", events[0])
	}
	if events[1].Visibility != session.VisibilityUIOnly || events[1].Protocol == nil || events[1].Protocol.UpdateType != string(session.ProtocolUpdateTypeAgentMessage) || events[1].Text != "hel" {
		t.Fatalf("events[1] = %+v, want ui-only assistant chunk hel", events[1])
	}
	if events[2].Visibility != session.VisibilityUIOnly || events[2].Protocol == nil || events[2].Protocol.UpdateType != string(session.ProtocolUpdateTypeAgentMessage) || events[2].Text != "lo" {
		t.Fatalf("events[2] = %+v, want ui-only assistant chunk lo", events[2])
	}
	if events[3].Type != session.EventTypeAssistant || events[3].Text != "hello" {
		t.Fatalf("events[3] = %+v, want final assistant hello", events[3])
	}
	if events[3].Visibility != session.VisibilityCanonical {
		t.Fatalf("final event visibility = %q, want canonical", events[3].Visibility)
	}
}

func TestChatAgentDefaultsToNonStreamingRequests(t *testing.T) {
	t.Parallel()

	testModel := &streamingModel{}
	chatAgent, err := New("chat", testModel, "Be terse.")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{
			SessionRef: session.SessionRef{SessionID: "sess-nonstream"},
		},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "hello")),
			Text:    "hello",
		}},
	})

	var events []*session.Event
	for event, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		events = append(events, event)
	}

	if testModel.last.Stream {
		t.Fatal("model request Stream = true, want false by default")
	}
	if got, want := len(events), 1; got != want {
		t.Fatalf("len(events) = %d, want %d", got, want)
	}
	if events[0].Type != session.EventTypeAssistant || events[0].Text != "hello" {
		t.Fatalf("events[0] = %+v, want final assistant hello", events[0])
	}
}

func TestChunkEventFromStreamEventPreservesBoundaryWhitespace(t *testing.T) {
	t.Parallel()

	reasoning := chunkEventFromStreamEvent(&model.StreamEvent{
		Type: model.StreamEventPartDelta,
		PartDelta: &model.PartDelta{
			Kind:      model.PartKindReasoning,
			TextDelta: "think ",
		},
	})
	if reasoning == nil || reasoning.Text != "think " || reasoning.Message == nil || reasoning.Message.ReasoningText() != "think " {
		t.Fatalf("reasoning chunk = %+v, want boundary whitespace preserved", reasoning)
	}

	space := chunkEventFromStreamEvent(&model.StreamEvent{
		Type: model.StreamEventPartDelta,
		PartDelta: &model.PartDelta{
			Kind:      model.PartKindReasoning,
			TextDelta: " ",
		},
	})
	if space == nil || space.Text != " " || space.Message == nil || space.Message.ReasoningText() != " " {
		t.Fatalf("space chunk = %+v, want whitespace-only reasoning chunk preserved", space)
	}
}

func TestChatAgentDoesNotImposeFixedToolLoopCap(t *testing.T) {
	t.Parallel()

	testModel := &longToolLoopModel{}
	echoTool := tool.NamedTool{
		Def: tool.Definition{
			Name:        "ECHO",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ID:   call.ID,
				Name: call.Name,
				Content: []model.Part{
					model.NewJSONPart([]byte(`{"value":"pong"}`)),
				},
			}, nil
		},
	}
	chatAgent, err := NewWithTools("chat", testModel, []tool.Tool{echoTool}, "Use tools when needed.")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-long-loop"}},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "loop")),
			Text:    "loop",
		}},
	})

	var (
		final  *session.Event
		runErr error
	)
	for event, err := range chatAgent.Run(ctx) {
		if err != nil {
			runErr = err
			break
		}
		final = event
	}
	if runErr != nil {
		t.Fatalf("Run() error = %v", runErr)
	}
	if final == nil || final.Text != "done" {
		t.Fatalf("final event = %+v, want assistant done", final)
	}
	if got, want := testModel.calls, 10; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
}

type recordingModel struct {
	last model.Request
}

func (m *recordingModel) Name() string { return "stub" }

func (m *recordingModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if req != nil {
		m.last = *req
		m.last.Messages = model.CloneMessages(req.Messages)
		m.last.Instructions = model.CloneParts(req.Instructions)
		m.last.Output = agent.ModelRequestOptions{Output: req.Output}.OutputSpec()
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "world"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

func ptrMessage(message model.Message) *model.Message {
	return &message
}

func persistedToolCallEvent(id string, name string, input map[string]any) *session.Event {
	call := model.ToolCall{
		ID:   id,
		Name: name,
		Args: string(mustRawJSON(input)),
	}
	return &session.Event{
		Type:     session.EventTypeToolCall,
		Protocol: toolCallProtocol(call, session.ProtocolUpdateTypeToolCall, "pending", maps.Clone(input), nil, nil),
		Meta:     mergeEventMeta(toolMeta(name)),
	}
}

func persistedToolResultEvent(id string, name string, input map[string]any, output map[string]any) *session.Event {
	call := model.ToolCall{
		ID:   id,
		Name: name,
		Args: string(mustRawJSON(input)),
	}
	meta := mergeEventMeta(toolMeta(name))
	return &session.Event{
		Type:     session.EventTypeToolResult,
		Protocol: toolCallProtocol(call, session.ProtocolUpdateTypeToolUpdate, "completed", maps.Clone(input), maps.Clone(output), toolResultACPContent(call, input, output, meta, "completed", false)),
		Meta:     meta,
	}
}

func mustRawJSON(value map[string]any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func nestedMap(values map[string]any, path ...string) map[string]any {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	out, _ := current.(map[string]any)
	return out
}

func boolPtr(v bool) *bool { return &v }

type toolLoopModel struct {
	requests []model.Request
}

func (m *toolLoopModel) Name() string { return "tool-loop" }

func (m *toolLoopModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if req != nil {
		cp := *req
		cp.Messages = model.CloneMessages(req.Messages)
		cp.Instructions = model.CloneParts(req.Instructions)
		cp.Tools = append([]model.ToolSpec(nil), req.Tools...)
		m.requests = append(m.requests, cp)
	}
	index := len(m.requests)
	return func(yield func(*model.StreamEvent, error) bool) {
		if index == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "call-1",
						Name: "ECHO",
						Args: `{"value":"pong"}`,
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "pong"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

type streamingModel struct {
	last model.Request
}

func (m *streamingModel) Name() string { return "streaming" }

func (m *streamingModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if req != nil {
		m.last = *req
		m.last.Messages = model.CloneMessages(req.Messages)
		m.last.Instructions = model.CloneParts(req.Instructions)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventPartDelta,
			PartDelta: &model.PartDelta{
				Kind:      model.PartKindReasoning,
				TextDelta: "thinking...",
			},
		}, nil)
		yield(&model.StreamEvent{
			Type: model.StreamEventPartDelta,
			PartDelta: &model.PartDelta{
				Kind:      model.PartKindText,
				TextDelta: "hel",
			},
		}, nil)
		yield(&model.StreamEvent{
			Type: model.StreamEventPartDelta,
			PartDelta: &model.PartDelta{
				Kind:      model.PartKindText,
				TextDelta: "lo",
			},
		}, nil)
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.MessageFromAssistantParts("hello", strings.TrimSpace("thinking..."), nil),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type blockingStreamingModel struct {
	started      chan struct{}
	releaseFinal chan struct{}
}

func (m *blockingStreamingModel) Name() string { return "blocking-streaming" }

func (m *blockingStreamingModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if m.started != nil {
			select {
			case <-m.started:
			default:
				close(m.started)
			}
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventPartDelta,
			PartDelta: &model.PartDelta{
				Kind:      model.PartKindText,
				TextDelta: "hel",
			},
		}, nil)
		if m.releaseFinal != nil {
			select {
			case <-m.releaseFinal:
			case <-time.After(5 * time.Second):
				yield(nil, context.DeadlineExceeded)
				return
			}
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, "hello"),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
		_ = req
	}
}

type longToolLoopModel struct {
	calls int
}

func (m *longToolLoopModel) Name() string { return "long-tool-loop" }

func (m *longToolLoopModel) Generate(_ context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		if callIndex <= 9 {
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "call-loop",
						Name: "ECHO",
						Args: `{"value":"pong"}`,
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
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
