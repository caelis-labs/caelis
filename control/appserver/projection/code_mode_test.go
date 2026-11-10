package projection

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestCodeModeProjectsActualToolAndTypedParent(t *testing.T) {
	for _, name := range []string{"Read", "maps__route"} {
		event := &session.Event{ID: "event", SessionID: "script-session", Type: session.EventTypeToolCall, Visibility: session.VisibilityMirror,
			Tool: &session.EventTool{ID: "inner", Name: name, Status: "started", Input: map[string]any{"origin": "北京南站"}, Parent: &session.EventParentTool{CallID: "script", Name: "js_repl"}}}
		cloned := session.CloneEvent(event)
		base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "script-session"}, cloned, SessionEventTransport{})
		envelopes := ProjectSessionEventEnvelope(base, cloned)
		if len(envelopes) != 1 || envelopes[0].ParentTool == nil || envelopes[0].ParentTool.ToolCallID != "script" || envelopes[0].ParentTool.ToolName != "js_repl" {
			t.Fatalf("nested projection=%+v", envelopes)
		}
		assertStandardToolName(t, envelopes[0].Update, name)
		raw, err := json.Marshal(envelopes[0])
		if err != nil || !strings.Contains(string(raw), "北京南站") {
			t.Fatalf("actual arguments lost: %s %v", raw, err)
		}
	}
}

func TestDirectParentSurvivesChildOriginWithoutInventingOne(t *testing.T) {
	for _, direct := range []bool{false, true} {
		event := &session.Event{Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "tool", Name: "MCP", Status: "completed"}, ChildOrigin: &session.EventChildOrigin{Scope: session.EventChildScopeSubagent, ScopeID: "task", ParentTool: session.EventParentTool{CallID: "spawn", Name: "StartThread"}}}
		if direct {
			event.Tool.Parent = &session.EventParentTool{CallID: "script", Name: "js_exec"}
		}
		base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session"}, event, SessionEventTransport{})
		want := "spawn"
		if direct {
			want = "script"
		}
		if base.ParentTool == nil || base.ParentTool.ToolCallID != want || base.ScopeID != "task" {
			t.Fatalf("scope/direct parent %+v", base)
		}
	}
}
