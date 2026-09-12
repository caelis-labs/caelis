package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

const richDiffFixture = "storage.go +2 -1\ndiff / hunk\n@@ -8,4 +8,5 @@\n func count() int {\n \n-  return oldCount\n+  return newCount\n+  // 新增说明 👩‍💻\n }\n@@ -50,1 +51,1 @@\n-old tail\n+new tail\n"

func TestRichDiffResponsiveLayoutAndLineNumbers(t *testing.T) {
	theme := tuikit.ResolveSelectedTheme("catppuccin-mocha", nil, true, false, colorprofile.TrueColor)
	model := parseDiffPanelText(richDiffFixture)
	if model.Lines[2].OldNo != 9 || model.Lines[2].NewNo != 9 {
		t.Fatal("blank context line did not advance line numbers")
	}
	for _, width := range []int{24, 80, 119, 120, 180} {
		rows := renderNumberedACPDiffPanelRows("diff", richDiffFixture, width, BlockRenderContext{Width: width, TermWidth: 220, Theme: theme})
		var plain strings.Builder
		for _, row := range rows {
			if displayColumns(row.Styled) > width || strings.Contains(row.Styled, "\n") {
				t.Fatalf("width %d overflow: %q", width, row.Styled)
			}
			if strings.TrimRight(ansi.Strip(row.Styled), " ") != strings.TrimRight(row.Plain, " ") {
				t.Fatalf("visible/plain mismatch: %#v", row)
			}
			plain.WriteString(row.Plain + "\n")
		}
		text := plain.String()
		if strings.Contains(text, "@@") || strings.Contains(text, "diff / hunk") {
			t.Fatal("raw diff metadata leaked into rich view")
		}
		if strings.Contains(text, " │ ") != (width >= 120) {
			t.Fatalf("layout did not follow panel width %d", width)
		}
		if (width >= 80 && (!strings.Contains(text, "oldCount") || !strings.Contains(text, "newCount"))) || !strings.Contains(text, "51") {
			t.Fatalf("missing changes or new-side line numbers: %s", text)
		}
	}
}

func TestRichDiffWrapsBothSidesWithoutTruncatingUnicode(t *testing.T) {
	old := "\t旧值 é 👩‍💻 " + strings.Repeat("中文", 65) + " OLD_END"
	next := "\t新值 é 👩‍💻 " + strings.Repeat("内容", 10) + " NEW_END"
	text := "demo.go +1 -1\n@@ -1 +1 @@\n-" + old + "\n+" + next
	for _, width := range []int{40, 120, 160} {
		rows := renderNumberedACPDiffPanelRows("diff", text, width, BlockRenderContext{Theme: tuikit.ResolveSelectedTheme("nord", nil, true, false, colorprofile.TrueColor)})
		var plain strings.Builder
		for _, row := range rows {
			if displayColumns(row.Styled) > width {
				t.Fatalf("width %d overflow", width)
			}
			plain.WriteString(row.Plain)
		}
		if !strings.Contains(plain.String(), "OLD_END") || !strings.Contains(plain.String(), "NEW_END") {
			t.Fatal("wrapped source was truncated")
		}
	}
}

func TestRichDiffEmphasizesOnlyChangedGraphemes(t *testing.T) {
	old := &diffPanelLine{Kind: diffPanelLineRemove, Text: "return oldName + é + 👩‍💻"}
	next := &diffPanelLine{Kind: diffPanelLineAdd, Text: "return newName + é + 👩‍💻"}
	markDiffTextChanges(old, next)
	if len(old.Changed) != 1 || old.Text[old.Changed[0].start:old.Changed[0].end] != "old" {
		t.Fatalf("old emphasis = %#v", old.Changed)
	}
	if len(next.Changed) != 1 || next.Text[next.Changed[0].start:next.Changed[0].end] != "new" {
		t.Fatalf("new emphasis = %#v", next.Changed)
	}
}

func TestRichDiffPairsRelatedLinesAcrossInsertedComment(t *testing.T) {
	model := parseDiffPanelText("demo.go\n@@ -1 +1,2 @@\n-    return count\n+    // only changed values\n+    return affected")
	pairs := alignDiffPanelLines(model.Lines[1:])
	if len(pairs) != 2 || pairs[0].old != nil || pairs[0].new == nil || pairs[1].old == nil || pairs[1].new == nil || !strings.Contains(pairs[1].new.Text, "return affected") {
		t.Fatalf("replacement alignment = %#v", pairs)
	}
}
