package tuikit

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// Chrome primitives — header, footer, hint, section divider, composer frame
//
// Chrome is the fixed UI surrounding the scrollable timeline. Every chrome
// element is rendered through these primitives so visual consistency is
// guaranteed across the app.
//
// Rendering is token-driven: each primitive receives a Tokens value (or the
// parent Theme) and a width. The caller in tuiapp simply composes the
// returned strings into the View() output.
// ---------------------------------------------------------------------------

// ChromeBarModel defines the content for a header or footer chrome bar.
type ChromeBarModel struct {
	LeftLabel  string // e.g. "workspace"
	LeftValue  string // e.g. "caelis"
	RightLabel string // e.g. "model"
	RightValue string // e.g. "claude-4-opus"
	Width      int
}

// RenderChromeBar renders a two-column status bar (header or footer).
// Tokens drive the styling: labels use ChromeMeta, values use TextPrimary.
func RenderChromeBar(theme Theme, m ChromeBarModel) string {
	tok := theme.Tokens()
	width := maxInt(1, m.Width)

	var leftParts []string
	if label := strings.TrimSpace(m.LeftLabel); label != "" {
		leftParts = append(leftParts, tok.ChromeMeta.Bold(true).Render(label))
	}
	if val := strings.TrimSpace(m.LeftValue); val != "" {
		leftParts = append(leftParts, tok.TextPrimary.Render(val))
	}
	left := strings.Join(leftParts, " ")

	var rightParts []string
	if label := strings.TrimSpace(m.RightLabel); label != "" {
		rightParts = append(rightParts, tok.ChromeMeta.Bold(true).Render(label))
	}
	if val := strings.TrimSpace(m.RightValue); val != "" {
		rightParts = append(rightParts, tok.TextPrimary.Render(val))
	}
	right := strings.Join(rightParts, " ")

	return composeStyledFooter(width, left, right)
}

// ChromeHintModel defines the content for a hint row.
type ChromeHintModel struct {
	Text  string
	Width int
}

// RenderChromeHint renders a hint row using token-driven styling.
func RenderChromeHint(_ Theme, m ChromeHintModel) string {
	width := maxInt(1, m.Width)
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return strings.Repeat(" ", width)
	}
	return composeStyledFooter(width, text, "")
}

// SectionDividerModel defines a labeled horizontal rule.
type SectionDividerModel struct {
	Label      string // optional centered label (e.g. "compose", "Turn 3")
	RightLabel string // optional right-aligned label
	Width      int
}

// RenderSectionDivider renders a labeled section divider line.
// If Label is empty, renders a plain horizontal rule.
func RenderSectionDivider(theme Theme, m SectionDividerModel) string {
	tok := theme.Tokens()
	width := maxInt(1, m.Width)
	label := strings.TrimSpace(m.Label)
	rightLabel := strings.TrimSpace(m.RightLabel)

	if label == "" && rightLabel == "" {
		return tok.Separator.Render(strings.Repeat("─", width))
	}

	styledLabel := tok.ComposerLabel.Render(label)
	labelWidth := lipgloss.Width(styledLabel)

	var styledRight string
	var rightWidth int
	if rightLabel != "" {
		styledRight = tok.ChromeHint.Render(rightLabel)
		rightWidth = lipgloss.Width(styledRight)
	}

	fillWidth := width - labelWidth
	if styledRight != "" {
		fillWidth -= rightWidth + 2 // " " + right
	}
	fillWidth = maxInt(1, fillWidth-1) // -1 for " " after label

	fill := tok.Separator.Render(strings.Repeat("─", fillWidth))
	line := styledLabel + " " + fill
	if styledRight != "" {
		line += " " + styledRight
	}
	return line
}

// ComposerFrameModel defines the structure of the composer frame.
type ComposerFrameModel struct {
	Width   int
	Focused bool
	Label   string // "compose" label
	Counter string // e.g. "2 attachments"
	Body    string // rendered input content
}

// RenderComposerFrame renders the full composer: divider + body.
// The divider shows the label and optional counter. The body is
// the rendered input bar (which the caller produces separately).
func RenderComposerFrame(theme Theme, m ComposerFrameModel) string {
	divider := RenderSectionDivider(theme, SectionDividerModel{
		Label:      m.Label,
		RightLabel: m.Counter,
		Width:      m.Width,
	})
	if strings.TrimSpace(m.Body) == "" {
		return divider
	}
	return divider + "\n" + m.Body
}

// StatusItemModel defines a single item in a chrome bar.
type StatusItemModel struct {
	Label string
	Value string
	Tone  string // "success", "warning", "error", "accent", or empty
}

// RenderStatusItem renders a label: value pair with tone coloring.
func RenderStatusItem(theme Theme, m StatusItemModel) string {
	tok := theme.Tokens()
	label := tok.ChromeMeta.Render(strings.TrimSpace(m.Label))
	value := strings.TrimSpace(m.Value)
	if value == "" {
		return label
	}
	var valueStyle lipgloss.Style
	switch strings.ToLower(strings.TrimSpace(m.Tone)) {
	case "success":
		valueStyle = tok.Success
	case "warning":
		valueStyle = tok.Warning
	case "error":
		valueStyle = tok.Danger
	case "accent":
		valueStyle = tok.Accent
	default:
		valueStyle = tok.TextPrimary
	}
	return label + " " + valueStyle.Render(value)
}

// BadgePillModel defines an inline status badge/pill.
type BadgePillModel struct {
	Label string
	Tone  string // "success", "warning", "error", "accent", or empty
}

// RenderBadgePill renders a small status badge using token-based tone colors.
func RenderBadgePill(theme Theme, m BadgePillModel) string {
	label := strings.TrimSpace(m.Label)
	if label == "" {
		return ""
	}
	return theme.TranscriptPillStyle(m.Tone).Render(label)
}
