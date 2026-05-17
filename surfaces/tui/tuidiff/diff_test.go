package tuidiff

import (
	"strings"
	"testing"
)

func TestBuildUnifiedLinesRendersStandardDiffWithHunkHeader(t *testing.T) {
	t.Parallel()

	lines := BuildUnifiedLines(
		"context-a\ncontext-b\nold line\ncontext-c\n",
		"context-a\ncontext-b\nnew line\ncontext-c\n",
	)
	got := strings.Join(lines, "\n")
	for _, want := range []string{"@@ -3,1 +3,1 @@", "-old line", "+new line"} {
		if !strings.Contains(got, want) {
			t.Fatalf("BuildUnifiedLines = %q, want %q", got, want)
		}
	}
}

func TestBuildUnifiedLinesRendersAppendDiffWithActualNewStart(t *testing.T) {
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
	got := strings.Join(BuildUnifiedLines(oldText, newText), "\n")

	if !strings.Contains(got, "@@ -10,0 +11,2 @@") {
		t.Fatalf("BuildUnifiedLines = %q, want append hunk starting at new line 11", got)
	}
}

func TestBuildUnifiedLinesPreservesUnchangedLinesBetweenEdits(t *testing.T) {
	t.Parallel()

	got := strings.Join(BuildUnifiedLines(
		"line 1\nold a\nshared middle\nold b\nline 5\n",
		"line 1\nnew a\nshared middle\nnew b\nline 5\n",
	), "\n")

	for _, want := range []string{"@@ -2,3 +2,3 @@", "-old a", " shared middle", "-old b", "+new a", "+new b"} {
		if !strings.Contains(got, want) {
			t.Fatalf("BuildUnifiedLines = %q, want %q", got, want)
		}
	}
	for _, forbidden := range []string{"-shared middle", "+shared middle"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("BuildUnifiedLines = %q, unchanged middle line should be context not %q", got, forbidden)
		}
	}
}

func TestBuildUnifiedLinesSkipsEmptyDiff(t *testing.T) {
	t.Parallel()

	if got := BuildUnifiedLines("same\n", "same\n"); len(got) != 0 {
		t.Fatalf("BuildUnifiedLines = %#v, want empty", got)
	}
}

func TestBuildUnifiedLinesFallsBackForLargeInputs(t *testing.T) {
	t.Parallel()

	oldLines := make([]string, 600)
	newLines := make([]string, 600)
	for i := range oldLines {
		oldLines[i] = "old"
		newLines[i] = "new"
	}
	got := strings.Join(BuildUnifiedLines(strings.Join(oldLines, "\n"), strings.Join(newLines, "\n")), "\n")
	if !strings.Contains(got, "@@ -1,600 +1,600 @@") {
		t.Fatalf("BuildUnifiedLines large fallback header = %q", got)
	}
}
