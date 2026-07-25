package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

type welcomeAction struct {
	token   string
	label   string
	command string
}

var welcomeActions = []welcomeAction{
	{token: welcomeActionTokenConnect, label: "Connect Model / Agent", command: "/connect"},
	{token: welcomeActionTokenResume, label: "Resume Session", command: "/resume"},
	{token: welcomeActionTokenQuit, label: "Quit", command: "/quit"},
}

func welcomeActionForToken(token string) (welcomeAction, bool) {
	token = strings.TrimSpace(token)
	for _, action := range welcomeActions {
		if token == action.token {
			return action, true
		}
	}
	return welcomeAction{}, false
}

func (m *Model) tryTriggerWelcomeActionToken(blockID string, token string, contentLine int) tea.Cmd {
	action, ok := welcomeActionForToken(token)
	if !ok || m == nil || m.doc == nil || contentLine < 0 || contentLine >= len(m.viewportClickBounds) {
		return nil
	}
	block := m.doc.Find(strings.TrimSpace(blockID))
	if block == nil || block.Kind() != BlockWelcome || m.selectionStart.line != contentLine {
		return nil
	}
	clickBounds := m.viewportClickBounds[contentLine]
	if !clickBounds.valid() || m.selectionStart.col < clickBounds.start || m.selectionStart.col >= clickBounds.end {
		return nil
	}
	_, cmd := m.submitInteractiveLine(action.command, action.command, nil)
	return cmd
}

// appendMainTranscriptBlock is the single content append path for the main
// transcript. Welcome dismissal follows visible document mutation rather than
// command parsing, so local slash output and streamed content behave alike.
func (m *Model) appendMainTranscriptBlock(block Block) {
	if m == nil || m.doc == nil || block == nil {
		return
	}
	m.dismissWelcomeCard()
	m.doc.Append(block)
}

func (m *Model) dismissWelcomeCard() {
	if m == nil {
		return
	}
	m.welcomeCardPending = false
	if m.doc == nil {
		return
	}

	blocks := m.doc.Blocks()
	welcomeWasFirst := len(blocks) > 0 && blocks[0].Kind() == BlockWelcome
	removed := false
	for _, block := range blocks {
		if block.Kind() != BlockWelcome {
			continue
		}
		removed = m.doc.Remove(block.BlockID()) || removed
	}
	if !removed {
		return
	}
	if welcomeWasFirst {
		m.trimLeadingWelcomeSpacing()
	}
	m.markViewportStructureDirty()
	m.refreshHistoryTailState()
}

func (m *Model) trimLeadingWelcomeSpacing() {
	for {
		blocks := m.doc.Blocks()
		if len(blocks) == 0 {
			return
		}
		spacer, ok := blocks[0].(*TranscriptBlock)
		if !ok || strings.TrimSpace(spacer.Raw) != "" {
			return
		}
		m.doc.Remove(spacer.BlockID())
	}
}
