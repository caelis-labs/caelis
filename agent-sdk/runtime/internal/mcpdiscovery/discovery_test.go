package mcpdiscovery

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestOnlyAdmittedCompleteCanonicalOutputGrantsDiscovery(t *testing.T) {
	b := tool.Binding{Name: "docs__read", Identity: "source", Version: "schema-v1"}
	entry := map[string]any{"name": b.Name, "binding": b, "input_schema": map[string]any{"type": "object"}}
	event := &session.Event{Type: session.EventTypeToolResult, Tool: &session.EventTool{Name: tool.ToolSearchToolName, Status: "completed", Output: map[string]any{"tools": []any{entry}}}, Meta: map[string]any{MetadataKey: true}}
	if got := FromEvents([]*session.Event{event}); got[b.Name] != b {
		t.Fatalf("canonical binding=%v", got)
	}
	for _, change := range []func(*session.Event){
		func(e *session.Event) { e.Meta = nil },
		func(e *session.Event) { e.Tool.Name = "ToolSearch" },
		func(e *session.Event) { e.Tool.Status = "failed" },
		func(e *session.Event) {
			e.Tool.Output = map[string]any{"tools": []any{map[string]any{"name": b.Name, "binding": b}}}
		},
		func(e *session.Event) { e.Meta[MetadataKey] = []tool.Binding{b}; e.Tool.Output = nil },
	} {
		candidate := session.CloneEvent(event)
		change(candidate)
		if got := FromEvents([]*session.Event{candidate}); len(got) != 0 {
			t.Fatalf("legacy, failed or incomplete receipt granted %v", got)
		}
	}
}
