package tuiapp

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type hintOptions struct {
	priority       HintPriority
	clearOnMessage bool
	clearAfter     time.Duration
}

func (m *Model) showHint(text string, opts hintOptions) tea.Cmd {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	priority := opts.priority
	if priority == HintPriorityUnspecified {
		priority = HintPriorityNormal
	}
	m.nextHintID++
	entry := hintEntry{
		id:             m.nextHintID,
		text:           text,
		priority:       priority,
		clearOnMessage: opts.clearOnMessage,
	}
	m.hintEntries = append(m.hintEntries, entry)
	m.syncActiveHint()
	return clearHintLaterCmd(entry.id, opts.clearAfter)
}

func (m *Model) removeHintByID(id uint64) {
	if id == 0 || len(m.hintEntries) == 0 {
		return
	}
	filtered := m.hintEntries[:0]
	for _, entry := range m.hintEntries {
		if entry.id == id {
			continue
		}
		filtered = append(filtered, entry)
	}
	m.hintEntries = filtered
	m.syncActiveHint()
}

func (m *Model) removeHintsByText(text string) {
	text = strings.TrimSpace(text)
	if text == "" || len(m.hintEntries) == 0 {
		return
	}
	filtered := m.hintEntries[:0]
	for _, entry := range m.hintEntries {
		if strings.TrimSpace(entry.text) == text {
			continue
		}
		filtered = append(filtered, entry)
	}
	m.hintEntries = filtered
	m.syncActiveHint()
}

func (m *Model) dismissMessageHints() {
	if len(m.hintEntries) == 0 {
		return
	}
	filtered := m.hintEntries[:0]
	for _, entry := range m.hintEntries {
		if entry.clearOnMessage {
			continue
		}
		filtered = append(filtered, entry)
	}
	m.hintEntries = filtered
	m.syncActiveHint()
}

func (m *Model) dismissVisibleHint() {
	if len(m.hintEntries) == 0 {
		m.hint = ""
		return
	}
	idx := m.visibleHintIndex()
	if idx < 0 {
		m.hintEntries = nil
		m.hint = ""
		return
	}
	m.hintEntries = append(m.hintEntries[:idx], m.hintEntries[idx+1:]...)
	m.syncActiveHint()
}

func (m *Model) syncActiveHint() {
	idx := m.visibleHintIndex()
	if idx < 0 {
		m.hint = ""
		return
	}
	m.hint = m.hintEntries[idx].text
}

func (m *Model) visibleHintIndex() int {
	if len(m.hintEntries) == 0 {
		return -1
	}
	best := -1
	for i, entry := range m.hintEntries {
		if strings.TrimSpace(entry.text) == "" {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		current := m.hintEntries[best]
		if entry.priority > current.priority || (entry.priority == current.priority && entry.id > current.id) {
			best = i
		}
	}
	return best
}
