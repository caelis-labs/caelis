package tuiapp

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

func (m *Model) commitUserDisplayLine(displayLine string) {
	displayLine = strings.TrimSpace(displayLine)
	if displayLine == "" {
		return
	}
	normalized := normalizeUserDisplayLine(displayLine)
	if m.userDisplayDedupOK && normalized != "" && normalizeUserDisplayLine(m.lastUserDisplayLine) == normalized {
		return
	}
	userLine := "▌ " + displayLine
	if m.hasCommittedLine {
		m.insertSpacing(tuikit.LineStyleUser, userLine)
	}
	block := NewUserNarrativeBlock(displayLine)
	m.doc.Append(block)
	m.lastCommittedStyle = tuikit.LineStyleUser
	m.lastCommittedRaw = userLine
	m.lastUserDisplayLine = displayLine
	m.userDisplayDedupOK = true
	m.hasCommittedLine = true
}

func normalizeUserDisplayLine(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func (m *Model) displayLineWithAttachments(line string) string {
	return m.displayLineWithInputAttachments(line, m.inputAttachments)
}

func (m *Model) displayLineWithInputAttachments(line string, attachments []inputAttachment) string {
	return composeDisplayWithToken(line, attachments, func(name string) string {
		name = strings.TrimSpace(name)
		if name == "" {
			return ""
		}
		return "[image: " + name + "] "
	})
}

func (m *Model) shouldUseTextareaVerticalNavigation(direction int) bool {
	if m.running {
		return false
	}
	if strings.TrimSpace(m.textarea.Value()) == "" {
		return false
	}
	lineInfo := m.textarea.LineInfo()
	if m.textarea.LineCount() <= 1 && lineInfo.Height <= 1 {
		return false
	}
	switch {
	case direction < 0:
		return m.textarea.Line() > 0 || lineInfo.RowOffset > 0
	case direction > 0:
		return m.textarea.Line() < m.textarea.LineCount()-1 || lineInfo.RowOffset+1 < lineInfo.Height
	default:
		return false
	}
}

func (m *Model) userTurnDividerLabel() string {
	if m.hasLastRunDuration {
		return formatTurnDuration(m.lastRunDuration)
	}
	return ""
}

func formatTurnDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	minutes := int(d / time.Minute)
	seconds := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", minutes, seconds)
}

func centeredDivider(width int, label string) string {
	if width <= 0 {
		return ""
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return strings.Repeat("─", width)
	}
	label = " " + label + " "
	labelWidth := displayColumns(label)
	if labelWidth >= width {
		return label
	}
	remaining := width - labelWidth
	left := remaining / 2
	right := remaining - left
	if left < 2 {
		left = 2
	}
	if right < 2 {
		right = 2
	}
	return strings.Repeat("─", left) + label + strings.Repeat("─", right)
}

func (m *Model) tryOpenSlashArgPicker(line string) bool {
	text := strings.TrimSpace(line)
	if text == "/resume" {
		if !m.isCommandAvailable("resume") {
			return false
		}
		m.openResumePicker()
		return len(m.resumeCandidates) > 0
	}
	if strings.HasPrefix(text, "/") && !strings.Contains(text, " ") {
		cmd := strings.TrimPrefix(text, "/")
		if !m.isCommandAvailable(cmd) {
			return false
		}
		// Check registered wizards first, then well-known simple commands.
		if m.findWizard(cmd) != nil {
			m.openSlashArgPicker(cmd)
			return m.slashArgActive
		}
		switch text {
		case "/agent", "/model":
			m.openSlashArgPicker(cmd)
			return len(m.slashArgCandidates) > 0
		}
	}
	return false
}

func isViewportEndKey(msg tea.KeyMsg) bool {
	press, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return false
	}
	key := tea.Key(press)
	return key.Code == tea.KeyEnd && key.Mod == 0
}
