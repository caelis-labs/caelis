package context

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
)

func TestMessagesRebuildsOnlyCanonicalModelVisibleMessages(t *testing.T) {
	events := []session.Event{
		{
			Type: session.EventUser,
			Message: &model.Message{
				Role:  model.RoleUser,
				Parts: []model.Part{model.NewTextPart("ping")},
			},
		},
		{
			Type:       session.EventNotice,
			Visibility: session.VisibilityUIOnly,
			Message: &model.Message{
				Role:  model.RoleAssistant,
				Parts: []model.Part{model.NewTextPart("live only")},
			},
		},
		{
			Type: session.EventToolCall,
		},
		{
			Type: session.EventAssistant,
			Message: &model.Message{
				Role: model.RoleAssistant,
				Parts: []model.Part{{
					Kind: model.PartToolUse,
					ToolUse: &model.ToolCall{
						ID:   "call-1",
						Name: "run_command",
					},
				}},
			},
		},
		{
			Type: session.EventToolResult,
			Message: &model.Message{
				Role: model.RoleTool,
				Parts: []model.Part{{
					Kind: model.PartToolResult,
					ToolResult: &model.ToolResultPart{
						ToolCallID: "call-1",
						Name:       "run_command",
						Content:    []model.Part{model.NewTextPart("hello")},
					},
				}},
			},
		},
	}

	messages := Messages(events)
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want user, assistant tool use, tool result", len(messages))
	}
	if messages[0].Role != model.RoleUser || messages[0].TextContent() != "ping" {
		t.Fatalf("first message = %#v, want user ping", messages[0])
	}
	if calls := messages[1].ToolCalls(); len(calls) != 1 || calls[0].ID != "call-1" {
		t.Fatalf("assistant tool calls = %#v, want call-1", calls)
	}
	if messages[2].Role != model.RoleTool {
		t.Fatalf("third message role = %q, want tool", messages[2].Role)
	}

	events[0].Message.Parts[0].Text.Text = "changed"
	if messages[0].TextContent() != "ping" {
		t.Fatalf("messages were not cloned: %q", messages[0].TextContent())
	}
}

func TestMessagesStartAtLatestCompactCheckpoint(t *testing.T) {
	events := []session.Event{
		{
			Type: session.EventUser,
			Message: &model.Message{
				Role:  model.RoleUser,
				Parts: []model.Part{model.NewTextPart("old prompt")},
			},
		},
		{
			Type: session.EventCompact,
			Message: &model.Message{
				Role:  model.RoleUser,
				Parts: []model.Part{model.NewTextPart("CONTEXT CHECKPOINT\ncurrent summary")},
			},
		},
		{
			Type: session.EventAssistant,
			Message: &model.Message{
				Role:  model.RoleAssistant,
				Parts: []model.Part{model.NewTextPart("new answer")},
			},
		},
	}

	messages := Messages(events)
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want compact checkpoint plus post-compact assistant", len(messages))
	}
	if got := messages[0].TextContent(); got != "CONTEXT CHECKPOINT\ncurrent summary" {
		t.Fatalf("first message = %q, want compact checkpoint", got)
	}
	if got := messages[1].TextContent(); got != "new answer" {
		t.Fatalf("second message = %q, want post-compact assistant", got)
	}
}
