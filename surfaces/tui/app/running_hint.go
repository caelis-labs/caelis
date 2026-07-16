package tuiapp

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *Model) runningFrame() string {
	if m == nil {
		return ""
	}
	frame := strings.TrimSpace(ansi.Strip(m.spinner.View()))
	if frame == "" {
		frame = "⠋"
	}
	return frame
}

func (m *Model) startRunningAnimation() {
	m.runningTick = 0
	m.spinnerTickScheduled = false
	m.runningActivity = runningActivityState{}
	if len(runningCarouselLines) > 0 {
		seed := int(time.Now().UnixNano() % int64(len(runningCarouselLines)))
		if seed < 0 {
			seed = -seed
		}
		m.runningTip = seed
	} else {
		m.runningTip = 0
	}
}

func (m *Model) stopRunningAnimation() {
	m.runningTick = 0
	m.runningTip = 0
	m.runningActivity = runningActivityState{}
	m.spinnerTickScheduled = false
}

func (m *Model) advanceRunningAnimation() {
	if len(runningCarouselLines) > 0 {
		m.runningTick++
		if m.runningTick%runningHintRotateEveryTicks == 0 {
			m.runningTip = (m.runningTip + 1) % len(runningCarouselLines)
		}
	}
}

func (m *Model) shouldUseStaticRunningCarousel() bool {
	if m == nil {
		return true
	}
	return m.noAnimation ||
		m.shouldDeferStreamViewportSync() ||
		m.streamPlayback.LastFrameRenderCost >= runningTickerStaticFrameCostThreshold
}

func (m *Model) scheduleSpinnerTick() tea.Cmd {
	if m == nil || !m.runningIndicatorActive() || m.spinnerTickScheduled {
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
	frame := m.runningFrame()
	prefix := m.theme.SpinnerStyle().Render(frame)
	if text := m.slashArgLoadStatusText(); m.slashArgLoadPending && text != "" {
		if m.width > 0 {
			text = truncateTailDisplay(text, maxInt(1, m.fixedRowContentWidth()-2))
		}
		return prefix + " " + m.theme.HelpHintTextStyle().Render(text)
	}
	if text, style := m.runningActivityText(); text != "" {
		if m.width > 0 {
			text = truncateTailDisplay(text, maxInt(1, m.fixedRowContentWidth()-2))
		}
		return prefix + " " + style.Render(text)
	}
	if len(runningCarouselLines) > 0 {
		rawText := runningCarouselLines[m.runningTip%len(runningCarouselLines)]
		text := ""
		if m.shouldUseStaticRunningCarousel() {
			m.diag.RunningTickerStaticRenders++
			text = m.theme.HelpHintTextStyle().Render(strings.TrimSpace(rawText))
		} else {
			text = m.renderRunningTickerText(rawText)
		}
		return prefix + " " + text
	}
	return prefix
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

func (m *Model) runningTickerStyleSet() []lipgloss.Style {
	if m == nil {
		return nil
	}
	themeKey := m.cachedThemeRenderKey()
	if len(m.runningTickerStyles) == 5 && m.runningTickerThemeKey == themeKey {
		return m.runningTickerStyles
	}
	m.diag.RunningTickerStyleCacheMisses++
	m.runningTickerThemeKey = themeKey
	m.runningTickerStyles = []lipgloss.Style{
		m.theme.HelpHintTextStyle(),
		m.theme.SecondaryTextStyle(),
		lipgloss.NewStyle().Foreground(m.theme.Info),
		lipgloss.NewStyle().Foreground(m.theme.SpinnerFg),
		lipgloss.NewStyle().Foreground(m.theme.Focus),
	}
	return m.runningTickerStyles
}

func (m *Model) renderRunningTickerText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	totalWidth := maxInt(1, displayColumns(text))
	pathWidth := float64(totalWidth) + (runningLightLead * 2)
	head := math.Mod(float64(m.runningTick)*runningLightSpeed, pathWidth) - runningLightLead
	styles := m.runningTickerStyleSet()
	if len(styles) < 5 {
		return text
	}
	m.diag.RunningTickerAnimatedRenders++

	var out strings.Builder
	column := 0
	for _, r := range runes {
		runeWidth := maxInt(1, displayColumns(string(r)))
		center := float64(column) + (float64(runeWidth) / 2)
		distance := math.Abs(center - head)
		level := 0
		intensity := 1 - (distance / runningLightBandRadius)
		switch {
		case intensity >= 0.82:
			level = 4
		case intensity >= 0.62:
			level = 3
		case intensity >= 0.42:
			level = 2
		case intensity >= 0.22:
			level = 1
		}
		out.WriteString(styles[level].Render(string(r)))
		column += runeWidth
	}
	return out.String()
}
