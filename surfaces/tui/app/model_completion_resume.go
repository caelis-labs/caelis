package tuiapp

import (
	"fmt"
	"strings"
	"time"
)

func (m *Model) renderResumeList() string {
	if len(m.resumeCandidates) == 0 {
		return ""
	}
	maxItems := minInt(8, len(m.resumeCandidates))
	start := 0
	if m.resumeIndex >= maxItems {
		start = m.resumeIndex - maxItems + 1
	}
	maxStart := maxInt(0, len(m.resumeCandidates)-maxItems)
	if start > maxStart {
		start = maxStart
	}
	end := minInt(len(m.resumeCandidates), start+maxItems)
	lines := make([]string, 0, end-start)
	count := end - start
	titles := make([]string, count)
	ages := make([]string, count)
	titleColumnWidth := 0
	ageColumnWidth := 0
	for i := start; i < end; i++ {
		item := m.resumeCandidates[i]
		title := firstNonEmpty(strings.TrimSpace(item.Title), strings.TrimSpace(item.Prompt), strings.TrimSpace(item.SessionID))
		title = strings.Join(strings.Fields(strings.TrimSpace(title)), " ")
		titles[i-start] = title
		titleStyle := m.theme.CommandStyle()
		if i == m.resumeIndex {
			titleStyle = m.theme.CommandActiveStyle()
		}
		titleColumnWidth = maxInt(titleColumnWidth, displayColumns(titleStyle.Render(title)))

		age := resumeDisplayAge(m.resumeCandidates[i])
		ages[i-start] = age
		ageColumnWidth = maxInt(ageColumnWidth, displayColumns(age))
	}
	if ageColumnWidth > 0 {
		ageColumnWidth = maxInt(ageColumnWidth, displayColumns("1d ago"))
	}
	for i := start; i < end; i++ {
		lines = append(lines, m.renderResumeCandidateLine(titles[i-start], ages[i-start], titleColumnWidth, ageColumnWidth, i == m.resumeIndex))
	}
	return m.renderCompletionOverlay("Recent", lines)
}

func (m *Model) renderResumeCandidateLine(title string, age string, titleColumnWidth int, ageColumnWidth int, selected bool) string {
	title = strings.Join(strings.Fields(strings.TrimSpace(title)), " ")
	age = strings.Join(strings.Fields(strings.TrimSpace(age)), " ")
	style := m.theme.CommandStyle()
	if selected {
		style = m.theme.CommandActiveStyle()
	}
	width := m.completionOverlayInnerWidth()
	if age == "" || ageColumnWidth <= 0 {
		return style.Render(truncateTailDisplay(title, width))
	}
	separator := "  "
	separatorWidth := displayColumns(separator)
	ageColumnWidth = minInt(ageColumnWidth, maxInt(0, width-separatorWidth-8))
	if ageColumnWidth <= 0 {
		return style.Render(truncateTailDisplay(title, width))
	}
	titleBudget := minInt(maxInt(1, titleColumnWidth), maxInt(1, width-separatorWidth-ageColumnWidth))
	titleText := truncateTailDisplay(title, titleBudget)
	line := style.Render(titleText)
	for displayColumns(line) > titleBudget && titleBudget > 1 {
		titleBudget--
		titleText = truncateTailDisplay(title, titleBudget)
		line = style.Render(titleText)
	}
	if pad := titleBudget - displayColumns(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	line += separator + m.theme.HelpHintTextStyle().Render(truncateTailDisplay(age, ageColumnWidth))
	return line
}

func resumeDisplayAge(candidate ResumeCandidate) string {
	if age := formatResumeAge(candidate.Age); age != "" {
		return age
	}
	if candidate.UpdatedAt.IsZero() {
		return ""
	}
	return formatResumeDuration(time.Since(candidate.UpdatedAt))
}

func formatResumeAge(age string) string {
	raw := strings.Join(strings.Fields(strings.TrimSpace(age)), " ")
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	switch lower {
	case "now", "just now":
		return "now"
	}
	value := strings.TrimSpace(strings.TrimSuffix(lower, " ago"))
	d, err := time.ParseDuration(value)
	if err != nil {
		return raw
	}
	return formatResumeDuration(d)
}

func formatResumeDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}
