package projection

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
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

func TestCodeModeProjectsOriginalSelectedResult(t *testing.T) {
	for _, test := range []struct {
		name   string
		part   model.Part
		output map[string]any
		want   string
	}{
		{name: "object", part: model.NewJSONPart(json.RawMessage(`{"tools":[{"name":"Read"}],"count":1}`)), output: map[string]any{"tools": []any{map[string]any{"name": "Read"}}, "count": 1}, want: `{"tools":[{"name":"Read"}],"count":1}`},
		{name: "summary field", part: model.NewJSONPart(json.RawMessage(`{"summary":"selected","other":42}`)), output: map[string]any{"summary": "selected", "other": 42}, want: `{"summary":"selected","other":42}`},
		{name: "array", part: model.NewJSONPart(json.RawMessage(`[1,"two"]`)), output: map[string]any{"result": []any{1, "two"}}, want: `[1,"two"]`},
		{name: "null", part: model.NewJSONPart(json.RawMessage(`null`)), output: map[string]any{"result": nil}, want: `null`},
		{name: "number", part: model.NewJSONPart(json.RawMessage(`9007199254740993`)), output: map[string]any{"result": json.Number("9007199254740993")}, want: `9007199254740993`},
		{name: "boolean", part: model.NewJSONPart(json.RawMessage(`false`)), output: map[string]any{"result": false}, want: `false`},
		{name: "JSON string", part: model.NewJSONPart(json.RawMessage(`"completed"`)), output: map[string]any{"result": "completed"}, want: `"completed"`},
		{name: "text", part: model.NewTextPart("selected\noriginal text"), output: map[string]any{"result": "selected\noriginal text"}, want: "selected\noriginal text"},
		{name: "literal completed", part: model.NewTextPart("completed"), output: map[string]any{"result": "completed"}, want: "completed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Chat stores the selected canonical result in Message; EventTool has
			// only its lossy raw-output map and no presentation Content.
			message := model.NewMessage(model.RoleTool, model.Part{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{
				ToolUseID: "script", Name: "js_exec", Content: []model.Part{test.part},
			}})
			event := &session.Event{Type: session.EventTypeToolResult, SessionID: "session", Message: &message,
				Tool: &session.EventTool{ID: "script", Name: "js_exec", Status: "completed", Output: test.output}}
			for _, replay := range []bool{false, true} {
				projected := event
				if replay {
					raw, err := json.Marshal(event)
					if err != nil {
						t.Fatal(err)
					}
					projected = &session.Event{}
					if err := json.Unmarshal(raw, projected); err != nil {
						t.Fatal(err)
					}
				}
				base := EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "session"}, projected, SessionEventTransport{})
				envelopes := ProjectSessionEventEnvelope(base, projected)
				if len(envelopes) != 1 {
					t.Fatalf("replay=%v: envelopes = %#v", replay, envelopes)
				}
				update, ok := envelopes[0].Update.(eventstream.ToolCallUpdate)
				if !ok || len(update.Content) != 1 {
					t.Fatalf("replay=%v: update = %#v", replay, envelopes[0].Update)
				}
				text, ok := update.Content[0].Content.(eventstream.TextContent)
				if !ok || text.Text != test.want {
					t.Fatalf("replay=%v: content = %#v, want %q", replay, update.Content, test.want)
				}
			}
		})
	}
}

func TestCodeModeStatusOnlyResultHasNoAuthoredContent(t *testing.T) {
	event := &session.Event{Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "script", Name: "js_exec", Status: "completed"}}
	updates, err := ProjectEvent(event)
	if err != nil || len(updates) != 1 {
		t.Fatalf("updates = %#v, error = %v", updates, err)
	}
	update, ok := updates[0].(eventstream.ToolCallUpdate)
	if !ok || len(update.Content) != 0 {
		t.Fatalf("status-only result invented content: %#v", updates[0])
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
