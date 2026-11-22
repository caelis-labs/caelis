package tuiapp

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

// slashCommandEcho selects the transcript text for commands that complete in
// the TUI. Agent, Skill, and prompt input take their echo from Control.
func (m *Model) slashCommandEcho(execLine, displayLine string) string {
	name, _, _, ok := controlprompt.ParseSlash(execLine)
	if !ok || !m.isCommandAvailable(name) {
		return ""
	}
	switch name {
	case "help", "status", "doctor", "model", "compact", "disconnect":
		if _, known := controlprompt.Lookup(name); known {
			return firstNonEmpty(displayLine, execLine)
		}
	case "connect", "plugin":
		// Credentials, wizard payloads, and plugin sources can contain secrets.
		return "/" + name
	}
	return ""
}

// appendUserNarrativeBlock appends a user line to the transcript.
func (m *Model) appendUserNarrativeBlock(block *UserNarrativeBlock) {
	if renderableTextHasContent(block.Raw) {
		blocks := m.doc.Blocks()
		for index := len(blocks) - 1; index >= 0; index-- {
			if _, previousUser := blocks[index].(*UserNarrativeBlock); previousUser {
				break
			}
			if turn, ok := blocks[index].(*MainACPTurnBlock); ok && !turn.followedByUser {
				turn.followedByUser = true
				m.markViewportBlockDirty(turn.BlockID())
			}
		}
	}
	m.mainTimelineBarrier()
	userLine := userNarrativePrefix + block.Raw
	if m.hasCommittedLine {
		m.insertSpacing(tuikit.LineStyleUser, userLine)
	}
	m.appendMainTranscriptBlock(block)
	m.lastCommittedStyle = tuikit.LineStyleUser
	m.lastCommittedRaw = userLine
	m.hasCommittedLine = true
}

type gatewayUserEchoOptions struct {
	displayLine    string
	appendLine     string
	dequeueNeedles []string
	event          TranscriptEvent
}

// userNarrativeBlockID binds a finalized user event to its document block.
// Event identity survives replay and replacement; text is never an identity.
// Unidentified notifications are separate messages, not guesses at retransmits.
func userNarrativeBlockID(event TranscriptEvent) string {
	if id := strings.TrimSpace(event.SourceEventID); id != "" {
		return fmt.Sprintf("user:event:%q:%q:%q", event.Scope, event.ScopeID, id)
	}
	if id := strings.TrimSpace(event.SourceProjectionID); id != "" {
		return fmt.Sprintf("user:projection:%q:%q:%q", event.Scope, event.ScopeID, id)
	}
	if id := event.MessageID; id != "" {
		return fmt.Sprintf("user:message:%q:%q:%q:%q", event.Scope, event.ScopeID, event.TurnID, id)
	}
	return ""
}

// applyGatewayUserEcho displays accepted input from Control, never submission
// intent. A duplicate event cannot consume another pending input with the same
// text. Queue matching affects status only, not whether a message is displayed.
func (m *Model) applyGatewayUserEcho(opts gatewayUserEchoOptions) tea.Model {
	displayLine := strings.TrimSpace(opts.displayLine)
	messageKey := ""
	if id := opts.event.MessageID; id != "" {
		messageKey = fmt.Sprintf("%q:%q:%q:%q", opts.event.Scope, opts.event.ScopeID, opts.event.TurnID, id)
	}
	if messageKey != "" {
		for _, item := range m.doc.Blocks() {
			block, ok := item.(*UserNarrativeBlock)
			if !ok || block.observedMessage != messageKey || (!opts.event.Replacement && !block.observedReplacement) {
				continue
			}
			if opts.event.Replacement {
				block.Raw = displayLine
			} else {
				line := opts.displayLine
				if opts.appendLine != "" {
					line = opts.appendLine
				}
				block.Raw += line
			}
			block.observedReplacement = true
			m.markViewportBlockDirty(block.BlockID())
			m.ensureViewportLayout()
			m.syncViewportContent()
			return m
		}
	}
	if displayLine == "" && !opts.event.Replacement {
		return m
	}
	blockID := userNarrativeBlockID(opts.event)
	if blockID != "" {
		if _, exists := m.doc.index[blockID]; exists {
			return m
		}
	}
	needles := append([]string(nil), opts.dequeueNeedles...)
	needles = append(needles, displayLine)
	if !opts.event.Replacement {
		m.pendingQueue.matchGatewayEcho(needles...)
	}
	block := NewUserNarrativeBlock(displayLine)
	block.observedMessage = messageKey
	block.observedReplacement = opts.event.Replacement
	if blockID != "" {
		block.id = blockID
	}
	m.appendUserNarrativeBlock(block)
	m.ensureViewportLayout()
	m.syncViewportContent()
	return m
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

func (m *Model) tryOpenSlashArgPicker(line string) (bool, tea.Cmd) {
	text := strings.TrimSpace(line)
	if text == "/resume" {
		if !m.isCommandAvailable("resume") {
			return false, nil
		}
		m.resetComposerAfterOverlayOpen()
		return true, m.openSessionPicker()
	}
	if strings.HasPrefix(text, "/") && !strings.Contains(text, " ") {
		cmd := strings.TrimPrefix(text, "/")
		if !m.isCommandAvailable(cmd) {
			return false, nil
		}
		// Check registered wizards first, then well-known simple commands.
		if m.findWizard(cmd) != nil || slashCommandCanOpenArgPicker(cmd) {
			loadCmd := m.openSlashArgPicker(cmd)
			return m.slashArgActive, loadCmd
		}
	}
	return false, nil
}

func slashCommandCanOpenArgPicker(command string) bool {
	spec, ok := controlprompt.Lookup(command)
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
