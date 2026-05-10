package tuiapp

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
)

func TestNarrativePrefixesUseUserReasoningAssistantMarkers(t *testing.T) {
	m := newPerfTestModel()
	ctx := BlockRenderContext{Width: 80, TermWidth: 80, Theme: m.theme}

	userRows := NewUserNarrativeBlock("please inspect this").Render(ctx)
	if len(userRows) == 0 || !strings.HasPrefix(userRows[0].Plain, "▌ please inspect this") {
		t.Fatalf("user rows = %#v, want user block marker", renderedPlainRows(userRows))
	}
	if userRows[0].Styled == userRows[0].Plain || !strings.Contains(userRows[0].Styled, "\x1b[") {
		t.Fatalf("user row missing styled contrast: %#v", userRows[0])
	}

	assistant := NewAssistantBlock()
	assistant.Streaming = false
	assistant.Raw = "done"
	assistantRows := assistant.Render(ctx)
	if len(assistantRows) == 0 || !strings.HasPrefix(assistantRows[0].Plain, "· done") {
		t.Fatalf("assistant rows = %#v, want assistant marker", renderedPlainRows(assistantRows))
	}

	reasoning := NewReasoningBlock()
	reasoning.Streaming = false
	reasoning.Raw = "**Canvas-based**\n1. Keep raw reasoning text"
	reasoningRows := reasoning.Render(ctx)
	joined := strings.Join(renderedPlainRows(reasoningRows), "\n")
	if !strings.Contains(joined, "› **Canvas-based**") {
		t.Fatalf("reasoning rows = %q, want raw reasoning with marker", joined)
	}
	if strings.Contains(joined, "• Canvas-based") || strings.Contains(joined, "Canvas-based pixel") {
		t.Fatalf("reasoning rows = %q, should not markdown-render reasoning text", joined)
	}
}

func TestReasoningColorizeOnlyStylesPrefix(t *testing.T) {
	theme := tuikit.DefaultTheme()
	line := "› **Canvas-based** reasoning"
	styled := tuikit.ColorizeLogLine(line, tuikit.LineStyleReasoning, theme)
	if !strings.Contains(styled, "**Canvas-based** reasoning") {
		t.Fatalf("styled reasoning = %q, want raw body text preserved", styled)
	}
}
