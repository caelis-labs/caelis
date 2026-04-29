package tuikit

import "strings"

// ---------------------------------------------------------------------------
// Layout spacing tokens — unified vertical rhythm & horizontal margins
//
// Three named spacing tokens govern ALL vertical gaps:
//   SpaceTight  = 0 blank lines  (e.g., between list items)
//   SpaceNormal = 1 blank line   (e.g., between paragraphs, around blocks)
//   SpaceBlock  = 2 blank lines  (e.g., between major sections)
//
// Every TUI region (transcript, log block, composer, status bar) references
// these constants so spacing stays consistent regardless of content type.
// ---------------------------------------------------------------------------

const (
	// ── Vertical spacing tokens (blank lines) ──────────────────────────

	// SpaceTight is zero blank lines — used between list items and
	// tightly coupled lines within the same block.
	SpaceTight = 0

	// SpaceNormal is one blank line — the default gap between paragraphs,
	// above/below lists, above user prompts, and around log blocks.
	SpaceNormal = 1

	// SpaceBlock is two blank lines — used between major layout sections
	// (e.g., between conversation turns when extra emphasis is desired).
	SpaceBlock = 2

	// ── Horizontal spacing (columns) ───────────────────────────────────

	// GutterNarrative is the left margin applied to the main viewport
	// content (assistant text, reasoning blocks).  Applied via indentBlock.
	GutterNarrative = 2

	// ReadableContentMaxWidth is the baseline readable transcript width. Past
	// this width the UI can continue expanding on moderately wide terminals
	// before applying the wide-terminal soft cap below.
	ReadableContentMaxWidth = 100

	// ReadableContentWideMaxWidth is the soft cap used on very wide terminals
	// so the transcript can breathe without collapsing back to a narrow card.
	ReadableContentWideMaxWidth = 132

	// ReadableContentMaxSidePadding is the maximum whitespace reserved on each
	// side of the main column in wide terminals.
	ReadableContentMaxSidePadding = 8

	// GutterUser is the total left margin for user prompt lines.
	// Keep user turns aligned with the main narrative column so the
	// brighter prefix/body styling can do the differentiation work.
	GutterUser = 2

	// GutterLog is the total left margin for log/tool/error/warn lines.
	// Keep logs on the same baseline as narrative and diff/tool previews.
	GutterLog = 2

	// InputInset is the left indent for the composer input bar.
	InputInset = 3

	// StatusInset is the horizontal padding inside status/footer rows
	// (applied via lipgloss Padding).
	StatusInset = InputInset

	// ── Vertical rules per content type ────────────────────────────────

	// Gap between conversation turns (user→assistant, assistant→user).
	SpaceTurnGap = SpaceNormal

	// Gap above and below a log block (narrative↔log boundary).
	SpaceLogBlockGap = SpaceNormal

	// Gap between consecutive tool call lines (▸ TOOL1 / ▸ TOOL2).
	SpaceToolGap = SpaceNormal

	// ── Composer layout ────────────────────────────────────────────────

	// ComposerMinHeight is the minimum visible rows for the input textarea.
	ComposerMinHeight = 1

	// ComposerPadTop is the number of empty lines above the input bar
	// (inside the bottom section, between hint row and input).
	ComposerPadTop = 0

	// ComposerPadBottom is the number of empty lines below the input bar
	// / completion lists (between last completion and lower separator).
	ComposerPadBottom = 0

	// ── Status bar ─────────────────────────────────────────────────────

	// StatusBarPadBottom is the number of empty lines below the status
	// footer row at the very bottom of the screen.
	StatusBarPadBottom = 0
)

// LineExtraGutter returns the extra whitespace to prepend to a colorized
// line based on its detected style, so that user prompts and log lines
// are visually indented beyond the narrative baseline (GutterNarrative).
func LineExtraGutter(style LineStyle) string {
	switch style {
	case LineStyleUser:
		n := GutterUser - GutterNarrative
		if n > 0 {
			return strings.Repeat(" ", n)
		}
	case LineStyleTool, LineStyleWarn, LineStyleError, LineStyleNote:
		n := GutterLog - GutterNarrative
		if n > 0 {
			return strings.Repeat(" ", n)
		}
	}
	return ""
}
