package tuiapp

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

// commitUserDisplayLine appends a local user display line without deduping.
// Gateway/user transcript echoes must enter through handleUserMessageMsg so
// they can be matched against the document before rendering.
func (m *Model) commitUserDisplayLine(displayLine string) {
	displayLine = strings.TrimSpace(displayLine)
	if displayLine == "" {
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
	m.hasCommittedLine = true
}

func normalizeUserDisplayLine(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func userDisplayLinesMatchForDedup(existing string, incoming string) bool {
	existingNormalized := normalizeUserDisplayLine(existing)
	incomingNormalized := normalizeUserDisplayLine(incoming)
	if existingNormalized == "" || incomingNormalized == "" {
		return false
	}
	if existingNormalized == incomingNormalized {
		return true
	}
	existingWithoutImages := normalizeUserDisplayLine(stripImageDisplayTokens(existing))
	incomingWithoutImages := normalizeUserDisplayLine(stripImageDisplayTokens(incoming))
	return existingWithoutImages != "" && existingWithoutImages == incomingWithoutImages
}

func stripImageDisplayTokens(text string) string {
	if text == "" {
		return ""
	}
	var out strings.Builder
	cursor := 0
	for cursor < len(text) {
		idx := strings.Index(text[cursor:], "[image")
		if idx < 0 {
			out.WriteString(text[cursor:])
			break
		}
		idx += cursor
		out.WriteString(text[cursor:idx])
		end := strings.Index(text[idx:], "]")
		if end < 0 {
			out.WriteString(text[idx:])
			break
		}
		tokenEnd := idx + end + 1
		token := text[idx:tokenEnd]
		if isImageDisplayToken(token) {
			out.WriteByte(' ')
			cursor = tokenEnd
			continue
		}
		out.WriteString(text[idx : idx+1])
		cursor = idx + 1
	}
	return out.String()
}

func isImageDisplayToken(token string) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	return strings.HasPrefix(token, "[image #") && strings.HasSuffix(token, "]") ||
		strings.HasPrefix(token, "[image:") && strings.HasSuffix(token, "]")
}

func (m *Model) displayLineWithAttachments(line string) string {
	return m.displayLineWithInputAttachments(line, m.inputAttachments)
}

func (m *Model) displayLineWithInputAttachments(line string, attachments []inputAttachment) string {
	return composeDisplayWithToken(line, attachments, func(index int, name string) string {
		name = strings.TrimSpace(name)
		if name == "" {
			return ""
		}
		return imageAttachmentToken(index)
	})
}

func (m *Model) shouldUseTextareaVerticalNavigation(direction int) bool {
	if m.turnRunning() {
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
	if m.liveTurn.HasLastDuration {
		return formatTurnDuration(m.liveTurn.LastDuration)
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
		if slashCommandCanOpenArgPicker(cmd) {
			m.openSlashArgPicker(cmd)
			return len(m.slashArgCandidates) > 0
		}
	}
	return false
}

func slashCommandCanOpenArgPicker(command string) bool {
	spec, ok := lookupSlashCommandSpec(command)
	if !ok {
		return false
	}
	return len(spec.ArgCandidates) > 0 || spec.DynamicCompleter
}

func isViewportEndKey(msg tea.KeyMsg) bool {
	press, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return false
	}
	key := tea.Key(press)
	return key.Code == tea.KeyEnd && key.Mod == 0
}
