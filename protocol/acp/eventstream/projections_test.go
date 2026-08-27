package eventstream

import (
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestToolCallFromEnvelopeReturnsACPToolCall(t *testing.T) {
	t.Parallel()
	env := Envelope{Kind: KindSessionUpdate, Update: schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall,
		ToolCallID:    "call-1",
		Title:         "Run tests",
		Kind:          schema.ToolKindExecute,
		Status:        schema.ToolStatusInProgress,
		RawInput:      map[string]any{"command": "go test ./..."},
		Content:       []schema.ToolCallContent{{Type: "terminal", TerminalID: "term-1"}},
		Meta:          map[string]any{"caelis": map[string]any{"runtime": map[string]any{"task_id": "task-1"}}},
	}}
	tool, ok := ToolCallFromEnvelope(env)
	if !ok || tool.ToolCallID != "call-1" || len(tool.Content) != 1 || tool.Content[0].TerminalID != "term-1" {
		t.Fatalf("tool call = %#v, %v", tool, ok)
	}
	tool.RawInput.(map[string]any)["command"] = "mutated"
	if source := env.Update.(schema.ToolCall); source.RawInput.(map[string]any)["command"] != "go test ./..." {
		t.Fatalf("source tool call mutated: %#v", source.RawInput)
	}
}

func TestToolCallUpdateFromEnvelopeReturnsACPToolCallUpdate(t *testing.T) {
	t.Parallel()
	title := "RunCommand"
	status := schema.ToolStatusInProgress
	env := Envelope{Kind: KindSessionUpdate, Update: schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    "call-1",
		Title:         &title,
		Status:        &status,
		RawInput:      map[string]any{"command": "make test"},
	}}
	update, ok := ToolCallUpdateFromEnvelope(env)
	if !ok || update.ToolCallID != "call-1" || update.Title == nil || *update.Title != "RunCommand" {
		t.Fatalf("tool call update = %#v, %v", update, ok)
	}
	*update.Title = "mutated"
	update.RawInput.(map[string]any)["command"] = "mutated"
	source := env.Update.(schema.ToolCallUpdate)
	if source.Title == nil || *source.Title != "RunCommand" || source.RawInput.(map[string]any)["command"] != "make test" {
		t.Fatalf("source tool update mutated: %#v", source)
	}
}
