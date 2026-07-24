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
		m.runningActivityTracker.beginTurn(m.liveTurn.StartedAt)
		m.refreshRunningActivity()
		return
	}
	m.runningActivity = runningActivityState{}
}

func (m *Model) stopRunningAnimation() {
	m.runningActivityTracker.endTurn()
	m.runningActivity = runningActivityState{}
	m.spinnerTickScheduled = false
}

func (m *Model) scheduleSpinnerTick() tea.Cmd {
	if m == nil || m.noAnimation || !m.runningIndicatorActive() || m.spinnerTickScheduled {
		return nil
	}
	m.spinnerTickScheduled = true
	return m.spinner.Tick
}

func (m *Model) resumeRunningAnimationIfNeeded() tea.Cmd {
	if m == nil || !m.runningIndicatorActive() {
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
	text, style := m.runningActivityText()
	if text == "" {
		text = runningPhaseThinking.label()
		style = m.theme.HelpHintTextStyle()
	}
	parts := []string{text}
	if m.runningActivity.Phase.showsElapsed() {
		parts = append(parts, formatRunningActivityElapsed(now, m.runningActivity.StartedAt))
	}
	if pending := m.pendingQueue.visibleCount(); pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", pending))
	}
	text = strings.Join(parts, " · ")
	if m.width > 0 {
		text = truncateTailDisplay(text, maxInt(1, m.fixedRowContentWidth()-2))
	}
	return prefix + " " + style.Render(text)
}

func formatRunningActivityElapsed(now time.Time, startedAt time.Time) string {
	if startedAt.IsZero() || now.Before(startedAt) {
		return "0s"
	}
	elapsed := now.Sub(startedAt).Truncate(time.Second)
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
	if m.slashArgLoadBytes > 0 {
		parts = append(parts, formatACPSetupBytes(m.slashArgLoadBytes)+" written")
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

func formatACPSetupBytes(size int64) string {
	const (
		kiB = int64(1024)
		miB = 1024 * kiB
		giB = 1024 * miB
	)
	switch {
	case size >= giB:
		return fmt.Sprintf("%.1f GB", float64(size)/float64(giB))
	case size >= miB:
		return fmt.Sprintf("%.1f MB", float64(size)/float64(miB))
	case size >= kiB:
		return fmt.Sprintf("%.0f KB", float64(size)/float64(kiB))
	default:
		return fmt.Sprintf("%d B", size)
	}
}

func (m *Model) runningIndicatorActive() bool {
	return m != nil && (m.turnRunning() || m.slashArgLoadPending)
}
