package tuiapp

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// containerHorizontalPadding is the inner left/right padding inside the
// composer chrome. A positive value is intentional: the neutral bar is slightly
// wider than the ">" prompt. Combined with composerOuterInset it keeps the
// prompt column on InputInset (GutterNarrative+1) instead of stacking past it.
const containerHorizontalPadding = 1

// composerChrome captures ComposerBg container padding applied around the composer.
type composerChrome struct {
	active            bool
	horizontalPadding int
	background        color.Color
}

func (m *Model) composerChrome() composerChrome {
	return m.promptComposerChrome(m != nil && !m.workspace.childFocused)
}

// Both editors use the same focus colors without mutating the transcript theme.
func (m *Model) promptComposerChrome(focused bool) composerChrome {
	if m == nil || m.theme.ComposerBg == nil || m.theme.NoColor {
		return composerChrome{}
	}
	bg := m.theme.ComposerBg
	if m.subagentOutputOverlay != nil && m.activePrompt == nil && focused && m.theme.ComposerFocusBg != nil {
		bg = m.theme.ComposerFocusBg
	}
	return composerChrome{active: true, horizontalPadding: containerHorizontalPadding, background: bg}
}

func (m *Model) composerBgStyle() lipgloss.Style {
	if !m.composerChrome().active {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Background(m.composerChrome().background)
}

func (c composerChrome) horizontalInset() int {
	if c.active {
		return c.horizontalPadding
	}
	return 0
}

func (c composerChrome) verticalRows() int {
	if c.active {
		return 2
	}
	return 0
}

func (c composerChrome) topRows() int {
	if c.active {
		return 1
	}
	return 0
}

// composerOuterInset is the left/right margin outside the ComposerBg container (or
// around the plain input when chrome is inactive). outer + chrome pad always
// equals InputInset so the prompt sits one column right of the transcript
// gutter while the gray bar stays slightly wider than the ">".
func (m *Model) composerOuterInset() int {
	if m == nil {
		return inputHorizontalInset
	}
	pad := m.composerChrome().horizontalInset()
	if pad >= inputHorizontalInset {
		return 0
	}
	return inputHorizontalInset - pad
}

func (m *Model) composerContainerWidth() int {
	return m.fixedRowWidth() - (m.composerOuterInset() * 2)
}

// composerInputColumnOffset is the screen column where composer prompt text
// begins (relative to the main column). Lands on InputInset (GutterNarrative+1).
func (m *Model) composerInputColumnOffset() int {
	return m.composerOuterInset() + m.composerChrome().horizontalInset()
}

func (m *Model) wrapInputBarInContainer(text string) string {
	return wrapPromptEditor(text, m.composerChrome(), m.composerContainerWidth(), m.composerBgStyle())
}

func wrapPromptEditor(text string, chrome composerChrome, containerWidth int, bgStyle lipgloss.Style) string {
	if !chrome.active {
		return text
	}
	if containerWidth <= 0 {
		return text
	}

	lines := strings.Split(text, "\n")

	padding := chrome.horizontalInset()
	contentWidth := containerWidth - (padding * 2)
	if contentWidth < 0 {
		contentWidth = 0
	}

	padLeft := strings.Repeat(" ", padding)
	padRight := strings.Repeat(" ", padding)
	styledPadLeft := bgStyle.Render(padLeft)
	styledPadRight := bgStyle.Render(padRight)
	top := bgStyle.Render(strings.Repeat(" ", containerWidth))

	styledLines := make([]string, 0, len(lines)+2)
	styledLines = append(styledLines, top)

	for _, line := range lines {
		plainLine := ansi.Strip(line)
		lineLen := displayColumns(plainLine)

		var paddedContent string
		switch {
		case lineLen < contentWidth:
			paddedContent = line + bgStyle.Render(strings.Repeat(" ", contentWidth-lineLen))
		case lineLen > contentWidth:
			paddedContent = sliceByDisplayColumns(line, 0, contentWidth)
		default:
			paddedContent = line
		}

		styledLines = append(styledLines, styledPadLeft+paddedContent+styledPadRight)
	}

	styledLines = append(styledLines, top)
	return strings.Join(styledLines, "\n")
}

func (m *Model) finalizeInputBarRender(rendered string) string {
	wrapped := m.wrapInputBarInContainer(rendered)
	insetted := insetRenderedBlock(wrapped, m.composerOuterInset())
	return protectWideCellRepaintBlock(insetted, m.fixedRowWidth())
}
