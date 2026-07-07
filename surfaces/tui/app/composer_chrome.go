package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const containerHorizontalPadding = 2

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

func (m *Model) composerContainerWidth() int {
	return m.fixedRowWidth() - (inputHorizontalInset * 2)
}

func (m *Model) composerInputColumnOffset() int {
	return inputHorizontalInset + m.composerChrome().horizontalInset()
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
	insetted := insetRenderedBlock(wrapped, inputHorizontalInset)
	return protectWideCellRepaintBlock(insetted, m.fixedRowWidth())
}
