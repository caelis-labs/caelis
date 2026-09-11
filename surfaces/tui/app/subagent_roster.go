package tuiapp

import (
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

type subagentRosterRow struct {
	callID    string
	handle    string
	binding   string
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
		handle, binding := subagentRosterMetadata(view)
		status, startedAt, endedAt := m.subagentRosterViewState(callID, view)
		row := subagentRosterRow{
			callID:    strings.TrimSpace(callID),
			handle:    handle,
			binding:   binding,
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

func subagentRosterMetadata(view *subagentOutputView) (handle string, binding string) {
	if view == nil {
		return "", ""
	}
	title := compactSingleLine(view.title)
	actor := compactSingleLine(view.actor)
	prefix := actor
	if before, _, ok := strings.Cut(title, ":"); ok {
		if prefix == "" {
			prefix = strings.TrimSpace(before)
		}
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
		handle = "Participant"
	}
	return handle, binding
}

func compactSingleLine(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func (m *Model) renderSubagentRosterRow(row subagentRosterRow, selected bool, width int) string {
	style := m.theme.TextStyle().Background(m.theme.Tokens().OverlayBg.GetBackground())
	if selected {
		style = m.theme.SelectionStyle()
	}
	identity := m.renderSubagentIdentity(row, maxInt(0, width-2), style)
	return style.Render(" ") + identity + style.Render(strings.Repeat(" ", maxInt(0, width-displayColumns(identity)-1)))
}

func (m *Model) renderSubagentIdentity(row subagentRosterRow, width int, style lipgloss.Style) string {
	handle, binding := subagentRosterIdentityParts(row, width)
	bindingStyle := style.Bold(false)
	if !m.theme.NoColor {
		bindingStyle = bindingStyle.Foreground(m.theme.MutedText)
	}
	return style.Render(handle) + bindingStyle.Render(binding)
}

func subagentRosterBindingText(binding string) string {
	binding = strings.TrimSpace(binding)
	if binding == "" || strings.EqualFold(binding, "self") {
		return ""
	}
	return "[" + binding + "]"
}

func subagentRosterIdentityParts(row subagentRosterRow, budget int) (string, string) {
	if budget <= 0 {
		return "", ""
	}
	handle := strings.TrimSpace(row.handle)
	binding := subagentRosterBindingText(row.binding)
	if binding == "" {
		return truncateTailDisplay(handle, budget), ""
	}
	bindingBudget := minInt(displayColumns(binding), budget/2)
	handle = truncateTailDisplay(handle, budget-bindingBudget)
	remaining := budget - displayColumns(handle)
	if remaining <= 0 {
		return handle, ""
	}
	return handle, truncateTailDisplay(binding, remaining)
}
