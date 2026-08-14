package tuiapp

import (
	"fmt"
	"strings"
	"time"
)

func (m *Model) renderResumeList() string {
	return m.renderCompletionKind(completionResume)
}

func (m *Model) renderResumeListGeometry(geometry completionOverlayGeometry, candidates []ResumeCandidate) string {
	lines := make([]string, 0, geometry.candidateCount)
	count := geometry.candidateCount
	titles := make([]string, count)
	ages := make([]string, count)
	titleColumnWidth := 0
	ageColumnWidth := 0
	for i := geometry.windowStart; i < geometry.windowEnd; i++ {
		item := candidates[i]
		title := firstNonEmpty(strings.TrimSpace(item.Title), strings.TrimSpace(item.Prompt), strings.TrimSpace(item.SessionID))
		title = strings.Join(strings.Fields(strings.TrimSpace(title)), " ")
		titles[i-geometry.windowStart] = title
		titleColumnWidth = maxInt(titleColumnWidth, displayColumns(title))

		age := resumeDisplayAge(candidates[i])
		ages[i-geometry.windowStart] = age
		ageColumnWidth = maxInt(ageColumnWidth, displayColumns(age))
	}
	if ageColumnWidth > 0 {
		ageColumnWidth = maxInt(ageColumnWidth, displayColumns("1d ago"))
	}
	for i := geometry.windowStart; i < geometry.windowEnd; i++ {
		lines = append(lines, m.renderResumeCandidateLine(
			titles[i-geometry.windowStart],
			ages[i-geometry.windowStart],
			titleColumnWidth,
			ageColumnWidth,
			i == geometry.selected,
		))
	}
	return m.renderCompletionOverlay(geometry, lines)
}

func (m *Model) renderResumeCandidateLine(title string, age string, titleColumnWidth int, ageColumnWidth int, selected bool) string {
	title = strings.Join(strings.Fields(strings.TrimSpace(title)), " ")
	age = strings.Join(strings.Fields(strings.TrimSpace(age)), " ")
	width := m.completionRowInnerWidth()
	if age == "" || ageColumnWidth <= 0 {
		return m.renderResumeTitleOnly(title, width, selected)
	}
	separator := "  "
	separatorWidth := displayColumns(separator)
	ageColumnWidth = minInt(ageColumnWidth, maxInt(0, width-separatorWidth-8))
	if ageColumnWidth <= 0 {
		return m.renderResumeTitleOnly(title, width, selected)
	}
	titleBudget := minInt(maxInt(1, titleColumnWidth), maxInt(1, width-separatorWidth-ageColumnWidth))
	titleText := truncateTailDisplay(title, titleBudget)
	truncationBudget := titleBudget
	for displayColumns(titleText) > titleBudget && truncationBudget > 1 {
		truncationBudget--
		titleText = truncateTailDisplay(title, truncationBudget)
	}
	titlePadding := ""
	if pad := titleBudget - displayColumns(titleText); pad > 0 {
		titlePadding = strings.Repeat(" ", pad)
	}
	ageText := truncateTailDisplay(age, ageColumnWidth)
	if selected {
		return m.renderCompletionSelectedLine(width, titleText+titlePadding+separator+ageText)
	}
	return m.renderCompletionUnselectedLine(
		width,
		m.theme.CommandStyle().Padding(0, 0).Render(titleText),
		titlePadding,
		separator,
		m.theme.HelpHintTextStyle().Render(ageText),
	)
}

func (m *Model) renderResumeTitleOnly(title string, width int, selected bool) string {
	titleText := truncateTailDisplay(title, width)
	if selected {
		return m.renderCompletionSelectedLine(width, titleText)
	}
	return m.renderCompletionUnselectedLine(
		width,
		m.theme.CommandStyle().Padding(0, 0).Render(titleText),
	)
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
