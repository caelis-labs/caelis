package tuiapp

import (
	"time"

	"charm.land/lipgloss/v2"
)

func (phase runningActivityPhase) label() string {
	switch phase {
	case runningPhaseModelWait:
		return "Waiting for response"
	case runningPhaseThinking:
		return "Thinking"
	case runningPhaseResponding:
		return "Responding"
	case runningPhaseSearch:
		return "Searching web"
	case runningPhaseFetch:
		return "Fetching web"
	case runningPhaseToolWait:
		return "Waiting on"
	case runningPhaseCancel:
		return "Canceling"
	case runningPhaseReview:
		return "Review approval"
	case runningPhaseInterrupt:
		return "Interrupting"
	case runningPhaseRetrying:
		return "Retrying"
	case runningPhaseCompact:
		return "Compacting context"
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
	return phase != ""
}

func (m *Model) completeRunningActivity(key string) {
	if m == nil {
		return
	}
	m.runningHintTracker.complete(key, time.Now())
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
	m.runningHintTracker.start(key, phase, target, time.Now(), callID)
	m.refreshRunningActivity()
}

func (m *Model) setRunningInterruptActivity() {
	if m == nil {
		return
	}
	m.runningHintTracker.setOverlay(runningPhaseInterrupt, "interrupt", time.Now())
	m.refreshRunningActivity()
}

func (m *Model) clearRunningInterruptActivity() {
	if m == nil {
		return
	}
	m.runningHintTracker.clearOverlay("interrupt")
	m.refreshRunningActivity()
}

func (m *Model) refreshRunningActivity() {
	if m == nil {
		return
	}
	m.runningActivity = m.runningHintTracker.visible(m.turnRunning())
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
