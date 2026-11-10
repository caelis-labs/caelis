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
								events = append(events, &session.Event{Type: typ, Visibility: session.VisibilityMirror, Tool: &session.EventTool{ID: fmt.Sprint("child", i), Name: name, Status: status, Parent: parent, Input: map[string]any{"path": "/task/input.txt", "query": "QUERY_MARKER"}, Content: []session.EventToolContent{{Type: "content", Text: "CHILD_RESULT"}}}})
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
							b.collapseAllToolPanels()
							block = b
							kept = b.Events
						}
						ctx := m.blockRenderContext(60)
						physical := func() string {
							return strings.Join(m.renderViewportRowCacheFromRows(block, ctx, viewportRowCache{}, block.Render(ctx)).plainLines, "\n")
						}
						before := physical()
						t.Logf("collapsed production viewport\n%s", before)
						// Successful older children are intentionally folded. Expand via the
						// rendered shared disclosure before asserting every actual action.
						for _, r := range block.Render(ctx) {
							if strings.HasPrefix(r.ClickToken, "acp_exploration_stable:tools:") {
								key := strings.TrimPrefix(r.ClickToken, "acp_exploration_stable:")
								if scope == "child-origin" {
									m.subagentOutputViews["spawn"].block.toggleExplorationExpanded(key)
								} else {
									m.tryToggleFoldToken(block.BlockID(), r.ClickToken)
								}
								break
							}
						}
						before = physical()
						for _, want := range []string{"Read", "View", "service__lookup", "Edit", "SUMMARY_FIRST"} {
							if !strings.Contains(before, want) {
								t.Fatalf("missing %q: %s", want, before)
							}
						}
						for _, bad := range []string{"Ran script", "js_exec", "SOURCE_MUST_NOT_RENDER", "script-source.js", "[failed]", "│"} {
							if strings.Contains(before, bad) {
								t.Fatalf("script decoration %q: %s", bad, before)
							}
						}
						if failed && !strings.Contains(before, "failed") {
							t.Fatal("real tool failure hidden")
						}
						clicked := false
						for i := 0; i < 3; i++ {
							token := ""
							for _, row := range block.Render(ctx) {
								if row.ClickToken == acpToolPanelClickToken("script") {
									token = row.ClickToken
									break
								}
							}
							if token == "" {
								t.Fatal("no rendered final-result entry")
							}
							if scope == "child-origin" {
								clicked = m.subagentOutputViews["spawn"].block.toggleToolPanelClick("script")
							} else {
								clicked = m.tryToggleFoldToken(block.BlockID(), token)
							}
							if !clicked {
								t.Fatal("rendered result click not routed")
							}
							if strings.Contains(physical(), "SUMMARY_MIDDLE") {
								break
							}
						}
						if !strings.Contains(physical(), "SUMMARY_MIDDLE") || strings.Contains(physical(), "SOURCE_MUST_NOT_RENDER") {
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
	for _, failed := range []bool{false, true} {
		b := NewMainACPTurnBlock("no-child")
		output := "SELECTED_SUMMARY"
		if failed {
			output = "user-script.js:3: user failure"
		}
		b.UpdateToolWithMeta("script", "js_exec", "JavaScript", output, true, failed, ToolUpdateMeta{FullArgs: `{"file":"user-script.js"}`})
		b.Status = "completed"
		ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(36)
		for _, rows := range [][]RenderedRow{b.Render(ctx), renderHistoricalTurn(b, ctx)} {
			text := joinRenderedPlain(rows)
			if !strings.Contains(text, output) || strings.Contains(text, "Explored") || strings.Contains(text, "Ran script") || strings.Contains(text, "[failed]") {
				t.Fatal(text)
			}
			found := false
			for _, row := range rows {
				found = found || row.ClickToken == acpToolPanelClickToken("script")
			}
			if !found {
				t.Fatal("no result/error disclosure")
			}
		}
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
	for _, want := range []string{"service__lookup", "denied", "GUARDIAN_REASON", "SELECTED_FAILURE"} {
		if !strings.Contains(before, want) {
			t.Fatal(before)
		}
	}
	if strings.Count(before, "GUARDIAN_REASON") != 1 {
		t.Fatal("duplicated approval: " + before)
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
		if joinRenderedPlain(selected) != joinRenderedPlain(shell[1:]) {
			t.Fatalf("different common body at %d\n%s\n%s", width, joinRenderedPlain(selected), joinRenderedPlain(shell))
		}
		for _, row := range selected {
			if row.ACPHeader || row.ClickToken != acpToolPanelClickToken("call") {
				t.Fatal("result introduced header or lost disclosure")
			}
		}
	}
}
