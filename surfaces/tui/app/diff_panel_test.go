package tuiapp

import (
	"strings"
	"testing"
)

func TestParseDiffPanelTextUsesHunkLineNumbersForAppend(t *testing.T) {
	t.Parallel()

	model := parseDiffPanelText("@@ -10,0 +11,2 @@\n+line 11\n+line 12")
	if len(model.Lines) != 3 {
		t.Fatalf("len(lines) = %d, want hunk plus 2 rows: %#v", len(model.Lines), model.Lines)
	}
	if model.Lines[1].NewNo != 11 || model.Lines[2].NewNo != 12 {
		t.Fatalf("new line numbers = %d,%d, want 11,12", model.Lines[1].NewNo, model.Lines[2].NewNo)
	}
}

func TestRenderStandardDiffPanelRowsUsesHunkLineNumbers(t *testing.T) {
	t.Parallel()

	m := newPerfTestModel()
	ctx := BlockRenderContext{Width: 72, TermWidth: 72, Theme: m.theme}
	rows := renderNumberedACPDiffPanelRows("patch-1", "@@ -10,0 +11,2 @@\n+line 11\n+line 12", 72, ctx)

	plain := strings.Join(renderedPlainRows(rows), "\n")
	for _, want := range []string{"  11 +line 11", "  12 +line 12"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered rows = %q, want %q", plain, want)
		}
	}
}

func TestIsDiffPanelTextAcceptsStandardDiffWithHunk(t *testing.T) {
	t.Parallel()

	if !isDiffPanelText("@@ -1,1 +1,1 @@\n-old\n+new") {
		t.Fatal("isDiffPanelText() = false, want true for standard diff with hunk")
	}
	if isDiffPanelText("-old\n+new") {
		t.Fatal("isDiffPanelText() = true, want false when standard diff has no hunk line numbers")
	}
}
