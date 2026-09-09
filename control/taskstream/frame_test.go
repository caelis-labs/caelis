package taskstream

import (
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

func TestFramesForFallbackClosedTurnMatchesLastContentScopeTurnID(t *testing.T) {
	t.Parallel()

	at := time.Unix(500, 0)
	snapshot := fallbackSnapshot{
		ActivityID: "activity-2",
		State:      string(task.StateCompleted),
		UpdatedAt:  at.Add(4 * time.Second),
		Frames: []Frame{{
			TerminalID: "task-1:2",
			ActivityID: "activity-2",
			UpdatedAt:  at,
			Event: &session.Event{
				ID: "content-1", Type: session.EventTypeAssistant, Text: "follow-up",
				Time: at,
				Scope: &session.EventScope{
					TurnID: "task-1:2",
					Participant: session.ParticipantRef{
						ID: "task-1", Kind: session.ParticipantKindSubagent, DelegationID: "task-1",
					},
				},
			},
		}},
	}
	frames := framesForFallback(snapshot)
	if len(frames) != 2 {
		t.Fatalf("frames = %#v, want content plus closed terminal", frames)
	}
	closed := frames[1]
	if !closed.Closed || closed.Event == nil || closed.Event.Scope == nil || closed.Event.Scope.TurnID != "task-1:2" {
		t.Fatalf("closed terminal Turn identity = %#v, want last content Scope.TurnID", closed)
	}
}

func TestFramesForFallbackClosedWithoutScopeTurnIDKeepsActivityID(t *testing.T) {
	t.Parallel()

	snapshot := fallbackSnapshot{
		ActivityID: "activity-2",
		State:      string(task.StateCompleted),
		UpdatedAt:  time.Unix(510, 0),
		Frames: []Frame{{
			ActivityID: "activity-2",
			Event: &session.Event{
				ID: "content-1", Type: session.EventTypeAssistant, Text: "follow-up",
				Scope: &session.EventScope{
					Participant: session.ParticipantRef{Kind: session.ParticipantKindSubagent},
				},
			},
		}},
	}
	frames := framesForFallback(snapshot)
	if len(frames) != 2 || frames[1].Event != nil || frames[1].ActivityID != "activity-2" {
		t.Fatalf("closed terminal without Scope.TurnID = %#v", frames)
	}
}
