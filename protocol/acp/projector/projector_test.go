package projector

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestEventProjectorNormalizesRuntimeToolStatus(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Kind:          ToolKindOther,
				Status:        "running",
				RawInput: map[string]any{
					"prompt": "child work",
				},
				RawOutput: map[string]any{
					"task_id": "task-1",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", updates[0])
	}
	if update.Status == nil || *update.Status != ToolStatusInProgress {
		t.Fatalf("status = %v, want %q", update.Status, ToolStatusInProgress)
	}
}

func TestProjectPermissionRequestUsesDurablePermissionAfterRoundTrip(t *testing.T) {
	t.Parallel()

	source := &session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeLifecycle,
		Meta: map[string]any{
			"caelis": map[string]any{
				"approval": map[string]any{"mode": "manual"},
			},
		},
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodRequestPermission,
			Permission: &session.ProtocolApproval{
				ToolCall: session.ProtocolToolCall{
					ID:       "call-rm",
					Name:     "RUN_COMMAND",
					Kind:     ToolKindExecute,
					Title:    "RUN_COMMAND rm",
					Status:   "waiting_approval",
					RawInput: map[string]any{"command": "rm -rf tmp"},
				},
				Options: []session.ProtocolApprovalOption{
					{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
					{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
				},
			},
		},
	}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("json.Marshal(event) error = %v", err)
	}
	var decoded session.Event
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(event) error = %v", err)
	}
	if decoded.Protocol == nil || session.ProtocolPermissionOf(&decoded) == nil {
		t.Fatalf("decoded protocol = %#v, want durable permission", decoded.Protocol)
	}

	req, ok, err := (EventProjector{}).ProjectPermissionRequest(&decoded)
	if err != nil {
		t.Fatalf("ProjectPermissionRequest() error = %v", err)
	}
	if !ok || req == nil {
		t.Fatal("ProjectPermissionRequest() did not project durable permission")
	}
	if req.SessionID != "session-1" || req.ToolCall.ToolCallID != "call-rm" {
		t.Fatalf("permission request = %#v, want session/call ids", req)
	}
	if len(req.Options) != 2 || req.Options[0].OptionID != "allow_once" {
		t.Fatalf("permission options = %#v", req.Options)
	}
	caelis, _ := req.Meta["caelis"].(map[string]any)
	approvalMeta, _ := caelis["approval"].(map[string]any)
	if approvalMeta["mode"] != "manual" {
		t.Fatalf("permission meta = %#v, want approval mode", req.Meta)
	}
}

func TestEventProjectorRemapsBuiltinTerminalContentToDisplayID(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Kind:          "RUN_COMMAND",
				Status:        "running",
				Content: []session.ProtocolToolCallContent{{
					Type:       "terminal",
					TerminalID: "runtime-terminal-1",
					Content:    session.ProtocolTextContent("line\n"),
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", updates[0])
	}
	assertTerminalAnchor(t, update.Content, "call-1")
	assertTerminalInfo(t, update.Meta, "call-1")
	assertTerminalOutput(t, update.Meta, "call-1", "line\n")
}

func assertTerminalAnchor(t *testing.T, content []ToolCallContent, terminalID string) {
	t.Helper()
	if len(content) != 1 {
		t.Fatalf("content = %#v, want one terminal anchor", content)
	}
	item := content[0]
	if item.Type != "terminal" || item.TerminalID != terminalID {
		t.Fatalf("content = %#v, want terminal anchor %q", content, terminalID)
	}
	if text := terminalTextContent(item.Content); text != "" {
		t.Fatalf("terminal anchor content text = %q, want empty", text)
	}
}

func assertTerminalInfo(t *testing.T, meta map[string]any, terminalID string) {
	t.Helper()
	info, ok := metautil.TerminalInfo(meta)
	if !ok || info.TerminalID != terminalID {
		t.Fatalf("terminal_info = %#v, want terminal id %q", meta, terminalID)
	}
}

func assertTerminalOutput(t *testing.T, meta map[string]any, terminalID string, text string) {
	t.Helper()
	output, ok := metautil.TerminalOutput(meta)
	if !ok || output.TerminalID != terminalID || output.Data != text {
		t.Fatalf("terminal_output = %#v, want terminal id %q text %q", meta, terminalID, text)
	}
}

func assertTerminalExit(t *testing.T, meta map[string]any, terminalID string) {
	t.Helper()
	exit, ok := metautil.TerminalExit(meta)
	if !ok || exit.TerminalID != terminalID {
		t.Fatalf("terminal_exit = %#v, want terminal id %q", meta, terminalID)
	}
}

func TestEventProjectorConcatenatesMultipleTerminalContentItems(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Kind:          "RUN_COMMAND",
				Status:        "completed",
				Content: []session.ProtocolToolCallContent{
					{Type: "terminal", TerminalID: "call-1", Content: session.ProtocolTextContent("caelis")},
					{Type: "terminal", TerminalID: "call-1", Content: session.ProtocolTextContent("codex")},
					{Type: "terminal", TerminalID: "call-1", Content: session.ProtocolTextContent("demo")},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", updates[0])
	}
	assertTerminalAnchor(t, update.Content, "call-1")
	assertTerminalInfo(t, update.Meta, "call-1")
	assertTerminalOutput(t, update.Meta, "call-1", "caeliscodexdemo")
	assertTerminalExit(t, update.Meta, "call-1")
}

func TestEventProjectorUsesDurableProtocolUpdateForTerminalToolCall(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: UpdateToolCall,
				ToolCallID:    "call-1",
				Title:         "RUN_COMMAND date",
				Kind:          ToolKindExecute,
				Status:        ToolStatusPending,
				RawInput:      map[string]any{"command": "date"},
				Content: []session.ProtocolToolCallContent{{
					Type:       "terminal",
					TerminalID: "call-1",
					Content:    session.ProtocolTextContent("ignored body\n"),
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want protocol tool_call only", len(updates))
	}
	call, ok := updates[0].(ToolCall)
	if !ok {
		t.Fatalf("update = %T, want ToolCall", updates[0])
	}
	if call.ToolCallID != "call-1" || call.Kind != ToolKindExecute || call.Title != "RUN_COMMAND date" {
		t.Fatalf("tool call = %#v, want durable protocol identity", call)
	}
	assertTerminalAnchor(t, call.Content, "call-1")
	assertTerminalInfo(t, call.Meta, "call-1")
	assertTerminalOutput(t, call.Meta, "call-1", "ignored body\n")
}

func TestEventProjectorProjectsCanonicalMessages(t *testing.T) {
	userMessage := model.NewTextMessage(model.RoleUser, "hello")
	userEvent := session.CanonicalizeEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeUser,
		Message:   &userMessage,
	})
	if userEvent.Message == nil {
		t.Fatalf("canonical user event = %#v, want durable message", userEvent)
	}
	userUpdates, err := (EventProjector{}).ProjectEvent(userEvent)
	if err != nil {
		t.Fatalf("ProjectEvent(user) error = %v", err)
	}
	if len(userUpdates) != 1 {
		t.Fatalf("ProjectEvent(user) produced %d updates, want 1", len(userUpdates))
	}
	userChunk, ok := userUpdates[0].(ContentChunk)
	if !ok || userChunk.SessionUpdate != UpdateUserMessage {
		t.Fatalf("user update = %#v, want user message chunk", userUpdates[0])
	}
	if content, ok := userChunk.Content.(TextContent); !ok || content.Text != "hello" {
		t.Fatalf("user content = %#v, want hello text", userChunk.Content)
	}

	assistantMessage := model.MessageFromAssistantParts("done", "thinking", nil)
	assistantEvent := session.CanonicalizeEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeAssistant,
		Message:   &assistantMessage,
	})
	if assistantEvent.Message == nil {
		t.Fatalf("canonical assistant event = %#v, want durable message", assistantEvent)
	}
	assistantUpdates, err := (EventProjector{}).ProjectEvent(assistantEvent)
	if err != nil {
		t.Fatalf("ProjectEvent(assistant) error = %v", err)
	}
	if len(assistantUpdates) != 2 {
		t.Fatalf("ProjectEvent(assistant) produced %d updates, want thought + message: %#v", len(assistantUpdates), assistantUpdates)
	}
	thought, ok := assistantUpdates[0].(ContentChunk)
	if !ok || thought.SessionUpdate != UpdateAgentThought {
		t.Fatalf("assistant updates[0] = %#v, want thought chunk", assistantUpdates[0])
	}
	if content, ok := thought.Content.(TextContent); !ok || content.Text != "thinking" {
		t.Fatalf("thought content = %#v, want thinking", thought.Content)
	}
	messageChunk, ok := assistantUpdates[1].(ContentChunk)
	if !ok || messageChunk.SessionUpdate != UpdateAgentMessage {
		t.Fatalf("assistant updates[1] = %#v, want message chunk", assistantUpdates[1])
	}
	if content, ok := messageChunk.Content.(TextContent); !ok || content.Text != "done" {
		t.Fatalf("message content = %#v, want done", messageChunk.Content)
	}

	reasoningOnly := model.NewReasoningMessage(model.RoleAssistant, "only thought", model.ReasoningVisibilityVisible)
	reasoningEvent := session.CanonicalizeEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeAssistant,
		Message:   &reasoningOnly,
	})
	reasoningUpdates, err := (EventProjector{}).ProjectEvent(reasoningEvent)
	if err != nil {
		t.Fatalf("ProjectEvent(reasoning-only) error = %v", err)
	}
	if len(reasoningUpdates) != 1 {
		t.Fatalf("ProjectEvent(reasoning-only) produced %d updates, want thought only: %#v", len(reasoningUpdates), reasoningUpdates)
	}
	if chunk, ok := reasoningUpdates[0].(ContentChunk); !ok || chunk.SessionUpdate != UpdateAgentThought {
		t.Fatalf("reasoning-only update = %#v, want thought chunk", reasoningUpdates[0])
	}

	systemMessage := model.NewTextMessage(model.RoleSystem, "system prompt")
	systemEvent := session.CanonicalizeEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeSystem,
		Message:   &systemMessage,
	})
	systemUpdates, err := (EventProjector{}).ProjectEvent(systemEvent)
	if err != nil {
		t.Fatalf("ProjectEvent(system) error = %v", err)
	}
	if len(systemUpdates) != 0 {
		t.Fatalf("ProjectEvent(system) produced %#v, want no ACP session/update", systemUpdates)
	}
}

func TestEventProjectorSplitsProtocolAssistantSnapshotReasoning(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeAssistant,
		Text:      "partial answer",
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				Content: map[string]any{
					"type":          "assistant_snapshot",
					"text":          "partial answer",
					"reasoningText": "partial thought",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 2 {
		t.Fatalf("ProjectEvent() produced %d updates, want thought + message: %#v", len(updates), updates)
	}
	thought, ok := updates[0].(ContentChunk)
	if !ok || thought.SessionUpdate != UpdateAgentThought {
		t.Fatalf("updates[0] = %#v, want thought chunk", updates[0])
	}
	if content, ok := thought.Content.(TextContent); !ok || content.Text != "partial thought" {
		t.Fatalf("thought content = %#v, want partial thought", thought.Content)
	}
	message, ok := updates[1].(ContentChunk)
	if !ok || message.SessionUpdate != UpdateAgentMessage {
		t.Fatalf("updates[1] = %#v, want message chunk", updates[1])
	}
	if content, ok := message.Content.(TextContent); !ok || content.Text != "partial answer" {
		t.Fatalf("message content = %#v, want partial answer", message.Content)
	}
}

func TestEventProjectorProjectsDurableContentChunkMessageIDAndMeta(t *testing.T) {
	t.Parallel()

	message := model.NewTextMessage(model.RoleAssistant, "hello")
	event := session.CanonicalizeEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeAssistant,
		Message:   &message,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				MessageID:     "msg-1",
				Meta:          map[string]any{"vendor": map[string]any{"trace": "abc"}},
			},
		},
	})
	updates, err := (EventProjector{}).ProjectEvent(event)
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1: %#v", len(updates), updates)
	}
	chunk, ok := updates[0].(ContentChunk)
	if !ok {
		t.Fatalf("update = %T, want ContentChunk", updates[0])
	}
	if chunk.MessageID != "msg-1" {
		t.Fatalf("MessageID = %q, want msg-1", chunk.MessageID)
	}
	vendor, _ := chunk.Meta["vendor"].(map[string]any)
	if vendor["trace"] != "abc" {
		t.Fatalf("Meta = %#v, want vendor trace", chunk.Meta)
	}
}

func TestEventProjectorDoesNotAttachMismatchedProtocolMetaToAssistantChunk(t *testing.T) {
	t.Parallel()

	message := model.NewTextMessage(model.RoleAssistant, "hello")
	event := session.CanonicalizeEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Message:   &message,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: UpdateToolCall,
				ToolCallID:    "call-1",
				Kind:          ToolKindExecute,
				Title:         "RUN_COMMAND date",
				Meta:          map[string]any{"vendor": map[string]any{"trace": "tool-meta"}},
			},
		},
	})
	updates, err := (EventProjector{}).ProjectEvent(event)
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 2 {
		t.Fatalf("ProjectEvent() produced %d updates, want assistant chunk + tool call: %#v", len(updates), updates)
	}
	chunk, ok := updates[0].(ContentChunk)
	if !ok {
		t.Fatalf("first update = %T, want ContentChunk", updates[0])
	}
	if len(chunk.Meta) != 0 || chunk.MessageID != "" {
		t.Fatalf("assistant chunk = %#v, want no mismatched tool metadata", chunk)
	}
	call, ok := updates[1].(ToolCall)
	if !ok {
		t.Fatalf("second update = %T, want ToolCall", updates[1])
	}
	vendor, _ := call.Meta["vendor"].(map[string]any)
	if vendor["trace"] != "tool-meta" {
		t.Fatalf("tool call meta = %#v, want vendor trace", call.Meta)
	}
}

func TestEventProjectorProjectsCompactCheckpoint(t *testing.T) {
	compactMessage := model.NewTextMessage(model.RoleUser, "CONTEXT CHECKPOINT\nObjective: continue")
	event := session.CanonicalizeEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeCompact,
		Message:   &compactMessage,
	})
	updates, err := (EventProjector{}).ProjectEvent(event)
	if err != nil {
		t.Fatalf("ProjectEvent(compact) error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent(compact) produced %d updates, want 1", len(updates))
	}
	chunk, ok := updates[0].(ContentChunk)
	if !ok || chunk.SessionUpdate != UpdateCompact {
		t.Fatalf("compact update = %#v, want compact content chunk", updates[0])
	}
	if content, ok := chunk.Content.(TextContent); !ok || !strings.Contains(content.Text, "CONTEXT CHECKPOINT") {
		t.Fatalf("compact content = %#v, want checkpoint text", chunk.Content)
	}
}

func TestEventProjectorProjectsCanonicalToolPayloads(t *testing.T) {
	assistantMessage := model.MessageFromAssistantParts("I will run it.", "Need output first.", []model.ToolCall{{
		ID:   "call-1",
		Name: "RUN_COMMAND",
		Args: `{"command":"date","workdir":"/tmp/work"}`,
	}})
	callEvent := session.CanonicalizeEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Message:   &assistantMessage,
		Tool: &session.EventTool{
			ID:     "call-1",
			Name:   "RUN_COMMAND",
			Kind:   ToolKindExecute,
			Title:  "RUN_COMMAND date",
			Status: ToolStatusPending,
			Input:  map[string]any{"command": "date", "workdir": "/tmp/work"},
			Content: []session.EventToolContent{{
				Type:       "terminal",
				TerminalID: "call-1",
			}},
		},
	})
	if callEvent.Tool == nil || callEvent.Message == nil {
		t.Fatalf("canonical tool call event = %#v, want durable message and tool", callEvent)
	}
	callUpdates, err := (EventProjector{}).ProjectEvent(callEvent)
	if err != nil {
		t.Fatalf("ProjectEvent(tool call) error = %v", err)
	}
	if len(callUpdates) != 3 {
		t.Fatalf("ProjectEvent(tool call) produced %d updates, want thought + message + tool_call: %#v", len(callUpdates), callUpdates)
	}
	call, ok := callUpdates[2].(ToolCall)
	if !ok {
		t.Fatalf("tool call update = %T, want ToolCall", callUpdates[2])
	}
	if call.ToolCallID != "call-1" || call.Kind != ToolKindExecute || call.Title != "RUN_COMMAND date" {
		t.Fatalf("tool call = %#v, want RUN_COMMAND execute call", call)
	}
	assertTerminalAnchor(t, call.Content, "call-1")
	assertTerminalInfo(t, call.Meta, "call-1")

	toolMessage := model.MessageFromToolResponse(&model.ToolResponse{
		ID:     "call-1",
		Name:   "RUN_COMMAND",
		Result: map[string]any{"stdout": "ok\n", "exit_code": 0},
	})
	resultEvent := session.CanonicalizeEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID:     "call-1",
			Name:   "RUN_COMMAND",
			Kind:   ToolKindExecute,
			Title:  "RUN_COMMAND echo ok",
			Status: ToolStatusCompleted,
			Input:  map[string]any{"command": "echo ok"},
			Output: map[string]any{"stdout": "ok\n", "exit_code": 0},
			Content: []session.EventToolContent{{
				Type:       "terminal",
				TerminalID: "call-1",
				Text:       "ok\n",
			}},
		},
		Message: &toolMessage,
	})
	if resultEvent.Tool == nil || resultEvent.Message == nil {
		t.Fatalf("canonical tool result event = %#v, want durable message and tool", resultEvent)
	}
	resultUpdates, err := (EventProjector{}).ProjectEvent(resultEvent)
	if err != nil {
		t.Fatalf("ProjectEvent(tool result) error = %v", err)
	}
	if len(resultUpdates) != 1 {
		t.Fatalf("ProjectEvent(tool result) produced %d updates, want 1", len(resultUpdates))
	}
	result, ok := resultUpdates[0].(ToolCallUpdate)
	if !ok {
		t.Fatalf("tool result update = %T, want ToolCallUpdate", resultUpdates[0])
	}
	if result.ToolCallID != "call-1" || result.Status == nil || *result.Status != ToolStatusCompleted {
		t.Fatalf("tool result = %#v, want completed call-1", result)
	}
	if output, ok := result.RawOutput.(map[string]any); !ok || output["stdout"] != "ok\n" {
		t.Fatalf("raw output = %#v, want stdout", result.RawOutput)
	}
	assertTerminalAnchor(t, result.Content, "call-1")
	assertTerminalInfo(t, result.Meta, "call-1")
	assertTerminalOutput(t, result.Meta, "call-1", "ok\n")
	assertTerminalExit(t, result.Meta, "call-1")
}

func TestEventProjectorProjectsPlanPayload(t *testing.T) {
	event := session.CanonicalizeEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypePlan,
		PlanPayload: &session.EventPlanPayload{Entries: []session.EventPlanEntry{
			{
				Content:  "inspect",
				Status:   "completed",
				Priority: "high",
			}, {
				Content: "fix",
				Status:  "pending",
			},
		}},
	})
	if event.PlanPayload == nil {
		t.Fatalf("canonical plan event = %#v, want plan payload", event)
	}
	updates, err := (EventProjector{}).ProjectEvent(event)
	if err != nil {
		t.Fatalf("ProjectEvent(plan) error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent(plan) produced %d updates, want 1", len(updates))
	}
	plan, ok := updates[0].(PlanUpdate)
	if !ok {
		t.Fatalf("update = %T, want PlanUpdate", updates[0])
	}
	if len(plan.Entries) != 2 {
		t.Fatalf("plan entries = %#v, want 2", plan.Entries)
	}
	if plan.Entries[0].Content != "inspect" || plan.Entries[0].Priority != "high" {
		t.Fatalf("first plan entry = %#v, want inspect/high", plan.Entries[0])
	}
	if plan.Entries[1].Content != "fix" || plan.Entries[1].Priority != "medium" {
		t.Fatalf("second plan entry = %#v, want fix/medium default", plan.Entries[1])
	}
}

func TestEventProjectorPreservesExplicitEmptyPlanUpdate(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(session.CanonicalizeEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypePlan,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: UpdatePlan,
				Entries:       []session.ProtocolPlanEntry{},
			},
		},
	}))
	if err != nil {
		t.Fatalf("ProjectEvent(empty plan) error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent(empty plan) produced %d updates, want 1: %#v", len(updates), updates)
	}
	plan, ok := updates[0].(PlanUpdate)
	if !ok {
		t.Fatalf("update = %T, want PlanUpdate", updates[0])
	}
	if len(plan.Entries) != 0 {
		t.Fatalf("plan entries = %#v, want empty replacement", plan.Entries)
	}
}

func TestEventProjectorPreservesPartialProtocolToolUpdate(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Status:        ToolStatusCompleted,
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", updates[0])
	}
	if update.Title != nil {
		t.Fatalf("title = %q, want nil for partial status update", *update.Title)
	}
	if update.Kind != nil {
		t.Fatalf("kind = %q, want nil for partial status update", *update.Kind)
	}
	if update.Status == nil || *update.Status != ToolStatusCompleted {
		t.Fatalf("status = %v, want %q", update.Status, ToolStatusCompleted)
	}
}

func TestEventProjectorNotificationsSerializePartialToolUpdate(t *testing.T) {
	notifications, err := (EventProjector{}).ProjectNotifications(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Status:        ToolStatusCompleted,
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectNotifications() error = %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("ProjectNotifications() produced %d notifications, want 1", len(notifications))
	}
	raw, err := json.Marshal(notifications[0])
	if err != nil {
		t.Fatalf("json.Marshal(notification) error = %v", err)
	}
	text := string(raw)
	for _, field := range []string{`"title"`, `"kind"`, `"rawInput"`, `"rawOutput"`, `"content"`, `"locations"`} {
		if strings.Contains(text, field) {
			t.Fatalf("serialized partial update = %s, unexpectedly contains %s", text, field)
		}
	}
	if !strings.Contains(text, `"sessionUpdate":"tool_call_update"`) ||
		!strings.Contains(text, `"toolCallId":"call-1"`) ||
		!strings.Contains(text, `"status":"completed"`) {
		t.Fatalf("serialized partial update = %s, want id and changed status only", text)
	}
}

func TestEventProjectorPreservesStandardDiffContent(t *testing.T) {
	oldText := "old line\n"
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Kind:          "PATCH",
				Status:        "completed",
				Content: []session.ProtocolToolCallContent{{
					Type:    "diff",
					Path:    "/workspace/demo.txt",
					OldText: &oldText,
					NewText: "new line\n",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", updates[0])
	}
	if len(update.Content) != 1 {
		t.Fatalf("content = %#v, want one diff content item", update.Content)
	}
	diff := update.Content[0]
	if diff.Type != "diff" || diff.Path != "/workspace/demo.txt" || diff.OldText == nil || *diff.OldText != oldText || diff.NewText != "new line\n" {
		t.Fatalf("diff content = %#v, want standard path/oldText/newText", diff)
	}
}

func TestEventProjectorReplaysDurableProtocolTextContent(t *testing.T) {
	cases := []struct {
		name       string
		event      *session.Event
		updateType string
		want       string
	}{
		{
			name: "user message",
			event: &session.Event{
				SessionID: "session-1",
				Type:      session.EventTypeUser,
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: UpdateUserMessage,
						Content:       session.ProtocolTextContent("stored user"),
					},
				},
			},
			updateType: UpdateUserMessage,
			want:       "stored user",
		},
		{
			name: "assistant message",
			event: &session.Event{
				SessionID: "session-1",
				Type:      session.EventTypeAssistant,
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: UpdateAgentMessage,
						Content:       session.ProtocolTextContent("stored assistant"),
					},
				},
			},
			updateType: UpdateAgentMessage,
			want:       "stored assistant",
		},
		{
			name: "legacy assistant thought snapshot",
			event: &session.Event{
				SessionID: "session-1",
				Type:      session.EventTypeAssistant,
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: UpdateAgentThought,
						Content: map[string]any{
							"type":          "assistant_snapshot",
							"reasoningText": "stored thought",
						},
					},
				},
			},
			updateType: UpdateAgentThought,
			want:       "stored thought",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			notifications, err := (EventProjector{}).ProjectNotifications(tt.event)
			if err != nil {
				t.Fatalf("ProjectNotifications() error = %v", err)
			}
			if len(notifications) != 1 {
				t.Fatalf("ProjectNotifications() produced %d notifications, want 1", len(notifications))
			}
			if notifications[0].SessionID != "session-1" {
				t.Fatalf("SessionID = %q, want session-1", notifications[0].SessionID)
			}
			chunk, ok := notifications[0].Update.(ContentChunk)
			if !ok {
				t.Fatalf("update = %T, want ContentChunk", notifications[0].Update)
			}
			if chunk.SessionUpdate != tt.updateType {
				t.Fatalf("SessionUpdate = %q, want %q", chunk.SessionUpdate, tt.updateType)
			}
			content, ok := chunk.Content.(TextContent)
			if !ok {
				t.Fatalf("Content = %T, want TextContent", chunk.Content)
			}
			if content.Text != tt.want {
				t.Fatalf("text = %q, want %q", content.Text, tt.want)
			}
		})
	}
}

func TestEventProjectorProjectsCanonicalAssistantMessageWithToolCalls(t *testing.T) {
	message := model.MessageFromAssistantParts("I will run the command.", "Need shell output first.", []model.ToolCall{{
		ID:   "call-1",
		Name: "RUN_COMMAND",
		Args: `{"command":"date","workdir":"/tmp/work"}`,
	}, {
		ID:   "call-2",
		Name: "ECHO",
		Args: `{"value":"done"}`,
	}})
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Message:   &message,
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if got, want := len(updates), 4; got != want {
		t.Fatalf("ProjectEvent() produced %d updates, want %d: %#v", got, want, updates)
	}
	thought, ok := updates[0].(ContentChunk)
	if !ok || thought.SessionUpdate != UpdateAgentThought {
		t.Fatalf("updates[0] = %#v, want agent thought chunk", updates[0])
	}
	messageChunk, ok := updates[1].(ContentChunk)
	if !ok || messageChunk.SessionUpdate != UpdateAgentMessage {
		t.Fatalf("updates[1] = %#v, want agent message chunk", updates[1])
	}
	firstCall, ok := updates[2].(ToolCall)
	if !ok {
		t.Fatalf("updates[2] = %T, want ToolCall", updates[2])
	}
	if firstCall.ToolCallID != "call-1" || firstCall.Kind != ToolKindExecute {
		t.Fatalf("first call = %#v, want RUN_COMMAND execute call", firstCall)
	}
	assertTerminalAnchor(t, firstCall.Content, "call-1")
	assertTerminalInfo(t, firstCall.Meta, "call-1")
	secondCall, ok := updates[3].(ToolCall)
	if !ok {
		t.Fatalf("updates[3] = %T, want ToolCall", updates[3])
	}
	if secondCall.ToolCallID != "call-2" || secondCall.Title != "ECHO" {
		t.Fatalf("second call = %#v, want ECHO call", secondCall)
	}
}

func TestEventProjectorProjectsSpawnAsExecuteWithTerminalMeta(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Kind:          ToolKindExecute,
				Status:        "running",
				RawInput: map[string]any{
					"agent":  "codex",
					"prompt": "child work",
				},
				RawOutput: map[string]any{"task_id": "task-1"},
				Content:   []session.ProtocolToolCallContent{{Type: "terminal", TerminalID: "terminal-1"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", updates[0])
	}
	if update.Kind == nil || *update.Kind != ToolKindExecute {
		t.Fatalf("kind = %v, want %q", update.Kind, ToolKindExecute)
	}
	if update.Title == nil || *update.Title != "SPAWN codex: child work" {
		t.Fatalf("title = %v, want SPAWN codex: child work", update.Title)
	}
	assertTerminalAnchor(t, update.Content, "call-1")
	assertTerminalInfo(t, update.Meta, "call-1")
}

func TestEventProjectorAddsTerminalInfoToRunningToolUpdate(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Kind:          ToolKindExecute,
				Status:        "running",
				RawInput:      map[string]any{"command": "echo hi"},
				RawOutput:     map[string]any{"state": "running", "task_id": "task-1"},
			},
		},
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"task": map[string]any{
						"task_id":     "task-1",
						"terminal_id": "runtime-terminal-1",
						"running":     true,
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %T, want ToolCallUpdate", updates[0])
	}
	if got := stringPtrValue(update.Status); got != ToolStatusInProgress {
		t.Fatalf("status = %q, want in_progress", got)
	}
	assertTerminalAnchor(t, update.Content, "call-1")
	assertTerminalInfo(t, update.Meta, "call-1")
}

func TestEventProjectorProjectsRunCommandDisplayTerminalMetadata(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: UpdateToolCall,
				ToolCallID:    "call-1",
				Kind:          ToolKindExecute,
				Status:        "pending",
				RawInput: map[string]any{
					"command": "echo hi",
					"workdir": "/tmp/work",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want tool_call only", len(updates))
	}
	call, ok := updates[0].(ToolCall)
	if !ok {
		t.Fatalf("updates[0] = %T, want ToolCall", updates[0])
	}
	if call.Kind != ToolKindExecute {
		t.Fatalf("kind = %q, want %q", call.Kind, ToolKindExecute)
	}
	assertTerminalAnchor(t, call.Content, "call-1")
	assertTerminalInfo(t, call.Meta, "call-1")
}

func TestEventProjectorPreservesReasoningBoundaryWhitespace(t *testing.T) {
	message := model.NewReasoningMessage(model.RoleAssistant, "think ", model.ReasoningVisibilityVisible)
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeAssistant,
		Message:   &message,
		Text:      "think ",
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{SessionUpdate: UpdateAgentThought},
		},
	})
	if err != nil {
		t.Fatalf("ProjectEvent() error = %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("ProjectEvent() produced %d updates, want 1", len(updates))
	}
	update, ok := updates[0].(ContentChunk)
	if !ok {
		t.Fatalf("update = %T, want ContentChunk", updates[0])
	}
	content, ok := update.Content.(TextContent)
	if !ok {
		t.Fatalf("update.Content = %T, want TextContent", update.Content)
	}
	if content.Text != "think " {
		t.Fatalf("reasoning text = %q, want boundary whitespace preserved", content.Text)
	}
}
