package chat

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"maps"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sessionfile "github.com/OnslaughtSnail/caelis/impl/session/file"
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

func TestToolCallTitleIncludesSpawnPrompt(t *testing.T) {
	t.Parallel()

	call := model.ToolCall{
		ID:   "spawn-1",
		Name: "SPAWN",
		Args: `{"agent":"self","prompt":"总结当前目录"}`,
	}
	if got := toolCallTitle(call); got != "SPAWN self: 总结当前目录" {
		t.Fatalf("toolCallTitle(SPAWN) = %q", got)
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
	if events[0].Message == nil || len(events[0].Message.ToolCalls()) != 1 {
		t.Fatalf("events[0].Message = %+v, want one durable tool call message", events[0].Message)
	}
	if events[1].Type != session.EventTypeToolResult {
		t.Fatalf("events[1].Type = %q, want tool_result", events[1].Type)
	}
	if events[1].Tool == nil || events[1].Tool.Name != "ECHO" || events[1].Tool.Status != "completed" {
		t.Fatalf("events[1].Tool = %+v, want durable ECHO tool result payload", events[1].Tool)
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
	if events[2].Message == nil || events[2].Message.TextContent() != "pong" {
		t.Fatalf("events[2].Message = %+v, want durable assistant message", events[2].Message)
	}
}

func TestChatAgentRetriesInvalidModelToolCallWithoutPersistingIt(t *testing.T) {
	t.Parallel()

	testModel := &invalidThenValidToolModel{}
	invocations := 0
	echoTool := tool.NamedTool{
		Def: tool.Definition{
			Name:        "ECHO",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			invocations++
			var payload map[string]any
			if err := json.Unmarshal(call.Input, &payload); err != nil {
				t.Fatalf("tool input = %q, want valid JSON: %v", string(call.Input), err)
			}
			return tool.Result{
				ID:      call.ID,
				Name:    call.Name,
				Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{"value": payload["value"]}))},
			}, nil
		},
	}
	chatAgent, err := NewWithTools("chat", testModel, []tool.Tool{echoTool}, "Use tools when needed.")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-invalid-tool"}},
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

	if got, want := len(testModel.requests), 3; got != want {
		t.Fatalf("len(requests) = %d, want retry plus final", got)
	}
	if got, want := invocations, 1; got != want {
		t.Fatalf("tool invocations = %d, want %d", got, want)
	}
	if got, want := canonicalMessagesJSON(t, testModel.requests[1].Messages), canonicalMessagesJSON(t, testModel.requests[0].Messages); got != want {
		t.Fatalf("retry request changed canonical input\nfirst: %s\nretry: %s", want, got)
	}
	var canonicalEvents []*session.Event
	var invalidWarning *session.Event
	var resetEvents int
	for _, event := range events {
		if event.Visibility == session.VisibilityUIOnly {
			if event.Type == session.EventTypeLifecycle && event.Lifecycle != nil && event.Lifecycle.Status == "attempt_reset" {
				resetEvents++
			}
			if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Status == "failed" {
				invalidWarning = event
			}
			continue
		}
		canonicalEvents = append(canonicalEvents, event)
	}
	if resetEvents != 1 {
		t.Fatalf("resetEvents = %d, want one reset for the invalid attempt", resetEvents)
	}
	warningText := ""
	if invalidWarning != nil && invalidWarning.Tool != nil {
		warningText, _ = invalidWarning.Tool.Output["error"].(string)
	}
	if invalidWarning == nil || !strings.Contains(warningText, "decode tool call input for ECHO") {
		t.Fatalf("invalid warning event = %+v, want ui-only decode warning", invalidWarning)
	}
	if got, want := len(canonicalEvents), 3; got != want {
		t.Fatalf("len(canonicalEvents) = %d, want only valid tool call/result/final", got)
	}
	calls := canonicalEvents[0].Message.ToolCalls()
	if len(calls) != 1 || calls[0].Args != `{"value":"pong"}` {
		t.Fatalf("persisted tool calls = %#v, want only canonical valid args", calls)
	}
	if strings.Contains(canonicalMessagesJSON(t, testModel.requests[1].Messages), `{"value":"pong"`) {
		t.Fatalf("repair request replayed invalid assistant tool call: %s", canonicalMessagesJSON(t, testModel.requests[1].Messages))
	}
	if strings.Contains(canonicalMessagesJSON(t, testModel.requests[2].Messages), "invalid tool call") {
		t.Fatalf("post-tool request retained transient repair prompt: %s", canonicalMessagesJSON(t, testModel.requests[2].Messages))
	}
	if strings.Contains(canonicalMessagesJSON(t, testModel.requests[2].Messages), "All checks pass") {
		t.Fatalf("post-tool request retained invalid attempt text: %s", canonicalMessagesJSON(t, testModel.requests[2].Messages))
	}
	replayed := append([]*session.Event{{
		Type:    session.EventTypeUser,
		Message: ptrMessage(model.NewTextMessage(model.RoleUser, "say pong")),
		Text:    "say pong",
	}}, canonicalEvents[:2]...)
	replayedMessages := messagesFromContext(agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-invalid-tool"}},
		Events:  replayed,
	}))
	if got, want := canonicalMessagesJSON(t, testModel.requests[2].Messages), canonicalMessagesJSON(t, replayedMessages); got != want {
		t.Fatalf("post-tool live request diverged from persisted replay\nrequest: %s\nreplay:  %s", got, want)
	}
}

func TestCanonicalizeAssistantToolCallsPreservesNumericArgumentLexemes(t *testing.T) {
	t.Parallel()

	rawArgs := `{"id":9007199254740993,"amount":0.12345678901234567890}`
	message := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
		ID:   "call-precise",
		Name: "ECHO",
		Args: rawArgs,
	}}, "")

	canonical, calls, err := canonicalizeAssistantToolCalls(message)
	if err != nil {
		t.Fatalf("canonicalizeAssistantToolCalls() error = %v", err)
	}
	if len(calls) != 1 || calls[0].Args != rawArgs {
		t.Fatalf("calls = %#v, want raw args preserved", calls)
	}
	if got := canonical.ToolCalls()[0].Args; got != rawArgs {
		t.Fatalf("canonical message args = %q, want %q", got, rawArgs)
	}
}

func TestChatAgentExecutesSameStepToolCallsConcurrently(t *testing.T) {
	t.Parallel()

	testModel := &contextStabilityModel{toolNames: []string{"RUN_COMMAND", "RUN_COMMAND"}}
	var active int32
	var overlapped atomic.Bool
	runCommandTool := tool.NamedTool{
		Def: tool.Definition{
			Name:        "RUN_COMMAND",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			if atomic.AddInt32(&active, 1) > 1 {
				overlapped.Store(true)
			}
			defer atomic.AddInt32(&active, -1)
			select {
			case <-time.After(120 * time.Millisecond):
			case <-ctx.Done():
				return tool.Result{}, ctx.Err()
			}
			return tool.Result{
				ID:      call.ID,
				Name:    call.Name,
				Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{"value": call.ID}))},
			}, nil
		},
	}
	chatAgent, err := NewWithTools("chat", testModel, []tool.Tool{runCommandTool}, "Use tools when needed.")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-1"}},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "inspect both")),
			Text:    "inspect both",
		}},
	})

	for _, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
	}
	if !overlapped.Load() {
		t.Fatal("same-step tool calls did not overlap; want concurrent execution")
	}
	if got := len(testModel.requests); got != 2 {
		t.Fatalf("len(requests) = %d, want two model turns", got)
	}
	results := testModel.requests[1].Messages[len(testModel.requests[1].Messages)-2:]
	if got := results[0].ToolResults()[0].ToolUseID; got != "call-alpha" {
		t.Fatalf("first tool result id = %q, want call-alpha", got)
	}
	if got := results[1].ToolResults()[0].ToolUseID; got != "call-beta" {
		t.Fatalf("second tool result id = %q, want call-beta", got)
	}
}

func TestChatAgentExecutesMixedSameStepToolCallsSerially(t *testing.T) {
	t.Parallel()

	testModel := &contextStabilityModel{toolNames: []string{"RUN_COMMAND", "ECHO"}}
	var active int32
	var overlapped atomic.Bool
	invoke := func(ctx context.Context, call tool.Call) (tool.Result, error) {
		if atomic.AddInt32(&active, 1) > 1 {
			overlapped.Store(true)
		}
		defer atomic.AddInt32(&active, -1)
		select {
		case <-time.After(30 * time.Millisecond):
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
		return tool.Result{
			ID:      call.ID,
			Name:    call.Name,
			Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{"value": call.ID}))},
		}, nil
	}
	chatAgent, err := NewWithTools("chat", testModel, []tool.Tool{
		tool.NamedTool{Def: tool.Definition{Name: "RUN_COMMAND", InputSchema: map[string]any{"type": "object"}}, Invoke: invoke},
		tool.NamedTool{Def: tool.Definition{Name: "ECHO", InputSchema: map[string]any{"type": "object"}}, Invoke: invoke},
	}, "Use tools when needed.")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-1"}},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "inspect both")),
			Text:    "inspect both",
		}},
	})

	for _, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
	}
	if overlapped.Load() {
		t.Fatal("mixed same-step tool calls overlapped; want serial execution for non-RUN_COMMAND tools")
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

func TestToolCallEventsPersistCompleteAssistantMessage(t *testing.T) {
	t.Parallel()

	text := "I will inspect the files first."
	reasoning := "Need to inspect both values before answering."
	message := model.MessageFromAssistantParts(text, reasoning, []model.ToolCall{{
		ID:               "call-1",
		Name:             "ECHO",
		Args:             `{"value":"one"}`,
		ThoughtSignature: "sig-one",
	}, {
		ID:               "call-2",
		Name:             "ECHO",
		Args:             `{"value":"two"}`,
		ThoughtSignature: "sig-two",
	}})
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
		t.Fatalf("tool-call text = %q, want %q", got, text)
	}
	if got := session.EventText(events[1]); got != "" {
		t.Fatalf("second tool-call anchor text = %q, want empty", got)
	}
	if usage := nestedMap(events[0].Meta, "caelis", "sdk", "usage"); usage == nil {
		t.Fatalf("tool-call meta = %#v, want usage", events[0].Meta)
	}
	if usage := nestedMap(events[1].Meta, "caelis", "sdk", "usage"); usage != nil {
		t.Fatalf("second tool-call anchor usage = %#v, want nil", usage)
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
	if decoded.Message == nil {
		t.Fatal("round-tripped tool-call message = nil, want durable model message")
	}
	if got := decoded.Message.ReasoningText(); got != reasoning {
		t.Fatalf("round-tripped reasoning = %q, want %q", got, reasoning)
	}
	calls := decoded.Message.ToolCalls()
	if len(calls) != 2 || calls[0].ThoughtSignature != "sig-one" || calls[1].ThoughtSignature != "sig-two" {
		t.Fatalf("round-tripped tool calls = %#v, want both calls with replay signatures", calls)
	}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-tool-text"}},
		Events: []*session.Event{
			&decoded,
			persistedToolResultEvent("call-1", "ECHO", map[string]any{"value": "one"}, map[string]any{"value": "ok"}),
			persistedToolResultEvent("call-2", "ECHO", map[string]any{"value": "two"}, map[string]any{"value": "ok"}),
		},
	})
	messages := messagesFromContext(ctx)
	if got, want := len(messages), 3; got != want {
		t.Fatalf("len(messages) = %d, want %d: %#v", got, want, messages)
	}
	if got := messages[0].TextContent(); got != text {
		t.Fatalf("tool-call message text = %q, want %q", got, text)
	}
	if got := messages[0].ReasoningText(); got != reasoning {
		t.Fatalf("tool-call message reasoning = %q, want %q", got, reasoning)
	}
}

func TestModelContextRoundTripsThroughSessionStore(t *testing.T) {
	t.Parallel()

	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-context-roundtrip" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-context-roundtrip",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	user := model.NewTextMessage(model.RoleUser, "inspect both values")
	appendEvent := func(event *session.Event) {
		t.Helper()
		if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
			SessionRef: activeSession.SessionRef,
			Event:      event,
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	appendEvent(&session.Event{Type: session.EventTypeUser, Message: &user})

	assistant := model.MessageFromAssistantParts("I will inspect both values.", "Need both tool results.", []model.ToolCall{{
		ID:               "call-1",
		Name:             "ECHO",
		Args:             `{"value":"one"}`,
		ThoughtSignature: "sig-one",
	}, {
		ID:               "call-2",
		Name:             "ECHO",
		Args:             `{"value":"two"}`,
		ThoughtSignature: "sig-two",
	}})
	for _, event := range modelToolCallEvents(assistant, &model.Response{Message: assistant}) {
		appendEvent(event)
	}
	appendEvent(persistedToolResultEvent("call-1", "ECHO", map[string]any{"value": "one"}, map[string]any{"value": "ok"}))
	appendEvent(persistedToolResultEvent("call-2", "ECHO", map[string]any{"value": "two"}, map[string]any{"value": "ok"}))

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: activeSession,
		Events:  loaded.Events,
	})
	messages := messagesFromContext(ctx)
	if got, want := len(messages), 4; got != want {
		t.Fatalf("len(messages) = %d, want %d: %#v", got, want, messages)
	}
	if got := messages[0].TextContent(); got != "inspect both values" {
		t.Fatalf("user text = %q, want original text", got)
	}
	if got := messages[1].ReasoningText(); got != "Need both tool results." {
		t.Fatalf("assistant reasoning = %q, want original reasoning", got)
	}
	calls := messages[1].ToolCalls()
	if len(calls) != 2 || calls[0].ThoughtSignature != "sig-one" || calls[1].ThoughtSignature != "sig-two" {
		t.Fatalf("assistant tool calls = %#v, want calls with replay signatures", calls)
	}
	if got := len(messages[2].ToolResults()) + len(messages[3].ToolResults()); got != 2 {
		t.Fatalf("tool result count = %d, want 2", got)
	}
}

func TestServerSideToolReplayPartsRoundTripThroughSessionStore(t *testing.T) {
	t.Parallel()

	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-server-tool-roundtrip" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-server-tool-roundtrip",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	user := model.NewTextMessage(model.RoleUser, "search before answering")
	searchCall := json.RawMessage(`{"id":"search-1","toolType":"GOOGLE_SEARCH_WEB","args":{"query":"latest release"}}`)
	assistant := model.NewMessage(model.RoleAssistant,
		model.NewTextPart("checking"),
		serverSideToolReplayTestPart("server_tool_call", searchCall),
		model.NewTextPart("done"),
	)
	for _, event := range []*session.Event{
		{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &user, Text: user.TextContent()},
		{Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Message: &assistant, Text: assistant.TextContent()},
	} {
		if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
			SessionRef: activeSession.SessionRef,
			Event:      event,
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	messages := messagesFromContext(agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: activeSession,
		Events:  loaded.Events,
	}))
	if got, want := len(messages), 2; got != want {
		t.Fatalf("len(messages) = %d, want %d: %#v", got, want, messages)
	}
	gotParts := messages[1].Parts
	if len(gotParts) != 3 {
		t.Fatalf("assistant parts = %#v, want text/replay/text", gotParts)
	}
	replayPart := gotParts[1]
	if replayPart.Reasoning == nil || replayPart.Reasoning.Visibility != model.ReasoningVisibilityTokenOnly {
		t.Fatalf("replay part = %#v, want token-only reasoning carrier", replayPart)
	}
	if replayPart.Reasoning.Replay == nil || replayPart.Reasoning.Replay.Provider != "gemini" || replayPart.Reasoning.Replay.Kind != "server_tool_call" {
		t.Fatalf("replay meta = %#v, want gemini server_tool_call", replayPart.Reasoning.Replay)
	}
	raw := replayPart.Reasoning.ProviderDetails["part"]
	var gotPayload map[string]any
	var wantPayload map[string]any
	if err := json.Unmarshal(raw, &gotPayload); err != nil {
		t.Fatalf("unmarshal round-tripped provider detail: %v", err)
	}
	if err := json.Unmarshal(searchCall, &wantPayload); err != nil {
		t.Fatalf("unmarshal expected provider detail: %v", err)
	}
	if !reflect.DeepEqual(gotPayload, wantPayload) {
		t.Fatalf("provider detail = %#v, want %#v", gotPayload, wantPayload)
	}
}

func serverSideToolReplayTestPart(kind string, raw json.RawMessage) model.Part {
	part := model.NewReasoningPart("", model.ReasoningVisibilityTokenOnly)
	part.Reasoning.Replay = &model.ReplayMeta{Provider: "gemini", Kind: kind}
	part.Reasoning.ProviderDetails = map[string]json.RawMessage{
		"part": append(json.RawMessage(nil), raw...),
	}
	return part
}

func TestLiveModelContextPrefixMatchesPersistedReplay(t *testing.T) {
	t.Parallel()

	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-context-stability" },
	}))
	activeSession, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-context-stability",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	testModel := &contextStabilityModel{}
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
				Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
					"value": payload["value"],
				}))},
			}, nil
		},
	}
	chatAgent, err := NewWithTools("chat", testModel, []tool.Tool{echoTool}, "Use tools when needed.")
	if err != nil {
		t.Fatalf("NewWithTools() error = %v", err)
	}

	user := model.NewTextMessage(model.RoleUser, "inspect alpha and beta")
	liveEvents := []*session.Event{{
		Type:       session.EventTypeUser,
		Visibility: session.VisibilityCanonical,
		Message:    &user,
		Text:       user.TextContent(),
	}}
	runCtx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: activeSession,
		Events:  liveEvents,
	})
	for event, runErr := range chatAgent.Run(runCtx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		liveEvents = append(liveEvents, event)
	}

	liveMessages := messagesFromContext(agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: activeSession,
		Events:  liveEvents,
	}))
	for _, event := range liveEvents {
		if _, err := sessions.AppendEvent(context.Background(), session.AppendEventRequest{
			SessionRef: activeSession.SessionRef,
			Event:      event,
		}); err != nil {
			t.Fatalf("AppendEvent(%s) error = %v", event.Type, err)
		}
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: activeSession.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	for _, event := range loaded.Events {
		if event == nil || event.Tool == nil {
			continue
		}
		if event.Protocol != nil {
			t.Fatalf("loaded tool event %s persisted protocol payload: %#v", event.ID, event.Protocol)
		}
	}
	replayedMessages := messagesFromContext(agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: loaded.Session,
		Events:  loaded.Events,
	}))

	if got, want := canonicalMessagesJSON(t, replayedMessages), canonicalMessagesJSON(t, liveMessages); got != want {
		t.Fatalf("replayed context diverged from live prefix\nlive:   %s\nreplay: %s", want, got)
	}
	if got, want := len(testModel.requests), 2; got != want {
		t.Fatalf("model request count = %d, want %d", got, want)
	}
	if got, want := canonicalMessagesJSON(t, testModel.requests[1].Messages), canonicalMessagesJSON(t, liveMessages[:4]); got != want {
		t.Fatalf("second live LLM request prefix diverged from live event context\nrequest: %s\ncontext: %s", got, want)
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
			persistedToolCallEvent("command-1", "RUN_COMMAND", map[string]any{"command": "sleep 1", "yield_time_ms": 5}),
			persistedToolCallEvent("spawn-1", "SPAWN", map[string]any{"agent": "self", "prompt": "check"}),
			persistedToolResultEvent("command-1", "RUN_COMMAND", map[string]any{"command": "sleep 1", "yield_time_ms": 5}, map[string]any{"task_id": "command-task", "state": "running"}),
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
	if calls[0].ID != "command-1" || calls[1].ID != "spawn-1" {
		t.Fatalf("tool call order = %#v, want command then spawn", calls)
	}
	if got := messages[2].ToolResults()[0].ToolUseID; got != "command-1" {
		t.Fatalf("first tool result id = %q, want command-1", got)
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
			persistedToolCallEvent("command-1", "RUN_COMMAND", map[string]any{"command": "sleep 1"}),
			persistedToolCallEvent("spawn-1", "SPAWN", map[string]any{"agent": "self", "prompt": "check"}),
			persistedToolResultEvent("command-1", "RUN_COMMAND", map[string]any{"command": "sleep 1"}, map[string]any{"task_id": "command-task", "state": "running"}),
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

func TestMessagesFromContextDropsInvalidToolCallRun(t *testing.T) {
	t.Parallel()

	invalidAssistant := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
		ID:   "command-1",
		Name: "RUN_COMMAND",
		Args: `{"command":"git status"`,
	}}, "I will check status.")
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-invalid-history"}},
		Events: []*session.Event{
			{
				Type:    session.EventTypeUser,
				Message: ptrMessage(model.NewTextMessage(model.RoleUser, "continue")),
				Text:    "continue",
			},
			{
				Type:    session.EventTypeToolCall,
				Message: &invalidAssistant,
				Text:    invalidAssistant.TextContent(),
				Tool: &session.EventTool{
					ID:     "command-1",
					Name:   "RUN_COMMAND",
					Status: "pending",
				},
			},
			persistedToolResultEvent("command-1", "RUN_COMMAND", map[string]any{}, map[string]any{"error": "decode failed"}),
			{
				Type:    session.EventTypeUser,
				Message: ptrMessage(model.NewTextMessage(model.RoleUser, "try again")),
				Text:    "try again",
			},
		},
	})

	messages := messagesFromContext(ctx)
	if got, want := len(messages), 2; got != want {
		t.Fatalf("len(messages) = %d, want only user messages: %#v", got, messages)
	}
	if got := messages[0].TextContent(); got != "continue" {
		t.Fatalf("messages[0] text = %q", got)
	}
	if got := messages[1].TextContent(); got != "try again" {
		t.Fatalf("messages[1] text = %q", got)
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

func TestToolResultMessagePreservesCanonicalCommandPayloadForModel(t *testing.T) {
	t.Parallel()

	const deniedPath = "/home/test/go/pkg/mod/cache/download/work.ctyun.cn/git/ctstack_cmp_v2/system/@v/v0.0.0.tmp"
	message := toolResultMessage(model.ToolCall{
		ID:   "call-1",
		Name: "RUN_COMMAND",
	}, tool.Result{
		ID:      "call-1",
		Name:    "RUN_COMMAND",
		Content: []model.Part{model.NewJSONPart([]byte(`{"result":"go: writing stat cache: open /home/test/go/pkg/mod/cache/download/work.ctyun.cn/git/ctstack_cmp_v2/system/@v/v0.0.0.tmp: read-only file system\n","exit_code":1,"error":"Sandbox permission denied. Use a writable workspace path or request elevated permissions."}`))},
	})

	results := message.ToolResults()
	if len(results) != 1 {
		t.Fatalf("ToolResults() len = %d, want 1", len(results))
	}
	if results[0].IsError {
		t.Fatal("tool result IsError = true for command exit status, want false")
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
		Name: "RUN_COMMAND",
		Args: `{"command":"echo hello"}`,
	}, tool.Result{
		ID:      "call-1",
		Name:    "RUN_COMMAND",
		IsError: true,
		Content: []model.Part{model.NewJSONPart([]byte(`{"error":"terminal session failed"}`))},
	}, nil)

	if event.Tool == nil {
		t.Fatalf("event.Tool = nil, want tool result payload")
	}
	if got, _ := event.Tool.Output["error"].(string); got != "terminal session failed" {
		t.Fatalf("raw output error = %q, want terminal session failed", got)
	}
	if got := event.Tool.Status; got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
}

func TestToolResultEventSuppressesSuccessfulTaskWaitACPContent(t *testing.T) {
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

	if event.Tool == nil {
		t.Fatalf("event tool = nil, want tool update")
	}
	if got := event.Tool.Status; got != "running" {
		t.Fatalf("status = %q, want running", got)
	}
	content := event.Tool.Content
	if len(content) != 0 {
		t.Fatalf("content = %#v, want no display content for successful TASK wait", content)
	}
	if got, _ := event.Tool.Output["output_preview"].(string); got != "正在读取 hello_from_spawn.txt\n" {
		t.Fatalf("raw output output_preview = %q, want model-visible wait slice preserved", got)
	}
}

func TestToolResultEventSuppressesCompletedTaskWaitACPContent(t *testing.T) {
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

	if event.Tool == nil {
		t.Fatalf("event tool = nil, want tool update")
	}
	content := event.Tool.Content
	if len(content) != 0 {
		t.Fatalf("content = %#v, want no display content for completed TASK wait", content)
	}
	if got, _ := event.Tool.Output["final_message"].(string); got != "child final answer\n" {
		t.Fatalf("raw output final_message = %q, want model-visible final message preserved", got)
	}
}

func TestToolResultEventPreservesTaskWriteACPContent(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "task-write-1",
		Name: "TASK",
		Args: `{"action":"write","task_id":"jeff","input":"continue"}`,
	}, tool.Result{
		ID:   "task-write-1",
		Name: "TASK",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"task_id":       "jeff",
			"state":         "running",
			"latest_output": "input accepted\n",
		}))},
	}, nil)

	if event.Tool == nil {
		t.Fatalf("event tool = nil, want tool update")
	}
	content := event.Tool.Content
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one terminal content item", content)
	}
	if content[0].Type != "terminal" {
		t.Fatalf("content type = %q, want terminal", content[0].Type)
	}
	if got := content[0].Text; got != "input accepted\n" {
		t.Fatalf("content text = %q, want TASK write result", got)
	}
}

func TestToolResultEventSuppressesFailedTaskWaitACPContent(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "task-wait-1",
		Name: "TASK",
		Args: `{"action":"wait","task_id":"command-task"}`,
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

	if event.Tool == nil {
		t.Fatalf("event tool = nil, want tool update")
	}
	if got := event.Tool.Status; got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	content := event.Tool.Content
	if len(content) != 0 {
		t.Fatalf("content = %#v, want no display content for failed TASK wait", content)
	}
	if got, _ := event.Tool.Output["error"].(string); got == "" {
		t.Fatalf("raw output error = %q, want preserved error for model context", got)
	}
}

func TestToolResultEventSuppressesTaskCancelACPContent(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "task-cancel-1",
		Name: "TASK",
		Args: `{"action":"cancel","task_id":"command-task"}`,
	}, tool.Result{
		ID:   "task-cancel-1",
		Name: "TASK",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"state":     "cancelled",
			"result":    "cancelled command output\n",
			"error":     "context canceled",
			"exit_code": -1,
		}))},
	}, nil)

	if event.Tool == nil {
		t.Fatalf("event tool = nil, want tool update")
	}
	if content := event.Tool.Content; len(content) != 0 {
		t.Fatalf("content = %#v, want no display content for TASK cancel", content)
	}
}

func TestToolResultEventPreservesCommandResultFieldAsACPContent(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "command-status-1",
		Name: "RUN_COMMAND",
		Args: `{"command":"git status"}`,
	}, tool.Result{
		ID:   "command-status-1",
		Name: "RUN_COMMAND",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"result":    "On branch dev\nYour branch is behind 'origin/dev' by 3 commits.\n",
			"exit_code": 0,
		}))},
	}, nil)

	if event.Tool == nil {
		t.Fatalf("event tool = nil, want tool update")
	}
	content := event.Tool.Content
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one terminal content item", content)
	}
	if content[0].Type != "terminal" {
		t.Fatalf("content type = %q, want terminal", content[0].Type)
	}
	got := content[0].Text
	if got != "On branch dev\nYour branch is behind 'origin/dev' by 3 commits.\n" {
		t.Fatalf("content text = %q, want result field", got)
	}
}

func TestToolResultEventPreservesRawCommandOutputWhitespace(t *testing.T) {
	t.Parallel()

	raw := "  first line  \r\n   \r\nsecond line  \r\n"
	event := toolResultEvent(model.ToolCall{
		ID:   "command-output-1",
		Name: "RUN_COMMAND",
		Args: `{"command":"python test.py"}`,
	}, tool.Result{
		ID:   "command-output-1",
		Name: "RUN_COMMAND",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"result":    raw,
			"exit_code": 0,
		}))},
	}, nil)

	if event.Tool == nil {
		t.Fatalf("event tool = nil, want tool update")
	}
	content := event.Tool.Content
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one terminal content item", content)
	}
	if got := content[0].Text; got != raw {
		t.Fatalf("content text = %q, want raw command output %q", got, raw)
	}
}

func TestToolResultEventPreservesFailedCommandOutputBeforeExitSummary(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "command-tidy-1",
		Name: "RUN_COMMAND",
		Args: `{"command":"go mod tidy"}`,
	}, tool.Result{
		ID:   "command-tidy-1",
		Name: "RUN_COMMAND",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"result":    "go: module internal registry: network unreachable\n",
			"exit_code": 1,
		}))},
	}, nil)

	if event.Tool == nil {
		t.Fatalf("event tool = nil, want tool update")
	}
	if got := event.Tool.Status; got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	content := event.Tool.Content
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one terminal content item", content)
	}
	got := content[0].Text
	if got != "go: module internal registry: network unreachable\n" {
		t.Fatalf("content text = %q, want failed result field", got)
	}
}

func TestToolResultEventUsesStatusForSilentCommandFailure(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "command-silent-failure-1",
		Name: "RUN_COMMAND",
		Args: `{"command":"false"}`,
	}, tool.Result{
		ID:   "command-silent-failure-1",
		Name: "RUN_COMMAND",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"exit_code": 1,
		}))},
	}, nil)

	if event.Tool == nil {
		t.Fatalf("event tool = nil, want tool update")
	}
	if got := event.Tool.Status; got != "failed" {
		t.Fatalf("status = %q, want failed", got)
	}
	content := event.Tool.Content
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one terminal content item", content)
	}
	got := content[0].Text
	if got != "exit 1" {
		t.Fatalf("content text = %q, want exit code status", got)
	}
}

func TestToolResultEventOmitsContentForSuccessfulSilentCommand(t *testing.T) {
	t.Parallel()

	event := toolResultEvent(model.ToolCall{
		ID:   "command-silent-success-1",
		Name: "RUN_COMMAND",
		Args: `{"command":"true"}`,
	}, tool.Result{
		ID:   "command-silent-success-1",
		Name: "RUN_COMMAND",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"state":     "completed",
			"exit_code": 0,
		}))},
	}, nil)

	if event.Tool == nil {
		t.Fatalf("event tool = nil, want tool update")
	}
	if got := event.Tool.Status; got != "completed" {
		t.Fatalf("status = %q, want completed", got)
	}
	if len(event.Tool.Content) != 0 {
		t.Fatalf("content = %#v, want no canonical terminal placeholder", event.Tool.Content)
	}
}

func TestToolResultEventUsesCanonicalTruncatedOutputForDisplayAndMessage(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("permission denied\n", tool.DefaultTruncationPolicy().ByteBudget()/2)
	result := tool.Result{
		ID:   "call-1",
		Name: "RUN_COMMAND",
		Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
			"result":    large,
			"exit_code": 1,
		}))},
	}
	call := model.ToolCall{
		ID:   "call-1",
		Name: "RUN_COMMAND",
		Args: `{"command":"find /tmp -delete"}`,
	}
	canonical, truncationMeta := canonicalToolResult(result)
	message := toolResultMessageFromCanonical(call, canonical)
	event := toolResultEvent(model.ToolCall{
		ID:   "call-1",
		Name: "RUN_COMMAND",
		Args: `{"command":"find /tmp -delete"}`,
	}, canonical, &message, truncationMeta)

	if event.Tool == nil {
		t.Fatal("event.Tool = nil, want durable tool payload")
	}
	toolPayload := session.EventToolProjection(event)
	if toolPayload == nil {
		t.Fatal("EventToolProjection(event) = nil, want tool result projection")
		return
	}
	rawOutput := toolPayload.Output
	resultText, _ := rawOutput["result"].(string)
	if resultText == large {
		t.Fatalf("raw result kept original huge output, want canonical truncated rawOutput")
	}
	if !strings.Contains(resultText, "lines omitted") {
		t.Fatalf("raw result = %q, want omitted line marker", resultText)
	}
	if rawOutput["_tool_truncation"] != nil {
		t.Fatalf("raw output = %#v, should not carry model truncation metadata", rawOutput)
	}
	truncation := nestedMap(session.CanonicalizeEvent(event).Meta, "caelis", "runtime", "tool", "truncation")
	if truncation["truncated"] != true {
		t.Fatalf("event meta truncation = %#v, want truncation metadata", truncation)
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
	if _, info := tool.TruncateMap(toolPayload.Output, tool.DefaultTruncationPolicy()); info.Truncated {
		t.Fatalf("event.Tool.Output still requires truncation: %#v", info)
	}
	normalized := session.CanonicalizeEvent(event)
	if err := session.ValidateDurableCoreEvent(normalized); err != nil {
		t.Fatalf("ValidateDurableCoreEvent() error = %v", err)
	}
	beforeMessage, ok := session.ModelMessageOf(event)
	if !ok {
		t.Fatal("ModelMessageOf(event) = false, want tool result message")
	}
	afterMessage, ok := session.ModelMessageOf(normalized)
	if !ok {
		t.Fatal("ModelMessageOf(normalized) = false, want tool result message")
	}
	before := canonicalMessagesJSON(t, []model.Message{beforeMessage})
	after := canonicalMessagesJSON(t, []model.Message{afterMessage})
	if before != after {
		t.Fatalf("canonicalized tool result changed model-visible message\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestToolResultMessageCompactsLargeJSONPayloadForModel(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("permission denied\n", tool.DefaultTruncationPolicy().ByteBudget()/2)
	message := toolResultMessage(model.ToolCall{
		ID:   "call-1",
		Name: "RUN_COMMAND",
	}, tool.Result{
		ID:   "call-1",
		Name: "RUN_COMMAND",
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
	if !strings.Contains(resultText, "lines omitted") {
		t.Fatalf("result = %q, want omitted line marker", resultText)
	}
	if payload["_tool_truncation"] != nil || payload["output_meta"] != nil {
		t.Fatalf("payload = %#v, should not expose truncation metadata to model", payload)
	}
}

func TestToolResultContextUsesCanonicalRawOutput(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("x", tool.DefaultTruncationPolicy().ByteBudget()*2)
	output, info := tool.TruncateMap(map[string]any{"result": large}, tool.DefaultTruncationPolicy())
	if !info.Truncated {
		t.Fatal("test fixture did not require truncation")
	}
	message, ok := messageFromDurableEvent(&session.Event{
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID:     "call-1",
			Name:   "RUN_COMMAND",
			Title:  "RUN_COMMAND echo",
			Status: "completed",
			Output: output,
		},
	})
	if !ok {
		t.Fatal("messageFromDurableEvent() ok = false, want true")
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
	if resultText != output["result"] {
		t.Fatalf("result = %q, want canonical output %q", resultText, output["result"])
	}
	if !strings.Contains(resultText, "tokens truncated") {
		t.Fatalf("result = %q, want truncation marker", resultText)
	}
	if payload["_tool_truncation"] != nil || payload["output_meta"] != nil {
		t.Fatalf("payload = %#v, should not expose truncation metadata to model", payload)
	}
}

func TestToolResultContextUsesContentWhenRawOutputAbsent(t *testing.T) {
	t.Parallel()

	message, ok := messageFromDurableEvent(&session.Event{
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID:     "call-1",
			Name:   "RUN_COMMAND",
			Title:  "RUN_COMMAND printf",
			Status: "completed",
			Content: []session.EventToolContent{{
				Type:       "terminal",
				TerminalID: "call-1",
				Text:       "  output\n",
			}},
		},
	})
	if !ok {
		t.Fatal("messageFromDurableEvent() ok = false, want true")
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
		t.Fatalf("payload[result] = %q, want terminal content text", got)
	}
}

func TestToolResultContextUsesCanonicalCommandFailureShape(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("find: cannot delete /tmp/gomod/pkg: permission denied\n", tool.DefaultTruncationPolicy().ByteBudget()/4)
	output, info := tool.TruncateMap(map[string]any{
		"result":    "stderr:\n" + large,
		"exit_code": 1,
		"task_id":   "task-11",
		"state":     "failed",
	}, tool.DefaultTruncationPolicy())
	if !info.Truncated {
		t.Fatal("test fixture did not require truncation")
	}
	message, ok := messageFromDurableEvent(&session.Event{
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID:     "call-1",
			Name:   "RUN_COMMAND",
			Title:  "RUN_COMMAND find /tmp/gomod -delete",
			Status: "failed",
			Output: output,
		},
	})
	if !ok {
		t.Fatal("messageFromDurableEvent() ok = false, want true")
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
	if resultText != output["result"] {
		t.Fatalf("result = %q, want canonical output %q", resultText, output["result"])
	}
	if !strings.Contains(resultText, "lines omitted") {
		t.Fatalf("result = %q, want omitted line marker", resultText)
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
	largeProgress := strings.Repeat("progress line\n", tool.DefaultTruncationPolicy().ByteBudget()/2)
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
				Content: []model.Part{model.NewJSONPart(mustJSON(map[string]any{
					"task_id": "task-1",
					"state":   "running",
					"running": true,
					"result":  largeProgress,
				}))},
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
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "run command")),
			Text:    "run command",
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
	deadline := time.After(10 * time.Second)
	for progress == nil {
		select {
		case err := <-errCh:
			t.Fatalf("Run() error before progress = %v", err)
		case <-progressReported:
		case event := <-eventsCh:
			if event == nil {
				t.Fatal("Run() ended before tool progress")
				continue
			}
			if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Status == "running" {
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
	if progress.Tool == nil {
		t.Fatalf("progress tool = nil, want tool update")
	}
	if got, _ := progress.Tool.Output["task_id"].(string); got != "task-1" {
		t.Fatalf("progress task_id = %q, want task-1", got)
	}
	progressResult, _ := progress.Tool.Output["result"].(string)
	if progressResult == largeProgress {
		t.Fatal("progress result kept original huge output, want canonical truncation")
	}
	if !strings.Contains(progressResult, "lines omitted") {
		t.Fatalf("progress result = %q, want omitted line marker", progressResult)
	}
	if _, info := tool.TruncateMap(progress.Tool.Output, tool.DefaultTruncationPolicy()); info.Truncated {
		t.Fatalf("progress output still requires truncation: %#v", info)
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
		m.last.Tools = model.CloneToolSpecs(req.Tools)
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
		Type: session.EventTypeToolCall,
		Tool: toolEventPayload(call, "pending", maps.Clone(input), nil, nil),
		Meta: mergeEventMeta(toolMeta(name)),
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
		Type: session.EventTypeToolResult,
		Tool: toolEventPayload(call, "completed", maps.Clone(input), maps.Clone(output), toolResultContent(call, input, output, meta, "completed", false)),
		Meta: meta,
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

type invalidThenValidToolModel struct {
	requests []model.Request
}

func (m *invalidThenValidToolModel) Name() string { return "invalid-then-valid-tool" }

func (m *invalidThenValidToolModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
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
						ID:   "call-invalid",
						Name: "ECHO",
						Args: `{"value":"pong"`,
					}}, "All checks pass. Now let me commit."),
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
						ID:   "call-valid",
						Name: "ECHO",
						Args: `{"value":"pong"}`,
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
					Message:      model.NewTextMessage(model.RoleAssistant, "pong"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

type contextStabilityModel struct {
	requests  []model.Request
	toolNames []string
}

func (m *contextStabilityModel) Name() string { return "context-stability" }

func (m *contextStabilityModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
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
					Message: model.MessageFromAssistantParts("I will inspect both values.", "Need both tool results before answering.", []model.ToolCall{{
						ID:               "call-alpha",
						Name:             m.toolName(0),
						Args:             `{"value":"alpha"}`,
						ThoughtSignature: "sig-alpha",
					}, {
						ID:               "call-beta",
						Name:             m.toolName(1),
						Args:             `{"value":"beta"}`,
						ThoughtSignature: "sig-beta",
					}}),
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
					Message:      model.NewTextMessage(model.RoleAssistant, "alpha and beta inspected"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *contextStabilityModel) toolName(index int) string {
	if m != nil && index >= 0 && index < len(m.toolNames) {
		if name := strings.TrimSpace(m.toolNames[index]); name != "" {
			return name
		}
	}
	return "ECHO"
}

func canonicalMessagesJSON(t *testing.T, messages []model.Message) string {
	t.Helper()
	raw, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("json.Marshal(messages) error = %v", err)
	}
	return string(raw)
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

type retryToolModel struct {
	calls atomic.Int32
}

func (m *retryToolModel) Name() string { return "retry-tool-model" }

func (m *retryToolModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	call := m.calls.Add(1)

	return func(yield func(*model.StreamEvent, error) bool) {
		if call >= 3 {
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
			return
		}

		if call == 1 {
			yield(&model.StreamEvent{
				Type: model.StreamEventPartDelta,
				PartDelta: &model.PartDelta{
					Kind:       model.PartKindToolUse,
					InputDelta: `{"value":`,
				},
			}, nil)
			yield(nil, errors.New("providers: sse scanner: unexpected EOF"))
			return
		}

		// call == 2
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
					ID:   "call-retry",
					Name: "ECHO",
					Args: `{"value":"retry-pong"}`,
				}}, ""),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonToolCalls,
			},
		}, nil)
	}
}

func TestChatAgentSpeculativeToolCallRetry(t *testing.T) {
	t.Parallel()

	testModel := &retryToolModel{}
	var toolCallsCount int
	echoTool := tool.NamedTool{
		Def: tool.Definition{
			Name:        "ECHO",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			toolCallsCount++
			return tool.Result{
				ID:   call.ID,
				Name: call.Name,
				Content: []model.Part{
					model.NewJSONPart([]byte(`{"value":"retry-pong"}`)),
				},
			}, nil
		},
	}

	retryingModel := model.WithRetry(testModel, model.RetryConfig{
		MaxRetries: 2,
		BaseDelay:  time.Nanosecond,
		MaxDelay:   time.Nanosecond,
	})

	chatAgent, err := (Factory{SystemPrompt: "Use tools."}).NewAgent(context.Background(), agent.AgentSpec{
		Name:  "chat",
		Model: retryingModel,
		Tools: []tool.Tool{echoTool},
		Request: agent.ModelRequestOptions{
			Stream: boolPtr(true),
		},
	})
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}

	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "sess-retry-tool"}},
		Events: []*session.Event{{
			Type:    session.EventTypeUser,
			Message: ptrMessage(model.NewTextMessage(model.RoleUser, "run")),
			Text:    "run",
		}},
	})

	var gotEvents []*session.Event
	for event, runErr := range chatAgent.Run(ctx) {
		if runErr != nil {
			t.Fatalf("Run() error = %v", runErr)
		}
		if event != nil {
			gotEvents = append(gotEvents, event)
		}
	}

	if gotEvents == nil {
		t.Fatal("gotEvents is nil")
	}
	if got, want := toolCallsCount, 1; got != want {
		t.Fatalf("toolCallsCount = %d, want %d", got, want)
	}
	if got, want := int(testModel.calls.Load()), 3; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}

	// Expected event stream:
	// - partial tool call from the first attempt (not canonical)
	// - attempt_reset (UI only)
	// - successful tool_call from the retry (canonical)
	// - matching tool_result (canonical)
	var resetEventCount int
	var canonicalToolCallsCount int
	for _, ev := range gotEvents {
		if ev.Type == session.EventTypeLifecycle && ev.Lifecycle != nil && ev.Lifecycle.Status == "attempt_reset" {
			resetEventCount++
		}
		if ev.Type == session.EventTypeToolCall && ev.Visibility == session.VisibilityCanonical {
			canonicalToolCallsCount++
		}
	}

	if got, want := resetEventCount, 1; got != want {
		t.Fatalf("resetEventCount = %d, want %d", got, want)
	}
	if got, want := canonicalToolCallsCount, 1; got != want {
		t.Fatalf("canonicalToolCallsCount = %d, want %d", got, want)
	}
}
