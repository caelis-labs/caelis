package acpprojector

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestFormatToolContentRendersStandardDiffWithHunkHeader(t *testing.T) {
	t.Parallel()

	oldText := "context-a\ncontext-b\nold line\ncontext-c\n"
	got := FormatToolContent([]schema.ToolCallContent{{
		Type:    "diff",
		Path:    "/workspace/demo.txt",
		OldText: &oldText,
		NewText: "context-a\ncontext-b\nnew line\ncontext-c\n",
	}})

	for _, want := range []string{"@@ -3,1 +3,1 @@", "-old line", "+new line"} {
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
	got := FormatToolContent([]schema.ToolCallContent{{
		Type:    "diff",
		Path:    "/workspace/demo.txt",
		OldText: &oldText,
		NewText: newText,
	}})

	if !strings.Contains(got, "@@ -10,0 +11,2 @@") {
		t.Fatalf("formatted diff = %q, want append hunk starting at new line 11", got)
	}
}

func TestFormatToolContentPreservesUnchangedLinesBetweenEdits(t *testing.T) {
	t.Parallel()

	oldText := "line 1\nold a\nshared middle\nold b\nline 5\n"
	got := FormatToolContent([]schema.ToolCallContent{{
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

func TestFormatToolContentSkipsEmptyDiff(t *testing.T) {
	t.Parallel()

	text := "same\n"
	got := FormatToolContent([]schema.ToolCallContent{{
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

	got := FormatToolContent([]schema.ToolCallContent{{
		Type:       "terminal",
		TerminalID: "call-1",
		Content:    schema.TextContent{Type: "text", Text: "terminal output\n"},
	}})

	if got != "" {
		t.Fatalf("formatted terminal content = %q, want terminal output to come from _meta", got)
	}
}

func TestFormatToolStartHidesMetadataOnlyListArgs(t *testing.T) {
	t.Parallel()

	got := FormatToolStart("LIST", map[string]any{"metadata": true})
	if got != "" {
		t.Fatalf("FormatToolStart(LIST metadata) = %q, want empty", got)
	}
}
