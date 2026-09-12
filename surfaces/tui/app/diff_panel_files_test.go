package tuiapp

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/acpprojector"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

const multiFileGitDiff = "diff --git a/first.go b/first.go\nindex 1111111..2222222 100644\n--- a/first.go\n+++ b/first.go\n@@ -1 +1 @@\n-return oldValue\n+return newValue\ndiff --git a/second.py b/second.py\nindex 3333333..4444444 100644\n--- a/second.py\n+++ b/second.py\n@@ -1 +1 @@\n-print(old_value)\n+print(new_value)\n"

func multiFileACPDiff() string {
	oldGo, oldPython := "return oldValue\n", "print(old_value)\n"
	return acpprojector.FormatToolContent([]acpprojector.ToolContent{
		{Type: "diff", Path: "first.go", OldText: &oldGo, NewText: "return newValue\n"},
		{Type: "diff", Path: "second.py", OldText: &oldPython, NewText: "print(new_value)\n"},
	})
}

func TestRichDiffPreservesEachFileIdentity(t *testing.T) {
	for name, text := range map[string]string{"git": multiFileGitDiff, "acp": multiFileACPDiff()} {
		t.Run(name, func(t *testing.T) {
			model := parseDiffPanelText(text)
			for _, line := range model.Lines {
				if line.Kind == diffPanelLineMeta {
					continue
				}
				want := "first.go"
				if strings.HasPrefix(line.Text, "print(") {
					want = "second.py"
				}
				if line.Path != want {
					t.Errorf("%q Path = %q, want %q", line.Text, line.Path, want)
				}
			}
			for _, width := range []int{80, 160} {
				rows := renderNumberedACPDiffPanelRows("diff", text, width, BlockRenderContext{Theme: tuikit.DefaultTheme()})
				var visible []string
				for _, row := range rows {
					if displayColumns(row.Styled) > width {
						t.Fatalf("row exceeds width %d", width)
					}
					// Background fill pads the physical row, not the source text.
					if strings.TrimRight(ansi.Strip(row.Styled), " ") != strings.TrimRight(row.Plain, " ") {
						t.Fatalf("styled and plain rows differ at width %d: visible=%q plain=%q", width, ansi.Strip(row.Styled), row.Plain)
					}
					visible = append(visible, row.Plain)
				}
				rendered := strings.Join(visible, "\n")
				for _, path := range []string{"first.go", "second.py"} {
					if strings.Count(rendered, path) != 1 {
						t.Errorf("width %d: file header %q missing or repeated:\n%s", width, path, rendered)
					}
				}
			}
		})
	}
}

func TestRichDiffFileBoundaryResetsSyntax(t *testing.T) {
	first := "first.go +1 -1\n@@ -1 +1 @@\n-/* old comment\n+/* new comment\n"
	second := "second.py +1 -1\n@@ -1 +1 @@\n-print(old_value)\n+print(new_value)\n"
	combined, alone := parseDiffPanelText(first+second), parseDiffPanelText(second)
	ctx := BlockRenderContext{Theme: tuikit.ResolveSelectedTheme("nord", nil, true, false, colorprofile.TrueColor)}
	highlightDiffPanelLines(combined.Lines, ctx)
	highlightDiffPanelLines(alone.Lines, ctx)
	for _, want := range alone.Lines {
		if want.Kind == diffPanelLineMeta {
			continue
		}
		found := false
		for _, got := range combined.Lines {
			if got.Text == want.Text {
				found = true
				if got.Path != want.Path || got.StyledText != want.StyledText {
					t.Errorf("previous file changed Python highlighting: %q", got.Text)
				}
			}
		}
		if !found {
			t.Errorf("missing second file source %q", want.Text)
		}
	}
}

func TestRichDiffPreservesSourceThatLooksLikeFileHeaders(t *testing.T) {
	text := "first.go +2 -2\n@@ -1,3 +1,3 @@\n second.py +1 -1\n--- a/not-a-file\n-old\n+++ b/not-a-file\n+new\nsecond.py +1 -1\n@@ -7 +9 @@\n-old_python\n+new_python\n"
	model := parseDiffPanelText(text)
	var source []diffPanelLine
	for _, line := range model.Lines {
		if line.Kind != diffPanelLineMeta {
			source = append(source, line)
		}
	}
	if len(source) != 7 {
		t.Fatalf("source lines = %d, want 7", len(source))
	}
	for _, line := range source[:5] {
		if line.Path != "first.go" {
			t.Errorf("source interpreted as header: %#v", line)
		}
	}
	if source[5].Path != "second.py" || source[5].OldNo != 7 || source[6].Path != "second.py" || source[6].NewNo != 9 {
		t.Fatalf("second file not reset: %#v", source[5:])
	}
}

func TestRichDiffDeletedAndQuotedFilePaths(t *testing.T) {
	for name, text := range map[string]string{
		"deleted.go":  "--- a/deleted.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n",
		"new.py":      "--- /dev/null\n+++ b/new.py\n@@ -0,0 +1 @@\n+new\n",
		"dir/a b.go":  "--- \"a/dir/a b.go\"\n+++ \"b/dir/a b.go\"\n@@ -1 +1 @@\n-old\n+new\n",
		"b/nested.go": "--- a/b/nested.go\n+++ b/b/nested.go\n@@ -1 +1 @@\n-old\n+new\n",
	} {
		t.Run(name, func(t *testing.T) {
			model := parseDiffPanelText(text)
			if len(model.Lines) < 2 || model.Lines[0].Text != name {
				t.Fatalf("missing file header: %#v", model)
			}
			for _, line := range model.Lines {
				if line.Path != name {
					t.Errorf("file identity = %q, want %q", line.Path, name)
				}
			}
		})
	}
}

func TestRichDiffMultiFileGolden(t *testing.T) {
	for _, width := range []int{80, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			theme := tuikit.ResolveSelectedTheme("nord", nil, true, false, colorprofile.TrueColor)
			var rows []string
			for _, row := range renderNumberedACPDiffPanelRows("diff", multiFileACPDiff(), width, BlockRenderContext{Theme: theme}) {
				rows = append(rows, strings.TrimRight(ansi.Strip(row.Styled), " "))
			}
			want, err := os.ReadFile(fmt.Sprintf("testdata/diff_panel/multi_file_%d.txt", width))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(rows, "\n") + "\n"; got != string(want) {
				t.Fatalf("rendered diff differs from golden:\n%s", got)
			}
		})
	}
}
