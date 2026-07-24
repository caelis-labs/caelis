package tuiapp

import (
	"time"

	"charm.land/lipgloss/v2"
)

func (phase runningActivityPhase) label() string {
	switch phase {
	case runningPhaseThinking:
		return "Thinking"
	case runningPhaseResponding:
		return "Responding"
	case runningPhaseWait:
		return "Wait"
	case runningPhaseRead:
		return "Read"
	case runningPhaseCancel:
		return "Cancel"
	case runningPhaseReview:
		return "Review approval"
	case runningPhaseInterrupt:
		return "Interrupting"
	default:
		return ""
	}
}

func (target runningActivityTarget) label() string {
	switch target {
	case runningTargetShell:
		return "shell"
	case runningTargetSubagent:
		return "subagent"
	case runningTargetTask:
		return "task"
	default:
		return ""
	}
}

func (state runningActivityState) label() string {
	label := state.Phase.label()
	if target := state.Target.label(); target != "" {
		label += " " + target
	}
	return label
}

func (phase runningActivityPhase) showsElapsed() bool {
	switch phase {
	case runningPhaseWait, runningPhaseRead, runningPhaseCancel, runningPhaseReview, runningPhaseInterrupt:
		return true
	default:
		return false
	}
}

func (m *Model) completeRunningActivity(key string) {
	if m == nil {
		return
	}
	m.runningActivityTracker.complete(key)
	m.refreshRunningActivity()
}

func (m *Model) setRunningToolActivity(
	phase runningActivityPhase,
	target runningActivityTarget,
	key string,
	callID string,
) {
	if m == nil {
		return
	}
	m.runningActivityTracker.start(key, phase, target, time.Now(), callID)
	m.refreshRunningActivity()
}

func (m *Model) setRunningInterruptActivity() {
	if m == nil {
		return
	}
	m.runningActivityTracker.setOverlay(runningPhaseInterrupt, "interrupt", time.Now())
	m.refreshRunningActivity()
}

func (m *Model) clearRunningInterruptActivity() {
	if m == nil {
		return
	}
	m.runningActivityTracker.clearOverlay("interrupt")
	m.refreshRunningActivity()
}

func (m *Model) refreshRunningActivity() {
	if m == nil {
		return
	}
	m.runningActivity = m.runningActivityTracker.visible(m.turnRunning())
}

func (m *Model) runningActivityText() (string, lipgloss.Style) {
	if m == nil {
		return "", lipgloss.Style{}
	}
	label := m.runningActivity.label()
	switch m.runningActivity.Phase {
	case runningPhaseReview, runningPhaseInterrupt:
		return label, m.theme.WarnStyle()
	default:
		return label, m.theme.HelpHintTextStyle()
	}
}
