package runtime

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func TestLegacyDiscoveryMarkerIsIgnored(t *testing.T) {
	t.Parallel()
	b := tool.Binding{Name: "docs__read", Identity: "source", Version: "schema-v1"}
	entry := map[string]any{"name": b.Name, "binding": b, "input_schema": map[string]any{"type": "object"}}
	event := &session.Event{Type: session.EventTypeToolResult, Tool: &session.EventTool{Name: mcp.SearchToolName, Status: "completed", Output: map[string]any{"tools": []any{entry}}}, Meta: map[string]any{"caelis.mcp.discovery.v1": true}}
	if got := searchBindings(event); len(got) != 0 {
		t.Fatalf("legacy search output restored a binding: %v", got)
	}
}

func TestFullDiscoveryResultsRequireTheirOwningSource(t *testing.T) {
	t.Parallel()
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
	var plain tool.Result
	if err := json.Unmarshal([]byte(`{"content":[{"kind":"json","json":{"value":`+string(raw)+`}}]}`), &plain); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*session.EventTool){
		func(fact *session.EventTool) { fact.Discovery = false },
		func(fact *session.EventTool) { fact.Outcome = session.ToolOutcomeFailed },
		func(fact *session.EventTool) { fact.Result = &plain },
	} {
		event := session.CloneEvent(original)
		change(event.Tool)
		if got := searchBindings(event); len(got) != 0 {
			t.Fatalf("ineligible result restored a binding: %v", got)
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
	applyMCPDiscoveryBindings(bindings, event)
	return bindings
}
