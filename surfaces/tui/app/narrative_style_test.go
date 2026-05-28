package tuiapp

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

func TestNarrativePrefixesUseUserReasoningAssistantMarkers(t *testing.T) {
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 80, TermWidth: 80, Theme: m.theme}

	userRows := NewUserNarrativeBlock("please inspect git@github.com:OnslaughtSnail/knowledge.git").Render(ctx)
	if len(userRows) < 3 || userRows[0].Plain != "" || userRows[len(userRows)-1].Plain != "" {
		t.Fatalf("user rows = %#v, want padded user block", renderedPlainRows(userRows))
	}
	userContentRow := userRows[1]
	if !strings.HasPrefix(userContentRow.Plain, "▌ please inspect git@github.com") {
		t.Fatalf("user rows = %#v, want user block marker", renderedPlainRows(userRows))
	}
	expectedUserStyled := ctx.Theme.UserStyle().Width(ctx.Width).Render(userContentRow.Plain)
	if userContentRow.Styled != expectedUserStyled {
		t.Fatalf("user row styled with extra token coloring:\n got: %q\nwant: %q", userContentRow.Styled, expectedUserStyled)
	}
	if !strings.Contains(userContentRow.Styled, "\x1b[48;") || !strings.Contains(userRows[0].Styled, "\x1b[48;") {
		t.Fatalf("user rows missing background contrast: %#v", userRows)
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

func TestCommittedUserDisplayLineUsesPlainUserSurface(t *testing.T) {
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	ctx := BlockRenderContext{Width: 90, TermWidth: 90, Theme: m.theme}
	text := "帮我把当前笔记项目推送到git@github.com:OnslaughtSnail/knowledge.git"

	m.commitUserDisplayLine(text)
	if m.doc.Len() != 1 {
		t.Fatalf("document length = %d, want 1", m.doc.Len())
	}
	rows := m.doc.Blocks()[0].Render(ctx)
	if len(rows) == 0 {
		t.Fatal("committed user line rendered no rows")
	}
	wantPlain := "▌ " + text
	if len(rows) < 3 || rows[0].Plain != "" || rows[len(rows)-1].Plain != "" {
		t.Fatalf("committed user rows = %#v, want padded user block", renderedPlainRows(rows))
	}
	contentRow := rows[1]
	if contentRow.Plain != wantPlain {
		t.Fatalf("committed user plain = %q, want %q", contentRow.Plain, wantPlain)
	}
	if got := strings.TrimRight(ansi.Strip(contentRow.Styled), " "); got != wantPlain {
		t.Fatalf("committed user styled strips to %q, want %q", got, wantPlain)
	}
	expectedStyled := ctx.Theme.UserStyle().Width(ctx.Width).Render(wantPlain)
	if contentRow.Styled != expectedStyled {
		t.Fatalf("committed user line should be one plain user surface:\n got: %q\nwant: %q", rows[0].Styled, expectedStyled)
	}
}

func TestCommittedUserDisplayLineDedupsGatewayEchoAfterImageDisplay(t *testing.T) {
	m := NewModel(Config{})

	m.commitUserDisplayLine("[image #1] describe this")
	m.handleUserMessageMsg(UserMessageMsg{Text: "describe this"})

	if m.doc.Len() != 1 {
		t.Fatalf("document length = %d, want gateway echo deduped", m.doc.Len())
	}
	block, ok := m.doc.Blocks()[0].(*UserNarrativeBlock)
	if !ok || block.Raw != "[image #1] describe this" {
		t.Fatalf("user block = %#v, want original image display line", m.doc.Blocks()[0])
	}
}

func TestImageAttachmentDisplayUsesShortOrdinalLabels(t *testing.T) {
	m := NewModel(Config{})
	attachments := []inputAttachment{
		{Name: "clipboard-20260527-172440-5239-17272.png", Offset: 0},
		{Name: "another-very-long-file-name.png", Offset: len([]rune("look "))},
	}

	if got := m.displayLineWithInputAttachments("look here", attachments); got != "[image #1] look [image #2] here" {
		t.Fatalf("display line = %q, want short ordinal image labels", got)
	}
	display, _ := composeInputDisplay("look here", len([]rune("look here")), attachments)
	if got := strings.TrimSpace(display); got != "[image #1] look [image #2] here" {
		t.Fatalf("input display = %q, want short ordinal image labels", got)
	}
}

func TestReasoningColorizeUsesMutedNonItalicBody(t *testing.T) {
	theme := tuikit.DefaultTheme()
	line := "› **Canvas-based** reasoning"
	styled := tuikit.ColorizeLogLine(line, tuikit.LineStyleReasoning, theme)
	if strings.Contains(styled, "\x1b[3m") {
		t.Fatalf("styled reasoning = %q, should not use italic styling", styled)
	}
	if !strings.Contains(styled, "**Canvas-based** reasoning") {
		t.Fatalf("styled reasoning = %q, want raw body text preserved", styled)
	}
	if !strings.Contains(styled, theme.ReasoningStyle().Render("**Canvas-based** reasoning")) {
		t.Fatalf("styled reasoning = %q, want muted reasoning body styling", styled)
	}
}
