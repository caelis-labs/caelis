package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// containerHorizontalPadding is the inner left/right padding inside the UserBg
// composer chrome. A positive value is intentional: the gray bar is slightly
// wider than the ">" prompt. Combined with composerOuterInset it keeps the
// prompt column on InputInset (GutterNarrative+1) instead of stacking past it.
const containerHorizontalPadding = 1

// composerChrome captures UserBg container padding applied around the composer.
type composerChrome struct {
	active bool
}

func (m *Model) composerChrome() composerChrome {
	if m == nil || m.theme.UserBg == nil || m.theme.NoColor {
		return composerChrome{}
	}
	return composerChrome{active: true}
}

func (m *Model) composerBgStyle() lipgloss.Style {
	if !m.composerChrome().active {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Background(m.theme.UserBg)
}

func (c composerChrome) horizontalInset() int {
	if c.active {
		return containerHorizontalPadding
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

// composerOuterInset is the left/right margin outside the UserBg container (or
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
	chrome := m.composerChrome()
	if !chrome.active {
		return text
	}

	containerWidth := m.composerContainerWidth()
	if containerWidth <= 0 {
		return text
	}

	lines := strings.Split(text, "\n")
	bgStyle := m.composerBgStyle()

	contentWidth := containerWidth - (containerHorizontalPadding * 2)
	if contentWidth < 0 {
		contentWidth = 0
	}

	padLeft := strings.Repeat(" ", containerHorizontalPadding)
	padRight := strings.Repeat(" ", containerHorizontalPadding)
	styledPadLeft := bgStyle.Render(padLeft)
	styledPadRight := bgStyle.Render(padRight)

	styledLines := make([]string, 0, len(lines)+2)
	styledLines = append(styledLines, bgStyle.Render(strings.Repeat(" ", containerWidth)))

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

	styledLines = append(styledLines, bgStyle.Render(strings.Repeat(" ", containerWidth)))
	return strings.Join(styledLines, "\n")
}

func (m *Model) finalizeInputBarRender(rendered string) string {
	wrapped := m.wrapInputBarInContainer(rendered)
	insetted := insetRenderedBlock(wrapped, m.composerOuterInset())
	return protectWideCellRepaintBlock(insetted, m.fixedRowWidth())
}
