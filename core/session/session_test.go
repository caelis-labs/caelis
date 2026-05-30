package session

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
)

func TestCloneEventPreservesCanonicalMessageIndependently(t *testing.T) {
	event := Event{
		Type: EventAssistant,
		Message: &model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("hello")},
			Meta:  map[string]any{"provider": "test"},
		},
		Tool: &ToolEvent{
			ID:     "call-1",
			Name:   "READ",
			Input:  map[string]any{"path": "a.txt"},
			Output: map[string]any{"text": "hello"},
		},
		Meta: map[string]any{"surface": "test"},
	}

	cloned := CloneEvent(event)
	event.Message.Parts[0].Text.Text = "mutated"
	event.Message.Meta["provider"] = "mutated"
	event.Tool.Input["path"] = "b.txt"
	event.Meta["surface"] = "mutated"

	if got := EventText(cloned); got != "hello" {
		t.Fatalf("EventText(cloned) = %q, want hello", got)
	}
	if got := cloned.Message.Meta["provider"]; got != "test" {
		t.Fatalf("cloned message meta = %v, want test", got)
	}
	if got := cloned.Tool.Input["path"]; got != "a.txt" {
		t.Fatalf("cloned tool input path = %v, want a.txt", got)
	}
	if got := cloned.Meta["surface"]; got != "test" {
		t.Fatalf("cloned event meta = %v, want test", got)
	}
}
