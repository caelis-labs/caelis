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
