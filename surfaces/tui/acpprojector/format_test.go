package acpprojector

import (
	"strings"
	"testing"
)

func TestFormatToolContentRendersStandardDiffWithHunkHeader(t *testing.T) {
	t.Parallel()

	oldText := "context-a\ncontext-b\nold line\ncontext-c\n"
	got := FormatToolContent([]ToolContent{{
		Type:    "diff",
		Path:    "/workspace/demo.txt",
		OldText: &oldText,
		NewText: "context-a\ncontext-b\nnew line\ncontext-c\n",
	}})

	for _, want := range []string{"demo.txt +1 -1", "@@ -3,1 +3,1 @@", "-old line", "+new line"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted diff = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "diff / hunk") {
		t.Fatalf("formatted diff = %q, should not contain legacy marker", got)
	}
}

func TestFormatToolContentRendersAppendDiffWithActualNewStart(t *testing.T) {
	t.Parallel()

	oldText := strings.Join([]string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
		"line 8",
		"line 9",
		"line 10",
	}, "\n") + "\n"
	newText := oldText + "line 11\nline 12\n"
	got := FormatToolContent([]ToolContent{{
		Type:    "diff",
		Path:    "/workspace/demo.txt",
		OldText: &oldText,
		NewText: newText,
	}})

	if !strings.Contains(got, "@@ -10,0 +11,2 @@") {
		t.Fatalf("formatted diff = %q, want append hunk starting at new line 11", got)
	}
	if !strings.Contains(got, "demo.txt +2 -0") {
		t.Fatalf("formatted diff = %q, want concise file change header", got)
	}
}

func TestFormatToolContentPreservesUnchangedLinesBetweenEdits(t *testing.T) {
	t.Parallel()

	oldText := "line 1\nold a\nshared middle\nold b\nline 5\n"
	got := FormatToolContent([]ToolContent{{
		Type:    "diff",
		Path:    "/workspace/demo.txt",
		OldText: &oldText,
		NewText: "line 1\nnew a\nshared middle\nnew b\nline 5\n",
	}})

	for _, want := range []string{"@@ -2,3 +2,3 @@", "-old a", " shared middle", "-old b", "+new a", "+new b"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted diff = %q, want %q", got, want)
		}
	}
	for _, forbidden := range []string{"-shared middle", "+shared middle"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("formatted diff = %q, unchanged middle line should be context not %q", got, forbidden)
		}
	}
}

func TestFormatToolContentCountsSignedContentLines(t *testing.T) {
	t.Parallel()

	oldText := "plain\n---\n"
	got := FormatToolContent([]ToolContent{{
		Type:    "diff",
		Path:    "/workspace/demo.md",
		OldText: &oldText,
		NewText: "plain\n++foo\n",
	}})

	if !strings.Contains(got, "demo.md +1 -1") {
		t.Fatalf("formatted diff = %q, want signed content lines counted in header", got)
	}
	for _, want := range []string{"+++foo", "----"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatted diff = %q, want %q", got, want)
		}
	}
}

func TestFormatToolContentSkipsEmptyDiff(t *testing.T) {
	t.Parallel()

	text := "same\n"
	got := FormatToolContent([]ToolContent{{
		Type:    "diff",
		Path:    "/workspace/demo.txt",
		OldText: &text,
		NewText: text,
	}})

	if got != "" {
		t.Fatalf("formatted diff = %q, want empty", got)
	}
}

func TestFormatToolContentDoesNotRenderTerminalContentBodies(t *testing.T) {
	t.Parallel()

	got := FormatToolContent([]ToolContent{{
		Type:       "terminal",
		TerminalID: "call-1",
		Content:    map[string]any{"type": "text", "text": "terminal output\n"},
	}})

	if got != "" {
		t.Fatalf("formatted terminal content = %q, want terminal output to come from _meta", got)
	}
}

func TestFormatToolStartKeepsGenericArgs(t *testing.T) {
	t.Parallel()

	got := FormatToolStart("ExternalList", map[string]any{"metadata": true})
	if got != `{"metadata":true}` {
		t.Fatalf("FormatToolStart(ExternalList metadata) = %q, want generic JSON", got)
	}
}

func TestExternalDiffLargeSparseEditsStayLocalized(t *testing.T) {
	before := "old first\n" + strings.Repeat("same middle\n", 1400) + "old last\n"
	after := "new first\n" + strings.Repeat("same middle\n", 1400) + "new last\n"
	text := FormatToolContent([]ToolContent{{Type: "diff", Path: "large.go", OldText: &before, NewText: after}})
	if !strings.Contains(text, "large.go +2 -2") || strings.Count(text, "@@ -") != 2 {
		t.Fatalf("sparse external diff lost locality: %s", text)
	}
	if strings.Contains(text, "-same middle") || strings.Contains(text, "+same middle") {
		t.Fatal("unchanged lines became changes")
	}
}

func TestExternalDiffPreservesAnEmptySourceLine(t *testing.T) {
	got := buildUnifiedLines("", "\n")
	if len(got) != 2 || got[1] != "+" {
		t.Fatalf("blank source insertion = %#v", got)
	}
}
