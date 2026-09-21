package kernel

import (
	"context"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestBackgroundChildApprovalWithoutParentToolOrTurn(t *testing.T) {
	for _, mainRunning := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "main-running"}[mainRunning], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			sessions := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
			active, err := sessions.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "owner"})
			if err != nil {
				t.Fatal(err)
			}
			gw, err := New(Config{Sessions: sessions, Runtime: mockRuntime{}, Resolver: staticResolver{}, DefaultApprovalMode: ApprovalModeManual})
			if err != nil {
				t.Fatal(err)
			}
			var main *turnHandle
			if mainRunning {
				if _, err := sessions.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "main"}); err != nil {
					t.Fatal(err)
				}
				main = newTurnHandle(turnHandleConfig{ctx: ctx, sessionRef: active.SessionRef, handleID: "main-handle", runID: "main-run", turnID: "main-turn", approvals: gw.sessionApprovals(active.SessionRef)})
				gw.active[active.SessionID] = main
			}
			events := make(chan eventstream.Envelope, 16)
			done := make(chan error, 1)
			go func() {
				response, err := gw.RequestChildApproval(ctx, agent.ApprovalRequest{
					SessionRef: active.SessionRef, PauseTokenID: "child-permission",
					Metadata: map[string]any{"subagent": true, "task_id": "child-task", "participant_id": "child-agent", "participant_kind": "subagent", "participant_session_id": "child-session"},
					Origin:   &agent.ApprovalOrigin{Role: agent.ApprovalRoleSubagent, Endpoint: agent.ApprovalEndpointExternalACP, TaskID: "child-task", ParticipantID: "child-agent", SessionID: "child-session", ParentSessionID: active.SessionID},
					Tool:     tool.Definition{Name: "Write"}, Call: tool.Call{ID: "write-1", Name: "Write"},
					Approval: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "write-1", Name: "Write"}, Options: []session.ProtocolApprovalOption{{ID: "allow", Name: "Allow once", Kind: "allow_once"}}},
				}, TurnEventObserverFunc(func(_ context.Context, event eventstream.Envelope) error { events <- event; return nil }))
				if err == nil && !response.Approved {
					t.Error("child approval was not delivered")
				}
				done <- err
			}()
			select {
			case event := <-events:
				if event.Scope != eventstream.ScopeSubagent || event.ScopeID != "child-task" {
					t.Fatalf("approval escaped child scope: %#v", event)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			target, ok := gw.ApprovalTarget(active.SessionID, "child-permission")
			if !ok {
				t.Fatal("child approval has no resolvable target")
			}
			state, err := gw.ControlClientRuntimeState(ctx, active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			if state.Approval.Active == nil || state.Approval.Active.Target.HandleID != target.HandleID ||
				state.Approval.Active.Target.RunID != target.RunID || state.Approval.Active.Target.TurnID != target.TurnID {
				t.Fatalf("approval bootstrap lost its child target: %#v", state.Approval)
			}
			if gw.active[active.SessionID] != main {
				t.Fatal("child approval replaced the main Turn")
			}
			if err := gw.SubmitActiveTurn(ctx, SubmitActiveTurnRequest{SessionRef: active.SessionRef, Kind: SubmissionKindApproval, Approval: &ApprovalDecision{RequestID: "child-permission", Outcome: "selected", OptionID: "allow", Approved: true}}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if _, ok := gw.ApprovalTarget(active.SessionID, "child-permission"); ok {
				t.Fatal("settled child approval retained its queue entry")
			}
			loaded, err := sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range loaded.Events {
				if event.ChildOrigin == nil {
					continue
				}
				count++
				if event.ChildOrigin.TaskID != "child-task" || event.ChildOrigin.ParentTool.CallID != "" || session.ValidateEventChildOrigin(*event.ChildOrigin) != nil {
					t.Fatalf("invalid durable child approval: %#v", event.ChildOrigin)
				}
			}
			if count != 2 {
				t.Fatalf("approval replay has %d child events, want request and settlement", count)
			}
		})
	}
}
