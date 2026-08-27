package eventstream

import (
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

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
