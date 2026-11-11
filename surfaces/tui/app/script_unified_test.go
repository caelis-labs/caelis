package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func unifiedScriptEvents(completion string) []*session.Event {
	events := consecutiveScriptEvents()[:6]
	events[2].Tool.Status = "failed"
	events[2].Tool.Content = []session.EventToolContent{{Type: "content", Text: "CHILD_ERROR_PRIVATE_DETAIL"}}
	events[5].Tool.Status = "failed"
	events[5].Meta = map[string]any{"code_mode_script_completion": completion}
	events[5].Tool.Content = []session.EventToolContent{{Type: "content", Text: "FINAL_RETURN_PRIVATE_DETAIL"}}
	return events
}
func TestUnifiedJSTrajectoryNeverRendersDetails(t *testing.T) {
	for _, scope := range []string{"main", "child"} {
		for _, replay := range []bool{false, true} {
			m, b := groupedCanonicalModel(t, unifiedScriptEvents("completed"), scope, replay)
			ctx := m.blockRenderContext(60)
			for _, expanded := range []bool{false, true} {
				if expanded {
					clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken("tools:script1"))
				}
				rows := b.Render(ctx)
				text := joinRenderedPlain(rows)
				if !strings.HasPrefix(text, "• JS") || !strings.Contains(text, "Read script1Read.txt") {
					t.Fatal(text)
				}
				for _, hidden := range []string{"Called tools", "Script error", "failed", "Result", "completed", "SOURCE_JS_NEVER", "CHILD_ERROR_PRIVATE_DETAIL", "FINAL_RETURN_PRIVATE_DETAIL", "│"} {
					if strings.Contains(text, hidden) {
						t.Fatalf("%s: %s", hidden, text)
					}
				}
				if strings.Count(text, "└") != 1 {
					t.Fatal(text)
				}
			}
		}
	}
}
func TestUnifiedJSMarkUsesExecutionObservationNotCumulativeError(t *testing.T) {
	for _, completion := range []string{"completed", "failed", ""} {
		m, b := groupedCanonicalModel(t, unifiedScriptEvents(completion), "main", true)
		rows := b.Render(m.blockRenderContext(60))
		if !strings.HasPrefix(rows[0].Plain, "• JS") {
			t.Fatal(rows[0].Plain)
		}
		if (rows[0].acpHeaderMarkTone == acpHeaderMarkDanger) != (completion == "failed") {
			t.Fatalf("completion=%q tone=%v", completion, rows[0].acpHeaderMarkTone)
		}
		if !b.(*MainACPTurnBlock).Events[0].Err {
			t.Fatal("canonical error flag erased")
		}
	}
}
func TestUnifiedJSIndependentDetailDrawer(t *testing.T) {
	m, b := groupedCanonicalModel(t, unifiedScriptEvents("completed"), "main", false)
	ctx := m.blockRenderContext(60)
	before := joinRenderedPlain(b.Render(ctx))
	m.btwOverlay = &textDrawerState{Question: "BTW owner", Answer: "original BTW"}
	if !m.tryToggleFoldToken(b.BlockID(), "tool_details:script1") {
		t.Fatal("root detail unavailable")
	}
	drawer := m.renderPrimaryDrawer()
	if !strings.Contains(drawer, "SOURCE_JS_NEVER") {
		t.Fatal(drawer)
	}
	m.handleBTWOverlayKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if !strings.Contains(m.renderPrimaryDrawer(), "FINAL_RETURN_PRIVATE_DETAIL") {
		t.Fatal("scroll did not reach original output")
	}
	if joinRenderedPlain(b.Render(ctx)) != before {
		t.Fatal("detail polluted trajectory")
	}
	m.applyBTWOverlayImmediate("continued BTW", true)
	if !strings.Contains(m.activeTextDrawer().Answer, "FINAL_RETURN_PRIVATE_DETAIL") {
		t.Fatal("BTW wrote into tool detail")
	}
	m.handleBTWOverlayKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !strings.Contains(m.renderPrimaryDrawer(), "continued BTW") {
		t.Fatal("detail close lost BTW owner")
	}
	if !m.tryToggleFoldToken(b.BlockID(), "tool_details:script1Read") || !strings.Contains(m.activeTextDrawer().Answer, "CHILD_ERROR_PRIVATE_DETAIL") {
		t.Fatal("child detail unavailable")
	}
}

func TestUnifiedJSIndependentGroupsHistoryAndNarrow(t *testing.T) {
	for _, scope := range []string{"main", "child"} {
		for _, replay := range []bool{false, true} {
			for _, width := range []int{36, 60, 100} {
				m, b := groupedCanonicalModel(t, consecutiveScriptEvents(), scope, replay)
				ctx := m.blockRenderContext(width)
				for _, historical := range []bool{false, true} {
					rows := b.Render(ctx)
					if historical {
						switch block := b.(type) {
						case *MainACPTurnBlock:
							rows = renderHistoricalTurn(block, ctx)
						}
					}
					text := joinRenderedPlain(rows)
					if strings.Count(text, "• JS") != 2 || !strings.Contains(text, "script1Read") || !strings.Contains(text, "script2Read") || (!historical && !strings.Contains(text, "BETWEEN_SCRIPT_STEPS")) {
						t.Fatal(text)
					}
					if strings.Count(text, "└") != 2 || strings.Contains(text, "FINAL_SUMMARY") || strings.Contains(text, "SOURCE_JS_NEVER") {
						t.Fatal(text)
					}
				}
				clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken("tools:script1"))
				if !m.openToolDetails(b.BlockID(), "script2") || !strings.Contains(m.activeTextDrawer().Answer, "FINAL_SUMMARY") {
					t.Fatal("main/child original result detail lost")
				}
				for _, row := range b.Render(ctx) {
					if row.PreWrapped && displayColumns(row.Plain) > width {
						t.Fatalf("wide %q", row.Plain)
					}
				}
			}
		}
	}
}
func TestUnifiedJSZeroChildrenAndActualTargetReview(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	b := NewMainACPTurnBlock("zero")
	for _, done := range []bool{false, true} {
		b.UpdateToolWithMeta("zero", "js_exec", "", "OWN_SCRIPT_STACK", done, true, ToolUpdateMeta{ScriptCompletion: "failed"})
		text := joinRenderedPlain(b.Render(m.blockRenderContext(60)))
		if !strings.HasPrefix(text, "• JS") || strings.Contains(text, "OWN_SCRIPT_STACK") {
			t.Fatal(text)
		}
	}
	model, block := groupedCanonicalModel(t, unifiedScriptEvents("completed"), "main", true)
	root := block.(*MainACPTurnBlock)
	root.AddApprovalReviewEvent("script1Read", "Read", "original args", "denied", "ACTUAL_TARGET_RATIONALE")
	ctx := model.blockRenderContext(60)
	clickScriptToken(t, model, root, ctx, acpStableExplorationClickToken("tools:script1"))
	text := joinRenderedPlain(root.Render(ctx))
	if !strings.Contains(text, "[denied]") || strings.Contains(text, "ACTUAL_TARGET_RATIONALE") {
		t.Fatal(text)
	}
	if !model.openToolDetails(root.BlockID(), "script1Read") || !strings.Contains(model.activeTextDrawer().Answer, "ACTUAL_TARGET_RATIONALE") {
		t.Fatal("target rationale lost")
	}
	before := joinRenderedPlain(root.Render(ctx))
	root.UpdateToolWithMeta("script1Read", "Read", "", "LATE_RUNNING", false, false, ToolUpdateMeta{ParentToolName: "js_exec", ParentToolCallID: "script1"})
	if joinRenderedPlain(root.Render(ctx)) != before {
		t.Fatal("late event reopened terminal call")
	}
}

func TestUnifiedJSUsesExploredSummaryColumns(t *testing.T) {
	children := []SubagentEvent{
		{Kind: SEToolCall, CallID: "one", Name: "Read", Args: "docs/first.txt", Done: true},
		{Kind: SEToolCall, CallID: "two", Name: "Read", Args: "docs/second.txt", Done: true},
	}
	for _, width := range []int{36, 60, 100} {
		m := NewModel(Config{})
		ctx := m.blockRenderContext(width)
		events := []SubagentEvent{{Kind: SEToolCall, CallID: "root", Name: "js_exec", Done: true, ScriptCompletion: "completed"}}
		for _, child := range children {
			child.ParentToolCallID = "root"
			child.ParentToolName = "js_exec"
			events = append(events, child)
		}
		rows, _, _ := renderInvocationToolGroupRows("b", events, 0, width, ctx, acpTranscriptRenderOptions{})
		expected := explorationGroupDetailRowsWithWorkspaceMode(children, width, ctx.Workspace, explorationToolDetailLiveSummary)
		if len(rows)-1 != len(expected) {
			t.Fatal("different public summary wrapping")
		}
		for i, line := range expected {
			if rows[i+1].Plain != line {
				t.Fatalf("width %d: JS %q Explored %q", width, rows[i+1].Plain, line)
			}
		}
	}
}

func TestUnifiedJSDetailHitRangeMatchesRenderedLabel(t *testing.T) {
	m, b := groupedCanonicalModel(t, unifiedScriptEvents("completed"), "main", false)
	ctx := m.blockRenderContext(60)
	rows := b.Render(ctx)
	row := rows[0]
	wrapped := m.wrapRenderedRowsForViewport(b, rows, 60, ctx)
	bound := wrapped.clickBounds[0]
	if got := sliceByDisplayColumns(wrapped.plainLines[0], bound.start, bound.end); got != "details" {
		t.Fatalf("detail hit range %q on %q", got, row.Plain)
	}
	if row.ClickToken != "tool_details:script1" || row.ClickTokenAlt != acpStableExplorationClickToken("tools:script1") {
		t.Fatal("detail and folding targets conflated")
	}
}
