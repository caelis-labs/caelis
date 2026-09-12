package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

const mutationSingleFileDiff = "remote_script_test.go +2 -0\n@@ -0,0 +1,2 @@\n+first\n+second\n"

const mutationSingleFileDiffNoNewline = mutationSingleFileDiff + "\\ No newline at end of file\n"

func TestMutationRichDiffOmitsDuplicateSingleFileHeader(t *testing.T) {
	t.Parallel()

	for _, width := range []int{80, 160} {
		plain := renderMutationTurnVisible(t, surfaceToolWrite, "remote_script_test.go +2 -0", mutationSingleFileDiff, false, width)
		if !strings.Contains(plain, "• Edit remote_script_test.go +2 -0") {
			t.Fatalf("width %d: missing tool header:\n%s", width, plain)
		}
		if got := strings.Count(plain, "remote_script_test.go +2 -0"); got != 1 {
			t.Fatalf("width %d: filename/count header count = %d, want 1 in tool header only:\n%s", width, got, plain)
		}
		if !strings.Contains(plain, "first") || !strings.Contains(plain, "second") {
			t.Fatalf("width %d: rich diff body missing source:\n%s", width, plain)
		}
	}

	model := parseDiffPanelText(mutationSingleFileDiff)
	for _, line := range model.Lines {
		if line.Kind == diffPanelLineMeta {
			continue
		}
		if line.Path != "remote_script_test.go" {
			t.Fatalf("source path = %q, want remote_script_test.go for syntax identity", line.Path)
		}
	}
}

func TestMutationRichDiffKeepsPerFileHeaders(t *testing.T) {
	t.Parallel()

	for _, width := range []int{80, 160} {
		plain := renderMutationTurnVisible(t, surfaceToolPatch, "first.go +1 -1", multiFileACPDiff(), false, width)
		if !strings.Contains(plain, "• Edit first.go +1 -1") {
			t.Fatalf("width %d: missing tool header:\n%s", width, plain)
		}
		if strings.Count(plain, "first.go") < 2 {
			t.Fatalf("width %d: multi-file diff dropped the first file body header:\n%s", width, plain)
		}
		if strings.Count(plain, "second.py") != 1 {
			t.Fatalf("width %d: multi-file diff lost the second file header:\n%s", width, plain)
		}
	}
}

func TestStandaloneRichDiffKeepsSingleFileHeader(t *testing.T) {
	t.Parallel()

	theme := tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	for _, width := range []int{80, 160} {
		rows := renderNumberedACPDiffPanelRows("diff", mutationSingleFileDiff, width, BlockRenderContext{
			Width:     width,
			TermWidth: width,
			Theme:     theme,
		})
		plain := joinRenderedVisible(rows)
		if strings.Count(plain, "remote_script_test.go +2 -0") != 1 {
			t.Fatalf("width %d: standalone single-file header missing:\n%s", width, plain)
		}
	}
}

func TestMutationRichDiffOmitsPathOnlyDuplicateHeader(t *testing.T) {
	t.Parallel()

	text := "--- a/demo.go\n+++ b/demo.go\n@@ -1 +1 @@\n-old\n+new\n"
	for _, width := range []int{80, 160} {
		plain := renderMutationTurnVisible(t, surfaceToolPatch, "demo.go +1 -1", text, false, width)
		if !strings.Contains(plain, "• Edit demo.go +1 -1") {
			t.Fatalf("width %d: missing tool header:\n%s", width, plain)
		}
		if got := strings.Count(plain, "demo.go"); got != 1 {
			t.Fatalf("width %d: path-only body header count = %d, want omitted in favor of tool header:\n%s", width, got, plain)
		}
	}
}

func TestMutationRichDiffKeepsRenameIdentityHeader(t *testing.T) {
	t.Parallel()

	text := "--- a/old.go\n+++ b/new.go\n@@ -1 +1 @@\n-old\n+new\n"
	for _, width := range []int{80, 160} {
		plain := renderMutationTurnVisible(t, surfaceToolPatch, "old.go +1 -1", text, false, width)
		if !strings.Contains(plain, "• Edit old.go +1 -1") {
			t.Fatalf("width %d: missing tool header:\n%s", width, plain)
		}
		if !strings.Contains(plain, "new.go") {
			t.Fatalf("width %d: rename identity header missing from body:\n%s", width, plain)
		}
	}

	model := parseDiffPanelText(text)
	for _, line := range model.Lines {
		if line.Path != "new.go" {
			t.Fatalf("rename path = %q, want new.go", line.Path)
		}
	}
}

func TestMutationRichDiffIgnoresNoNewlineMetadata(t *testing.T) {
	t.Parallel()

	for _, width := range []int{80, 160} {
		plain := renderMutationTurnVisible(t, surfaceToolWrite, "remote_script_test.go +2 -0", mutationSingleFileDiffNoNewline, false, width)
		if got := strings.Count(plain, "remote_script_test.go +2 -0"); got != 1 {
			t.Fatalf("width %d: no-newline metadata kept a duplicate file header: count=%d\n%s", width, got, plain)
		}
		if !strings.Contains(plain, "No newline at end of file") {
			t.Fatalf("width %d: no-newline metadata missing from body:\n%s", width, plain)
		}
	}
}

func TestEmbeddedRichDiffAlignsWithHeaderTextAndUsesInsetWidth(t *testing.T) {
	t.Parallel()

	const inset = 2
	theme := tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	for _, width := range []int{80, 119, 120, 121, 122, 160} {
		ctx := BlockRenderContext{Width: width, TermWidth: width, Theme: theme}
		standalone := renderNumberedACPDiffPanelRows("diff", mutationSingleFileDiff, width, ctx)
		inner := renderNumberedACPDiffPanelRows("diff", mutationSingleFileDiff, maxInt(1, width-inset), ctx)
		embedded := renderACPDiffPanelRows("diff", mutationSingleFileDiff, width, ctx)
		if len(embedded) != len(inner) {
			t.Fatalf("width %d: embedded rows = %d, want %d source rows from inset-width layout:\n%s", width, len(embedded), len(inner), joinRenderedVisible(embedded))
		}
		pad := strings.Repeat(" ", inset)
		for i, row := range embedded {
			if row.Plain != pad+inner[i].Plain || strings.TrimRight(ansi.Strip(row.Styled), " ") != strings.TrimRight(pad+ansi.Strip(inner[i].Styled), " ") {
				t.Fatalf("width %d row %d: embed is not inset inner layout\nplain=%q\nwant=%q", width, i, row.Plain, pad+inner[i].Plain)
			}
			if row.selectionIndent != inset || !row.PreWrapped {
				t.Fatalf("width %d row %d: selectionIndent=%d PreWrapped=%v, want inset=%d prewrapped", width, i, row.selectionIndent, row.PreWrapped, inset)
			}
			if displayColumns(row.Styled) > width {
				t.Fatalf("width %d row %d overflow after inset: %q", width, i, row.Styled)
			}
		}
		standaloneSplit := strings.Contains(joinRenderedVisible(standalone), " │ ")
		embeddedSplit := strings.Contains(joinRenderedVisible(embedded), " │ ")
		wantStandaloneSplit := width >= 120
		wantEmbeddedSplit := width-inset >= 120
		if standaloneSplit != wantStandaloneSplit {
			t.Fatalf("width %d: standalone split = %v, want %v\n%s", width, standaloneSplit, wantStandaloneSplit, joinRenderedVisible(standalone))
		}
		if embeddedSplit != wantEmbeddedSplit {
			t.Fatalf("width %d: embedded split = %v, want %v (inner width %d)\n%s", width, embeddedSplit, wantEmbeddedSplit, width-inset, joinRenderedVisible(embedded))
		}
	}
}

func TestEmbeddedRichDiffAdapterDoesNotAddTinyWidthOverflow(t *testing.T) {
	t.Parallel()

	theme := tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY)
	for _, width := range []int{1, 2, 3} {
		ctx := BlockRenderContext{Width: width, TermWidth: width, Theme: theme}
		embedded := renderACPDiffPanelRows("diff", mutationSingleFileDiff, width, ctx)
		if width == 1 {
			standalone := renderNumberedACPDiffPanelRows("diff", mutationSingleFileDiff, 1, ctx)
			if joinRenderedVisible(embedded) != joinRenderedVisible(standalone) {
				t.Fatalf("width 1 adapter changed standalone layout:\nembed:\n%s\nstandalone:\n%s", joinRenderedVisible(embedded), joinRenderedVisible(standalone))
			}
			continue
		}
		for i, row := range embedded {
			if displayColumns(row.Styled) > width {
				t.Fatalf("width %d row %d overflowed: cols=%d %q", width, i, displayColumns(row.Styled), row.Styled)
			}
		}
	}
}

func TestMutationRichDiffAlignsWithEditColumnOnMainAndChild(t *testing.T) {
	t.Parallel()

	got := map[int]string{}
	for _, width := range []int{80, 122, 160} {
		var mainPlain, childPlain string
		for _, kind := range []string{"main", "child"} {
			rows := renderMutationTurnRows(t, kind, surfaceToolWrite, "remote_script_test.go +2 -0", mutationSingleFileDiff, false, width)
			plain := joinRenderedVisible(rows)
			if kind == "main" {
				mainPlain = plain
			} else {
				childPlain = plain
			}
			assertMutationRichDiffAlignedWithEdit(t, kind, width, rows, plain)
		}
		if mainPlain != childPlain {
			t.Fatalf("width %d: main/child rich diff paths diverged\nmain:\n%s\nchild:\n%s", width, mainPlain, childPlain)
		}
		got[width] = mainPlain
	}
	var mismatches []string
	for _, width := range []int{80, 122} {
		if got[width] != mutationRichDiffGolden[width] {
			mismatches = append(mismatches, fmt.Sprintf("width %d quoted=%q\n%s", width, got[width], got[width]))
		}
	}
	if len(mismatches) > 0 {
		t.Fatalf("full block mismatch:\n%s", strings.Join(mismatches, "\n\n"))
	}
}

// Full visible blocks for the shared main/child render path. 80 stays unified;
// 122 is the first split after subtracting the 2-column embed inset (inner 120).
// Leading spaces before │ are inset + empty old cell + the separator's left space.
var mutationRichDiffGolden = map[int]string{
	80: "• Edit remote_script_test.go +2 -0\n     1 + first\n     2 + second",
	122: "• Edit remote_script_test.go +2 -0\n" +
		strings.Repeat(" ", 61) + "│  1 + first\n" +
		strings.Repeat(" ", 61) + "│  2 + second",
}

func TestMutationRichDiffPreservesClickTokenSelectionAndHeaderDedup(t *testing.T) {
	t.Parallel()

	const width = 80
	rows := renderMutationTurnRows(t, "main", surfaceToolWrite, "remote_script_test.go +2 -0", mutationSingleFileDiff, false, width)
	plain := joinRenderedVisible(rows)
	if got := strings.Count(plain, "remote_script_test.go +2 -0"); got != 1 {
		t.Fatalf("header dedup lost after inset: count=%d\n%s", got, plain)
	}
	token := acpToolPanelClickToken("edit-1")
	const inset = 2
	bodyRows := 0
	for _, row := range rows {
		visible := strings.TrimRight(ansi.Strip(row.Styled), " ")
		if strings.TrimSpace(visible) == "" {
			continue
		}
		if row.ClickToken != token {
			t.Fatalf("click token = %q, want %q on %q", row.ClickToken, token, visible)
		}
		if row.ACPHeader {
			if row.selectionIndent != inset {
				t.Fatalf("header selection indent = %d, want %d", row.selectionIndent, inset)
			}
			continue
		}
		bodyRows++
		if row.selectionIndent != inset {
			t.Fatalf("body selection indent = %d, want %d on %q", row.selectionIndent, inset, visible)
		}
		copied := selectionTextFromLinesWithIndents(
			[]string{row.Plain},
			[]int{row.selectionIndent},
			textSelectionPoint{line: 0, col: 0},
			textSelectionPoint{line: 0, col: displayColumns(row.Plain)},
		)
		wantCopy := sliceByDisplayColumns(row.Plain, inset, displayColumns(row.Plain))
		if copied != wantCopy {
			t.Fatalf("copy = %q, want inset-stripped %q from %q", copied, wantCopy, row.Plain)
		}
		if !strings.Contains(copied, "first") && !strings.Contains(copied, "second") {
			t.Fatalf("copy dropped source text: %q", copied)
		}
	}
	if bodyRows == 0 {
		t.Fatalf("missing rich diff body:\n%s", plain)
	}
}

func TestMutationRichDiffPreservesErroredDiffOutput(t *testing.T) {
	t.Parallel()

	for _, width := range []int{80, 160} {
		plain := renderMutationTurnVisible(t, surfaceToolWrite, "remote_script_test.go +2 -0", mutationSingleFileDiff, true, width)
		if !strings.Contains(plain, "• Edit remote_script_test.go +2 -0 failed") {
			t.Fatalf("width %d: missing failed tool header:\n%s", width, plain)
		}
		if got := strings.Count(plain, "remote_script_test.go +2 -0"); got < 2 {
			t.Fatalf("width %d: errored diff dropped the first output line: count=%d\n%s", width, got, plain)
		}
		if !strings.Contains(plain, "first") || !strings.Contains(plain, "second") {
			t.Fatalf("width %d: errored diff body missing source:\n%s", width, plain)
		}
	}

	panel := []RenderedRow{
		{Plain: "remote_script_test.go +2 -0", Styled: "remote_script_test.go +2 -0"},
		{Plain: "+first", Styled: "+first"},
	}
	got := omitRedundantMutationDiffFileHeader("remote_script_test.go +2 -0", mutationSingleFileDiff, true, panel)
	if len(got) != 2 || got[0].Plain != panel[0].Plain {
		t.Fatalf("omit with err=true dropped the first error line: %#v", got)
	}
}

func renderMutationTurnVisible(t *testing.T, name string, args string, output string, err bool, width int) string {
	t.Helper()
	return joinRenderedVisible(renderMutationTurnRows(t, "main", name, args, output, err, width))
}

func renderMutationTurnRows(t *testing.T, kind string, name string, args string, output string, err bool, width int) []RenderedRow {
	t.Helper()
	var block interface {
		Render(BlockRenderContext) []RenderedRow
		UpdateToolWithMeta(callID, name, args, output string, final bool, err bool, meta ToolUpdateMeta)
	}
	switch kind {
	case "child":
		child := NewParticipantTurnBlock("child-edit", "worker")
		child.Status = "completed"
		block = child
	default:
		main := NewMainACPTurnBlock("turn-edit")
		main.Status = "completed"
		block = main
	}
	block.UpdateToolWithMeta("edit-1", name, args, output, true, err, ToolUpdateMeta{ToolKind: "edit"})
	return block.Render(BlockRenderContext{
		Width:     width,
		TermWidth: width,
		Theme:     tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY),
	})
}

func assertMutationRichDiffAlignedWithEdit(t *testing.T, kind string, width int, rows []RenderedRow, plain string) {
	t.Helper()
	const inset = 2
	header := ""
	for _, row := range rows {
		visible := strings.TrimRight(ansi.Strip(row.Styled), " ")
		if strings.HasPrefix(visible, "• Edit ") {
			header = visible
			break
		}
	}
	if header == "" {
		t.Fatalf("%s width %d: missing Edit header:\n%s", kind, width, plain)
	}
	editCol := displayColumns("• ")
	if editCol != inset || !strings.HasPrefix(header, "• Edit ") {
		t.Fatalf("%s width %d: Edit column = %d, want inset %d in %q", kind, width, editCol, inset, header)
	}
	body := 0
	for _, row := range rows {
		if row.ACPHeader {
			continue
		}
		visible := strings.TrimRight(ansi.Strip(row.Styled), " ")
		if strings.TrimSpace(visible) == "" {
			continue
		}
		body++
		if !strings.HasPrefix(visible, strings.Repeat(" ", editCol)) {
			t.Fatalf("%s width %d: diff left edge %q is left of Edit column %d\n%s", kind, width, visible, editCol, plain)
		}
		if leadingDisplaySpaces(visible) < editCol {
			t.Fatalf("%s width %d: diff starts at column %d, want >= Edit column %d\n%s", kind, width, leadingDisplaySpaces(visible), editCol, plain)
		}
	}
	if body == 0 {
		t.Fatalf("%s width %d: missing indented rich diff body:\n%s", kind, width, plain)
	}
}

func leadingDisplaySpaces(text string) int {
	count := 0
	for _, r := range text {
		if r != ' ' {
			return count
		}
		count++
	}
	return count
}

func joinRenderedVisible(rows []RenderedRow) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, strings.TrimRight(ansi.Strip(row.Styled), " "))
	}
	return strings.Join(parts, "\n")
}
