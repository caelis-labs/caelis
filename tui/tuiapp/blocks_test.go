package tuiapp

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
)

func TestAssistantBlockRenderSuppressesDefaultAssistantLabel(t *testing.T) {
	block := NewAssistantBlock("assistant")
	block.Raw = "hello"
	rows := block.Render(BlockRenderContext{
		Width:     80,
		TermWidth: 100,
		Theme:     tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY),
	})
	if len(rows) == 0 {
		t.Fatal("Render() returned no rows")
	}
	if strings.Contains(rows[0].Plain, "assistant:") {
		t.Fatalf("assistant row = %q, want no assistant label", rows[0].Plain)
	}
}

func TestReasoningBlockRenderSuppressesDefaultAssistantLabel(t *testing.T) {
	block := NewReasoningBlock("assistant")
	block.Raw = "thinking"
	rows := block.Render(BlockRenderContext{
		Width:     80,
		TermWidth: 100,
		Theme:     tuikit.ResolveThemeFromOptions(true, colorprofile.NoTTY),
	})
	if len(rows) == 0 {
		t.Fatal("Render() returned no rows")
	}
	if strings.Contains(rows[0].Plain, "assistant:") {
		t.Fatalf("reasoning row = %q, want no assistant label", rows[0].Plain)
	}
}

func TestMergeSubagentStreamChunkPreservesOverlappingDelta(t *testing.T) {
	got := mergeSubagentStreamChunk("abcabc", "abcXYZ")
	if got != "abcabcabcXYZ" {
		t.Fatalf("merged chunk = %q, want exact appended delta", got)
	}
}

func TestMergeSubagentStreamChunkAcceptsCumulativeReplay(t *testing.T) {
	got := mergeSubagentStreamChunk("你好", "你好，世界")
	if got != "你好，世界" {
		t.Fatalf("merged cumulative chunk = %q, want cumulative replacement", got)
	}
}
