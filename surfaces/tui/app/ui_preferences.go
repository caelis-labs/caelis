package tuiapp

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/uipreferences"
)

// Confirmed choices have one owner, separate from theme and divider previews.
// Only sparse, explicit edits enter the save queue; loading defaults must never
// turn a later edit into a stale replacement of another TUI's preferences.
type uiPreferencesState struct {
	value        uipreferences.Preferences
	pending      uipreferences.Preferences
	beforeLoad   uipreferences.Preferences
	loaded       bool
	saving       bool
	themeFromEnv bool
}

type uiPreferencesLoadedMsg struct {
	value uipreferences.Preferences
	err   error
}

type uiPreferencesSavedMsg struct{ err error }

func (m *Model) loadUIPreferences() tea.Cmd {
	client := m.cfg.UIPreferences
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p, err := client.LoadUIPreferences(ctx)
		return uiPreferencesLoadedMsg{value: p, err: err}
	}
}

func (m *Model) setUIPreferences(update uipreferences.Preferences) tea.Cmd {
	m.uiPreferences.value = m.uiPreferences.value.Merge(update).WithDefaults()
	if !m.uiPreferences.loaded {
		m.uiPreferences.beforeLoad = m.uiPreferences.beforeLoad.Merge(update)
	}
	m.uiPreferences.pending = m.uiPreferences.pending.Merge(update)
	return m.saveUIPreferences()
}

func (m *Model) saveUIPreferences() tea.Cmd {
	if m.uiPreferences.saving || m.cfg.UIPreferences == nil || m.uiPreferences.pending == (uipreferences.Preferences{}) {
		return nil
	}
	m.uiPreferences.saving = true
	p, client := m.uiPreferences.pending, m.cfg.UIPreferences
	m.uiPreferences.pending = uipreferences.Preferences{}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return uiPreferencesSavedMsg{err: client.SaveUIPreferences(ctx, p)}
	}
}

func (m *Model) applyLoadedUIPreferences(msg uiPreferencesLoadedMsg) tea.Cmd {
	local := m.uiPreferences.beforeLoad
	m.uiPreferences.loaded = true
	m.uiPreferences.beforeLoad = uipreferences.Preferences{}
	if msg.err == nil {
		msg.err = msg.value.Validate()
	}
	if msg.err != nil {
		return m.uiPreferencesError("load", msg.err)
	}
	p := msg.value.WithDefaults().Merge(local)
	// Do not move a divider or change its axis underneath an active gesture.
	if m.workspace.resizing || m.workspace.dragging {
		p.SubagentLayout = m.uiPreferences.value.SubagentLayout
		p.HorizontalRatio = m.uiPreferences.value.HorizontalRatio
		p.VerticalRatio = m.uiPreferences.value.VerticalRatio
	}
	m.uiPreferences.value = p
	m.resizeWorkspace()
	if !m.uiPreferences.themeFromEnv || local.Theme != "" {
		m.applySavedTheme(p.Theme)
	}
	return nil
}

func (m *Model) finishUIPreferencesSave(msg uiPreferencesSavedMsg) tea.Cmd {
	m.uiPreferences.saving = false
	// A failed write is reported, not silently retried. Subsequent confirmed
	// edits still proceed, without replaying unrelated fields from that write.
	return tea.Batch(m.saveUIPreferences(), m.uiPreferencesError("save", msg.err))
}

func (m *Model) uiPreferencesError(action string, err error) tea.Cmd {
	if err == nil {
		return nil
	}
	return m.showHint("Could not "+action+" UI preferences: "+err.Error(), hintOptions{
		priority: HintPriorityHigh, clearOnMessage: true,
	})
}
