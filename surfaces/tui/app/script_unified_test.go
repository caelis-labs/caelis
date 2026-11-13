package tuiapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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

func TestUnifiedJSUsesSingleLineSummaryColumns(t *testing.T) {
	for _, width := range []int{24, 36, 60, 100} {
		m := NewModel(Config{})
		ctx := m.blockRenderContext(width)
		events := []SubagentEvent{
			{Kind: SEToolCall, CallID: "root", Name: "js_exec", Done: true},
			{Kind: SEToolCall, CallID: "one", Name: "Read", Args: "docs/first.txt", ParentToolCallID: "root", ParentToolName: "js_exec", Done: true},
			{Kind: SEToolCall, CallID: "two", Name: "Read", Args: "docs/second.txt", ParentToolCallID: "root", ParentToolName: "js_exec", Done: true},
		}
		rows, _, _ := renderInvocationToolGroupRows("b", events, 0, width, ctx, acpTranscriptRenderOptions{})
		want := "  └ Read " + compactSingleLineHeader("first.txt, second.txt", width-9)
		if len(rows) != 2 || rows[1].Plain != want || !rows[1].PreWrapped {
			t.Fatalf("width %d: %#v", width, rows)
		}
	}
}

func longScriptArgumentEvents(status string) []*session.Event {
	out := consecutiveScriptEvents()[:6]
	for _, event := range out {
		if event.Tool.Name == "Read" {
			event.Tool.Name = "context7__query_docs"
			event.Tool.Input = map[string]any{"libraryId": "/caelis/docs", "query": strings.Repeat("context details ", 18) + "EXACT_CONTEXT7_MIDDLE" + strings.Repeat(" documentation", 18)}
		}
		if event.Tool.Name == "desktop_world__exec" {
			event.Tool.Input = map[string]any{"app": "fixture.app", "script": strings.Repeat("await app.look();\n", 18) + "EXACT_DTW_MIDDLE" + strings.Repeat(" await app.look();", 18)}
		}
		if event.Type == session.EventTypeToolResult {
			event.Tool.Status = status
			if status == "started" {
				event.Type = session.EventTypeToolCall
			}
		}
		event.Tool.Content = []session.EventToolContent{{Type: "content", Text: "PRIVATE_RESULT_BODY"}}
	}
	return out
}

func scriptBlockEvents(b Block) []SubagentEvent {
	switch block := b.(type) {
	case *MainACPTurnBlock:
		return block.Events
	case *ParticipantTurnBlock:
		return block.Events
	}
	return nil
}

func TestUnifiedJSLongArgumentsLiveReplayDisclosure(t *testing.T) {
	for _, scope := range []string{"main", "child"} {
		for _, replay := range []bool{false, true} {
			for _, status := range []string{"started", "completed", "failed"} {
				for _, width := range []int{24, 36, 60, 100} {
					t.Run(fmt.Sprintf("%s/replay=%v/%s/%d", scope, replay, status, width), func(t *testing.T) {
						canonical := longScriptArgumentEvents(status)
						m, b := groupedCanonicalModel(t, canonical, scope, replay)
						ctx := m.blockRenderContext(width)
						compact := b.Render(ctx)
						if len(compact) != 3 {
							t.Fatalf("calls wrapped: %s", joinRenderedPlain(compact))
						}
						for _, row := range compact {
							if displayColumns(row.Plain) > width || ansi.StringWidth(row.Styled) > width || strings.Contains(row.Plain, "\n") {
								t.Fatalf("wrapped row: %q", row.Plain)
							}
						}
						for _, call := range []struct {
							id, marker string
							index      int
						}{{"script1Read", "EXACT_CONTEXT7_MIDDLE", 1}, {"script1desktop_world__exec", "EXACT_DTW_MIDDLE", 3}} {
							original, err := json.MarshalIndent(canonical[call.index].Tool.Input, "", "  ")
							if err != nil {
								t.Fatal(err)
							}
							ev, ok := latestToolEventForCallID(scriptBlockEvents(b), call.id)
							if !ok || ev.FullArgs != string(original) {
								t.Fatalf("original arguments lost: %q != %q", ev.FullArgs, original)
							}
							if strings.Contains(joinRenderedPlain(compact), call.marker) {
								t.Fatal("middle not folded")
							}
							clickScriptToken(t, m, b, ctx, acpToolPanelClickToken(call.id))
							full := joinRenderedPlain(b.Render(ctx))
							// Wrapping can split the marker; remove display whitespace.
							if !strings.Contains(strings.Join(strings.Fields(full), ""), call.marker) {
								t.Fatalf("missing original args: %s", full)
							}
							if strings.Contains(full, "PRIVATE_RESULT_BODY") || strings.Contains(full, "SOURCE_JS_NEVER") {
								t.Fatal(full)
							}
							clickScriptToken(t, m, b, ctx, acpToolPanelClickToken(call.id))
							if got := joinRenderedPlain(b.Render(ctx)); got != joinRenderedPlain(compact) {
								t.Fatalf("collapse differs: %s", got)
							}
							retained, _ := latestToolEventForCallID(scriptBlockEvents(b), call.id)
							if retained.FullArgs != string(original) {
								t.Fatal("click changed original arguments")
							}
						}
						clickScriptToken(t, m, b, ctx, acpStableExplorationClickToken("tools:script1"))
						if len(b.Render(ctx)) != 3 {
							t.Fatal("root expansion wrapped calls")
						}
						clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("script1Read"))
						if len(b.Render(ctx)) <= 3 {
							t.Fatal("expanded root lost child disclosure")
						}
					})
				}
			}
		}
	}
}

func TestUnifiedJSLongArgumentsPhysicalMouseAndGolden(t *testing.T) {
	for _, width := range []int{24, 36, 60} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m, b := groupedCanonicalModel(t, longScriptArgumentEvents("completed"), "main", true)
			m.Update(tea.WindowSizeMsg{Width: width, Height: 35})
			m.syncViewportContent()
			ctx := m.blockRenderContext(width)
			path := filepath.Join("testdata", fmt.Sprintf("script_args_%d.golden", width))
			got := joinRenderedPlain(b.Render(ctx)) + "\n"

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("%s mismatch:\n%s", path, got)
			}
			frames := []string{m.View().Content}
			click := func() {
				t.Helper()
				for line, token := range m.viewportClickTokens {
					if token == acpToolPanelClickToken("script1Read") {
						clickViewportColumn(t, m, line, 5)
						return
					}
				}
				t.Fatal("call has no viewport mouse target")
			}
			click()
			if !b.(*MainACPTurnBlock).toolPanelFullOutput("script1Read") {
				t.Fatal("mouse did not expand arguments")
			}
			frames = append(frames, m.View().Content)
			click()
			if b.(*MainACPTurnBlock).toolPanelFullOutput("script1Read") {
				t.Fatal("mouse did not collapse arguments")
			}
			frames = append(frames, m.View().Content)
			updates := renderFullscreenFramesForTest(t, width, 35, frames...)
			for n, frame := range frames {
				assertPhysicalFullscreenFrame(t, width, 35, frame, updates[:n+1])
			}
		})
	}
}

func TestUnifiedJSAggregateLongCallsAndNames(t *testing.T) {
	for _, width := range []int{8, 24, 36} {
		m := NewModel(Config{})
		ctx := m.blockRenderContext(width)
		b := NewMainACPTurnBlock("aggregate")
		b.UpdateToolWithMeta("root", "js_exec", "", "", true, false, ToolUpdateMeta{})
		name := "desktop_world__exec_with_a_very_long_tool_name"
		args := strings.Repeat("argument ", 25) + "TAIL"
		for _, id := range []string{"one", "two"} {
			b.UpdateToolWithMeta(id, name, args, "PRIVATE_BODY", true, false, ToolUpdateMeta{ParentToolCallID: "root", ParentToolName: "js_exec", FullArgs: args})
		}
		rows := b.Render(ctx)
		if len(rows) != 2 || rows[1].ClickToken != acpStableExplorationClickToken("tools:root") {
			t.Fatal(joinRenderedPlain(rows))
		}
		if !b.toggleExplorationExpanded("tools:root") {
			t.Fatal("root toggle failed")
		}
		rows = b.Render(ctx)
		if len(rows) != 3 {
			t.Fatal("aggregate did not reveal individual calls")
		}
		for _, row := range rows[1:] {
			if displayColumns(row.Plain) > width || ansi.StringWidth(row.Styled) > width {
				t.Fatalf("long name wrapped: %q", row.Plain)
			}
		}
		if !b.toggleToolPanelClick("one") {
			t.Fatal("call toggle failed")
		}
		if !b.toolPanelFullOutput("one") || b.toolPanelFullOutput("two") {
			t.Fatal("child click changed sibling")
		}
		b.toggleExplorationExpanded("tools:root")
		b.UpdateToolWithMeta("three", name, args, "", true, false, ToolUpdateMeta{ParentToolCallID: "root", ParentToolName: "js_exec", FullArgs: args})
		found := false
		for _, row := range b.Render(ctx) {
			if row.ClickToken == acpToolPanelClickToken("one") {
				found = true
			}
		}
		if !found || len(b.Render(ctx)) <= 3 {
			t.Fatal("adjacent arrival hid explicitly opened arguments")
		}
		b.UpdateToolWithMeta("empty", "empty_tool", "", "PRIVATE_BODY", true, false, ToolUpdateMeta{ParentToolCallID: "root", ParentToolName: "js_exec"})
		for _, row := range b.Render(ctx) {
			if row.ClickToken == acpToolPanelClickToken("empty") {
				t.Fatal("empty call offered argument expansion")
			}
		}
	}
}

func TestUnifiedJSArgumentClicksAcrossCompletion(t *testing.T) {
	for _, scope := range []string{"main", "child"} {
		m, b := groupedCanonicalModel(t, longScriptArgumentEvents("started"), scope, false)
		ctx := m.blockRenderContext(36)
		for cycle := 0; cycle < 3; cycle++ {
			clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("script1Read"))
			if cycle == 0 {
				meta := ToolUpdateMeta{ParentToolCallID: "script1", ParentToolName: "js_exec"}
				switch block := b.(type) {
				case *MainACPTurnBlock:
					block.UpdateToolWithMeta("script1Read", "context7__query_docs", "", "PRIVATE_BODY", true, false, meta)
				case *ParticipantTurnBlock:
					block.UpdateToolWithMeta("script1Read", "context7__query_docs", "", "PRIVATE_BODY", true, false, meta)
				}
			}
			full := strings.Join(strings.Fields(joinRenderedPlain(b.Render(ctx))), "")
			if !strings.Contains(full, "EXACT_CONTEXT7_MIDDLE") {
				t.Fatal("single click did not expand original arguments")
			}
			clickScriptToken(t, m, b, ctx, acpToolPanelClickToken("script1Read"))
			if len(b.Render(ctx)) != 3 {
				t.Fatal("single click did not collapse arguments")
			}
		}
	}
}
