package tuiapp

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestControlServiceCanSubmitRunningPromptForSteerableTurns(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		status controlprompt.AgentStatusSnapshot
		err    error
		want   bool
	}{
		{name: "kernel", status: controlprompt.AgentStatusSnapshot{HasActiveTurn: true, ActiveTurnKind: "kernel"}, want: true},
		{name: "participant", status: controlprompt.AgentStatusSnapshot{HasActiveTurn: true, ActiveTurnKind: "participant"}, want: true},
		{name: "idle", status: controlprompt.AgentStatusSnapshot{}},
		{name: "other active kind", status: controlprompt.AgentStatusSnapshot{HasActiveTurn: true, ActiveTurnKind: "task"}},
		{name: "status failure", err: errors.New("status unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := activeTurnStatusStub{status: test.status, err: test.err}
			if got := controlServiceCanSubmitRunningPrompt(context.Background(), service); got != test.want {
				t.Fatalf("controlServiceCanSubmitRunningPrompt() = %v, want %v", got, test.want)
			}
		})
	}
}

type activeTurnStatusStub struct {
	status controlprompt.AgentStatusSnapshot
	err    error
}

func (s activeTurnStatusStub) AgentStatus(context.Context) (controlprompt.AgentStatusSnapshot, error) {
	return s.status, s.err
}
