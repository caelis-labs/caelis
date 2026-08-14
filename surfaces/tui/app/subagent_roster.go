package tuiapp

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

const (
	subagentRosterIdentityMaxColumns = 24
	subagentRosterPromptMaxColumns   = 64
	subagentRosterPromptMinColumns   = 12
)

type subagentRosterRow struct {
	callID    string
	handle    string
	binding   string
	summary   string
	status    subagentOutputStatus
	startedAt time.Time
	endedAt   time.Time
}

func (r subagentRosterRow) running() bool {
	return r.status == subagentOutputRunning
}

type subagentRosterOverlayGeometry struct {
	x      int
	y      int
	closeX int
	closeY int
	width  int
	height int
	rows   []subagentRosterRowGeometry
}

type subagentRosterRowGeometry struct {
	top    int
	bottom int
}

type subagentRosterOverlayState struct {
	index          int
	selectedCallID string
	rows           []subagentRosterRow
	geometry       subagentRosterOverlayGeometry
	pressedCallID  string
}

func (m *Model) subagentRosterCount() int {
	if m == nil {
		return 0
	}
	count := 0
	for _, view := range m.subagentOutputViews {
		if view != nil && normalizeTaskStreamHandle(view.taskHandle) != "" {
			count++
		}
	}
	return count
}

func (m *Model) subagentRosterRunningCount() int {
	if m == nil {
		return 0
	}
	count := 0
	for callID, view := range m.subagentOutputViews {
		if view == nil || normalizeTaskStreamHandle(view.taskHandle) == "" {
			continue
		}
		status, _, _ := m.subagentRosterViewState(callID, view)
		if status == subagentOutputRunning {
			count++
		}
	}
	return count
}

func (m *Model) subagentRosterRows() []subagentRosterRow {
	if m == nil {
		return nil
	}
	rows := make([]subagentRosterRow, 0, len(m.subagentOutputViews))
	for callID, view := range m.subagentOutputViews {
		if view == nil || normalizeTaskStreamHandle(view.taskHandle) == "" {
			continue
		}
		handle, binding, summary := subagentRosterMetadata(view)
		status, startedAt, endedAt := m.subagentRosterViewState(callID, view)
		row := subagentRosterRow{
			callID:    strings.TrimSpace(callID),
			handle:    handle,
			binding:   binding,
			summary:   summary,
			status:    status,
			startedAt: startedAt,
			endedAt:   endedAt,
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if left.running() != right.running() {
			return left.running()
		}
		if left.running() && !left.startedAt.Equal(right.startedAt) {
			if left.startedAt.IsZero() {
				return false
			}
			if right.startedAt.IsZero() {
				return true
			}
			return left.startedAt.Before(right.startedAt)
		}
		if !left.running() && !left.endedAt.Equal(right.endedAt) {
			if left.endedAt.IsZero() {
				return false
			}
			if right.endedAt.IsZero() {
				return true
			}
			return left.endedAt.After(right.endedAt)
		}
		if left.handle != right.handle {
			return left.handle < right.handle
		}
		return left.callID < right.callID
	})
	return rows
}

func subagentRosterMetadata(view *subagentOutputView) (handle string, binding string, summary string) {
	if view == nil {
		return "", "", ""
	}
	title := compactSingleLine(view.title)
	actor := compactSingleLine(view.actor)
	prefix := actor
	if before, after, ok := strings.Cut(title, ":"); ok {
		if prefix == "" {
			prefix = strings.TrimSpace(before)
		}
		summary = strings.TrimSpace(after)
	} else if title != "" && title != actor {
		summary = title
	}

	handle = normalizeTaskStreamHandle(view.taskHandle)
	if open := strings.Index(prefix, "["); open >= 0 {
		if closeOffset := strings.Index(prefix[open+1:], "]"); closeOffset >= 0 {
			binding = strings.TrimSpace(prefix[open+1 : open+1+closeOffset])
		}
		if handle == "" {
			handle = strings.TrimSpace(prefix[:open])
		}
	} else if handle == "" {
		handle = strings.TrimSpace(prefix)
	}
	if handle == "" {
		handle = "subagent"
	}
	return handle, binding, summary
}

func compactSingleLine(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func (m *Model) openSubagentRosterOverlay() bool {
	if m == nil || m.activePrompt != nil || m.subagentRosterCount() == 0 {
		return false
	}
	if m.subagentOutputOverlay != nil {
		m.closeSubagentOutputOverlay()
	}
	m.clearInputOverlays()
	m.showPalette = false
	m.subagentOverlay = nil
	m.subagentRosterPressed = false
	m.subagentRosterOverlay = &subagentRosterOverlayState{}
	return true
}

func (m *Model) closeSubagentRosterOverlay() {
	if m == nil {
		return
	}
	m.subagentRosterOverlay = nil
	m.subagentRosterPressed = false
}

func (m *Model) renderSubagentRosterOverlay() string {
	if m == nil || m.subagentRosterOverlay == nil {
		return ""
	}
	state := m.subagentRosterOverlay
	state.rows = m.subagentRosterRows()
	if len(state.rows) == 0 {
		m.closeSubagentRosterOverlay()
		return ""
	}
	m.reconcileSubagentRosterSelection()

	width := minInt(maxInt(60, m.fixedRowWidth()-20), 92)
	if m.width > 0 {
		width = minInt(width, maxInt(20, m.width-4))
	}
	innerWidth := maxInt(16, width-m.overlayBorderChromeWidth())
	body := []string{
		m.renderSubagentRosterTitle(innerWidth),
		"",
	}
	rowLines, rowOffsets := m.renderSubagentRosterRows(state.rows, innerWidth, len(body), time.Now())
	body = append(body, rowLines...)
	body = append(body, "", m.theme.HelpHintTextStyle().Render(truncateTailDisplay("↑↓ select  Enter open  Esc close", innerWidth)))

	frame := tuikit.RenderResponsiveOverlayFrame(m.theme, tuikit.ResponsiveOverlayFrameModel{
		Body:      body,
		Width:     width,
		UseBorder: m.overlayUsesBorder(),
	})
	renderedWidth := lipgloss.Width(frame)
	renderedHeight := len(strings.Split(frame, "\n"))
	startX := maxInt(0, (m.width-renderedWidth)/2)
	startY := maxInt(0, (m.height-renderedHeight)/2)
	borderInset := 0
	contentInset := 0
	if m.overlayUsesBorder() {
		borderInset = 1
		contentInset = 2
	}
	screenRows := make([]subagentRosterRowGeometry, len(rowOffsets))
	for index, offset := range rowOffsets {
		screenRows[index] = subagentRosterRowGeometry{top: -1, bottom: -1}
		if offset.top >= 0 {
			screenRows[index] = subagentRosterRowGeometry{
				top:    startY + borderInset + offset.top,
				bottom: startY + borderInset + offset.bottom,
			}
		}
	}
	state.geometry = subagentRosterOverlayGeometry{
		x: startX, y: startY,
		closeX: startX + contentInset + maxInt(0, innerWidth-1),
		closeY: startY + borderInset,
		width:  renderedWidth, height: renderedHeight, rows: screenRows,
	}
	return frame
}

func (m *Model) renderSubagentRosterTitle(width int) string {
	title := m.theme.TitleStyle().Render("Subagents")
	close := m.theme.HelpHintTextStyle().Render("×")
	gap := maxInt(1, width-displayColumns(title)-displayColumns(close))
	return title + strings.Repeat(" ", gap) + close
}

func (m *Model) renderSubagentRosterRows(rows []subagentRosterRow, width int, bodyOffset int, now time.Time) ([]string, []subagentRosterRowGeometry) {
	if len(rows) == 0 {
		return nil, nil
	}
	maxVisible := maxInt(1, m.height-12)
	start := 0
	if len(rows) > maxVisible {
		start = m.subagentRosterOverlay.index - maxVisible/2
		start = clampInt(start, 0, len(rows)-maxVisible)
	}
	end := minInt(len(rows), start+maxVisible)
	lines := make([]string, 0, end-start+5)
	offsets := make([]subagentRosterRowGeometry, len(rows))
	for index := range offsets {
		offsets[index] = subagentRosterRowGeometry{top: -1, bottom: -1}
	}
	if start > 0 {
		lines = append(lines, m.theme.HelpHintTextStyle().Render("  ↑ more"))
	}
	lastRunning := !rows[start].running()
	for index := start; index < end; index++ {
		row := rows[index]
		if index == start || row.running() != lastRunning {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			label := "Done"
			if row.running() {
				label = "Running"
			}
			lines = append(lines, m.renderSubagentRosterSection(label, subagentRosterSectionCount(rows, row.running())))
		}
		lastRunning = row.running()
		selected := index == m.subagentRosterOverlay.index
		offsets[index].top = bodyOffset + len(lines)
		lines = append(lines, m.renderSubagentRosterRow(row, selected, width, now))
		offsets[index].bottom = bodyOffset + len(lines) - 1
	}
	if end < len(rows) {
		lines = append(lines, m.theme.HelpHintTextStyle().Render("  ↓ more"))
	}
	return lines, offsets
}

func (m *Model) renderSubagentRosterSection(label string, count int) string {
	return m.theme.SecondaryTextStyle().Bold(true).Render(label) +
		m.theme.MutedTextStyle().Render("  "+fmt.Sprintf("%d", count))
}

func subagentRosterSectionCount(rows []subagentRosterRow, running bool) int {
	count := 0
	for _, row := range rows {
		if row.running() == running {
			count++
		}
	}
	return count
}

func (m *Model) renderSubagentRosterRow(row subagentRosterRow, selected bool, width int, now time.Time) string {
	layout := layoutSubagentRosterRow(row, maxInt(1, width-2), now)
	if selected {
		selection := m.theme.SelectionStyle()
		return selection.Render(" "+layout.mark+" ") +
			selection.Bold(true).Render(layout.handle) +
			selection.Render(layout.binding+layout.prompt+layout.gap+layout.time+" ")
	}

	markStyle := lipgloss.NewStyle().Foreground(m.theme.Accent)
	switch row.status {
	case subagentOutputSucceeded:
		markStyle = lipgloss.NewStyle().Foreground(m.theme.Success)
	case subagentOutputFailed:
		markStyle = m.theme.ErrorStyle()
	}
	line := " " + markStyle.Render(layout.mark) + " " + m.theme.TextStyle().Bold(true).Render(layout.handle)
	line += m.theme.MutedTextStyle().Render(layout.binding)
	line += m.theme.HelpHintTextStyle().Render(layout.prompt)
	line += layout.gap + m.theme.MutedTextStyle().Render(layout.time)
	return line + " "
}

type subagentRosterRowLayout struct {
	mark    string
	handle  string
	binding string
	prompt  string
	gap     string
	time    string
}

func layoutSubagentRosterRow(row subagentRosterRow, width int, now time.Time) subagentRosterRowLayout {
	mark := subagentRosterStatusMark(row.status)
	timeText := subagentRosterTimeText(row, now)
	timeReserve := displayColumns(timeText)
	if timeText != "" {
		timeReserve += 2
	}
	bodyBudget := maxInt(1, width-displayColumns(mark)-1-timeReserve)
	identityBudget := minInt(subagentRosterIdentityMaxColumns, bodyBudget)
	summary := compactSingleLine(row.summary)
	if summary != "" && bodyBudget > subagentRosterPromptMinColumns+2 {
		identityBudget = minInt(identityBudget, bodyBudget-subagentRosterPromptMinColumns-2)
	}
	handle, binding := subagentRosterIdentityParts(row, maxInt(1, identityBudget))
	identityWidth := displayColumns(handle) + displayColumns(binding)

	prompt := ""
	if promptBudget := bodyBudget - identityWidth - 2; summary != "" && promptBudget >= subagentRosterPromptMinColumns {
		prompt = "  " + subagentRosterPromptText(summary, promptBudget)
	}
	headWidth := displayColumns(mark) + 1 + identityWidth + displayColumns(prompt)
	gap := strings.Repeat(" ", maxInt(0, width-headWidth-displayColumns(timeText)))
	return subagentRosterRowLayout{
		mark: mark, handle: handle, binding: binding, prompt: prompt, gap: gap, time: timeText,
	}
}

func subagentRosterStatusMark(status subagentOutputStatus) string {
	switch status {
	case subagentOutputSucceeded:
		return "✓"
	case subagentOutputFailed:
		return "×"
	default:
		return "•"
	}
}

func subagentRosterBindingText(binding string) string {
	binding = strings.TrimSpace(binding)
	if binding == "" || strings.EqualFold(binding, "self") {
		return ""
	}
	return "[" + binding + "]"
}

func subagentRosterIdentityParts(row subagentRosterRow, budget int) (string, string) {
	handle := strings.TrimSpace(row.handle)
	binding := subagentRosterBindingText(row.binding)
	if binding == "" {
		return truncateTailDisplay(handle, budget), ""
	}
	bindingBudget := minInt(displayColumns(binding), maxInt(1, budget/2))
	handleBudget := maxInt(1, budget-bindingBudget-2)
	handle = truncateTailDisplay(handle, handleBudget)
	remaining := budget - displayColumns(handle) - 2
	if remaining <= 0 {
		return handle, ""
	}
	binding = truncateTailDisplay(binding, remaining)
	return handle, "  " + binding
}

func subagentRosterPromptText(summary string, budget int) string {
	if budget <= 0 {
		return ""
	}
	return truncateMiddleDisplayWidthPlain(summary, minInt(subagentRosterPromptMaxColumns, budget))
}

func subagentRosterTimeText(row subagentRosterRow, now time.Time) string {
	if row.running() {
		return formatSubagentRosterElapsed(now, row.startedAt)
	}
	if row.endedAt.IsZero() {
		return ""
	}
	return row.endedAt.Local().Format("15:04")
}

func formatSubagentRosterElapsed(now time.Time, startedAt time.Time) string {
	if startedAt.IsZero() || now.Before(startedAt) {
		return "00:00"
	}
	elapsed := now.Sub(startedAt).Truncate(time.Second)
	hours := int(elapsed / time.Hour)
	minutes := int(elapsed % time.Hour / time.Minute)
	seconds := int(elapsed % time.Minute / time.Second)
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func (m *Model) reconcileSubagentRosterSelection() {
	state := m.subagentRosterOverlay
	if state == nil || len(state.rows) == 0 {
		return
	}
	if state.selectedCallID != "" {
		for index, row := range state.rows {
			if row.callID == state.selectedCallID {
				state.index = index
				break
			}
		}
	}
	state.index = clampInt(state.index, 0, len(state.rows)-1)
	state.selectedCallID = state.rows[state.index].callID
}
