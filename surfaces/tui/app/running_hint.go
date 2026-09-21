package tuiapp

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *Model) runningFrame() string {
	if m == nil {
		return ""
	}
	if m.noAnimation {
		return "•"
	}
	frame := strings.TrimSpace(ansi.Strip(m.spinner.View()))
	if frame == "" {
		frame = "⠋"
	}
	return frame
}

func (m *Model) startRunningAnimation() {
	m.spinnerTickScheduled = false
	if m.turnRunning() {
		m.runningHintTracker.beginTurn(m.liveTurn.StartedAt)
		m.refreshRunningActivity()
		return
	}
	m.runningActivity = runningActivityState{}
}

func (m *Model) stopRunningAnimation() {
	m.runningHintTracker.endTurn()
	m.runningActivity = runningActivityState{}
	m.spinnerTickScheduled = false
}

func (m *Model) scheduleSpinnerTick() tea.Cmd {
	if m == nil || m.noAnimation || !m.animationIndicatorActive() || m.spinnerTickScheduled {
		return nil
	}
	m.spinnerTickScheduled = true
	return m.spinner.Tick
}

func (m *Model) resumeRunningAnimationIfNeeded() tea.Cmd {
	if m == nil || !m.animationIndicatorActive() {
		return nil
	}
	return m.scheduleSpinnerTick()
}

func (m *Model) buildRunningHintText() string {
	return m.buildRunningHintTextAt(time.Now())
}

func (m *Model) buildRunningHintTextAt(now time.Time) string {
	frame := m.runningFrame()
	prefix := m.theme.SpinnerStyle().Render(frame)
	if text := m.slashArgLoadStatusText(); m.slashArgLoadPending && text != "" {
		if m.width > 0 {
			text = truncateTailDisplay(text, maxInt(1, m.fixedRowContentWidth()-2))
		}
		return prefix + " " + m.theme.HelpHintTextStyle().Render(text)
	}
	width := 0
	if m.width > 0 {
		width = maxInt(1, m.fixedRowContentWidth()-2)
	}
	return m.renderActivityHint(m.runningActivity, now, width, m.pendingQueue.visibleCount())
}

// renderActivityHint shares presentation across panes without sharing their
// activity or pending-input state. Width excludes the spinner and its space.
func (m *Model) renderActivityHint(activity runningActivityState, now time.Time, width, pending int) string {
	prefix := m.theme.SpinnerStyle().Render(m.runningFrame())
	text, style := m.runningActivityStyle(activity)
	if text == "" {
		text = runningPhaseModelWait.label()
		style = m.theme.HelpHintTextStyle()
	}
	parts := []string{text}
	if activity.Phase.showsElapsed() {
		parts = append(parts, formatRunningActivityElapsed(now, activity.StartedAt))
	}
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", pending))
	}
	text = strings.Join(parts, " · ")
	if width > 0 {
		text = truncateTailDisplay(text, width)
	}
	return prefix + " " + style.Render(text)
}

func formatRunningActivityElapsed(now time.Time, startedAt time.Time) string {
	if startedAt.IsZero() || now.Before(startedAt) {
		return "0.1s"
	}
	elapsed := now.Sub(startedAt)
	if elapsed < 10*time.Second {
		tenths := maxInt(1, int(elapsed/(100*time.Millisecond)))
		return fmt.Sprintf("%.1fs", float64(tenths)/10)
	}
	elapsed = elapsed.Truncate(time.Second)
	if elapsed < time.Minute {
		return fmt.Sprintf("%ds", int(elapsed/time.Second))
	}
	if elapsed < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(elapsed/time.Minute), int(elapsed%time.Minute/time.Second))
	}
	return fmt.Sprintf("%dh%02dm", int(elapsed/time.Hour), int(elapsed%time.Hour/time.Minute))
}

func (m *Model) slashArgLoadStatusText() string {
	if m == nil || !m.slashArgLoadPending {
		return ""
	}
	label := strings.TrimRight(strings.TrimSpace(m.slashArgLoadLabel), ".")
	if label == "" {
		return ""
	}
	parts := []string{label}
	if !m.slashArgLoadStartedAt.IsZero() {
		if elapsed := time.Since(m.slashArgLoadStartedAt); elapsed >= time.Second {
			parts = append(parts, formatACPSetupElapsed(elapsed))
		}
	}
	parts = append(parts, "Esc cancels")
	return strings.Join(parts, " · ")
}

func formatACPSetupElapsed(elapsed time.Duration) string {
	elapsed = elapsed.Round(time.Second)
	if elapsed < time.Minute {
		return fmt.Sprintf("%ds elapsed", int(elapsed/time.Second))
	}
	minutes := int(elapsed / time.Minute)
	seconds := int(elapsed%time.Minute) / int(time.Second)
	return fmt.Sprintf("%dm %02ds elapsed", minutes, seconds)
}

func (m *Model) runningIndicatorActive() bool {
	return m != nil && (m.turnRunning() || m.slashArgLoadPending)
}

func (m *Model) animationIndicatorActive() bool {
	return m != nil && (m.runningIndicatorActive() || m.subagentOutputPulseActive() || m.sessionSwitchPending || m.sessionHistory != nil ||
		m.wizardOverlay != nil && m.wizardBusy())
}
