package tuiapp

import tea "charm.land/bubbletea/v2"

type completionKind uint8

const (
	completionNone completionKind = iota
	completionMention
	completionSlashArg
	completionSlashCommand
)

type completionWindowState struct {
	kind  completionKind
	start int
}

type completionOverlayChrome struct {
	useBorder        bool
	topBorderRows    int
	bottomBorderRows int
	topInsetRows     int
	bottomInsetRows  int
	footerRows       int
	headerRows       int
	detailRows       int
}

func completionOverlayChromeFor(useBorder bool) completionOverlayChrome {
	chrome := completionOverlayChrome{useBorder: useBorder, footerRows: 1}
	if useBorder {
		chrome.topBorderRows = 1
		chrome.bottomBorderRows = 1
		chrome.topInsetRows = 1
		chrome.bottomInsetRows = 1
	}
	return chrome
}

func (c completionOverlayChrome) rowCount() int {
	return c.topBorderRows +
		c.bottomBorderRows +
		c.topInsetRows +
		c.bottomInsetRows +
		c.footerRows + c.headerRows + c.detailRows
}

func (c completionOverlayChrome) candidateOffset() int {
	return c.topBorderRows + c.topInsetRows + c.headerRows
}

func (m *Model) completionChrome() completionOverlayChrome {
	chrome := completionOverlayChromeFor(m.overlayUsesBorder())
	if m.isModelPicker() {
		chrome.headerRows = 1
		chrome.detailRows = 2
		if m.height > 0 && m.height < 20 {
			chrome.detailRows = 1
		}
	}
	return chrome
}

// completionOverlayGeometry is the single paint and pointer layout model for
// the active completion overlay.
type completionOverlayGeometry struct {
	kind           completionKind
	selected       int
	windowStart    int
	windowEnd      int
	total          int
	left           int
	top            int
	width          int
	height         int
	innerWidth     int
	candidateTop   int
	candidateCount int
	chrome         completionOverlayChrome
	scroll         completionScrollAffordance
}

func (m *Model) completionOverlayVisibleLimit(total int) int {
	if total <= 0 {
		return 0
	}
	limit := completionOverlayVisibleItems
	if m.height > 0 {
		chrome := m.completionChrome()
		available := m.workspaceLayout().main.height - m.bottomSectionHeight() - chrome.rowCount()
		if available < 1 {
			available = 1
		}
		limit = minInt(limit, available)
	}
	return minInt(total, limit)
}

func (m *Model) buildCompletionOverlayGeometry(snapshot completionSnapshot) completionOverlayGeometry {
	total := maxInt(0, snapshot.total)
	selected := snapshot.selected
	if selected < 0 {
		selected = 0
	}
	if selected >= total && total > 0 {
		selected = total - 1
	}
	visible := m.completionOverlayVisibleLimit(total)
	start := 0
	if m.completionWindow.kind == snapshot.kind {
		start = m.completionWindow.start
	} else {
		start, _ = completionWindowRange(selected, total, visible)
	}
	maxStart := maxInt(0, total-visible)
	if start < 0 {
		start = 0
	}
	if start > maxStart {
		start = maxStart
	}
	if selected < start {
		start = selected
	}
	if selected >= start+visible && visible > 0 {
		start = selected - visible + 1
	}
	if start < 0 {
		start = 0
	}
	if start > maxStart {
		start = maxStart
	}
	end := minInt(total, start+visible)

	chrome := m.completionChrome()
	height := end - start + chrome.rowCount()
	main := m.workspaceLayout().main
	top := main.y + main.height - m.bottomSectionHeight() - height
	if top < main.y {
		top = main.y
	}
	candidateTop := top + chrome.candidateOffset()
	atBottom := selected >= total-1
	scroll := completionScrollFromWindow(start, end, total, atBottom, snapshot.canLoadMore)
	return completionOverlayGeometry{
		kind:           snapshot.kind,
		selected:       selected,
		windowStart:    start,
		windowEnd:      end,
		total:          total,
		left:           m.mainColumnX() + inputHorizontalInset,
		top:            top,
		width:          m.completionOverlayRenderedRowWidth(),
		height:         height,
		innerWidth:     m.completionOverlayInnerWidth(),
		candidateTop:   candidateTop,
		candidateCount: end - start,
		chrome:         chrome,
		scroll:         scroll,
	}
}

func (g completionOverlayGeometry) contains(mouse tea.Mouse) bool {
	return mouse.X >= g.left &&
		mouse.X < g.left+g.width &&
		mouse.Y >= g.top &&
		mouse.Y < g.top+g.height
}

func (g completionOverlayGeometry) candidateIndexAt(mouse tea.Mouse) (int, bool) {
	if !g.contains(mouse) {
		return 0, false
	}
	row := mouse.Y - g.candidateTop
	if row < 0 || row >= g.candidateCount {
		return 0, false
	}
	return g.windowStart + row, true
}

func (m *Model) pinCompletionWindow(geometry completionOverlayGeometry) {
	m.completionWindow = completionWindowState{
		kind:  geometry.kind,
		start: geometry.windowStart,
	}
}
