package projector

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestEventProjectorNormalizesRuntimeToolStatus(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Protocol: &session.EventProtocol{
			UpdateType: UpdateToolCallInfo,
			ToolCall: &session.ProtocolToolCall{
				ID:     "call-1",
				Name:   "SPAWN",
				Kind:   ToolKindOther,
				Status: "running",
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

func TestEventProjectorRemapsBuiltinTerminalContentToDisplayID(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			UpdateType: UpdateToolCallInfo,
			ToolCall: &session.ProtocolToolCall{
				ID:     "call-1",
				Name:   "RUN_COMMAND",
				Status: "running",
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
	if len(update.Content) != 1 {
		t.Fatalf("content = %#v, want one terminal content item", update.Content)
	}
	if got := update.Content[0].TerminalID; got != "call-1" {
		t.Fatalf("terminal id = %q, want display tool call id", got)
	}
	if update.Content[0].Content != nil {
		t.Fatalf("terminal content body = %#v, want output carried in _meta", update.Content[0].Content)
	}
	output, ok := update.Meta["terminal_output"].(map[string]any)
	if !ok {
		t.Fatalf("meta = %#v, want terminal_output", update.Meta)
	}
	if output["terminal_id"] != "call-1" || output["data"] != "line\n" {
		t.Fatalf("terminal_output = %#v, want display id call-1 and line output", output)
	}
}

func TestEventProjectorSeparatesMultipleTerminalContentItems(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			UpdateType: UpdateToolCallInfo,
			ToolCall: &session.ProtocolToolCall{
				ID:     "call-1",
				Name:   "RUN_COMMAND",
				Status: "completed",
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
	output, ok := update.Meta["terminal_output"].(map[string]any)
	if !ok {
		t.Fatalf("meta = %#v, want terminal_output", update.Meta)
	}
	if got := output["data"]; got != "caelis\ncodex\ndemo" {
		t.Fatalf("terminal_output data = %#v, want separated terminal records", got)
	}
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
				Meta: map[string]any{
					"terminal_info": map[string]any{
						"terminal_id": "call-1",
						"tool":        "RUN_COMMAND",
					},
				},
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
	if len(call.Content) != 1 || call.Content[0].Type != "terminal" || call.Content[0].TerminalID != "call-1" || call.Content[0].Content != nil {
		t.Fatalf("content = %#v, want standard terminal marker without body", call.Content)
	}
	output, ok := call.Meta["terminal_output"].(map[string]any)
	if !ok || output["data"] != "ignored body\n" {
		t.Fatalf("meta = %#v, want terminal body moved to _meta.terminal_output", call.Meta)
	}
}

func TestEventProjectorPreservesStandardDiffContent(t *testing.T) {
	oldText := "old line\n"
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Protocol: &session.EventProtocol{
			UpdateType: UpdateToolCallInfo,
			ToolCall: &session.ProtocolToolCall{
				ID:     "call-1",
				Name:   "PATCH",
				Status: "completed",
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
					UpdateType: UpdateUserMessage,
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
					UpdateType: UpdateAgentMessage,
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
					UpdateType: UpdateAgentThought,
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
	if len(firstCall.Content) != 1 || firstCall.Content[0].Type != "terminal" || firstCall.Content[0].TerminalID != "call-1" {
		t.Fatalf("first call content = %#v, want display terminal marker", firstCall.Content)
	}
	secondCall, ok := updates[3].(ToolCall)
	if !ok {
		t.Fatalf("updates[3] = %T, want ToolCall", updates[3])
	}
	if secondCall.ToolCallID != "call-2" || secondCall.Title != "ECHO" {
		t.Fatalf("second call = %#v, want ECHO call", secondCall)
	}
}

func TestEventProjectorProjectsSpawnAsExecuteWithTerminalContent(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Protocol: &session.EventProtocol{
			UpdateType: UpdateToolCallInfo,
			ToolCall: &session.ProtocolToolCall{
				ID:     "call-1",
				Name:   "SPAWN",
				Status: "running",
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
	if len(update.Content) != 1 || update.Content[0].Type != "terminal" || update.Content[0].TerminalID != "call-1" {
		t.Fatalf("content = %#v, want terminal content remapped to display id call-1", update.Content)
	}
	if update.Content[0].Content != nil {
		t.Fatalf("terminal content body = %#v, want empty standard terminal marker", update.Content[0].Content)
	}
}

func TestEventProjectorProjectsRunCommandDisplayTerminalMetadata(t *testing.T) {
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Protocol: &session.EventProtocol{
			UpdateType: UpdateToolCall,
			ToolCall: &session.ProtocolToolCall{
				ID:     "call-1",
				Name:   "RUN_COMMAND",
				Status: "pending",
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
	if len(call.Content) != 1 || call.Content[0].Type != "terminal" || call.Content[0].TerminalID != "call-1" {
		t.Fatalf("content = %#v, want display terminal content for call-1", call.Content)
	}
	if call.Content[0].Content != nil {
		t.Fatalf("terminal content body = %#v, want empty standard terminal marker", call.Content[0].Content)
	}
	info, ok := call.Meta["terminal_info"].(map[string]any)
	if !ok {
		t.Fatalf("meta = %#v, want terminal_info", call.Meta)
	}
	if info["terminal_id"] != "call-1" || info["cwd"] != "/tmp/work" || info["tool"] != "RUN_COMMAND" {
		t.Fatalf("terminal_info = %#v, want terminal_id call-1 cwd /tmp/work tool RUN_COMMAND", info)
	}
}

func TestEventProjectorPreservesReasoningBoundaryWhitespace(t *testing.T) {
	message := model.NewReasoningMessage(model.RoleAssistant, "think ", model.ReasoningVisibilityVisible)
	updates, err := (EventProjector{}).ProjectEvent(&session.Event{
		SessionID: "session-1",
		Type:      session.EventTypeAssistant,
		Message:   &message,
		Text:      "think ",
		Protocol: &session.EventProtocol{
			UpdateType: UpdateAgentThought,
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
