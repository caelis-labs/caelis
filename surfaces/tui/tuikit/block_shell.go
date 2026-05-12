package tuikit

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// Timeline Block Shell — unified visual container for all document blocks.
//
// Every block in the scrollable timeline (tool panels, diffs, subagent
// sessions, activity folds, etc.) renders inside a BlockShell. The shell
// provides:
//
//   - A consistent header row (icon + kind badge + title + state + elapsed)
//   - An optional rail/border (transcript pipe vs. card box)
//   - An optional footer row (status summary or action hints)
//
// The shell delegates all content rendering to the caller; it only provides
// the structural chrome around the content rows.
// ---------------------------------------------------------------------------

// BlockShellVariant controls the visual frame of the block shell.
type BlockShellVariant string

const (
	// BlockShellRail renders a thin vertical rail ("│ ") to the left of
	// content, matching the transcript pipe style.
	BlockShellRail BlockShellVariant = "rail"

	// BlockShellBox renders a rounded-corner border box around the content.
	BlockShellBox BlockShellVariant = "box"

	// BlockShellNone renders no border; the header/footer are still shown.
	BlockShellNone BlockShellVariant = "none"
)

// BlockShellModel defines the content and structure of a block shell.
type BlockShellModel struct {
	Variant  BlockShellVariant
	Width    int
	Expanded bool

	// Header fields
	Kind    string // e.g. "BASH", "DIFF", "SPAWN"
	Title   string // e.g. command text, file path, agent name
	State   string // e.g. "running", "completed", "failed"
	Elapsed time.Duration

	// Content (only rendered when Expanded is true)
	Body []string

	// Footer (optional, rendered below body)
	Footer string
}

// RenderBlockShell renders a unified block shell with header, optional body,
// optional footer. Token-driven styling ensures visual consistency across all
// block types.
func RenderBlockShell(theme Theme, m BlockShellModel) []string {
	tok := theme.Tokens()
	width := maxInt(1, m.Width)

	// ── Build header ──────────────────────────────────────────────
	headerWidth := width
	if m.Variant == BlockShellBox {
		headerWidth = maxInt(1, width-4) // account for box padding
	}
	header := renderBlockShellHeader(tok, theme, headerWidth, m)

	// ── Collapsed: header only ────────────────────────────────────
	if !m.Expanded {
		switch m.Variant {
		case BlockShellBox:
			return wrapInBox(theme, width, []string{header}, "")
		case BlockShellRail:
			return []string{theme.TranscriptShellStyle().Render("╭─ ") + header}
		default:
			return []string{header}
		}
	}

	// ── Expanded: header + body + footer ──────────────────────────
	footer := strings.TrimSpace(m.Footer)

	switch m.Variant {
	case BlockShellBox:
		content := make([]string, 0, len(m.Body)+2)
		content = append(content, header)
		content = append(content, m.Body...)
		return wrapInBox(theme, width, content, footer)

	case BlockShellRail:
		lines := make([]string, 0, len(m.Body)+3)
		lines = append(lines, theme.TranscriptShellStyle().Render("╭─ ")+header)
		railPrefix := tok.BlockRail.Render("│ ")
		for _, line := range m.Body {
			if strings.TrimSpace(line) == "" {
				lines = append(lines, railPrefix)
			} else {
				lines = append(lines, railPrefix+line)
			}
		}
		if footer != "" {
			lines = append(lines, theme.TranscriptShellStyle().Render("╰─ ")+footer)
		} else {
			lines = append(lines, theme.TranscriptShellStyle().Render("╰─"))
		}
		return lines

	default: // BlockShellNone
		lines := make([]string, 0, len(m.Body)+2)
		lines = append(lines, header)
		lines = append(lines, m.Body...)
		if footer != "" {
			lines = append(lines, footer)
		}
		return lines
	}
}

// renderBlockShellHeader builds the header line for a block shell.
func renderBlockShellHeader(tok Tokens, theme Theme, width int, m BlockShellModel) string {
	icon := "▾"
	if !m.Expanded {
		icon = "▸"
	}

	leftParts := []string{
		tok.BlockRail.Bold(true).Render(icon),
	}

	if kind := strings.ToUpper(strings.TrimSpace(m.Kind)); kind != "" {
		leftParts = append(leftParts, RenderBadgePill(theme, BadgePillModel{
			Label: kind,
			Tone:  "accent",
		}))
	}

	if title := strings.TrimSpace(m.Title); title != "" {
		leftParts = append(leftParts, tok.BlockHeader.Render(title))
	}

	if state := strings.TrimSpace(m.State); state != "" {
		leftParts = append(leftParts, RenderBadgePill(theme, BadgePillModel{
			Label: statusLabel(state),
			Tone:  statusTone(state),
		}))
	}

	left := strings.Join(filterNonEmptyStrings(leftParts), " ")

	// Elapsed time on the right.
	right := ""
	if m.Elapsed > 0 {
		right = tok.BlockMeta.Render(formatElapsed(m.Elapsed))
	}

	return composeStyledFooter(width, left, right)
}

// wrapInBox renders content inside a rounded-border lipgloss box.
func wrapInBox(theme Theme, width int, content []string, footer string) []string {
	all := make([]string, 0, len(content)+1)
	all = append(all, content...)
	if footer != "" {
		all = append(all, footer)
	}
	boxStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.PanelBorder).
		Padding(0, 1).
		Width(width)
	return strings.Split(boxStyle.Render(strings.Join(all, "\n")), "\n")
}

// formatElapsed formats a duration into a human-readable string.
func formatElapsed(d time.Duration) string {
	switch {
	case d < time.Second:
		ms := d.Milliseconds()
		if ms <= 0 {
			return "<1ms"
		}
		return d.Truncate(time.Millisecond).String()
	case d < time.Minute:
		return strings.TrimRight(strings.TrimRight(
			d.Truncate(100*time.Millisecond).String(),
			"0"), ".")
	default:
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s == 0 {
			return strings.Replace(d.Truncate(time.Minute).String(), "m0s", "m", 1)
		}
		_ = m // use truncated string
		return d.Truncate(time.Second).String()
	}
}
