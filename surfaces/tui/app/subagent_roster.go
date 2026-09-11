package tuiapp

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

const (
	subagentRosterIdentityMaxColumns = 32
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
