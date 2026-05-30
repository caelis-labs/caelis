package viewmodel

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
)

func TestFromSnapshotProjectsTranscriptApprovalsAndParticipants(t *testing.T) {
	view := FromSnapshot(session.Snapshot{
		Session: session.Session{
			Ref:   session.Ref{AppName: "caelis", UserID: "tester", SessionID: "sess-1"},
			Title: "scratch",
			Workspace: session.Workspace{
				Key: "repo",
				CWD: "/repo",
			},
		},
		Events: []session.Event{
			{
				ID:   "evt-1",
				Type: session.EventUser,
				Actor: session.ActorRef{
					Kind: session.ActorUser,
					Name: "tester",
				},
				Message: &model.Message{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("ping")}},
			},
			{
				ID:         "evt-transient",
				Type:       session.EventAssistant,
				Visibility: session.VisibilityUIOnly,
				Message:    &model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.NewTextPart("stream")}},
			},
			{
				ID:   "evt-2",
				Type: session.EventApproval,
				Scope: &session.EventScope{
					TurnID: "turn-1",
					Participant: session.ParticipantBinding{
						ID:        "reviewer",
						Kind:      session.ParticipantACP,
						Role:      session.ParticipantDelegated,
						AgentName: "Reviewer",
						SessionID: "remote-1",
						Source:    "external_acp",
					},
				},
				Approval: &session.ApprovalEvent{
					ID:     "approval-call-1",
					Status: session.ApprovalPending,
					Tool: &session.ToolEvent{
						Name:  "run_command",
						Input: map[string]any{"command": "printf hello"},
					},
					Options: []session.ApprovalOption{{ID: "allow_once", Name: "Allow once"}},
				},
			},
			{
				ID:   "evt-3",
				Type: session.EventAssistant,
				Message: &model.Message{
					Role:  model.RoleAssistant,
					Parts: []model.Part{model.NewTextPart("pong")},
				},
			},
		},
	})

	if view.Ref.SessionID != "sess-1" || view.Title != "scratch" || view.Workspace.Key != "repo" {
		t.Fatalf("view session = %#v", view)
	}
	if view.Status != "waiting_approval" {
		t.Fatalf("status = %q, want waiting_approval", view.Status)
	}
	if len(view.Transcript) != 3 {
		t.Fatalf("transcript = %#v, want user, approval, assistant", view.Transcript)
	}
	if view.Transcript[0].Text != "ping" || view.Transcript[2].Text != "pong" {
		t.Fatalf("transcript texts = %#v", view.Transcript)
	}
	if len(view.PendingApprovals) != 1 {
		t.Fatalf("pending approvals = %#v, want one", view.PendingApprovals)
	}
	if approval := view.PendingApprovals[0]; approval.ID != "approval-call-1" || approval.Tool != "run_command" || approval.Command != "printf hello" || approval.TurnID != "turn-1" {
		t.Fatalf("approval = %#v", approval)
	}
	if len(view.Participants) != 1 || view.Participants[0].ID != "reviewer" || view.Participants[0].Name != "Reviewer" {
		t.Fatalf("participants = %#v, want reviewer", view.Participants)
	}
}
