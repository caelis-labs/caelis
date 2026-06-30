package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	runningActivityApprovalReviewDefault = "Reviewing approval request..."
	runningActivityInterruptingDefault   = "Interrupting current turn..."
)

func (kind runningActivityKind) defaultDetail() string {
	switch kind {
	case runningActivityApprovalReview:
		return runningActivityApprovalReviewDefault
	case runningActivityInterrupting:
		return runningActivityInterruptingDefault
	default:
		return ""
	}
}

func (m *Model) handleRunningActivityMsg(msg RunningActivityMsg) (tea.Model, tea.Cmd) {
	if msg.Active {
		m.setRunningActivity(msg.Kind, msg.Detail)
	} else {
		m.clearRunningActivity(msg.Kind)
	}
	m.ensureViewportLayout()
	return m, m.resumeRunningAnimationIfNeeded()
}

func (m *Model) setRunningActivity(kind runningActivityKind, detail string) {
	if m == nil || kind == "" {
		return
	}
	m.runningActivity = runningActivityState{
		Kind:   kind,
		Detail: strings.TrimSpace(detail),
	}
}

func (m *Model) clearRunningActivity(kind runningActivityKind) {
	if m == nil {
		return
	}
	if kind == "" || m.runningActivity.Kind == kind {
		m.runningActivity = runningActivityState{}
	}
}

func (m *Model) setRunningInterruptActivity() {
	m.setRunningActivity(runningActivityInterrupting, "")
}

func (m *Model) clearRunningInterruptActivity() {
	m.clearRunningActivity(runningActivityInterrupting)
}

func (m *Model) runningActivityText() (string, lipgloss.Style) {
	if m == nil {
		return "", lipgloss.Style{}
	}
	detail := compactString(strings.TrimSpace(m.runningActivity.Detail), 0)
	if detail == "" {
		detail = m.runningActivity.Kind.defaultDetail()
	}
	switch m.runningActivity.Kind {
	case runningActivityApprovalReview, runningActivityInterrupting:
		return detail, m.theme.WarnStyle()
	default:
		return "", lipgloss.Style{}
	}
}
