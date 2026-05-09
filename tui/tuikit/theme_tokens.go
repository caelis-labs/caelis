package tuikit

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// Semantic Token System
//
// Tokens provide a layer of indirection between visual components and raw
// colors. Every chrome, block-shell, composer, and overlay primitive
// references tokens rather than Theme color fields directly.
//
// The token set is intentionally semantic: surface elevation, text hierarchy,
// semantic signals, structural edges, chrome/composer/block primitives, and
// transcript-specific roles for tools and markdown.
//
// Each Theme resolves tokens via ResolveTokens(). Components obtain tokens
// through Theme.Tokens().
// ---------------------------------------------------------------------------

// Tokens holds the resolved semantic design tokens for the current theme.
type Tokens struct {
	// ── Surface elevation ──────────────────────────────────────────
	Surface0 lipgloss.Style // deepest background (app bg)
	Surface1 lipgloss.Style // card / panel background
	Surface2 lipgloss.Style // raised / hover / active background

	// ── Text hierarchy ─────────────────────────────────────────────
	TextPrimary   lipgloss.Style // main body text
	TextSecondary lipgloss.Style // secondary labels, meta
	TextMuted     lipgloss.Style // placeholders, disabled

	// ── Semantic signals ───────────────────────────────────────────
	Accent  lipgloss.Style // brand / interactive accent
	Focus   lipgloss.Style // focused element highlight
	Success lipgloss.Style // success / completed
	Warning lipgloss.Style // warning / caution
	Danger  lipgloss.Style // error / destructive

	// ── Structural edges ───────────────────────────────────────────
	BorderSubtle lipgloss.Style // light separators, rail lines
	BorderStrong lipgloss.Style // focused borders, active panels

	// ── Purpose-specific surfaces ──────────────────────────────────
	ChromeBg  lipgloss.Style // header / footer bar background
	CardBg    lipgloss.Style // card / panel body
	CodeBg    lipgloss.Style // code block / inline code background
	OverlayBg lipgloss.Style // modal / overlay backdrop

	// ── Chrome text ────────────────────────────────────────────────
	ChromeTitle lipgloss.Style // header/footer bold title
	ChromeMeta  lipgloss.Style // header/footer metadata
	ChromeHint  lipgloss.Style // hint row text

	// ── Composer ───────────────────────────────────────────────────
	ComposerBorder      lipgloss.Style // composer frame border
	ComposerBorderFocus lipgloss.Style // composer frame focused border
	ComposerLabel       lipgloss.Style // "compose" label
	ComposerPlaceholder lipgloss.Style // placeholder / ghost text
	ComposerCounter     lipgloss.Style // char / attachment counter

	// ── Block shell ────────────────────────────────────────────────
	BlockRail   lipgloss.Style // timeline indentation rail
	BlockHeader lipgloss.Style // block header title
	BlockMeta   lipgloss.Style // block header metadata / elapsed time
	BlockBadge  lipgloss.Style // inline status badge

	// ── Overlay / modal ────────────────────────────────────────────
	OverlayBorder lipgloss.Style // modal frame border
	OverlayTitle  lipgloss.Style // modal title text

	// ── Scrollbar ──────────────────────────────────────────────────
	ScrollTrack lipgloss.Style
	ScrollThumb lipgloss.Style

	// ── Separator ──────────────────────────────────────────────────
	Separator lipgloss.Style // horizontal rule / divider character

	// ── Tool transcript ────────────────────────────────────────────
	ToolIcon   lipgloss.Style
	ToolName   lipgloss.Style
	ToolArgs   lipgloss.Style
	ToolResult lipgloss.Style
	ToolError  lipgloss.Style
	ToolOutput lipgloss.Style

	// ── Markdown / prose ───────────────────────────────────────────
	MarkdownHeading    lipgloss.Style
	MarkdownLink       lipgloss.Style
	MarkdownInlineCode lipgloss.Style
	MarkdownCodeBlock  lipgloss.Style
	MarkdownQuote      lipgloss.Style
	MarkdownTableHead  lipgloss.Style
	MarkdownTableEdge  lipgloss.Style
	MarkdownRule       lipgloss.Style
}

// resolveTokens derives Tokens from a fully populated Theme.
func resolveTokens(t Theme) Tokens {
	return Tokens{
		// Surfaces
		Surface0: bgStyle(t.AppBg),
		Surface1: bgStyle(t.ModalBg),
		Surface2: bgStyle(t.StatusBg),

		// Text
		TextPrimary:   fgStyle(t.TextPrimary),
		TextSecondary: quietStyle(t, t.TextSecondary),
		TextMuted:     quietStyle(t, t.MutedText),

		// Signals
		Accent:  fgStyle(t.Accent),
		Focus:   fgStyle(t.Focus),
		Success: fgStyle(t.Success),
		Warning: fgStyle(t.Warning),
		Danger:  fgStyle(t.Error),

		// Edges
		BorderSubtle: fgStyle(t.PanelBorder),
		BorderStrong: fgStyle(t.Focus),

		// Surfaces
		ChromeBg:  bgStyle(t.StatusBg),
		CardBg:    bgStyle(t.ModalBg),
		CodeBg:    bgStyle(t.CodeBlockBg),
		OverlayBg: bgStyle(t.ModalBg),

		// Chrome text
		ChromeTitle: fgStyle(t.PanelTitle).Bold(true),
		ChromeMeta:  quietStyle(t, t.SecondaryText),
		ChromeHint:  quietStyle(t, t.TextSecondary),

		// Composer
		ComposerBorder:      fgStyle(t.ComposerBorder),
		ComposerBorderFocus: fgStyle(t.ComposerBorderFocus),
		ComposerLabel:       quietStyle(t, t.SecondaryText).Bold(true),
		ComposerPlaceholder: quietStyle(t, t.MutedText).Italic(true),
		ComposerCounter:     quietStyle(t, t.MutedText),

		// Block shell
		BlockRail:   fgStyle(t.TranscriptRail),
		BlockHeader: fgStyle(t.PanelTitle).Bold(true),
		BlockMeta:   quietStyle(t, t.MutedText),
		BlockBadge:  quietStyle(t, t.SecondaryText).Bold(true),

		// Overlay
		OverlayBorder: fgStyle(t.Focus),
		OverlayTitle:  fgStyle(t.PanelTitle).Bold(true),

		// Scrollbar
		ScrollTrack: fgStyle(t.ScrollbarTrack),
		ScrollThumb: fgStyle(t.ScrollbarThumb),

		// Separator
		Separator: fgStyle(t.PanelBorder),

		// Tool transcript
		ToolIcon:   fgStyle(t.ToolFg),
		ToolName:   fgStyle(t.Focus).Bold(true),
		ToolArgs:   quietStyle(t, t.ReasoningFg),
		ToolResult: quietStyle(t, t.SecondaryText),
		ToolError:  fgStyle(firstColor(t.MutedText, t.TextSecondary, t.Warning, t.Error)),
		ToolOutput: quietStyle(t, t.TextSecondary),

		// Markdown / prose
		MarkdownHeading:    fgStyle(t.Accent).Bold(true),
		MarkdownLink:       fgStyle(t.LinkFg).Underline(true),
		MarkdownInlineCode: withBg(fgStyle(t.CodeFg), t.CodeBg),
		MarkdownCodeBlock:  withBg(fgStyle(t.CodeBlockFg), t.CodeBlockBg),
		MarkdownQuote:      quietStyle(t, t.ReasoningFg).Italic(true),
		MarkdownTableHead:  withBg(fgStyle(t.TextPrimary), t.TableHeaderBg).Bold(true),
		MarkdownTableEdge:  fgStyle(t.TableBorder),
		MarkdownRule:       quietStyle(t, t.MutedText),
	}
}

func fgStyle(c color.Color) lipgloss.Style {
	style := lipgloss.NewStyle()
	if c != nil {
		style = style.Foreground(c)
	}
	return style
}

func bgStyle(c color.Color) lipgloss.Style {
	return withBg(lipgloss.NewStyle(), c)
}

func withBg(style lipgloss.Style, c color.Color) lipgloss.Style {
	if c != nil {
		style = style.Background(c)
	}
	return style
}

func quietStyle(t Theme, c color.Color) lipgloss.Style {
	style := fgStyle(c)
	if c == nil && !t.NoColor {
		style = style.Faint(true)
	}
	return style
}
