package tuiapp

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func codeModeBlockForTest() *MainACPTurnBlock {
	b := NewMainACPTurnBlock("code-mode-turn")
	b.UpdateToolWithMeta("script", "js_exec", "JavaScript", "", false, false, ToolUpdateMeta{FullArgs: `{"code":"const x = 'SCRIPT_TAIL'; return x;"}`})
	b.UpdateToolWithMeta("search", "tool_search", `"capture"`, "two candidates", true, false, ToolUpdateMeta{ParentToolCallID: "script", ParentToolName: "js_exec"})
	b.UpdateToolWithMeta("capture", "desktop_world__exec", `app="自有 fixture"`, "", false, false, ToolUpdateMeta{ParentToolCallID: "script", ParentToolName: "js_exec", ToolKind: "execute", ToolTitle: "Observe"})
	return b
}

func TestCodeModeDisclosureKeepsCallIdentityScriptAndOutput(t *testing.T) {
	b := codeModeBlockForTest()
	ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(100)
	b.UpdateTool("script", "js_exec", "JavaScript", "FINAL_SCRIPT_OUTPUT", true, false)
	for range 2 {
		if !b.toggleToolPanelClick("script") {
			t.Fatal("script panel click unavailable")
		}
	}
	text := joinRenderedPlain(b.Render(ctx))
	for _, want := range []string{"Ran script", "SCRIPT_TAIL", "FINAL_SCRIPT_OUTPUT"} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
	for _, hidden := range []string{"desktop_world__exec", "tool_search", "Code Mode", "JavaScript"} {
		if strings.Contains(text, hidden) {
			t.Fatal(text)
		}
	}
	if len(b.Events) != 3 || b.Events[2].ParentToolCallID != "script" {
		t.Fatal("render mutated child identity")
	}
}

func TestCodeModeFailureApprovalOrphanAndNativeBoundaries(t *testing.T) {
	b := codeModeBlockForTest()
	b.AddApprovalReviewEvent("capture", "desktop_world__exec", "own fixture", "denied", "Not permitted")
	b.UpdateToolWithMeta("capture", "desktop_world__exec", "fixture", "denied", true, true, ToolUpdateMeta{ParentToolCallID: "script", ParentToolName: "js_exec"})
	b.UpdateTool("script", "js_exec", "JavaScript", "denied", true, true)
	b.UpdateTool("native", "Read", "native.txt", "NATIVE_RESULT", true, false)
	b.UpdateToolWithMeta("orphan", "service__lookup", "query", "ORPHAN_RESULT", true, false, ToolUpdateMeta{ParentToolCallID: "not-in-this-turn", ParentToolName: "js_exec"})
	b.Status = "completed"
	ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(100)
	text := joinRenderedPlain(b.Render(ctx))
	for _, want := range []string{"Ran script", "[failed]", "denied", "Not permitted", "native.txt", "service__lookup"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if strings.Count(text, "Not permitted") != 1 {
		t.Fatal("approval duplicated")
	}
	b.toggleToolPanelClick("script")
	if strings.Count(joinRenderedPlain(b.Render(ctx)), "Not permitted") != 1 {
		t.Fatal("opening group duplicated approval")
	}
}

func TestCodeModeLateTaskUpdatesRetainOneGroupAndDisclosure(t *testing.T) {
	b := codeModeBlockForTest()
	b.toggleToolPanelClick("script")
	meta := ToolUpdateMeta{ParentToolCallID: "script", ParentToolName: "js_exec", ToolStatusExplicit: true, ToolStatus: "completed"}
	b.UpdateToolWithMeta("capture", "desktop_world__exec", "fixture", "finished", true, false, meta)
	b.UpdateTool("script", "js_exec", "JavaScript", "finished", true, false)
	b.Status = "completed"
	meta.ToolStatus = "running"
	b.UpdateToolWithMeta("capture", "desktop_world__exec", "fixture", "late running snapshot", false, false, meta)
	ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(60)
	text := joinRenderedPlain(b.Render(ctx))
	if strings.Count(text, "Ran script") != 1 || strings.Contains(text, " calls") || strings.Contains(text, "late running snapshot") {
		t.Fatalf("late Task snapshot reopened or duplicated work: %s", text)
	}
}

func TestCodeModeNarrowScriptWrapAndCopy(t *testing.T) {
	b := codeModeBlockForTest()
	b.UpdateTool("script", "js_exec", "JavaScript", "finished", true, false)
	b.Status = "completed"
	for i := 0; i < 3 && !b.toolPanelFullOutput("script"); i++ {
		if !b.toggleToolPanelClick("script") {
			t.Fatal("script details unavailable")
		}
	}
	for _, width := range []int{60, 36} {
		rows := b.Render(NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(width))
		var lines []string
		var indents []int
		for _, row := range rows {
			physical := []string{row.Plain}
			if row.ACPHeader {
				if wrapped, _, ok := wrapACPTranscriptHeaderForViewport(row.Plain, width, NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(width), row.acpHeaderMarkTone, row.acpHeaderMarkDim); ok {
					physical = wrapped
				}
			}
			for _, line := range physical {
				if displayColumns(line) > width {
					t.Fatalf("wide physical row %d: %q", width, line)
				}
				lines = append(lines, line)
				indents = append(indents, row.selectionIndent)
			}
		}
		if len(lines) == 0 {
			t.Fatal("no script rows")
		}
		copied := selectionTextFromLinesWithIndents(lines, indents, textSelectionPoint{line: 0, col: 0}, textSelectionPoint{line: len(lines) - 1, col: displayColumns(lines[len(lines)-1])})
		if !strings.Contains(copied, "SCRIPT_TAIL") || strings.Contains(copied, "desktop_world__exec") {
			t.Fatal(copied)
		}
	}
}

func TestCodeModeScriptSummaryOmitsCountsAndDuration(t *testing.T) {
	b := NewMainACPTurnBlock("timed-code-mode")
	start := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	b.UpdateToolWithMeta("script", "js_exec", "JavaScript", "", false, false, ToolUpdateMeta{OccurredAt: start, ToolStatusExplicit: true, ToolStatus: "running"})
	b.UpdateToolWithMeta("script", "js_exec", "JavaScript", "finished", true, false, ToolUpdateMeta{OccurredAt: start.Add(2 * time.Second), ToolStatusExplicit: true, ToolStatus: "completed"})
	b.UpdateToolWithMeta("script", "js_exec", "JavaScript", "late", false, false, ToolUpdateMeta{OccurredAt: start.Add(8 * time.Second), ToolStatusExplicit: true, ToolStatus: "running"})
	text := joinRenderedPlain(b.Render(NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(60)))
	if !strings.Contains(text, "Ran script") || strings.Contains(text, "late") || strings.Contains(text, "2.0s") || strings.Contains(text, "Code Mode") {
		t.Fatalf("duration/lifecycle changed by stale snapshot: %s", text)
	}
}

func TestCodeModeScriptNameAndInlineSourceDisclosure(t *testing.T) {
	for _, file := range []string{"/private/task/probe.js", `C:\task\probe.js`} {
		b := NewMainACPTurnBlock("script-file")
		raw := `{"file":` + strconv.Quote(file) + `}`
		b.UpdateToolWithMeta("script", "js_exec", "JavaScript", "done", true, false, ToolUpdateMeta{FullArgs: raw})
		b.Status = "completed"
		got := joinRenderedPlain(b.Render(NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(36)))
		if !strings.Contains(got, "Ran script probe.js") || strings.Contains(got, "js_exec") || strings.Contains(got, "JavaScript") || strings.Contains(got, "Code Mode") {
			t.Fatalf("file action: %s", got)
		}
		t.Logf("FILE ACTION\n%s", got)
	}
	b := NewMainACPTurnBlock("inline-source")
	code := "const first = 1;\nconst second = 2;\nconst third = 3;\nconst SCRIPT_MIDDLE = 4;\nconst fifth = 5;\nconst sixth = 6;\nconst seventh = 7;\nreturn first;"
	raw, _ := json.Marshal(map[string]any{"code": code})
	b.UpdateToolWithMeta("script", "js_exec", "JavaScript", "done", true, false, ToolUpdateMeta{FullArgs: string(raw)})
	b.Status = "completed"
	ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(60)
	closed := joinRenderedPlain(b.Render(ctx))
	t.Logf("INLINE COLLAPSED\n%s", closed)
	if !strings.Contains(closed, "Ran script const first") || strings.Contains(closed, "SCRIPT_MIDDLE") {
		t.Fatal(closed)
	}
	for i := 0; i < 3 && !b.toolPanelFullOutput("script"); i++ {
		if !b.toggleToolPanelClick("script") {
			t.Fatal("inline source disclosure unavailable")
		}
	}
	full := joinRenderedPlain(b.Render(ctx))
	t.Logf("INLINE EXPANDED\n%s", full)
	if !strings.Contains(full, "SCRIPT_MIDDLE") {
		t.Fatal(full)
	}
}
