package tuiapp

import (
	"strings"
	"testing"
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
	closed := joinRenderedPlain(b.Render(ctx))
	if !strings.Contains(closed, "Code Mode · 2 calls") || !strings.Contains(closed, "desktop_world__exec") || strings.Contains(closed, "tool_search") || strings.Contains(closed, "SCRIPT_TAIL") {
		t.Fatalf("running group: %s", closed)
	}
	b.toggleExplorationExpanded("script")
	rows := b.Render(ctx)
	open := joinRenderedPlain(rows)
	for _, want := range []string{"tool_search", "desktop_world__exec", "自有 fixture", "js_exec"} {
		if !strings.Contains(open, want) {
			t.Fatalf("missing %q: %s", want, open)
		}
	}
	if strings.Contains(open, "Using Computer") || strings.Contains(open, "SCRIPT_TAIL") {
		t.Fatalf("old group or eager script: %s", open)
	}
	for _, row := range rows {
		if strings.HasPrefix(row.Plain, "│ ") && row.selectionIndent < 2 {
			t.Fatal("gutter leaked into copy")
		}
		if row.Plain == "" {
			t.Fatal("blank row inside group")
		}
	}
	b.UpdateToolWithMeta("capture", "desktop_world__exec", `app="自有 fixture"`, strings.Repeat("output ", 100)+"RESULT_TAIL", true, false, ToolUpdateMeta{ParentToolCallID: "script", ParentToolName: "js_exec", FullArgs: `{"source":"return await capture();"}`})
	b.UpdateTool("script", "js_exec", "JavaScript", "complete", true, false)
	b.Status = "completed"
	if !b.explorationExpanded("script") || strings.Contains(joinRenderedPlain(b.Render(ctx)), "RESULT_TAIL") {
		t.Fatal("late completion changed disclosure")
	}
	for range 2 {
		if !b.toggleToolPanelClick("capture") {
			t.Fatal("child result disclosure unavailable")
		}
	}
	if !strings.Contains(joinRenderedPlain(b.Render(ctx)), "RESULT_TAIL") {
		t.Fatal("selected result missing")
	}
	for range 2 {
		if !b.toggleToolPanelClick("script") {
			t.Fatal("script disclosure unavailable")
		}
	}
	if !strings.Contains(joinRenderedPlain(b.Render(ctx)), "SCRIPT_TAIL") || !b.explorationExpanded("script") {
		t.Fatal("script folded the group or was lost")
	}
	b.toggleExplorationExpanded("script")
	closed = joinRenderedPlain(b.Render(ctx))
	if strings.Contains(closed, "desktop_world__exec") || strings.Contains(closed, "RESULT_TAIL") || strings.Contains(closed, "running") {
		t.Fatalf("completed collapsed group: %s", closed)
	}
	if len(b.Events) != 3 || b.Events[2].Name != "desktop_world__exec" || b.Events[2].ParentToolCallID != "script" {
		t.Fatal("render mutated projection")
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
	for _, want := range []string{"Code Mode · 2 calls · 1 failed", "denied", "Not permitted", "native.txt", "service__lookup"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if strings.Count(text, "Not permitted") != 1 {
		t.Fatal("approval duplicated")
	}
	b.toggleExplorationExpanded("script")
	if strings.Count(joinRenderedPlain(b.Render(ctx)), "Not permitted") != 1 {
		t.Fatal("opening group duplicated approval")
	}
}

func TestCodeModeLateTaskUpdatesRetainOneGroupAndDisclosure(t *testing.T) {
	b := codeModeBlockForTest()
	b.toggleExplorationExpanded("script")
	meta := ToolUpdateMeta{ParentToolCallID: "script", ParentToolName: "js_exec", ToolStatusExplicit: true, ToolStatus: "completed"}
	b.UpdateToolWithMeta("capture", "desktop_world__exec", "fixture", "finished", true, false, meta)
	b.UpdateTool("script", "js_exec", "JavaScript", "finished", true, false)
	b.Status = "completed"
	meta.ToolStatus = "running"
	b.UpdateToolWithMeta("capture", "desktop_world__exec", "fixture", "late running snapshot", false, false, meta)
	ctx := NewModel(Config{NoColor: true, NoAnimation: true}).blockRenderContext(60)
	text := joinRenderedPlain(b.Render(ctx))
	if strings.Count(text, "Code Mode") != 1 || !strings.Contains(text, "2 calls") || strings.Contains(text, "late running snapshot") || !b.explorationExpanded("script") {
		t.Fatalf("late Task snapshot reopened or duplicated work: %s", text)
	}
}
