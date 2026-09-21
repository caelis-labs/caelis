package acpagentbridge

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestBackgroundApprovalStopsOnSettledOrUncertainSubmission(t *testing.T) {
	for _, tc := range []struct {
		name        string
		outcome     appserver.Outcome
		clear       bool
		changeOwner bool
		wantErr     bool
	}{
		{name: "unknown-still-pending", outcome: appserver.OutcomeUnknown, wantErr: true},
		{name: "unknown-resolved", outcome: appserver.OutcomeUnknown, clear: true},
		{name: "conflict-resolved", outcome: appserver.OutcomeConflicted, clear: true},
		{name: "conflict-owner-changed", outcome: appserver.OutcomeConflicted, changeOwner: true},
		{name: "unknown-owner-changed", outcome: appserver.OutcomeUnknown, changeOwner: true},
		{name: "committed-overrides-conflict-error", outcome: appserver.OutcomeCommitted, wantErr: true},
		{name: "rejected-overrides-conflict-error", outcome: appserver.OutcomeRejected, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			active := &appserver.ActiveApproval{RequestID: "approval", Scope: eventstream.ScopeSubagent, ScopeID: "task",
				Target: appserver.TurnTarget{HandleID: "owner", RunID: "run", TurnID: "turn"}}
			client := &participantSessionClient{state: appserver.SessionState{SessionID: "session", Revision: 7,
				Approval: appserver.ApprovalState{Active: active}}}
			calls := 0
			client.resolveApprovalFn = func(_ context.Context, req appserver.ResolveApprovalRequest) (appserver.CommandResult, error) {
				calls++
				if req.ApprovalRequestID != string(active.RequestID) || req.Target != active.Target {
					t.Fatalf("approval target changed: %#v", req)
				}
				if tc.clear {
					client.state.Approval.Active = nil
				}
				if tc.changeOwner {
					changed := *active
					changed.Target.HandleID = "replacement"
					client.state.Approval.Active = &changed
				}
				return appserver.CommandResult{Outcome: tc.outcome}, session.ErrRevisionConflict
			}
			observer := &acpParticipantTasks{agent: &RuntimeAgent{sessionClient: client}, sessionID: "session"}
			err := observer.resolveApprovalWithRetry(t.Context(), active, controlprompt.ApprovalDecision{RequestID: active.RequestID, Approved: true})
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolve error = %v, want error %v", err, tc.wantErr)
			}
			if calls != 1 {
				t.Fatalf("uncertain or committed request was resent %d times", calls)
			}
		})
	}
}
