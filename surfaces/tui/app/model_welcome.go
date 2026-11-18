package tuiapp

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type welcomeAction struct {
	token   string
	label   string
	command string
}

var welcomeActions = []welcomeAction{
	{token: welcomeActionTokenResume, label: "Resume Session", command: "/resume"},
	{token: welcomeActionTokenConnect, label: "Connect Model", command: "/connect"},
	{token: welcomeActionTokenModel, label: "Switch Model", command: "/model"},
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

func (m *Model) hasWelcomeCard() bool {
	// The welcome block is inserted before startup logs and removed when the
	// main transcript starts.
	return m.doc != nil && m.doc.Len() > 0 && m.doc.Blocks()[0].Kind() == BlockWelcome
}

func (m *Model) welcomeActionAt(point textSelectionPoint) (welcomeAction, bool) {
	if !m.hasWelcomeCard() || point.line < 0 || point.line >= len(m.viewportClickBounds) {
		return welcomeAction{}, false
	}
	bounds := m.viewportClickBounds[point.line]
	if point.col < bounds.start || point.col >= bounds.end {
		return welcomeAction{}, false
	}
	return welcomeActionForToken(m.viewportClickTokens[point.line])
}

func (m *Model) renderWelcomeHover(view string) string {
	if m.welcomeMouse == nil || m.sessionPicker != nil || m.wizardOverlay != nil || m.activePrompt != nil || m.experimental != nil {
		return view
	}
	point, ok := m.mousePointToContentPoint(m.welcomeMouse.X, m.welcomeMouse.Y, false)
	if !ok {
		return view
	}
	if _, ok := m.welcomeActionAt(point); !ok {
		return view
	}
	row := point.line - m.viewportVisibleOffset()
	lines := strings.Split(view, "\n")
	bounds := m.viewportClickBounds[point.line]
	label := sliceByDisplayColumns(m.viewportPlainLines[point.line], bounds.start, bounds.end)
	lines[row] = ansi.Cut(lines[row], 0, bounds.start) + m.theme.SelectionStyle().Render(label) +
		ansi.Cut(lines[row], bounds.end, displayColumns(lines[row]))
	return strings.Join(lines, "\n")
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

func (m *Model) dismissWelcomeCard() bool {
	if m == nil {
		return false
	}
	m.welcomeCardPending = false
	if m.doc == nil {
		return false
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
		return false
	}
	if welcomeWasFirst {
		m.trimLeadingWelcomeSpacing()
	}
	m.markViewportStructureDirty()
	m.refreshHistoryTailState()
	return true
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
