package projector

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestEventProjectorProjectsEventToolSemanticNameInStandardNotifications(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		event    *session.Event
		wantName string
	}{
		{
			name: "task call",
			event: &session.Event{
				SessionID: "session-1",
				Type:      session.EventTypeToolCall,
				Meta:      map[string]any{"event_only": "do not project"},
				Tool: &session.EventTool{
					ID:     "call-task",
					Name:   "Task",
					Kind:   ToolKindExecute,
					Title:  "Task wait command-1",
					Status: ToolStatusPending,
					Input:  map[string]any{"action": "wait", "task_id": "command-1"},
				},
			},
			wantName: "Task",
		},
		{
			name: "spawn result",
			event: &session.Event{
				SessionID: "session-1",
				Type:      session.EventTypeToolResult,
				Meta:      map[string]any{"event_only": "do not project"},
				Tool: &session.EventTool{
					ID:     "call-spawn",
					Name:   "Spawn",
					Kind:   ToolKindExecute,
					Title:  "Spawn orbit: inspect",
					Status: ToolStatusCompleted,
					Input:  map[string]any{"agent": "orbit", "prompt": "inspect"},
					Output: map[string]any{"state": "completed"},
				},
			},
			wantName: "Spawn",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			notifications, err := (EventProjector{}).ProjectNotifications(tt.event)
			if err != nil {
				t.Fatalf("ProjectNotifications() error = %v", err)
			}
			if len(notifications) != 1 {
				t.Fatalf("ProjectNotifications() produced %d notifications, want 1", len(notifications))
			}
			meta := toolUpdateMeta(t, notifications[0].Update)
			assertRuntimeToolName(t, meta, tt.wantName)
			if _, leaked := meta["event_only"]; leaked {
				t.Fatalf("tool update meta = %#v, copied unrelated Event meta", meta)
			}
		})
	}
}

func TestEventProjectorProjectsProtocolToolSemanticNameWithoutEventMetaLeak(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		updateType string
		toolName   string
		title      string
	}{
		{
			name:       "task call",
			updateType: UpdateToolCall,
			toolName:   "Task",
			title:      "Task wait command-1",
		},
		{
			name:       "spawn update",
			updateType: UpdateToolCallInfo,
			toolName:   "Spawn",
			title:      "Spawn orbit: inspect",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			event := &session.Event{
				SessionID: "session-1",
				Type:      session.EventTypeToolCall,
				Meta: metautil.WithRuntimeSection(
					map[string]any{"event_only": "do not project"},
					metautil.RuntimeTool,
					map[string]any{metautil.RuntimeToolName: tt.toolName},
				),
				Protocol: &session.EventProtocol{
					Update: &session.ProtocolUpdate{
						SessionUpdate: tt.updateType,
						ToolCallID:    "call-1",
						Title:         tt.title,
						Kind:          ToolKindExecute,
						Status:        ToolStatusPending,
						Meta:          map[string]any{"vendor": map[string]any{"trace": "keep"}},
					},
				},
			}
			if tt.updateType == UpdateToolCallInfo {
				event.Type = session.EventTypeToolResult
				event.Protocol.Update.Status = ToolStatusCompleted
			}

			notifications, err := (EventProjector{}).ProjectNotifications(event)
			if err != nil {
				t.Fatalf("ProjectNotifications() error = %v", err)
			}
			if len(notifications) != 1 {
				t.Fatalf("ProjectNotifications() produced %d notifications, want 1", len(notifications))
			}
			meta := toolUpdateMeta(t, notifications[0].Update)
			assertRuntimeToolName(t, meta, tt.toolName)
			vendor, _ := meta["vendor"].(map[string]any)
			if vendor["trace"] != "keep" {
				t.Fatalf("tool update meta = %#v, lost ProtocolUpdate meta", meta)
			}
			if _, leaked := meta["event_only"]; leaked {
				t.Fatalf("tool update meta = %#v, copied unrelated Event meta", meta)
			}
		})
	}
}

func TestProtocolToolNameForUpdateUsesOneOrderedCandidateLadder(t *testing.T) {
	t.Parallel()

	runtimeToolMeta := func(name string) map[string]any {
		return metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
			metautil.RuntimeToolName: name,
		})
	}
	tests := []struct {
		name   string
		event  *session.Event
		update *session.ProtocolUpdate
		want   string
	}{
		{
			name: "canonical event wins",
			event: &session.Event{Tool: &session.EventTool{
				Name: "Task",
			}},
			update: &session.ProtocolUpdate{
				Meta: runtimeToolMeta("Spawn"), RawInput: map[string]any{"command": "go test"},
				Title: "Spawn orbit", Kind: "execute",
			},
			want: "Task",
		},
		{
			name:  "update meta wins event meta",
			event: &session.Event{Meta: runtimeToolMeta("Task")},
			update: &session.ProtocolUpdate{
				Meta: runtimeToolMeta("Spawn"), RawInput: map[string]any{"command": "go test"},
				Title: "Task wait", Kind: "execute",
			},
			want: "Spawn",
		},
		{
			name:  "empty canonical event tool does not skip update meta",
			event: &session.Event{Tool: &session.EventTool{}},
			update: &session.ProtocolUpdate{
				Meta: runtimeToolMeta("Spawn"), Title: "Unknown action", Kind: "execute",
			},
			want: "Spawn",
		},
		{
			name: "unmatched canonical message call does not skip update meta",
			event: &session.Event{Message: &model.Message{
				Parts: []model.Part{{
					Kind:    model.PartKindToolUse,
					ToolUse: &model.ToolUsePart{ID: "other-call", Name: "Task"},
				}},
			}},
			update: &session.ProtocolUpdate{
				ToolCallID: "call-1", Meta: runtimeToolMeta("Spawn"),
				Title: "Unknown action", Kind: "execute",
			},
			want: "Spawn",
		},
		{
			name:  "event meta wins raw input",
			event: &session.Event{Meta: runtimeToolMeta("Task")},
			update: &session.ProtocolUpdate{
				RawInput: map[string]any{"command": "go test"}, Title: "Spawn orbit", Kind: "execute",
			},
			want: "Task",
		},
		{
			name:   "raw input wins title",
			event:  &session.Event{},
			update: &session.ProtocolUpdate{RawInput: map[string]any{"command": "go test"}, Title: "Spawn orbit", Kind: "execute"},
			want:   "RunCommand",
		},
		{
			name:   "known title wins generic kind",
			event:  &session.Event{},
			update: &session.ProtocolUpdate{Title: "Task wait", Kind: "execute"},
			want:   "Task",
		},
		{
			name:   "title only rg compatibility",
			event:  &session.Event{},
			update: &session.ProtocolUpdate{Title: "rg task stream"},
			want:   "RG",
		},
		{
			name:   "title only find compatibility",
			event:  &session.Event{},
			update: &session.ProtocolUpdate{Title: "find task stream"},
			want:   "FIND",
		},
		{
			name:   "compatibility title wins generic kind",
			event:  &session.Event{},
			update: &session.ProtocolUpdate{Title: "rg task stream", Kind: "execute"},
			want:   "RG",
		},
		{
			name:   "kind wins unknown title",
			event:  &session.Event{},
			update: &session.ProtocolUpdate{Title: "Custom action", Kind: "execute"},
			want:   "execute",
		},
		{
			name:   "unknown title is final fallback",
			event:  &session.Event{},
			update: &session.ProtocolUpdate{Title: "Custom action"},
			want:   "Custom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := protocolToolNameForUpdate(tt.event, tt.update); got != tt.want {
				t.Fatalf("protocolToolNameForUpdate() = %q, want %q", got, tt.want)
			}
		})
	}
}

func toolUpdateMeta(t *testing.T, update Update) map[string]any {
	t.Helper()
	switch typed := update.(type) {
	case ToolCall:
		return typed.Meta
	case ToolCallUpdate:
		return typed.Meta
	default:
		t.Fatalf("update = %T, want ToolCall or ToolCallUpdate", update)
		return nil
	}
}

func assertRuntimeToolName(t *testing.T, meta map[string]any, want string) {
	t.Helper()
	if got := metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName); got != want {
		t.Fatalf("runtime tool name = %q, want %q; meta=%#v", got, want, meta)
	}
}
