package tuiapp

import "github.com/caelis-labs/caelis/surfaces/tui/tuikit"

func (m *Model) overlayUsesBorder() bool {
	return m.width >= tuikit.OverlayBorderMinWidth
}

func (m *Model) overlayBorderChromeWidth() int {
	if m.overlayUsesBorder() {
		return tuikit.OverlayBorderChromeWidth
	}
	return 0
}

// completionOverlayRenderedRowWidth is the display width of a single completion row
// after border and padding are applied.
func (m *Model) completionOverlayRenderedRowWidth() int {
	return m.completionOverlayInnerWidth() + m.overlayBorderChromeWidth()
}
