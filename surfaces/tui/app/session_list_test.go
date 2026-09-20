package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/charmbracelet/x/ansi"
)

func TestSessionPickerListsWithoutInspectingSessions(t *testing.T) {
	service := &sessionListOnlyService{}
	m := newSessionPickerTestModel(t, 80, 24, nil, nil)
	m.cfg.ListSessions = sessionListLoader(service)
	openSessionPickerForTest(t, m)
	if got := service.inspections.Load(); got != 0 {
		t.Fatalf("listing inspected %d Sessions, want only the directory request", got)
	}
	if len(m.sessionPicker.rows) != 200 {
		t.Fatalf("listed %d rows, want 200", len(m.sessionPicker.rows))
	}
	frame := m.View().Content
	if plain := ansi.Strip(frame); strings.Contains(plain, "Loading sessions") || !strings.Contains(plain, "Earlier work 000") || !strings.Contains(plain, "running") {
		t.Fatalf("directory result was not rendered:\n%s", plain)
	}
	updates := renderFullscreenFramesForTest(t, m.width, m.height, frame)
	assertPhysicalFullscreenFrame(t, m.width, m.height, frame, updates)
}

type sessionListOnlyService struct {
	ControlServices
	inspections atomic.Int64
}

func (*sessionListOnlyService) ListSessions(_ context.Context, limit int) ([]controlprompt.ResumeCandidate, error) {
	rows := make([]controlprompt.ResumeCandidate, limit)
	for i := range rows {
		rows[i] = controlprompt.ResumeCandidate{SessionID: fmt.Sprintf("session-%03d", i), Title: fmt.Sprintf("Earlier work %03d", i)}
	}
	rows[0].Running = true
	return rows, nil
}

// Retaining this trap proves the picker cannot silently reintroduce per-row
// inspection when a service happens to expose a detail-reading capability.
func (s *sessionListOnlyService) SessionRunning(context.Context, string) (bool, error) {
	s.inspections.Add(1)
	return false, nil
}
