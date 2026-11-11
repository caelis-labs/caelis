package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Canonical synthetic receipts traverse Control and the real main/child reducer
// and click route. This is not evidence of business tools or a model running.
func TestScriptResultsKeepOriginalInvocationAndChildDisclosures(t *testing.T) {
	for _, scope := range []string{"main", "child"} {
		for _, replay := range []bool{false, true} {
			for _, file := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/replay%v/file%v", scope, replay, file), func(t *testing.T) {
					events := consecutiveScriptEvents()
					last := events[len(events)-1]
					last.Tool.Content = []session.EventToolContent{{Type: "content", Text: "SUMMARY_FIRST\n" + strings.Repeat("summary line\n", 10) + "SUMMARY_MIDDLE\n" + strings.Repeat("tail line\n", 10) + "SUMMARY_LAST"}}
					if file {
						events[0].Tool.Input = map[string]any{"file": "/owned/user-script.js"}
					}
					for _, ev := range events {
						if ev.Tool != nil && ev.Tool.ID == "script2Read" && ev.Type == session.EventTypeToolResult {
							ev.Tool.Content = []session.EventToolContent{{Type: "content", Text: "ORIGINAL_CHILD_BODY"}}
						}
					}
					m, b := groupedCanonicalModel(t, events, scope, replay)
					ctx := m.blockRenderContext(60)
					text := joinRenderedPlain(b.Render(ctx))
					for _, bad := range []string{"Result", "completed", "SUMMARY_FIRST", "SOURCE_JS_NEVER", "user-script.js", "│"} {
						if strings.Contains(text, bad) {
							t.Fatalf("collapsed noise %s: %s", bad, text)
						}
					}
					clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken("tools:script2"))
					text = joinRenderedPlain(b.Render(ctx))
					if !strings.Contains(text, "SUMMARY_FIRST") || strings.Contains(text, "SUMMARY_MIDDLE") || strings.Contains(text, "ORIGINAL_CHILD_BODY") {
						t.Fatal(text)
					}
					clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("script2Read"))
					text = joinRenderedPlain(b.Render(ctx))
					if !strings.Contains(text, "ORIGINAL_CHILD_BODY") {
						t.Fatal(text)
					}
					for i := 0; i < 2; i++ {
						clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("script2"))
					}
					text = joinRenderedPlain(b.Render(ctx))
					if !strings.Contains(text, "SUMMARY_MIDDLE") || !strings.Contains(text, "SUMMARY_LAST") {
						t.Fatal(text)
					}
					clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken("tools:script2"))
					text = joinRenderedPlain(b.Render(ctx))
					if strings.Contains(text, "SUMMARY_FIRST") || strings.Contains(text, "ORIGINAL_CHILD_BODY") {
						t.Fatal(text)
					}
				})
			}
		}
	}
}
func TestResultOnlyZeroChildrenAndHistoricalErrorsRemainAccessible(t *testing.T) {
	for _, failed := range []bool{false, true} {
		for _, historical := range []bool{false, true} {
			b := NewMainACPTurnBlock("no-child")
			output := "ORIGINAL_VALUE"
			if failed {
				output = "user-script.js:3: user error"
			}
			b.UpdateToolWithMeta("script", "js_exec", "JavaScript", output, true, failed, ToolUpdateMeta{FullArgs: `{"file":"user-script.js"}`})
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
			if strings.Contains(text, "Result") || strings.Contains(text, "[failed]") || !strings.Contains(text, output) {
				t.Fatal(text)
			}
			found := false
			for _, row := range rows {
				found = found || row.ClickToken == acpToolPanelClickToken("script")
			}
			if !found || !b.toggleToolPanelClick("script") {
				t.Fatal("missing original invocation disclosure")
			}
			if !strings.Contains(joinRenderedPlain(render()), output) {
				t.Fatal("original output disappeared")
			}
			if !b.Events[0].Err && failed {
				t.Fatal("canonical failure flag lost")
			}
		}
	}
}
func TestResultOnlyPreservesTargetApprovalAndIgnoresLateRunning(t *testing.T) {
	b := NewMainACPTurnBlock("review")
	b.UpdateTool("script", "js_exec", "JavaScript", "", false, false)
	b.UpdateToolWithMeta("child", "service__lookup", "query", "denied", true, true, ToolUpdateMeta{ParentToolCallID: "script", ParentToolName: "js_exec", ToolStatusExplicit: true, ToolStatus: "failed"})
	b.AddApprovalReviewEvent("child", "service__lookup", "query", "denied", "GUARDIAN_REASON")
	b.UpdateToolWithMeta("script", "js_exec", "JavaScript", "SELECTED_FAILURE", true, true, ToolUpdateMeta{ToolStatusExplicit: true, ToolStatus: "failed"})
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	ctx := m.blockRenderContext(60)
	before := joinRenderedPlain(b.Render(ctx))
	if strings.Contains(before, "failed") || !strings.Contains(before, "service__lookup") || strings.Contains(before, "GUARDIAN_REASON") {
		t.Fatal(before)
	}
	if !b.toggleExplorationExpanded("tools:script") {
		t.Fatal("group disclosure")
	}
	before = joinRenderedPlain(b.Render(ctx))
	if !strings.Contains(before, "[denied]") || !strings.Contains(before, "SELECTED_FAILURE") {
		t.Fatal(before)
	}
	if !b.toggleToolPanelClick("child") {
		t.Fatal("target disclosure")
	}
	before = joinRenderedPlain(b.Render(ctx))
	if strings.Count(before, "GUARDIAN_REASON") != 1 {
		t.Fatal(before)
	}
	b.UpdateToolWithMeta("script", "js_exec", "JavaScript", "LATE_RUNNING", false, false, ToolUpdateMeta{ToolStatusExplicit: true, ToolStatus: "running"})
	if after := joinRenderedPlain(b.Render(ctx)); after != before {
		t.Fatalf("late event changed terminal display\n%s\n%s", before, after)
	}
}
func TestScriptResultUsesCommonExplorationColumns(t *testing.T) {
	for _, width := range []int{36, 60, 100} {
		ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(width)
		output := "original output\nsecond line"
		opts := acpTranscriptRenderOptions{ToolPanelFullOutput: func(string) bool { return true }}
		selected := renderACPToolResultOnlyRows("result", SubagentEvent{Kind: SEToolCall, CallID: "call", Name: "js_exec", Output: output, Done: true, Err: true}, width, ctx, opts)
		common := renderExplorationNarrativeRows("result", output, width, ctx, ctx.Theme.ToolErrorStyle(), acpToolPanelClickToken("call"), true)
		if len(selected) < 2 || joinRenderedPlain(selected[1:]) != joinRenderedPlain(common) {
			t.Fatalf("different public columns at %d\n%s\n%s", width, joinRenderedPlain(selected), joinRenderedPlain(common))
		}
	}
}
