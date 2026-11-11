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

// These are synthetic canonical events through the production Control/reducer,
// physical viewport and actual rendered click route, not GUI/live Tool evidence.
func TestScriptResultsUseNativeToolsAndCommonResultEntry(t *testing.T) {
	for _, scope := range []string{"main", "missing-parent", "child-origin"} {
		for _, replay := range []bool{false, true} {
			for _, file := range []bool{false, true} {
				for _, failed := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/replay%v/file%v/fail%v", scope, replay, file, failed), func(t *testing.T) {
						m := NewModel(Config{NoColor: true, NoAnimation: true})
						m.Update(tea.WindowSizeMsg{Width: 60, Height: 35})
						input := map[string]any{"code": "const SOURCE_MUST_NOT_RENDER=1;return SOURCE_MUST_NOT_RENDER;"}
						if file {
							input = map[string]any{"file": "/task/script-source.js"}
						}
						events := []*session.Event{{Type: session.EventTypeToolCall, Tool: &session.EventTool{ID: "script", Name: "js_exec", Status: "started", Input: input}}}
						for i, name := range []string{"Read", "ViewImage", "service__lookup", "Write"} {
							parent := &session.EventParentTool{CallID: "script", Name: "js_exec"}
							if scope == "missing-parent" {
								parent = nil
							}
							for _, status := range []string{"started", "completed"} {
								typ := session.EventTypeToolCall
								if status == "completed" {
									typ = session.EventTypeToolResult
									if failed && i == 0 {
										status = "failed"
									}
								}
								events = append(events, &session.Event{Type: typ, Visibility: session.VisibilityMirror, Tool: &session.EventTool{ID: fmt.Sprint("child", i), Name: name, Status: status, Parent: parent, Input: map[string]any{"path": "/task/input.txt", "query": "QUERY_MARKER"}, Content: []session.EventToolContent{{Type: "content", Text: fmt.Sprintf("CHILD_RESULT_%d\nCHILD_BODY_%d", i, i)}}}})
							}
						}
						status := "completed"
						if failed {
							status = "failed"
						}
						output := "SUMMARY_FIRST\n" + strings.Repeat("summary line\n", 8) + "SUMMARY_MIDDLE\n" + strings.Repeat("summary tail\n", 8) + "SUMMARY_LAST"
						events = append(events, &session.Event{Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "script", Name: "js_exec", Status: status, Input: input, Content: []session.EventToolContent{{Type: "content", Text: output}}}})
						for _, ev := range events {
							ev.SessionID = "fixture"
							if scope == "child-origin" {
								ev.ChildOrigin = &session.EventChildOrigin{Scope: session.EventChildScopeSubagent, ScopeID: "task", ParentTool: session.EventParentTool{CallID: "spawn", Name: "StartThread"}}
							}
							if replay {
								raw, _ := json.Marshal(ev)
								var restored session.Event
								if err := json.Unmarshal(raw, &restored); err != nil {
									t.Fatal(err)
								}
								ev = &restored
							}
							base := projection.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: "fixture"}, ev, projection.SessionEventTransport{TurnID: "turn"})
							for _, env := range projection.ProjectSessionEventEnvelope(base, ev) {
								m = applyACPEnvelopeForTest(t, m, env)
							}
						}
						var block Block
						var kept []SubagentEvent
						if scope == "child-origin" {
							view := m.subagentOutputViews["spawn"]
							if view == nil {
								t.Fatal("child pane missing")
							}
							view.block.Status = "completed"
							block = view.block
							kept = view.block.Events
						} else {
							b := requireMainACPTurnBlockForTest(t, m)
							b.Status = "completed"
							if scope == "missing-parent" {
								b.collapseAllToolPanels()
							}
							block = b
							kept = b.Events
						}
						ctx := m.blockRenderContext(60)
						physical := func() string {
							return strings.Join(m.renderViewportRowCacheFromRows(block, ctx, viewportRowCache{}, block.Render(ctx)).plainLines, "\n")
						}
						click := func(token string) {
							t.Helper()
							found := false
							for _, row := range block.Render(ctx) {
								found = found || row.ClickToken == token
							}
							if !found {
								t.Fatalf("no rendered disclosure %q: %s", token, physical())
							}
							var clicked bool
							if scope == "child-origin" {
								b := m.subagentOutputViews["spawn"].block
								if strings.HasPrefix(token, "acp_exploration_stable:") {
									clicked = b.toggleExplorationExpanded(strings.TrimPrefix(token, "acp_exploration_stable:"))
								} else {
									clicked = b.toggleToolPanelClick(strings.TrimPrefix(token, "acp_tool_panel:"))
								}
							} else {
								clicked = m.tryToggleFoldToken(block.BlockID(), token)
							}
							if !clicked {
								t.Fatalf("rendered disclosure not routed: %q", token)
							}
						}
						before := physical()
						t.Logf("collapsed production viewport\n%s", before)
						if strings.Contains(before, "SUMMARY_FIRST") {
							t.Fatal("selected result body starts expanded: " + before)
						}
						if scope != "missing-parent" {
							if !strings.Contains(before, "Called tools") {
								t.Fatal("missing script group: " + before)
							}
							for _, hidden := range []string{"Read", "View", "service__lookup", "Edit", "Result", "CHILD_RESULT"} {
								if strings.Contains(before, hidden) {
									t.Fatalf("collapsed group exposed %q: %s", hidden, before)
								}
							}
							click(acpStableExplorationClickToken("tools:child0"))
						}
						before = physical()
						for _, want := range []string{"Read", "View", "service__lookup", "Edit", "Result"} {
							if !strings.Contains(before, want) {
								t.Fatalf("missing %q: %s", want, before)
							}
						}
						for _, bad := range []string{"Ran script", "js_exec", "SOURCE_MUST_NOT_RENDER", "script-source.js", "[failed]", "│", "SUMMARY_FIRST"} {
							if strings.Contains(before, bad) {
								t.Fatalf("unexpected script content %q: %s", bad, before)
							}
						}
						if failed && !strings.Contains(before, "failed") {
							t.Fatal("real tool failure hidden")
						}
						if scope != "missing-parent" {
							for i := 0; i < 4; i++ {
								if strings.Contains(before, fmt.Sprintf("CHILD_BODY_%d", i)) {
									t.Fatal("child result body starts expanded: " + before)
								}
							}
							click(acpToolPanelClickToken("child0"))
							if !strings.Contains(physical(), "CHILD_BODY_0") || strings.Contains(physical(), "SUMMARY_FIRST") {
								t.Fatal("child disclosure did not independently reveal its result: " + physical())
							}
						}
						click(acpToolPanelClickToken("script"))
						if !strings.Contains(physical(), "SUMMARY_FIRST") {
							t.Fatal("first result click did not reveal selected output: " + physical())
						}
						if strings.Contains(physical(), "SUMMARY_MIDDLE") {
							t.Fatal("result preview unexpectedly shows full output: " + physical())
						}
						click(acpToolPanelClickToken("script"))
						if !strings.Contains(physical(), "SUMMARY_MIDDLE") || !strings.Contains(physical(), "SUMMARY_LAST") || strings.Contains(physical(), "SOURCE_MUST_NOT_RENDER") {
							t.Fatal(physical())
						}
						if len(kept) != 5 {
							t.Fatalf("deleted/duplicated audit events: %d", len(kept))
						}
					})
				}
			}
		}
	}
}

func TestResultOnlyZeroChildrenAndHistoricalErrorsRemainAccessible(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		failed bool
	}{
		{name: "summary", output: "SELECTED_SUMMARY"},
		{name: "literal completed", output: "completed"},
		{name: "error", output: "user-script.js:3: user failure", failed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, historical := range []bool{false, true} {
				b := NewMainACPTurnBlock("no-child")
				b.UpdateToolWithMeta("script", "js_exec", "JavaScript", test.output, true, test.failed, ToolUpdateMeta{FullArgs: `{"file":"user-script.js"}`})
				b.Status = "completed"
				ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(60)
				render := func() []RenderedRow {
					if historical {
						return renderHistoricalTurn(b, ctx)
					}
					return b.Render(ctx)
				}
				rows := render()
				text := joinRenderedPlain(rows)
				if !strings.Contains(text, "Result") || strings.Contains(text, test.output) {
					t.Fatal("result/error should start behind its own header: " + text)
				}
				found := false
				for _, row := range rows {
					found = found || row.ClickToken == acpToolPanelClickToken("script")
				}
				if !found || !b.toggleToolPanelClick("script") {
					t.Fatal("no working result/error disclosure")
				}
				text = joinRenderedPlain(render())
				if !strings.Contains(text, test.output) || strings.Contains(text, "Explored") || strings.Contains(text, "Ran script") || strings.Contains(text, "[failed]") {
					t.Fatal(text)
				}
			}
		})
	}
}

func TestResultOnlyPreservesTargetApprovalAndIgnoresLateRunning(t *testing.T) {
	b := NewMainACPTurnBlock("review")
	b.UpdateTool("script", "js_exec", "JavaScript", "", false, false)
	b.UpdateToolWithMeta("child", "service__lookup", "query", "denied", true, true, ToolUpdateMeta{ParentToolCallID: "script", ParentToolName: "js_exec", ToolStatusExplicit: true, ToolStatus: "failed"})
	b.AddApprovalReviewEvent("child", "service__lookup", "query", "denied", "GUARDIAN_REASON")
	b.UpdateToolWithMeta("script", "js_exec", "JavaScript", "SELECTED_FAILURE", true, true, ToolUpdateMeta{FullArgs: `{"code":"SOURCE_MUST_NOT_RENDER"}`, ToolStatusExplicit: true, ToolStatus: "failed"})
	ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(60)
	before := joinRenderedPlain(b.Render(ctx))
	if !strings.Contains(before, "Called tools failed") || strings.Contains(before, "GUARDIAN_REASON") || strings.Contains(before, "SELECTED_FAILURE") {
		t.Fatal("group should start collapsed: " + before)
	}
	groupToken := acpStableExplorationClickToken("tools:child")
	found := false
	for _, row := range b.Render(ctx) {
		found = found || row.ClickToken == groupToken
	}
	if !found || !b.toggleExplorationExpanded("tools:child") {
		t.Fatal("no working group disclosure")
	}
	before = joinRenderedPlain(b.Render(ctx))
	for _, want := range []string{"service__lookup", "denied", "Result failed"} {
		if !strings.Contains(before, want) {
			t.Fatal(before)
		}
	}
	if strings.Contains(before, "GUARDIAN_REASON") || strings.Contains(before, "SELECTED_FAILURE") {
		t.Fatal("child and selected result bodies should start collapsed: " + before)
	}
	found = false
	for _, row := range b.Render(ctx) {
		found = found || row.ClickToken == acpToolPanelClickToken("child")
	}
	if !found || !b.toggleToolPanelClick("child") {
		t.Fatal("no working target review disclosure")
	}
	before = joinRenderedPlain(b.Render(ctx))
	if strings.Count(before, "GUARDIAN_REASON") != 1 || strings.Contains(before, "SELECTED_FAILURE") {
		t.Fatal("target rationale must remain separate from selected result: " + before)
	}
	found = false
	for _, row := range b.Render(ctx) {
		found = found || row.ClickToken == acpToolPanelClickToken("script")
	}
	if !found || !b.toggleToolPanelClick("script") {
		t.Fatal("no working selected failure disclosure")
	}
	before = joinRenderedPlain(b.Render(ctx))
	if !strings.Contains(before, "SELECTED_FAILURE") || strings.Count(before, "GUARDIAN_REASON") != 1 {
		t.Fatal(before)
	}
	b.UpdateToolWithMeta("script", "js_exec", "JavaScript", "LATE_RUNNING", false, false, ToolUpdateMeta{ToolStatusExplicit: true, ToolStatus: "running"})
	after := joinRenderedPlain(b.Render(ctx))
	if after != before || strings.Contains(after, "SOURCE_MUST_NOT_RENDER") {
		t.Fatalf("late event changed result\n%s\n%s", before, after)
	}
}

func TestResultOnlyFullOutputUsesTheShellBody(t *testing.T) {
	for _, width := range []int{36, 60, 100} {
		m := NewModel(Config{NoColor: true, NoAnimation: true})
		ctx := m.blockRenderContext(width)
		output := strings.Repeat("original output line\n", 8) + "FINAL_OUTPUT"
		opts := acpTranscriptRenderOptions{ToolPanelFullOutput: func(string) bool { return true }}
		selected := renderACPToolResultOnlyRows("result", SubagentEvent{Kind: SEToolCall, CallID: "call", Name: "js_exec", Output: output, Done: true, Err: true}, width, ctx, opts)
		shell := renderACPTerminalLifecycleRows("shell", SubagentEvent{Name: "RunCommand", Args: "echo fixture", FullArgs: "echo fixture", ToolKind: "execute", Done: true}, "call", output, width, ctx, true, true, true, true, opts)
		if len(selected) < 2 || !selected[0].ACPHeader || selected[0].ClickToken != acpToolPanelClickToken("call") || joinRenderedPlain(selected[1:]) != joinRenderedPlain(shell[1:]) {
			t.Fatalf("different common body at %d\n%s\n%s", width, joinRenderedPlain(selected), joinRenderedPlain(shell))
		}
		for _, row := range selected[1:] {
			if row.ACPHeader || row.ClickToken != acpToolPanelClickToken("call") {
				t.Fatal("result body introduced header or lost disclosure")
			}
		}
	}
}
