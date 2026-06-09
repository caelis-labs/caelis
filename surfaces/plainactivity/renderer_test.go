package plainactivity

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderUsesStablePrefixesAndSkipsBlankLines(t *testing.T) {
	got := Render([]Event{
		{Kind: Reasoning, Text: "checking repo\n\nnext step"},
		{Kind: Assistant, Text: "patched"},
		{Kind: ToolCall, Text: "Run go test ./surfaces/tui/app"},
	}, Options{})
	want := []string{
		"› checking repo",
		"› next step",
		"· patched",
		"• Run go test ./surfaces/tui/app",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Render() = %#v, want %#v", got, want)
	}
}

func TestRenderTailsLines(t *testing.T) {
	got := Render([]Event{
		{Kind: Reasoning, Text: "one"},
		{Kind: ToolCall, Text: "Read a.txt"},
		{Kind: Assistant, Text: "done"},
	}, Options{MaxLines: 2})
	want := []string{
		"• Read a.txt",
		"· done",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Render() = %#v, want %#v", got, want)
	}
}

func TestRenderWrapsWithinWidthWithPrefix(t *testing.T) {
	got := Render([]Event{
		{Kind: Assistant, Text: "alpha beta gamma delta"},
	}, Options{Width: 12})
	if len(got) < 2 {
		t.Fatalf("Render() = %#v, want wrapped lines", got)
	}
	for _, line := range got {
		if !strings.HasPrefix(line, "· ") {
			t.Fatalf("Render() line = %q, want assistant prefix", line)
		}
		if width := ansi.StringWidthWc(line); width > 12 {
			t.Fatalf("Render() line width = %d, want <= 12: %q", width, line)
		}
	}
}
