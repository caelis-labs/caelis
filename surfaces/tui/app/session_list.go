package tuiapp

import (
	"context"
	"sync"
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
			rows[i] = ResumeCandidate{SessionID: row.SessionID, Title: row.Title, Prompt: row.Prompt, Workspace: row.Workspace, Age: row.Age, UpdatedAt: row.UpdatedAt}
		}
		status, ok := service.(interface {
			SessionRunning(context.Context, string) (bool, error)
		})
		if !ok {
			return rows, nil
		}
		// Opening the list takes bounded snapshots; it does not activate Runtime
		// or subscribe every background Session.
		var wg sync.WaitGroup
		for worker := 0; worker < minInt(4, len(rows)); worker++ {
			wg.Add(1)
			go func(start int) {
				defer wg.Done()
				for i := start; i < len(rows) && ctx.Err() == nil; i += 4 {
					running, err := status.SessionRunning(ctx, rows[i].SessionID)
					if err == nil {
						rows[i].Running = running
					}
				}
			}(worker)
		}
		wg.Wait()
		return rows, ctx.Err()
	}
}

func (m *Model) requestSurfaceQuit() tea.Cmd {
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
