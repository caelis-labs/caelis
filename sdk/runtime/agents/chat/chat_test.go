package chat

import (
	"context"
	"encoding/json"
	"iter"
	"maps"
	"strings"
	"testing"
	"time"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

func TestChatAgentUsesSessionMessages(t *testing.T) {
	t.Parallel()

	model := &recordingModel{}
	agent, err := New("chat", model, "Be terse.")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{
				AppName:      "caelis",
				UserID:       "user-1",
				SessionID:    "sess-1",
				WorkspaceKey: "ws-1",
			},
		},
		Events: []*sdksession.Event{
			{
				Type:    sdksession.EventTypeUser,
				Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "hello")),
				Text:    "hello",
			},
		},
	})

	var final *sdksession.Event
	for event, runErr := range agent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		final = event
	}

	if got := len(model.last.Messages); got != 1 {
		t.Fatalf("len(Messages) = %d, want 1", got)
	}
	if got := model.last.Messages[0].TextContent(); got != "hello" {
		t.Fatalf("user text = %q, want %q", got, "hello")
	}
	if got := len(model.last.Instructions); got != 1 {
		t.Fatalf("len(Instructions) = %d, want 1", got)
	}
	if final == nil || final.Text != "world" {
		t.Fatalf("final event = %+v, want assistant world", final)
	}
}

func TestChatAgentIncludesSideDialogueAndExcludesDelegatedSubagents(t *testing.T) {
	t.Parallel()

	model := &recordingModel{}
	agent, err := New("chat", model, "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{SessionID: "sess-1"},
			Participants: []sdksession.ParticipantBinding{{
				ID:        "side-acp",
				Kind:      sdksession.ParticipantKindACP,
				Role:      sdksession.ParticipantRoleSidecar,
				Label:     "@codex",
				AgentName: "codex",
			}, {
				ID:        "child-1",
				Kind:      sdksession.ParticipantKindSubagent,
				Role:      sdksession.ParticipantRoleDelegated,
				Label:     "@ella",
				AgentName: "self",
			}},
		},
		Events: []*sdksession.Event{
			{
				Type:    sdksession.EventTypeUser,
				Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "main user")),
				Text:    "main user",
			},
			{
				Type:    sdksession.EventTypeAssistant,
				Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "side acp")),
				Text:    "side acp",
				Actor:   sdksession.ActorRef{Kind: sdksession.ActorKindParticipant, ID: "side-acp", Name: "@codex"},
				Scope: &sdksession.EventScope{
					Source: "acp_participant",
					Participant: sdksession.ParticipantRef{
						ID:   "side-acp",
						Kind: sdksession.ParticipantKindACP,
						Role: sdksession.ParticipantRoleSidecar,
					},
				},
			},
			{
				Type:    sdksession.EventTypeAssistant,
				Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "spawn child")),
				Text:    "spawn child",
				Scope: &sdksession.EventScope{
					Source: "agent_spawn",
					Participant: sdksession.ParticipantRef{
						ID:   "child-1",
						Kind: sdksession.ParticipantKindSubagent,
						Role: sdksession.ParticipantRoleDelegated,
					},
				},
			},
		},
	})

	for _, runErr := range agent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
	}

	if got := len(model.last.Messages); got != 2 {
		t.Fatalf("len(Messages) = %d, want main plus side final", got)
	}
	if got := model.last.Messages[0].TextContent(); got != "main user" {
		t.Fatalf("message text = %q, want main user", got)
	}
	if got := model.last.Messages[1].TextContent(); got != "[agent_source agent=codex handle=@codex]\nside acp" {
		t.Fatalf("side message text = %q", got)
	}
}

func TestFactoryMetadataSystemPromptOverridesFactoryDefault(t *testing.T) {
	t.Parallel()

	model := &recordingModel{}
	agent, err := (Factory{SystemPrompt: "factory-default"}).NewAgent(context.Background(), sdkruntime.AgentSpec{
		Name:  "chat",
		Model: model,
		Metadata: map[string]any{
			"system_prompt": "assembly-override",
		},
	})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}

	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{SessionID: "sess-override"},
		},
		Events: []*sdksession.Event{{
			Type:    sdksession.EventTypeUser,
			Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "hello")),
			Text:    "hello",
		}},
	})

	for _, runErr := range agent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
	}
	if got, want := len(model.last.Instructions), 1; got != want {
		t.Fatalf("len(Instructions) = %d, want %d", got, want)
	}
	if model.last.Instructions[0].Kind != sdkmodel.PartKindText || model.last.Instructions[0].Text == nil {
		t.Fatalf("instruction[0] = %+v, want text part", model.last.Instructions[0])
	}
	if got := model.last.Instructions[0].Text.Text; got != "assembly-override" {
		t.Fatalf("instruction text = %q, want %q", got, "assembly-override")
	}
}

func TestFactoryPassesOutputSpecToModelRequest(t *testing.T) {
	t.Parallel()

	model := &recordingModel{}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"outcome": map[string]any{"type": "string"},
		},
		"required": []any{"outcome"},
	}
	agent, err := (Factory{SystemPrompt: "Return JSON."}).NewAgent(context.Background(), sdkruntime.AgentSpec{
		Name:  "chat",
		Model: model,
		Request: sdkruntime.ModelRequestOptions{
			Output: &sdkmodel.OutputSpec{
				Mode:            sdkmodel.OutputModeSchema,
				JSONSchema:      schema,
				MaxOutputTokens: 128,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	schema["properties"].(map[string]any)["outcome"] = map[string]any{"type": "integer"}

	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{SessionID: "sess-output"},
		},
		Events: []*sdksession.Event{{
			Type:    sdksession.EventTypeUser,
			Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "hello")),
			Text:    "hello",
		}},
	})

	for _, runErr := range agent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
	}

	if model.last.Output == nil {
		t.Fatal("model request Output = nil, want schema")
	}
	if model.last.Output.Mode != sdkmodel.OutputModeSchema {
		t.Fatalf("Output.Mode = %q, want schema", model.last.Output.Mode)
	}
	if model.last.Output.MaxOutputTokens != 128 {
		t.Fatalf("Output.MaxOutputTokens = %d, want 128", model.last.Output.MaxOutputTokens)
	}
	properties, _ := model.last.Output.JSONSchema["properties"].(map[string]any)
	outcome, _ := properties["outcome"].(map[string]any)
	if got := outcome["type"]; got != "string" {
		t.Fatalf("schema properties.outcome.type = %v, want string", got)
	}
	if got := len(model.last.Tools); got != 0 {
		t.Fatalf("len(Tools) = %d, want 0", got)
	}
}

func TestModelRequestOptionsOutputSpecReturnsClone(t *testing.T) {
	t.Parallel()

	options := sdkruntime.ModelRequestOptions{
		Output: &sdkmodel.OutputSpec{
			Mode: sdkmodel.OutputModeSchema,
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

	model := &toolLoopModel{}
	tool := sdktool.NamedTool{
		Def: sdktool.Definition{
			Name:        "ECHO",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call sdktool.Call) (sdktool.Result, error) {
			var payload map[string]any
			_ = json.Unmarshal(call.Input, &payload)
			return sdktool.Result{
				ID:   call.ID,
				Name: call.Name,
				Content: []sdkmodel.Part{
					sdkmodel.NewJSONPart([]byte(`{"value":"pong"}`)),
				},
			}, nil
		},
	}
	agent, err := NewWithTools("chat", model, []sdktool.Tool{tool}, "Use tools when needed.")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}

	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{SessionID: "sess-1"},
		},
		Events: []*sdksession.Event{{
			Type:    sdksession.EventTypeUser,
			Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "say pong")),
			Text:    "say pong",
		}},
	})

	var events []*sdksession.Event
	for event, runErr := range agent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		events = append(events, event)
	}

	if got, want := len(model.requests), 2; got != want {
		t.Fatalf("len(model.requests) = %d, want %d", got, want)
	}
	if got, want := len(model.requests[0].Tools), 1; got != want {
		t.Fatalf("len(first request tools) = %d, want %d", got, want)
	}
	if got, want := len(events), 3; got != want {
		t.Fatalf("len(events) = %d, want %d", got, want)
	}
	if events[0].Type != sdksession.EventTypeToolCall {
		t.Fatalf("events[0].Type = %q, want tool_call", events[0].Type)
	}
	if events[0].Protocol == nil || events[0].Protocol.ToolCall == nil || events[0].Protocol.UpdateType != string(sdksession.ProtocolUpdateTypeToolCall) {
		t.Fatalf("events[0].Protocol = %+v, want tool_call protocol payload", events[0].Protocol)
	}
	if events[1].Type != sdksession.EventTypeToolResult {
		t.Fatalf("events[1].Type = %q, want tool_result", events[1].Type)
	}
	if events[1].Protocol == nil || events[1].Protocol.ToolCall == nil || events[1].Protocol.UpdateType != string(sdksession.ProtocolUpdateTypeToolUpdate) {
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
	if events[2].Type != sdksession.EventTypeAssistant || events[2].Text != "pong" {
		t.Fatalf("events[2] = %+v, want final assistant pong", events[2])
	}
	if events[2].Protocol == nil || events[2].Protocol.UpdateType != string(sdksession.ProtocolUpdateTypeAgentMessage) {
		t.Fatalf("events[2].Protocol = %+v, want agent_message protocol payload", events[2].Protocol)
	}
}

func TestChatAgentDrainsPendingUserSubmissionAfterToolResults(t *testing.T) {
	t.Parallel()

	model := &toolLoopModel{}
	tool := sdktool.NamedTool{
		Def: sdktool.Definition{
			Name:        "ECHO",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call sdktool.Call) (sdktool.Result, error) {
			return sdktool.Result{
				ID:      call.ID,
				Name:    call.Name,
				Content: []sdkmodel.Part{sdkmodel.NewJSONPart([]byte(`{"value":"pong"}`))},
			}, nil
		},
	}
	agent, err := NewWithTools("chat", model, []sdktool.Tool{tool}, "Use tools when needed.")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}

	drained := false
	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{SessionID: "sess-steer"},
		},
		Events: []*sdksession.Event{{
			Type:    sdksession.EventTypeUser,
			Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "say pong")),
			Text:    "say pong",
		}},
		DrainSubmissions: func() []sdkruntime.Submission {
			if drained {
				return nil
			}
			drained = true
			return []sdkruntime.Submission{{
				Kind: sdkruntime.SubmissionKindConversation,
				Text: "focus on the follow-up",
			}}
		},
	})

	var userEvents []*sdksession.Event
	for event, runErr := range agent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		if event != nil && event.Type == sdksession.EventTypeUser {
			userEvents = append(userEvents, event)
		}
	}

	if got, want := len(model.requests), 2; got != want {
		t.Fatalf("len(model.requests) = %d, want %d", got, want)
	}
	second := model.requests[1].Messages
	if got, want := len(second), 4; got != want {
		t.Fatalf("len(second.Messages) = %d, want %d (%#v)", got, want, second)
	}
	if got := second[3].Role; got != sdkmodel.RoleUser {
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
	message := sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
		ID:   "call-1",
		Name: "ECHO",
		Args: `{"value":"one"}`,
	}, {
		ID:   "call-2",
		Name: "ECHO",
		Args: `{"value":"two"}`,
	}}, text)
	resp := &sdkmodel.Response{
		Message: message,
		Usage: sdkmodel.Usage{
			PromptTokens:     10,
			CompletionTokens: 2,
			TotalTokens:      12,
		},
	}

	events := modelToolCallEvents(message, resp)
	if got, want := len(events), 2; got != want {
		t.Fatalf("len(events) = %d, want %d", got, want)
	}
	if got := sdksession.EventText(events[0]); got != text {
		t.Fatalf("first tool-call text = %q, want %q", got, text)
	}
	if got := sdksession.EventText(events[1]); got != "" {
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
	var decoded sdksession.Event
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal(event) error = %v", err)
	}
	if got := sdksession.EventText(&decoded); got != text {
		t.Fatalf("round-tripped tool-call text = %q, want %q", got, text)
	}

	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{SessionRef: sdksession.SessionRef{SessionID: "sess-tool-text"}},
		Events: []*sdksession.Event{
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

	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{SessionRef: sdksession.SessionRef{SessionID: "sess-tools"}},
		Events: []*sdksession.Event{
			{
				Type:    sdksession.EventTypeUser,
				Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "demo async tools")),
				Text:    "demo async tools",
			},
			persistedToolCallEvent("bash-1", "BASH", map[string]any{"command": "sleep 1", "yield_time_ms": 5}),
			persistedToolCallEvent("spawn-1", "SPAWN", map[string]any{"agent": "self", "prompt": "check"}),
			persistedToolResultEvent("bash-1", "BASH", map[string]any{"command": "sleep 1", "yield_time_ms": 5}, map[string]any{"task_id": "bash-task", "state": "running"}),
			persistedToolResultEvent("spawn-1", "SPAWN", map[string]any{"agent": "self", "prompt": "check"}, map[string]any{"task_id": "spawn-task", "state": "running"}),
			{
				Type:    sdksession.EventTypeUser,
				Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "next turn")),
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

	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{SessionRef: sdksession.SessionRef{SessionID: "sess-incomplete"}},
		Events: []*sdksession.Event{
			{
				Type:    sdksession.EventTypeUser,
				Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "demo async tools")),
				Text:    "demo async tools",
			},
			persistedToolCallEvent("bash-1", "BASH", map[string]any{"command": "sleep 1"}),
			persistedToolCallEvent("spawn-1", "SPAWN", map[string]any{"agent": "self", "prompt": "check"}),
			persistedToolResultEvent("bash-1", "BASH", map[string]any{"command": "sleep 1"}, map[string]any{"task_id": "bash-task", "state": "running"}),
			{
				Type:    sdksession.EventTypeUser,
				Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "next turn")),
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

	session := sdksession.Session{
		SessionRef: sdksession.SessionRef{SessionID: "sess-side-label"},
		Participants: []sdksession.ParticipantBinding{{
			ID:    "emma",
			Kind:  sdksession.ParticipantKindACP,
			Role:  sdksession.ParticipantRoleSidecar,
			Label: "@renamed",
		}},
	}
	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: session,
		Events: []*sdksession.Event{{
			Type:       sdksession.EventTypeUser,
			Visibility: sdksession.VisibilityCanonical,
			Message:    ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "刚才都做了什么？总结一下")),
			Text:       "刚才都做了什么？总结一下",
			Scope: &sdksession.EventScope{
				Participant: sdksession.ParticipantRef{
					ID:   "emma",
					Kind: sdksession.ParticipantKindACP,
					Role: sdksession.ParticipantRoleSidecar,
				},
			},
			Meta: map[string]any{"mention": "@emma", "agent": "claude"},
		}, {
			Type:       sdksession.EventTypeAssistant,
			Visibility: sdksession.VisibilityCanonical,
			Message:    ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "会话总结")),
			Text:       "会话总结",
			Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindParticipant, ID: "emma", Name: "@emma"},
			Scope: &sdksession.EventScope{
				Participant: sdksession.ParticipantRef{
					ID:   "emma",
					Kind: sdksession.ParticipantKindACP,
					Role: sdksession.ParticipantRoleSidecar,
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

func TestToolResultMessagePreservesTerminalLikeBashPayloadForModel(t *testing.T) {
	t.Parallel()

	const deniedPath = "/home/test/go/pkg/mod/cache/download/work.ctyun.cn/git/ctstack_cmp_v2/system/@v/v0.0.0.tmp"
	message := toolResultMessage(sdkmodel.ToolCall{
		ID:   "call-1",
		Name: "BASH",
	}, sdktool.Result{
		ID:      "call-1",
		Name:    "BASH",
		Content: []sdkmodel.Part{sdkmodel.NewJSONPart([]byte(`{"stdout":"go: writing stat cache: open /home/test/go/pkg/mod/cache/download/work.ctyun.cn/git/ctstack_cmp_v2/system/@v/v0.0.0.tmp: read-only file system\n","stderr":"","exit_code":1}`))},
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
	if _, ok := payload["sandbox_permission_denied"]; ok {
		t.Fatalf("sandbox_permission_denied present = %#v, want omitted from model payload", payload["sandbox_permission_denied"])
	}
	if _, ok := payload["error"]; ok {
		t.Fatalf("error present = %#v, want omitted from model payload", payload["error"])
	}
	if stdout, _ := payload["stdout"].(string); !strings.Contains(stdout, deniedPath) {
		t.Fatalf("stdout = %q, want original denied path", stdout)
	}
}

func TestChatAgentEmitsToolProgressWhileCallIsRunning(t *testing.T) {
	t.Parallel()

	model := &toolLoopModel{}
	release := make(chan struct{})
	progressReported := make(chan struct{})
	tool := sdktool.NamedTool{
		Def: sdktool.Definition{
			Name:        "ECHO",
			Description: "fake tool with progress",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(ctx context.Context, call sdktool.Call) (sdktool.Result, error) {
			if call.Observer == nil {
				t.Fatal("tool observer missing from call")
			}
			call.Observer.ObserveToolResult(sdktool.Result{
				ID:   call.ID,
				Name: call.Name,
				Meta: map[string]any{
					"task_id": "task-1",
					"state":   "running",
					"running": true,
				},
				Content: []sdkmodel.Part{sdkmodel.NewJSONPart([]byte(`{"task_id":"task-1","state":"running","running":true}`))},
			})
			close(progressReported)
			<-release
			return sdktool.Result{
				ID:      call.ID,
				Name:    call.Name,
				Content: []sdkmodel.Part{sdkmodel.NewJSONPart([]byte(`{"result":"done","state":"completed"}`))},
				Meta: map[string]any{
					"result": "done",
					"state":  "completed",
				},
			}, nil
		},
	}
	agent, err := NewWithTools("chat", model, []sdktool.Tool{tool}, "Use tools when needed.")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}

	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{SessionID: "sess-progress"},
		},
		Events: []*sdksession.Event{{
			Type:    sdksession.EventTypeUser,
			Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "run bash")),
			Text:    "run bash",
		}},
	})

	eventsCh := make(chan *sdksession.Event, 8)
	errCh := make(chan error, 1)
	go func() {
		defer close(eventsCh)
		for event, runErr := range agent.Run(ctx) {
			if runErr != nil {
				errCh <- runErr
				return
			}
			eventsCh <- event
		}
	}()

	var progress *sdksession.Event
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
			if event.Type == sdksession.EventTypeToolResult && event.Protocol != nil && event.Protocol.ToolCall != nil && event.Protocol.ToolCall.Status == "running" {
				progress = event
			}
		case <-deadline:
			t.Fatal("timed out waiting for running tool progress")
		}
	}
	if progress.Visibility != sdksession.VisibilityUIOnly {
		t.Fatalf("progress visibility = %q, want ui_only", progress.Visibility)
	}
	if progress.Message != nil {
		t.Fatalf("progress message = %+v, want nil so it is not appended to model history", progress.Message)
	}
	update := sdksession.ProtocolUpdateOf(progress)
	if update == nil {
		t.Fatalf("progress protocol = %#v, want tool update", progress.Protocol)
	}
	if got, _ := update.RawOutput["task_id"].(string); got != "task-1" {
		t.Fatalf("progress task_id = %q, want task-1", got)
	}

	close(release)
	var finalText string
	for event := range eventsCh {
		if event != nil && event.Type == sdksession.EventTypeAssistant {
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

	model := &streamingModel{}
	agent, err := (Factory{SystemPrompt: "Be terse."}).NewAgent(context.Background(), sdkruntime.AgentSpec{
		Name:  "chat",
		Model: model,
		Request: sdkruntime.ModelRequestOptions{
			Stream: boolPtr(true),
		},
	})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}

	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{SessionID: "sess-stream"},
		},
		Events: []*sdksession.Event{{
			Type:    sdksession.EventTypeUser,
			Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "hello")),
			Text:    "hello",
		}},
	})

	var events []*sdksession.Event
	for event, runErr := range agent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		events = append(events, event)
	}

	if !model.last.Stream {
		t.Fatal("model request Stream = false, want true")
	}
	if got, want := len(events), 4; got != want {
		t.Fatalf("len(events) = %d, want %d", got, want)
	}
	if events[0].Visibility != sdksession.VisibilityUIOnly || events[0].Protocol == nil || events[0].Protocol.UpdateType != string(sdksession.ProtocolUpdateTypeAgentThought) || events[0].Text != "thinking..." {
		t.Fatalf("events[0] = %+v, want ui-only reasoning chunk", events[0])
	}
	if events[1].Visibility != sdksession.VisibilityUIOnly || events[1].Protocol == nil || events[1].Protocol.UpdateType != string(sdksession.ProtocolUpdateTypeAgentMessage) || events[1].Text != "hel" {
		t.Fatalf("events[1] = %+v, want ui-only assistant chunk hel", events[1])
	}
	if events[2].Visibility != sdksession.VisibilityUIOnly || events[2].Protocol == nil || events[2].Protocol.UpdateType != string(sdksession.ProtocolUpdateTypeAgentMessage) || events[2].Text != "lo" {
		t.Fatalf("events[2] = %+v, want ui-only assistant chunk lo", events[2])
	}
	if events[3].Type != sdksession.EventTypeAssistant || events[3].Text != "hello" {
		t.Fatalf("events[3] = %+v, want final assistant hello", events[3])
	}
	if events[3].Visibility != sdksession.VisibilityCanonical {
		t.Fatalf("final event visibility = %q, want canonical", events[3].Visibility)
	}
}

func TestChatAgentDefaultsToNonStreamingRequests(t *testing.T) {
	t.Parallel()

	model := &streamingModel{}
	agent, err := New("chat", model, "Be terse.")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{
			SessionRef: sdksession.SessionRef{SessionID: "sess-nonstream"},
		},
		Events: []*sdksession.Event{{
			Type:    sdksession.EventTypeUser,
			Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "hello")),
			Text:    "hello",
		}},
	})

	var events []*sdksession.Event
	for event, runErr := range agent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		events = append(events, event)
	}

	if model.last.Stream {
		t.Fatal("model request Stream = true, want false by default")
	}
	if got, want := len(events), 1; got != want {
		t.Fatalf("len(events) = %d, want %d", got, want)
	}
	if events[0].Type != sdksession.EventTypeAssistant || events[0].Text != "hello" {
		t.Fatalf("events[0] = %+v, want final assistant hello", events[0])
	}
}

func TestChunkEventFromStreamEventPreservesBoundaryWhitespace(t *testing.T) {
	t.Parallel()

	reasoning := chunkEventFromStreamEvent(&sdkmodel.StreamEvent{
		Type: sdkmodel.StreamEventPartDelta,
		PartDelta: &sdkmodel.PartDelta{
			Kind:      sdkmodel.PartKindReasoning,
			TextDelta: "think ",
		},
	})
	if reasoning == nil || reasoning.Text != "think " || reasoning.Message == nil || reasoning.Message.ReasoningText() != "think " {
		t.Fatalf("reasoning chunk = %+v, want boundary whitespace preserved", reasoning)
	}

	space := chunkEventFromStreamEvent(&sdkmodel.StreamEvent{
		Type: sdkmodel.StreamEventPartDelta,
		PartDelta: &sdkmodel.PartDelta{
			Kind:      sdkmodel.PartKindReasoning,
			TextDelta: " ",
		},
	})
	if space == nil || space.Text != " " || space.Message == nil || space.Message.ReasoningText() != " " {
		t.Fatalf("space chunk = %+v, want whitespace-only reasoning chunk preserved", space)
	}
}

func TestChatAgentDoesNotImposeFixedToolLoopCap(t *testing.T) {
	t.Parallel()

	model := &longToolLoopModel{}
	tool := sdktool.NamedTool{
		Def: sdktool.Definition{
			Name:        "ECHO",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call sdktool.Call) (sdktool.Result, error) {
			return sdktool.Result{
				ID:   call.ID,
				Name: call.Name,
				Content: []sdkmodel.Part{
					sdkmodel.NewJSONPart([]byte(`{"value":"pong"}`)),
				},
			}, nil
		},
	}
	agent, err := NewWithTools("chat", model, []sdktool.Tool{tool}, "Use tools when needed.")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}

	ctx := sdkruntime.NewContext(sdkruntime.ContextSpec{
		Context: context.Background(),
		Session: sdksession.Session{SessionRef: sdksession.SessionRef{SessionID: "sess-long-loop"}},
		Events: []*sdksession.Event{{
			Type:    sdksession.EventTypeUser,
			Message: ptrMessage(sdkmodel.NewTextMessage(sdkmodel.RoleUser, "loop")),
			Text:    "loop",
		}},
	})

	var (
		final  *sdksession.Event
		runErr error
	)
	for event, err := range agent.Run(ctx) {
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
	if got, want := model.calls, 10; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
}

type recordingModel struct {
	last sdkmodel.Request
}

func (m *recordingModel) Name() string { return "stub" }

func (m *recordingModel) Generate(_ context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	if req != nil {
		m.last = *req
		m.last.Messages = sdkmodel.CloneMessages(req.Messages)
		m.last.Instructions = sdkmodel.CloneParts(req.Instructions)
		m.last.Output = sdkruntime.ModelRequestOptions{Output: req.Output}.OutputSpec()
	}
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "world"),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
			},
		}, nil)
	}
}

func ptrMessage(message sdkmodel.Message) *sdkmodel.Message {
	return &message
}

func persistedToolCallEvent(id string, name string, input map[string]any) *sdksession.Event {
	call := sdkmodel.ToolCall{
		ID:   id,
		Name: name,
		Args: string(mustRawJSON(input)),
	}
	return &sdksession.Event{
		Type:     sdksession.EventTypeToolCall,
		Protocol: toolCallProtocol(call, sdksession.ProtocolUpdateTypeToolCall, "pending", maps.Clone(input), nil),
		Meta:     mergeEventMeta(toolMeta(name)),
	}
}

func persistedToolResultEvent(id string, name string, input map[string]any, output map[string]any) *sdksession.Event {
	call := sdkmodel.ToolCall{
		ID:   id,
		Name: name,
		Args: string(mustRawJSON(input)),
	}
	return &sdksession.Event{
		Type:     sdksession.EventTypeToolResult,
		Protocol: toolCallProtocol(call, sdksession.ProtocolUpdateTypeToolUpdate, "completed", maps.Clone(input), maps.Clone(output)),
		Meta:     mergeEventMeta(toolMeta(name)),
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
	requests []sdkmodel.Request
}

func (m *toolLoopModel) Name() string { return "tool-loop" }

func (m *toolLoopModel) Generate(_ context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	if req != nil {
		cp := *req
		cp.Messages = sdkmodel.CloneMessages(req.Messages)
		cp.Instructions = sdkmodel.CloneParts(req.Instructions)
		cp.Tools = append([]sdkmodel.ToolSpec(nil), req.Tools...)
		m.requests = append(m.requests, cp)
	}
	index := len(m.requests)
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		if index == 1 {
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
						ID:   "call-1",
						Name: "ECHO",
						Args: `{"value":"pong"}`,
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "pong"),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
				FinishReason: sdkmodel.FinishReasonStop,
			},
		}, nil)
	}
}

type streamingModel struct {
	last sdkmodel.Request
}

func (m *streamingModel) Name() string { return "streaming" }

func (m *streamingModel) Generate(_ context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	if req != nil {
		m.last = *req
		m.last.Messages = sdkmodel.CloneMessages(req.Messages)
		m.last.Instructions = sdkmodel.CloneParts(req.Instructions)
	}
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventPartDelta,
			PartDelta: &sdkmodel.PartDelta{
				Kind:      sdkmodel.PartKindReasoning,
				TextDelta: "thinking...",
			},
		}, nil)
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventPartDelta,
			PartDelta: &sdkmodel.PartDelta{
				Kind:      sdkmodel.PartKindText,
				TextDelta: "hel",
			},
		}, nil)
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventPartDelta,
			PartDelta: &sdkmodel.PartDelta{
				Kind:      sdkmodel.PartKindText,
				TextDelta: "lo",
			},
		}, nil)
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.MessageFromAssistantParts("hello", strings.TrimSpace("thinking..."), nil),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
			},
		}, nil)
	}
}

type blockingStreamingModel struct {
	started      chan struct{}
	releaseFinal chan struct{}
}

func (m *blockingStreamingModel) Name() string { return "blocking-streaming" }

func (m *blockingStreamingModel) Generate(_ context.Context, req *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		if m.started != nil {
			select {
			case <-m.started:
			default:
				close(m.started)
			}
		}
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventPartDelta,
			PartDelta: &sdkmodel.PartDelta{
				Kind:      sdkmodel.PartKindText,
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
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "hello"),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
			},
		}, nil)
		_ = req
	}
}

type longToolLoopModel struct {
	calls int
}

func (m *longToolLoopModel) Name() string { return "long-tool-loop" }

func (m *longToolLoopModel) Generate(_ context.Context, _ *sdkmodel.Request) iter.Seq2[*sdkmodel.StreamEvent, error] {
	m.calls++
	callIndex := m.calls
	return func(yield func(*sdkmodel.StreamEvent, error) bool) {
		if callIndex <= 9 {
			yield(&sdkmodel.StreamEvent{
				Type: sdkmodel.StreamEventTurnDone,
				Response: &sdkmodel.Response{
					Message: sdkmodel.MessageFromToolCalls(sdkmodel.RoleAssistant, []sdkmodel.ToolCall{{
						ID:   "call-loop",
						Name: "ECHO",
						Args: `{"value":"pong"}`,
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       sdkmodel.ResponseStatusCompleted,
					FinishReason: sdkmodel.FinishReasonToolCalls,
				},
			}, nil)
			return
		}
		yield(&sdkmodel.StreamEvent{
			Type: sdkmodel.StreamEventTurnDone,
			Response: &sdkmodel.Response{
				Message:      sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "done"),
				TurnComplete: true,
				StepComplete: true,
				Status:       sdkmodel.ResponseStatusCompleted,
				FinishReason: sdkmodel.FinishReasonStop,
			},
		}, nil)
	}
}
