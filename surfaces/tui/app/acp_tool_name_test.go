package tuiapp

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/evalharness"
)

func TestACPStandardToolNamePatchesRenderAndReplay(t *testing.T) {
	t.Parallel()
	for _, scope := range []eventstream.Scope{eventstream.ScopeMain, eventstream.ScopeSubagent} {
		t.Run(string(scope), func(t *testing.T) {
			var events []SubagentEvent
			for _, tc := range []struct{ raw, want string }{
				{`{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"Inspect data","kind":"execute","name":"initial_tool","_meta":{"caelis":{"runtime":{"tool":{"name":"legacy_tool"}}}}}`, "initial_tool"},
				{`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","name":"renamed_tool"}`, "renamed_tool"},
				{`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","name":null,"_meta":{"caelis":{"runtime":{"tool":{"name":"stale_legacy"}}}}}`, "renamed_tool"},
				{`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","status":"completed"}`, "renamed_tool"},
				{`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","name":"settled_tool"}`, "settled_tool"},
				{`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","name":""}`, ""},
				{`{"sessionUpdate":"tool_call_update","toolCallId":"call-1","name":null}`, ""},
			} {
				update, err := eventstream.DecodeUpdateJSON([]byte(tc.raw))
				if err != nil {
					t.Fatal(err)
				}
				envelope := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, Scope: scope, Update: update}
				// Exercise the same serialized Envelope path used by live and replay delivery.
				raw, err := json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &envelope); err != nil {
					t.Fatal(err)
				}
				projected := ProjectACPEventToTranscriptEvents(envelope)
				if len(projected) != 1 {
					t.Fatalf("projected = %#v", projected)
				}
				event := projected[0]
				events, _, _ = applyToolEventUpdate(events, toolEventUpdate{
					CallID: event.ToolCallID, Name: event.ToolName, Args: event.ToolArgs, Output: event.ToolOutput,
					Final: event.Final, Err: event.ToolError, Meta: transcriptToolUpdateMeta(event),
				}, nil)
				if len(events) != 1 || events[0].Name != tc.want {
					t.Fatalf("after %s: events = %#v, want name %q", tc.raw, events, tc.want)
				}
			}
		})
	}

	m := NewModel(Config{AppName: "CAELIS", Workspace: "/tmp/workspace", NoColor: true, NoAnimation: true})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(*Model)
	for _, update := range []eventstream.Update{
		eventstream.ContentChunk{SessionUpdate: eventstream.UpdateUserMessage, Content: eventstream.TextContent{Type: "text", Text: "inspect"}},
		eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "call-1", Name: stringPtr("initial_tool"), Title: "Inspect", Kind: "execute", RawInput: map[string]any{"command": "pwd"}},
		eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "call-1", Name: stringPtr("renamed_tool")},
		eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "call-1", Status: stringPtr(eventstream.ToolStatusCompleted)},
	} {
		updated, _ = m.Update(eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "s", Update: update})
		m = updated.(*Model)
	}
	frame := evalharness.NormalizeFrame(m.View().Content)
	if !strings.Contains(frame, "renamed_tool") || strings.Contains(frame, "initial_tool") {
		t.Fatalf("rendered frame did not apply standard tool name patch:\n%s", frame)
	}
}
