package tuiapp

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
)

func sessionListLoader(service ControlServices) func(context.Context) ([]ResumeCandidate, error) {
	return func(ctx context.Context) ([]ResumeCandidate, error) {
		listed, err := service.ListSessions(ctx, 200)
		if err != nil {
			return nil, err
		}
		rows := make([]ResumeCandidate, len(listed))
		for i, row := range listed {
			rows[i] = ResumeCandidate{SessionID: row.SessionID, Title: row.Title, Prompt: row.Prompt, Workspace: row.Workspace, Age: row.Age, UpdatedAt: row.UpdatedAt, Running: row.Running}
		}
		return rows, ctx.Err()
	}
}

func (m *Model) requestSurfaceQuit() tea.Cmd {
	if m.textarea.Value() != "" || len(m.inputAttachments) > 0 {
		// Cleared drafts are local history, including commands and whitespace.
		// Use the same expanded-paste/image representation as submitted history.
		text, images := expandPastesRemapImages(m.textarea.Value(), m.inputAttachments)
		m.appendInputHistory(text, images)
		m.clearInputSelection()
		m.resetComposerAfterOverlayOpen()
		m.ctrlCArmed = false
		m.lastCtrlCAt = time.Time{}
		return nil
	}
	now := time.Now()
	if m.ctrlCArmed && now.Sub(m.lastCtrlCAt) <= ctrlCExitWindow {
		m.quit = true
		return tea.Quit
	}
	m.ctrlCArmed = true
	m.lastCtrlCAt = now
	m.ctrlCArmSeq++
	return tea.Batch(expireCtrlCCmd(now, m.ctrlCArmSeq), m.showHint("press Ctrl+C again to quit", hintOptions{priority: HintPriorityCritical, clearAfter: ctrlCExitWindow}))
}
