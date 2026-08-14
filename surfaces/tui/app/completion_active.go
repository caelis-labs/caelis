package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

type completionSnapshot struct {
	kind        completionKind
	selected    int
	total       int
	canLoadMore bool
}

// activeCompletionKind is the single priority resolver for completion key,
// paint, and pointer routing. Flags intentionally outrank lower candidate
// slices so an empty loading picker cannot expose another completion behind it.
func (m *Model) activeCompletionKind() completionKind {
	switch {
	case len(m.mentionCandidates) > 0:
		return completionMention
	case m.mentionRequestPending:
		return completionMention
	case m.resumeActive:
		return completionResume
	case m.slashArgActive:
		return completionSlashArg
	case len(m.slashCandidates) > 0:
		return completionSlashCommand
	default:
		return completionNone
	}
}

func (m *Model) completionSnapshot() (completionSnapshot, bool) {
	snapshot, ok := m.completionSnapshotForKind(m.activeCompletionKind())
	return snapshot, ok && snapshot.total > 0
}

func (m *Model) completionSnapshotForKind(kind completionKind) (completionSnapshot, bool) {
	switch kind {
	case completionMention:
		total := len(m.mentionCandidates)
		return completionSnapshot{
			kind:        kind,
			selected:    m.mentionIndex,
			total:       total,
			canLoadMore: shouldLoadMoreCompletionCandidates(total, m.mentionLimit),
		}, true
	case completionResume:
		return completionSnapshot{
			kind:     kind,
			selected: m.resumeIndex,
			total:    len(m.resumeCandidates),
		}, true
	case completionSlashArg:
		candidates := m.visibleSlashArgCandidates()
		return completionSnapshot{
			kind:     kind,
			selected: m.currentSlashArgIndex(candidates),
			total:    len(candidates),
		}, true
	case completionSlashCommand:
		return completionSnapshot{
			kind:     kind,
			selected: m.slashIndex,
			total:    len(m.slashCandidates),
		}, true
	default:
		return completionSnapshot{}, false
	}
}

func (m *Model) activeCompletionGeometry() (completionSnapshot, completionOverlayGeometry, bool) {
	snapshot, ok := m.completionSnapshot()
	if !ok {
		return completionSnapshot{}, completionOverlayGeometry{}, false
	}
	return snapshot, m.buildCompletionOverlayGeometry(snapshot), true
}

func (m *Model) completionOverlayActive() bool {
	_, ok := m.completionSnapshot()
	return ok
}

func (m *Model) completionKeyAt(kind completionKind, index int) string {
	if index < 0 {
		return ""
	}
	switch kind {
	case completionMention:
		if index >= len(m.mentionCandidates) {
			return ""
		}
		return completionCandidateStableKey(m.mentionCandidates[index])
	case completionResume:
		if index >= len(m.resumeCandidates) {
			return ""
		}
		return strings.TrimSpace(m.resumeCandidates[index].SessionID)
	case completionSlashArg:
		candidates := m.visibleSlashArgCandidates()
		if index >= len(candidates) {
			return ""
		}
		return strings.TrimSpace(candidates[index].Value) + "\x00" +
			strings.TrimSpace(candidates[index].Display)
	case completionSlashCommand:
		if index >= len(m.slashCandidates) {
			return ""
		}
		return strings.TrimSpace(m.slashCandidates[index])
	default:
		return ""
	}
}

func (m *Model) setCompletionIndex(kind completionKind, index int) {
	snapshot, ok := m.completionSnapshotForKind(kind)
	if !ok || index < 0 || index >= snapshot.total {
		return
	}
	switch kind {
	case completionMention:
		m.mentionIndex = index
	case completionResume:
		m.resumeIndex = index
	case completionSlashArg:
		m.slashArgIndex = index
	case completionSlashCommand:
		m.slashIndex = index
	}
}

func (m *Model) activateCompletion(kind completionKind) tea.Cmd {
	enter := tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	switch kind {
	case completionMention:
		_, cmd := m.handleMentionKey(enter)
		return cmd
	case completionResume:
		_, cmd := m.handleResumeKey(enter)
		return cmd
	case completionSlashArg:
		_, cmd := m.handleSlashArgKey(enter)
		return cmd
	case completionSlashCommand:
		_, cmd := m.handleSlashCommandKey(enter)
		return cmd
	default:
		return nil
	}
}

func (m *Model) loadMoreCompletion(kind completionKind) tea.Cmd {
	if kind != completionMention {
		return nil
	}
	return m.loadMoreMentionCandidates()
}

func (m *Model) renderCompletion(geometry completionOverlayGeometry) string {
	switch geometry.kind {
	case completionMention:
		return m.renderMentionListGeometry(geometry, m.mentionCandidates)
	case completionResume:
		return m.renderResumeListGeometry(geometry, m.resumeCandidates)
	case completionSlashArg:
		return m.renderSlashArgListGeometry(geometry, m.visibleSlashArgCandidates())
	case completionSlashCommand:
		return m.renderSlashCommandListGeometry(geometry, m.slashCandidates)
	default:
		return ""
	}
}

func (m *Model) renderInputOverlay() string {
	_, geometry, ok := m.activeCompletionGeometry()
	if !ok {
		return ""
	}
	return m.renderCompletion(geometry)
}

func (m *Model) renderCompletionKind(kind completionKind) string {
	snapshot, geometry, ok := m.activeCompletionGeometry()
	if !ok || snapshot.kind != kind {
		return ""
	}
	return m.renderCompletion(geometry)
}

func (m *Model) handleActiveCompletionKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch m.activeCompletionKind() {
	case completionMention:
		return m.handleMentionKey(msg)
	case completionResume:
		return m.handleResumeKey(msg)
	case completionSlashArg:
		return m.handleSlashArgKey(msg)
	case completionSlashCommand:
		return m.handleSlashCommandKey(msg)
	default:
		return false, nil
	}
}

func (m *Model) moveActiveCompletionSelection(delta int, wrap bool) tea.Cmd {
	if delta == 0 {
		return nil
	}
	snapshot, geometry, ok := m.activeCompletionGeometry()
	if !ok {
		return nil
	}
	m.pinCompletionWindow(geometry)
	if delta > 0 && snapshot.selected >= snapshot.total-1 && snapshot.canLoadMore {
		if cmd := m.loadMoreCompletion(snapshot.kind); cmd != nil {
			return cmd
		}
		if snapshot.kind == completionMention && m.mentionRequestPending {
			return nil
		}
	}
	next := snapshot.selected + delta
	if wrap {
		next = wrapSelectionIndex(snapshot.selected, snapshot.total, delta)
	} else {
		next = minInt(maxInt(0, next), snapshot.total-1)
	}
	m.setCompletionIndex(snapshot.kind, next)
	if refreshed, ok := m.completionSnapshot(); ok && refreshed.kind == snapshot.kind {
		m.pinCompletionWindow(m.buildCompletionOverlayGeometry(refreshed))
	}
	return nil
}
