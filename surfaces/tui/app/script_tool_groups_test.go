package tuiapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/model"
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
		if row.ClickToken != token {
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

func TestConsecutiveScriptToolsHaveOwnSharedDisclosure(t *testing.T) {
	for _, scope := range []string{"main", "child"} {
		for _, replay := range []bool{false, true} {
			for _, width := range []int{36, 60, 100} {
				t.Run(fmt.Sprintf("%s/replay%v/width%d", scope, replay, width), func(t *testing.T) {
					events := consecutiveScriptEvents()
					for _, ev := range events {
						if ev.Tool != nil && ev.Tool.ID == "script2Read" && ev.Type == session.EventTypeToolResult {
							ev.Tool.Content = []session.EventToolContent{{Type: "content", Text: "VISIBLE_CHILD_RESULT"}}
						}
					}
					m, b := groupedCanonicalModel(t, events, scope, replay)
					ctx := m.blockRenderContext(width)
					render := func() string {
						return strings.Join(m.renderViewportRowCacheFromRows(b, ctx, viewportRowCache{}, b.Render(ctx)).plainLines, "\n")
					}
					before := render()
					if strings.Count(before, "• Called tools") != 2 || !strings.Contains(before, "script1Read") || !strings.Contains(before, "script2Read") || !strings.Contains(before, "BETWEEN_SCRIPT_STEPS") {
						t.Fatalf("collapsed group leaked content: %s", before)
					}
					groupToken := acpStableExplorationClickToken("tools:script1")
					clickScriptToken(t, m, b, ctx, groupToken)
					trace := render()
					for _, want := range []string{"script1Read", "script2Read", "desktop_world", "BETWEEN_SCRIPT_STEPS"} {
						if !strings.Contains(trace, want) {
							t.Fatalf("missing %s: %s", want, trace)
						}
					}
					for _, hidden := range []string{"VISIBLE_CHILD_RESULT", "FINAL_SUMMARY", "completed", "SOURCE_JS_NEVER", "│"} {
						if strings.Contains(trace, hidden) {
							t.Fatalf("trace leaked %s: %s", hidden, trace)
						}
					}
					if width == 100 && !strings.Contains(trace, "return await app.look();") {
						t.Fatal("raw domain argument was replaced: " + trace)
					}
					clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken("tools:script2"))
					clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("script2Read"))
					if text := render(); !strings.Contains(text, "VISIBLE_CHILD_RESULT") {
						t.Fatalf("first child click did not reveal only its result: %s", text)
					}
					clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("script2"))
					if text := render(); !strings.Contains(text, "FINAL_SUMMARY") {
						t.Fatalf("script result click lost selected output: %s", text)
					}
					for _, row := range b.Render(ctx) {
						if displayColumns(row.Plain) > width && row.PreWrapped {
							t.Fatalf("wide row: %q", row.Plain)
						}
					}
					clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken("tools:script2"))
					clickScriptToken(t, m, b, ctx, groupToken)
					if after := render(); after != before {
						t.Fatalf("group collapse left nested output: %s", after)
					}
				})
			}
		}
	}
}

func TestInvocationGroupsKeepDirectToolsAndLiteralCompleted(t *testing.T) {
	for _, scope := range []string{"main", "child"} {
		events := consecutiveScriptEvents()
		direct := &session.Event{Type: session.EventTypeToolResult, SessionID: "fixture", Tool: &session.EventTool{ID: "direct", Name: "foreign__direct", Status: "completed", Content: []session.EventToolContent{{Type: "content", Text: "completed"}}}}
		events = append(events[:3], append([]*session.Event{direct}, events[3:]...)...)
		// A genuine returned string is not a status placeholder.
		events[len(events)-1].Tool.Content = []session.EventToolContent{{Type: "content", Text: "completed"}}
		m, b := groupedCanonicalModel(t, events, scope, true)
		ctx := m.blockRenderContext(100)
		text := joinRenderedPlain(b.Render(ctx))
		t.Log(text)
		if !strings.Contains(text, "\n• foreign__direct") || strings.Contains(text, "completed") {
			t.Fatal(text)
		}
		for _, row := range b.Render(ctx) {
			if strings.HasPrefix(row.ClickToken, "acp_exploration_stable:tools:") {
				clickScriptToken(t, m, b, ctx, row.ClickToken)
			}
		}
		clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("script2"))
		if text := joinRenderedPlain(b.Render(ctx)); strings.Count(text, "completed") != 1 {
			t.Fatalf("literal script result was lost or synthesized: %s", text)
		}
		var raw []SubagentEvent
		switch v := b.(type) {
		case *MainACPTurnBlock:
			raw = v.Events
		case *ParticipantTurnBlock:
			raw = v.Events
		}
		var directSeen bool
		for i, ev := range raw {
			if ev.CallID == "direct" {
				directSeen = true
				if ev.Output != "completed" {
					t.Fatal("literal direct receipt changed")
				}
				if invocationToolParents(raw)[raw[i].CallID] != "" {
					t.Fatal("direct tool admitted to script group")
				}
				if scope == "child" && ev.ParentToolName != "StartThread" {
					t.Fatal("direct child routing changed")
				}
			}
		}
		if !directSeen {
			t.Fatal("direct receipt lost")
		}
	}
}

func TestEmptyScriptDoesNotLeaveTimelineSpacing(t *testing.T) {
	for _, scope := range []string{"main", "child"} {
		direct := &session.Event{Type: session.EventTypeToolResult, SessionID: "fixture", Tool: &session.EventTool{ID: "read", Name: "Read", Input: map[string]any{"path": "go.mod"}, Status: "completed"}}
		empty := &session.Event{Type: session.EventTypeToolResult, SessionID: "fixture", Tool: &session.EventTool{ID: "empty", Name: "js_exec", Status: "completed"}}
		m, a := groupedCanonicalModel(t, []*session.Event{direct}, scope, false)
		_, b := groupedCanonicalModel(t, []*session.Event{empty, empty, direct, empty}, scope, true)
		ctx := m.blockRenderContext(60)
		if joinRenderedPlain(a.Render(ctx)) != joinRenderedPlain(b.Render(ctx)) {
			t.Fatalf("hidden script changed native spacing\n%s\n%s", joinRenderedPlain(a.Render(ctx)), joinRenderedPlain(b.Render(ctx)))
		}
	}
}

func TestInvocationGroupsPreserveFailuresAndRealTargetReview(t *testing.T) {
	m, b := groupedCanonicalModel(t, consecutiveScriptEvents(), "main", false)
	block := b.(*MainACPTurnBlock)
	block.UpdateToolWithMeta("script1Read", "Read", "bad.txt", "bad.txt: not found", true, true, ToolUpdateMeta{ParentToolCallID: "script1", ParentToolName: "js_exec"})
	block.AddApprovalReviewEvent("script1desktop_world__exec", "desktop_world__exec", "raw command", "denied", "REAL_TARGET_DENIAL")
	ctx := m.blockRenderContext(100)
	clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken("tools:script1"))
	text := joinRenderedPlain(block.Render(ctx))
	if strings.Contains(text, "REAL_TARGET_DENIAL") || strings.Contains(text, "bad.txt: not found") {
		t.Fatal("unclicked failure result leaked: " + text)
	}
	clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("script1desktop_world__exec"))
	text = joinRenderedPlain(block.Render(ctx))
	t.Log(text)
	if strings.Count(text, "REAL_TARGET_DENIAL") != 1 || !strings.Contains(text, "[denied]") || !strings.Contains(text, "Read script1Read.txt") || strings.Contains(text, "failed") || strings.Contains(text, "[failed]") {
		t.Fatal(text)
	}
	// Late status cannot reopen a terminal child or resurrect script input.
	block.UpdateToolWithMeta("script1Read", "Read", "bad.txt", "", false, false, ToolUpdateMeta{ParentToolCallID: "script1", ParentToolName: "js_exec"})
	if joinRenderedPlain(block.Render(ctx)) != text {
		t.Fatal("late running changed terminal group")
	}
}

func TestInvocationGroupRetainsReasoningBeforeZeroChildScript(t *testing.T) {
	for _, scope := range []string{"main", "child"} {
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replay%v", scope, replay), func(t *testing.T) {
				var events []*session.Event
				for _, ev := range consecutiveScriptEvents() {
					if ev.Tool != nil && ev.Tool.Parent != nil && ev.Tool.Parent.CallID == "script2" {
						continue
					}
					events = append(events, ev)
				}
				m, b := groupedCanonicalModel(t, events, scope, replay)
				ctx := m.blockRenderContext(60)
				clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken("tools:script1"))
				text := joinRenderedPlain(b.Render(ctx))
				if !strings.Contains(text, "BETWEEN_SCRIPT_STEPS") || !strings.Contains(text, "Ran script") || strings.Contains(text, "Result") {
					t.Fatal(text)
				}
			})
		}
	}
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

func TestScriptSearchDisplaysOnlyOriginalResultOnClick(t *testing.T) {
	const selected = `{"tools":[],"count":0}`
	message := model.NewMessage(model.RoleTool, model.Part{Kind: model.PartKindToolResult, ToolResult: &model.ToolResultPart{
		ToolUseID: "script", Name: "js_exec", Content: []model.Part{model.NewJSONPart(json.RawMessage(selected))},
	}})
	parent := &session.EventParentTool{CallID: "script", Name: "js_exec"}
	events := []*session.Event{
		{Type: session.EventTypeToolCall, SessionID: "fixture", Tool: &session.EventTool{ID: "script", Name: "js_exec", Status: "started"}},
		{Type: session.EventTypeToolCall, SessionID: "fixture", Tool: &session.EventTool{ID: "search", Name: "tool_search", Status: "started", Parent: parent, Input: map[string]any{"query": "github pull request get diff"}}},
		{Type: session.EventTypeToolResult, SessionID: "fixture", Tool: &session.EventTool{ID: "search", Name: "tool_search", Status: "completed", Parent: parent}},
		{Type: session.EventTypeToolResult, SessionID: "fixture", Message: &message, Tool: &session.EventTool{ID: "script", Name: "js_exec", Status: "completed", Output: map[string]any{"tools": []any{}, "count": 0}}},
	}
	for _, scope := range []string{"main", "child"} {
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/replay%v", scope, replay), func(t *testing.T) {
				m, b := groupedCanonicalModel(t, events, scope, replay)
				ctx := m.blockRenderContext(60)
				clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken("tools:script"))
				trace := joinRenderedPlain(b.Render(ctx))
				if !strings.Contains(trace, "tool_search") || !strings.Contains(trace, "github pull request get diff") || strings.Contains(trace, "completed") {
					t.Fatalf("search trace contains fabricated or expanded output: %s", trace)
				}
				clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("script"))
				result := joinRenderedPlain(b.Render(ctx))
				if !strings.Contains(result, selected) || strings.Contains(result, "completed") || strings.Contains(result, "Result (tool_search)") {
					t.Fatalf("original selected ToolResult not preserved: %s", result)
				}
				t.Logf("trace\n%s\nselected result\n%s", trace, result)
			})
		}
	}
}

// Optional artifact entry: writes task-owned synthetic canonical input, then
// invokes the existing production replay/VT snapshot and actual-click helper.
func TestConsecutiveScriptToolRenderArtifacts(t *testing.T) {
	dir := os.Getenv("CAELIS_SCRIPT_GROUP_ARTIFACTS")
	if dir == "" {
		t.Skip("task-owned render evidence directory required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var data []byte
	for _, event := range consecutiveScriptEvents() {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, raw...)
		data = append(data, '\n')
	}
	input := filepath.Join(dir, "canonical-input.jsonl")
	if err := os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAELIS_TUI_CODE_MODE_EVENTS", input)
	t.Setenv("CAELIS_TUI_CODE_MODE_ARTIFACTS", filepath.Join(dir, "after"))
	TestCodeModeFrozenNativeReplay(t)
}
