package controller

import (
	"errors"
	"testing"

	v2 "github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

func TestDraftControllerAndParticipantPermissionUseExactBoundSession(t *testing.T) {
	for _, role := range []string{"main", "participant"} {
		for _, requested := range []string{"remote", "other", " remote ", ""} {
			t.Run(role+requested, func(t *testing.T) {
				requester := &recordingControllerApprovalRequester{}
				req := v2.RequestPermissionRequest{SessionId: v2.SessionId(requested), Title: "Independent permission", Options: []v2.PermissionOption{{OptionId: "allow_once", Name: "Allow", Kind: "allow_once"}}, Subject: &v2.RequestPermissionSubject{Command: &v2.RequestPermissionSubjectCommand{Type: "command", Command: "git status", Cwd: "/workspace"}}}
				main := &controllerRun{remoteSessionID: "remote", approvalRequester: requester}
				participant := &participantRun{remoteSessionID: "remote", approvalRequester: requester}
				handler := main.draftPermissionHandler
				if role == "participant" {
					handler = participant.draftPermissionHandler
				}
				response, err := handler(t.Context(), req)
				if requested != "remote" {
					if !errors.Is(err, errACPPermissionSessionMismatchEndpoint) || requester.calls != 0 {
						t.Fatalf("mismatched endpoint entered approval: %+v %v", requester.last, err)
					}
					return
				}
				if err != nil || response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "allow_once" || requester.calls != 1 || requester.last.EndpointSessionID != "remote" || requester.last.Approval == nil || requester.last.Approval.ToolCall.ID != "" || requester.last.Approval.Context.Title != req.Title {
					t.Fatalf("draft native ownership lost: %+v %+v %v", requester.last, response, err)
				}
			})
		}
	}
}
