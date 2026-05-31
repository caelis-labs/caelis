package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

func (m *Model) appendPlainTranscriptBlock(text string) (tea.Model, tea.Cmd) {
	text = strings.TrimSpace(text)
	if text == "" {
		return m, nil
	}
	m.finalizeAssistantBlock()
	m.finalizeReasoningBlock()
	block := NewTranscriptBlock(text, tuikit.DetectLineStyle(text))
	m.doc.Append(block)
	m.hasCommittedLine = true
	m.lastCommittedStyle = block.Style
	m.syncViewportContent()
	return m, nil
}
