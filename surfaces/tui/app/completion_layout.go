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
	groupedRows    []completionOverlayRow
	chrome         completionOverlayChrome
	scroll         completionScrollAffordance
}

type completionOverlayRow struct {
	candidate int
	heading   string
}

func (m *Model) completionOverlayBodyLimit() int {
	limit := completionOverlayVisibleItems
	if m.height > 0 {
		chrome := m.completionChrome()
		available := m.workspaceLayout().main.height - m.bottomSectionHeight() - chrome.rowCount()
		if available < 1 {
			available = 1
		}
		limit = minInt(limit, available)
	}
	return limit
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
	bodyLimit := m.completionOverlayBodyLimit()
	visible := minInt(total, bodyLimit)
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
	bodyRows := end - start
	var groupedRows []completionOverlayRow
	if snapshot.kind == completionSlashCommand && len(m.slashGroups) > 0 && total > 0 && bodyLimit > 1 {
		groupedRows = make([]completionOverlayRow, 0, bodyLimit)
		for {
			groupedRows, end = layoutGroupedCompletionRows(groupedRows[:0], m.slashGroups, start, total, bodyLimit)
			if selected < end {
				break
			}
			start++
		}
		bodyRows = len(groupedRows)
	}

	chrome := m.completionChrome()
	height := bodyRows + chrome.rowCount()
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
		groupedRows:    groupedRows,
		chrome:         chrome,
		scroll:         scroll,
	}
}

// layoutGroupedCompletionRows keeps each heading with its first candidate and
// repeats the heading when a group continues at the top of the window. Short
// windows reserve their row budget for headings and candidates.
func layoutGroupedCompletionRows(rows []completionOverlayRow, groups []slashCompletionGroup, start, total, limit int) ([]completionOverlayRow, int) {
	group := 0
	for group+1 < len(groups) && groups[group+1].start <= start {
		group++
	}
	index := start
	for ; index < total && len(rows) < limit; index++ {
		if group+1 < len(groups) && groups[group+1].start == index {
			group++
		}
		if index == start || groups[group].start == index {
			gap := len(rows) > 0 && limit == completionOverlayVisibleItems
			needed := 2
			if gap {
				needed++
			}
			if len(rows)+needed > limit {
				break
			}
			if gap {
				rows = append(rows, completionOverlayRow{candidate: -1})
			}
			rows = append(rows, completionOverlayRow{candidate: -1, heading: groups[group].title})
		}
		rows = append(rows, completionOverlayRow{candidate: index})
	}
	return rows, index
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
	if len(g.groupedRows) > 0 {
		if row < 0 || row >= len(g.groupedRows) || g.groupedRows[row].candidate < 0 {
			return 0, false
		}
		return g.groupedRows[row].candidate, true
	}
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
