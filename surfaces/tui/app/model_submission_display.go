package tuiapp

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

// commitUserDisplayLine appends a local user display line without deduping.
// Gateway/user transcript echoes must enter through handleUserMessageMsg so
// they can be matched against the document before rendering.
func (m *Model) commitUserDisplayLine(displayLine string) {
	displayLine = strings.TrimSpace(displayLine)
	if displayLine == "" {
		return
	}
	m.mainTimelineBarrier()
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

type gatewayUserEchoOptions struct {
	displayLine        string
	dequeueNeedles     []string
	participantTurnKey string
}

func (m *Model) applyGatewayUserEcho(opts gatewayUserEchoOptions) tea.Model {
	displayLine := strings.TrimSpace(opts.displayLine)
	if displayLine == "" {
		return m
	}
	needles := append([]string(nil), opts.dequeueNeedles...)
	needles = append(needles, displayLine)
	matchedPendingPrompt, matchedPending := m.pendingQueue.matchGatewayEcho(needles...)
	if matchedPending && matchedPendingPrompt.isLocallyRendered() {
		m.ensureViewportLayout()
		m.syncViewportContent()
		return m
	}
	if !matchedPending && m.lastVisibleUserNarrativeMatchesForEcho(displayLine, opts.participantTurnKey) {
		m.ensureViewportLayout()
		m.syncViewportContent()
		return m
	}
	m.commitUserDisplayLine(displayLine)
	m.ensureViewportLayout()
	m.syncViewportContent()
	return m
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
	existingWithoutTokens := normalizeUserDisplayLine(stripComposerDisplayTokens(existing))
	incomingWithoutTokens := normalizeUserDisplayLine(stripComposerDisplayTokens(incoming))
	return existingWithoutTokens != "" && existingWithoutTokens == incomingWithoutTokens
}

func stripComposerDisplayTokens(text string) string {
	if text == "" {
		return ""
	}
	var out strings.Builder
	cursor := 0
	for cursor < len(text) {
		nextImage := strings.Index(text[cursor:], "[image")
		nextPaste := strings.Index(strings.ToLower(text[cursor:]), "[pasted")
		var idx int
		switch {
		case nextImage < 0 && nextPaste < 0:
			out.WriteString(text[cursor:])
			return out.String()
		case nextImage < 0:
			idx = cursor + nextPaste
		case nextPaste < 0:
			idx = cursor + nextImage
		default:
			idx = cursor + min(nextImage, nextPaste)
		}
		out.WriteString(text[cursor:idx])
		end := strings.Index(text[idx:], "]")
		if end < 0 {
			out.WriteString(text[idx:])
			break
		}
		tokenEnd := idx + end + 1
		token := text[idx:tokenEnd]
		if isComposerDisplayToken(token) {
			out.WriteByte(' ')
			cursor = tokenEnd
			continue
		}
		out.WriteString(text[idx : idx+1])
		cursor = idx + 1
	}
	return out.String()
}

// stripImageDisplayTokens is retained for older call sites / tests.
func stripImageDisplayTokens(text string) string {
	return stripComposerDisplayTokens(text)
}

func isComposerDisplayToken(token string) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	return isImageDisplayToken(token) || isPasteDisplayToken(token)
}

func isImageDisplayToken(token string) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	return strings.HasPrefix(token, "[image #") && strings.HasSuffix(token, "]") ||
		strings.HasPrefix(token, "[image:") && strings.HasSuffix(token, "]")
}

func isPasteDisplayToken(token string) bool {
	token = strings.ToLower(strings.TrimSpace(token))
	return strings.HasPrefix(token, "[pasted:") && strings.HasSuffix(token, "]") ||
		strings.HasPrefix(token, "[pasted ") && strings.HasSuffix(token, "]")
}

func (m *Model) displayLineWithAttachments(line string) string {
	return m.displayLineWithInputAttachments(line, m.inputAttachments)
}

func (m *Model) displayLineWithInputAttachments(line string, attachments []inputAttachment) string {
	return composeDisplayWithToken(line, attachments, func(item inputAttachment, imageIndex int) string {
		return strings.TrimSpace(attachmentDisplayToken(item, imageIndex))
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
		return m.resumeActive
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
