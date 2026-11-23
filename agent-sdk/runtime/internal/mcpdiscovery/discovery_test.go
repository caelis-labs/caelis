package mcpdiscovery

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func TestOnlyAdmittedCompleteCanonicalOutputGrantsDiscovery(t *testing.T) {
	b := tool.Binding{Name: "docs__read", Identity: "source", Version: "schema-v1"}
	entry := map[string]any{"name": b.Name, "binding": b, "input_schema": map[string]any{"type": "object"}}
	event := &session.Event{Type: session.EventTypeToolResult, Tool: &session.EventTool{Name: mcp.SearchToolName, Status: "completed", Output: map[string]any{"tools": []any{entry}}}, Meta: map[string]any{MetadataKey: true}}
	if got := searchBindings(event); got[b.Name] != b {
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
		if got := searchBindings(candidate); len(got) != 0 {
			t.Fatalf("legacy, failed or incomplete receipt granted %v", got)
		}
	}
}

func TestFullDiscoveryResultsRequireTheirOwningSource(t *testing.T) {
	binding := tool.Binding{Name: "docs__read", Identity: "source", Version: "v1"}
	raw, _ := json.Marshal(map[string]any{"tools": []any{map[string]any{"name": binding.Name, "binding": binding, "input_schema": map[string]any{"type": "object"}}}})
	original := &session.Event{Type: session.EventTypeToolResult, Tool: &session.EventTool{Name: mcp.SearchToolName, Outcome: session.ToolOutcomeSucceeded, Result: &tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: raw}}}, Discovery: true}}
	for _, nested := range []bool{false, true} {
		event := session.CloneEvent(original)
		if nested {
			event.Visibility = session.VisibilityMirror
			event.Tool.Parent = &session.EventParentTool{Kind: "code_mode"}
			event.Scope = &session.EventScope{Source: "runtime_tool"}
		}
		if searchBindings(event)[binding.Name] != binding {
			t.Fatal("owning discovery result lost")
		}
	}
	remote := session.CloneEvent(original)
	remote.Visibility = session.VisibilityMirror
	remote.Scope = &session.EventScope{Source: "acp"}
	if len(searchBindings(remote)) != 0 {
		t.Fatal("remote mirror granted discovery")
	}
}

func searchBindings(event *session.Event) map[string]tool.Binding {
	bindings := map[string]tool.Binding{}
	Apply(bindings, event)
	return bindings
}
