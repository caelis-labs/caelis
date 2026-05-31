package viewmodel

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	coretool "github.com/OnslaughtSnail/caelis/core/tool"
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
				Type: session.EventPlan,
				Plan: []session.PlanEntry{
					{Content: "Read code", Status: "completed"},
					{Content: "Implement fix", Status: "in_progress"},
				},
			},
			{
				ID:   "evt-4",
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
	if len(view.Transcript) != 4 {
		t.Fatalf("transcript = %#v, want user, approval, plan, assistant", view.Transcript)
	}
	if view.Transcript[0].Text != "ping" || view.Transcript[3].Text != "pong" {
		t.Fatalf("transcript texts = %#v", view.Transcript)
	}
	if len(view.Plan) != 2 || view.Plan[1].Content != "Implement fix" || view.Plan[1].Status != "in_progress" {
		t.Fatalf("plan = %#v, want latest canonical plan", view.Plan)
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

func TestFromSnapshotProjectsTranscriptTaskActions(t *testing.T) {
	view := FromSnapshot(session.Snapshot{
		Session: session.Session{Ref: session.Ref{SessionID: "sess-tasks"}},
		Events: []session.Event{{
			ID:   "tool-1",
			Type: session.EventToolResult,
			Tool: &session.ToolEvent{
				ID:     "call-1",
				Name:   "run_command",
				Status: session.ToolRunning,
				Meta: coretool.WithRuntimeTaskMeta(nil, map[string]any{
					"task_id":        "task-1",
					"state":          "running",
					"running":        true,
					"supports_input": true,
				}),
			},
		}},
	})

	if len(view.Transcript) != 1 {
		t.Fatalf("transcript = %#v, want one tool item", view.Transcript)
	}
	actions := view.Transcript[0].Actions
	if len(actions) != 4 {
		t.Fatalf("actions = %#v, want tail/wait/cancel/write", actions)
	}
	if !hasTranscriptAction(actions, "task.tail:task-1", "/task tail task-1", false, false) ||
		!hasTranscriptAction(actions, "task.wait:task-1", "/task wait task-1", false, false) ||
		!hasTranscriptAction(actions, "task.cancel:task-1", "/task cancel task-1", true, false) ||
		!hasTranscriptAction(actions, "task.write:task-1", "/task write task-1 -- ", false, true) {
		t.Fatalf("actions = %#v, want shared task command descriptors", actions)
	}
}

func TestFromSnapshotProjectsTerminalTranscriptTaskReleaseAction(t *testing.T) {
	view := FromSnapshot(session.Snapshot{
		Session: session.Session{Ref: session.Ref{SessionID: "sess-tasks"}},
		Events: []session.Event{{
			ID:   "tool-1",
			Type: session.EventToolResult,
			Tool: &session.ToolEvent{
				ID:     "call-1",
				Name:   "run_command",
				Status: session.ToolCompleted,
				Meta: coretool.WithRuntimeTaskMeta(nil, map[string]any{
					"task_id": "task-1",
					"state":   "completed",
				}),
			},
		}},
	})

	if len(view.Transcript) != 1 {
		t.Fatalf("transcript = %#v, want one tool item", view.Transcript)
	}
	actions := view.Transcript[0].Actions
	if len(actions) != 2 {
		t.Fatalf("actions = %#v, want tail/release", actions)
	}
	if !hasTranscriptAction(actions, "task.tail:task-1", "/task tail task-1", false, false) ||
		!hasTranscriptAction(actions, "task.release:task-1", "/task release task-1", false, false) {
		t.Fatalf("actions = %#v, want terminal shared task commands", actions)
	}
}

func hasTranscriptAction(actions []TranscriptAction, id string, command string, destructive bool, requiresInput bool) bool {
	for _, action := range actions {
		if action.ID == id && action.Command == command && action.Destructive == destructive && action.RequiresInput == requiresInput && action.Enabled {
			return true
		}
	}
	return false
}
