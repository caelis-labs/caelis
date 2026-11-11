package tuiapp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/projection"
)

// Synthetic canonical events use the real Control, reducer, physical viewport
// and rendered click route. They execute no tools, model or desktop actions.
func consecutiveScriptEvents() []*session.Event {
	var out []*session.Event
	add := func(id, name, parent, status, output string) {
		typ := session.EventTypeToolCall
		if status != "started" {
			typ = session.EventTypeToolResult
		}
		tool := &session.EventTool{ID: id, Name: name, Status: status, Input: map[string]any{"path": id + ".txt", "code": "RAW_DTW_ARGUMENT"}}
		if name == "desktop_world__exec" {
			tool.Input = map[string]any{"code": "return await app.look();"}
		}
		if name == "js_exec" {
			tool.Input = map[string]any{"code": "SOURCE_JS_NEVER"}
		}
		if parent != "" {
			tool.Parent = &session.EventParentTool{CallID: parent, Name: "js_exec"}
		}
		if output != "" {
			tool.Content = []session.EventToolContent{{Type: "content", Text: output}}
		}
		out = append(out, &session.Event{Type: typ, SessionID: "fixture", Visibility: session.VisibilityMirror, Tool: tool})
	}
	for n := 1; n <= 2; n++ {
		root := fmt.Sprint("script", n)
		add(root, "js_exec", "", "started", "")
		for _, name := range []string{"Read", "desktop_world__exec"} {
			id := root + name
			add(id, name, root, "started", "")
			add(id, name, root, "completed", "")
		}
		output := ""
		if n == 2 {
			output = `{"FINAL_SUMMARY":42}`
		}
		add(root, "js_exec", "", "completed", output)
		if n == 1 {
			out = append(out, &session.Event{SessionID: "fixture", Type: session.EventTypeAssistant, Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentThought), Content: session.ProtocolTextContent("BETWEEN_SCRIPT_STEPS")}}})
		}
	}
	return out
}

func groupedCanonicalModel(t *testing.T, events []*session.Event, scope string, replay bool) (*Model, Block) {
	t.Helper()
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 35})
	for _, original := range events {
		ev := session.CloneEvent(original)
		if scope == "child" {
			ev.ChildOrigin = &session.EventChildOrigin{Scope: session.EventChildScopeSubagent, ScopeID: "task", ParentTool: session.EventParentTool{CallID: "spawn", Name: "StartThread"}}
		}
		base := projection.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "fixture"}, ev, projection.SessionEventTransport{TurnID: "turn"})
		for _, env := range projection.ProjectSessionEventEnvelope(base, ev) {
			if replay {
				raw, err := json.Marshal(env)
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(raw, &env); err != nil {
					t.Fatal(err)
				}
			}
			m = applyACPEnvelopeForTest(t, m, env)
		}
	}
	if scope == "child" {
		v := m.subagentOutputViews["spawn"]
		if v == nil {
			t.Fatal("original StartThread pane missing")
		}
		v.block.Status = "completed"
		return m, v.block
	}
	b := requireMainACPTurnBlockForTest(t, m)
	b.Status = "completed"
	return m, b
}

func clickScriptToken(t *testing.T, m *Model, b Block, ctx BlockRenderContext, token string) {
	t.Helper()
	for _, row := range b.Render(ctx) {
		if row.ClickToken != token && row.ClickTokenAlt != token {
			continue
		}
		if child, ok := b.(*ParticipantTurnBlock); ok {
			if key, found := strings.CutPrefix(token, "acp_exploration_stable:"); found && child.toggleExplorationExpanded(key) {
				return
			}
			if id, found := strings.CutPrefix(token, "acp_tool_panel:"); found && child.toggleToolPanelClick(id) {
				return
			}
		} else if m.tryToggleFoldToken(b.BlockID(), token) {
			return
		}
		t.Fatalf("rendered click was not routed: %s", token)
	}
	t.Fatalf("disclosure not rendered: %s", token)
}

func TestChildInnerParentRoutingStaysWithinItsObservedTurn(t *testing.T) {
	m, b := groupedCanonicalModel(t, consecutiveScriptEvents(), "child", true)
	ctx := m.blockRenderContext(60)
	before := joinRenderedPlain(b.Render(ctx))
	event := TranscriptEvent{Scope: ACPProjectionSubagent, ScopeID: "task", TurnID: "turn", AnchorToolCallID: "script1", AnchorToolName: "js_exec"}
	other := m.ensureSubagentOutputView("other-spawn")
	other.observeChildEvent(TranscriptEvent{Kind: TranscriptEventTool, Scope: ACPProjectionSubagent, ScopeID: "other-task", TurnID: "turn", ToolCallID: "script1", ToolName: "js_exec", AnchorToolCallID: "other-spawn", AnchorToolName: "StartThread"})
	if key := m.subagentOutputEventKey(event); key != "spawn" {
		t.Fatalf("observed root routed to %s", key)
	}
	event.TurnID = "other-turn"
	if key := m.subagentOutputEventKey(event); key == "spawn" {
		t.Fatal("cross-Turn name/ID guessed owner")
	}
	event.AnchorToolCallID = "missing"
	if key := m.subagentOutputEventKey(event); key != "task:task" {
		t.Fatalf("orphan lost existing scoped fallback %s", key)
	}
	if joinRenderedPlain(b.Render(ctx)) != before {
		t.Fatal("routing changed old receipt view")
	}
}
