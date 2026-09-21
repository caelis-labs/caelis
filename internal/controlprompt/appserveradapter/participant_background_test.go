package appserveradapter

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type participantInputRecorder struct {
	appserver.SubagentInputClient
	request appserver.SubagentInputRequest
}

func TestReviewStartsPersistentTasksAndContinuesByHandle(t *testing.T) {
	sessions := &sessionClientAdapterTestClient{state: appserver.SessionState{
		SessionID: "parent", CWD: t.TempDir(), Revision: 7,
		Controller: session.ControllerBinding{EpochID: "epoch"},
	}}
	participants := &sessionClientAdapterTestParticipantClient{}
	adapter := newSessionClientAdapterForTest(t, sessions, participants, "parent", "cli-tui")
	input := &participantInputRecorder{}
	adapter.subagentInputs = input
	for _, instructions := range []string{"", "复审修复"} {
		attachments := []controlprompt.Attachment{{Name: "inline.png", Offset: len([]rune(instructions)), MimeType: "image/png", Data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a5l8AAAAASUVORK5CYII="}}
		result, err := adapter.StartReview(t.Context(), instructions, attachments)
		if err != nil {
			t.Fatal(err)
		}
		req := participants.start
		prompt, _ := controlprompt.ReviewPrompt(instructions)
		if result.TaskID != "task-1" || result.SessionID != "parent" || result.Turn != nil || !req.Background || req.Transient || req.DetachSource != "" || req.Handle != "reviewer" || req.Source != "slash_review" || req.Role != session.ParticipantRoleSidecar || req.Input != prompt {
			t.Fatalf("review is not a persistent task: result=%#v request=%#v", result, req)
		}
		if len(req.ContentParts) != 2 || req.ContentParts[0].Text != prompt || req.ContentParts[1].Type != model.ContentPartImage || req.ContentParts[1].Data != attachments[0].Data {
			t.Fatalf("review prompt lost attachment placement: %#v", req.ContentParts)
		}
		if req.ExpectedRevision == nil || *req.ExpectedRevision != 7 || req.ExpectedControllerEpoch != "epoch" {
			t.Fatalf("review bypassed Session guards: %#v", req)
		}
		for _, previous := range sessions.state.Participants {
			if previous.Label == req.Label {
				t.Fatal("new /review reused an existing handle")
			}
		}
		sessions.state.Participants = append(sessions.state.Participants, session.ParticipantBinding{
			ID: req.OperationID, Label: req.Label, Kind: session.ParticipantKindSubagent,
			Role: req.Role, Source: req.Source, DelegationID: result.TaskID,
		})
		for _, handle := range []string{strings.TrimPrefix(req.Label, "@"), "reviewer(" + strings.TrimPrefix(req.Label, "@") + ")"} {
			continued, err := adapter.ContinueAgentRun(t.Context(), handle, "recheck the fix", nil)
			if err != nil {
				t.Fatal(err)
			}
			if continued.TaskID != result.TaskID || continued.Turn != nil || input.request.ParticipantID != req.OperationID || input.request.Input != "recheck the fix" {
				t.Fatalf("review followup did not reach the same child: %#v %#v", continued, input.request)
			}
		}
	}
	if sessions.prompt.OperationID != "" || participants.prompt.OperationID != "" {
		t.Fatal("background review claimed a foreground Turn")
	}
}

func (r *participantInputRecorder) SubmitSubagentInput(_ context.Context, req appserver.SubagentInputRequest) (collaboration.UserInputStatus, error) {
	r.request = req
	return collaboration.UserInputStatus{ID: req.OperationID, State: "queued"}, nil
}

func TestBackgroundParticipantFollowupUsesChildMailbox(t *testing.T) {
	sessions := &sessionClientAdapterTestClient{state: appserver.SessionState{
		SessionID: "parent", CWD: t.TempDir(), Participants: []session.ParticipantBinding{{
			ID: "child-agent", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleSidecar,
			Label: "@orbit-worker", Source: "slash_profile_orbit", DelegationID: "child-task",
		}},
	}}
	participants := &sessionClientAdapterTestParticipantClient{}
	adapter := newSessionClientAdapterForTest(t, sessions, participants, "parent", "cli-tui")
	input := &participantInputRecorder{}
	adapter.subagentInputs = input
	result, err := adapter.ContinueAgentRun(t.Context(), "orbit(orbit-worker)", "follow up", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Turn != nil || result.TaskID != "child-task" || result.SessionID != "parent" || result.InputReceipt == nil || result.InputReceipt.ID != input.request.OperationID || result.InputReceipt.State != "queued" {
		t.Fatalf("unexpected background receipt: %#v", result)
	}
	if input.request.OperationID == "" || input.request.ParticipantID != "child-agent" || input.request.TaskID != "child-task" || input.request.Input != "follow up" {
		t.Fatalf("wrong child mailbox: %#v", input.request)
	}
	if participants.prompt.OperationID != "" || sessions.prompt.OperationID != "" {
		t.Fatal("child followup created a foreground Turn")
	}
}
