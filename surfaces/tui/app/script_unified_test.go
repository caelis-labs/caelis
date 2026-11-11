package tuiapp

import (
	"strings"
	"testing"

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
				if !strings.HasPrefix(text, "• Ran script") || !strings.Contains(text, "Read script1Read.txt") {
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
		if !strings.HasPrefix(rows[0].Plain, "• Ran script") {
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
					if strings.Count(text, "• Ran script") != 2 || !strings.Contains(text, "script1Read") || !strings.Contains(text, "script2Read") || (!historical && !strings.Contains(text, "BETWEEN_SCRIPT_STEPS")) {
						t.Fatal(text)
					}
					if strings.Count(text, "└") != 2 || strings.Contains(text, "FINAL_SUMMARY") || strings.Contains(text, "SOURCE_JS_NEVER") {
						t.Fatal(text)
					}
				}
				clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken("tools:script1"))
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
		want := "• Running script"
		if done {
			want = "• Ran script"
		}
		if !strings.HasPrefix(text, want) || strings.Contains(text, "OWN_SCRIPT_STACK") {
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
