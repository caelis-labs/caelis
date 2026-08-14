package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// SlashNoticePlacement is the TUI contract for one slash notice.
//
// Feedback stays under the command as a short confirmation.
// Content stays in the transcript as requested output (lists, details).
// Hint is temporary chrome and must not become conversation.
type SlashNoticePlacement int

const (
	SlashNoticeFeedback SlashNoticePlacement = iota
	SlashNoticeContent
	SlashNoticeHint
)

func (m *Model) handleSlashNoticeMsg(msg SlashNoticeMsg) (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return m, nil
	}
	if msg.Placement == SlashNoticeHint {
		return m, m.showHint(firstSlashNoticeLine(text), hintOptions{
			priority:       HintPriorityNormal,
			clearOnMessage: true,
			clearAfter:     systemHintDuration,
		})
	}
	return m.appendSlashOutputLines(renderSlashNoticeLines(msg))
}

func sendNotice(send func(tea.Msg), text string, placement SlashNoticePlacement) {
	text = strings.TrimSpace(text)
	if send == nil || text == "" {
		return
	}
	send(SlashNoticeMsg{Text: text, Placement: placement})
}

func sendControlSlashNotice(send func(tea.Msg), text string) {
	sendNotice(send, text, classifyControlSlashNotice(text))
}

func classifyControlSlashNotice(text string) SlashNoticePlacement {
	normalized := strings.ToLower(strings.TrimSpace(text))
	switch {
	case normalized == "":
		return SlashNoticeFeedback
	case strings.HasPrefix(normalized, "usage:"):
		return SlashNoticeHint
	case strings.Contains(normalized, "unknown command"), strings.Contains(normalized, "unknown skill"), strings.Contains(normalized, "unknown tui command"):
		return SlashNoticeHint
	case strings.HasPrefix(normalized, "available sessions:"),
		strings.HasPrefix(normalized, "ambiguous skill"),
		strings.Contains(normalized, "\nuse one of:"):
		return SlashNoticeContent
	default:
		return SlashNoticeFeedback
	}
}

func firstSlashNoticeLine(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return ""
	}
	line, _, _ := strings.Cut(text, "\n")
	return strings.TrimSpace(line)
}
