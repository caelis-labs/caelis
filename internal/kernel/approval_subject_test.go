package kernel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
)

func TestApprovalSubjectLiveStateFileReplayAndStaleIdentity(t *testing.T) {
	for _, name := range []string{"main", "child", "reported_main", "reported_child"} {
		t.Run(name, func(t *testing.T) {
			child := strings.HasSuffix(name, "child")
			root := t.TempDir()
			store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "user", PreferredSessionID: "subject-parity", Workspace: session.WorkspaceRef{CWD: "/workspace"}})
			if err != nil {
				t.Fatal(err)
			}
			def := tool.Definition{Name: "RunCommand", InputSchema: map[string]any{"properties": map[string]any{"justification": map[string]any{"writeOnly": true}}}}
			call := tool.Call{ID: "native-call", Name: def.Name, Input: json.RawMessage(`{"command":"git fetch","justification":"MARKED-APPROVAL-SECRET","sandbox_permissions":"require_escalated"}`)}
			decision, err := presets.WorkspaceWriteMode().DecideTool(t.Context(), policy.ToolContext{Tool: def, Call: call, Session: active, Options: policy.ModeOptions{WorkspaceRoot: active.CWD}, Sandbox: sandbox.Descriptor{Backend: sandbox.BackendHost}})
			if err != nil || decision.Approval == nil {
				t.Fatalf("native approval = %+v, %v", decision, err)
			}
			request := &agent.ApprovalRequest{SessionRef: active.SessionRef, Session: active, RunID: "run-1", TurnID: "turn-1", PauseTokenID: "native-pause", Tool: def, Call: call, Approval: decision.Approval, Metadata: decision.Metadata}
			if child {
				request.Metadata["subagent"] = true
				request.Metadata["task_id"] = "child-task"
				request.Metadata["parent_call_id"] = "spawn-call"
			}
			if strings.HasPrefix(name, "reported_") {
				// An unmarked remote title, effect or scope can repeat a marked
				// target. The shared owner must withhold every derived display
				// field while retaining the actual private action and choices.
				field := session.KnownApprovalField("MARKED-APPROVAL-SECRET")
				decision.Approval.Context = &session.PermissionContext{DisplaySubjectHint: &session.ApprovalSubject{Kind: "command", Title: &field, Description: &field,
					Tool: field, Operation: field, Effect: field, Scope: field, Targets: []session.ApprovalField{{State: session.ApprovalFieldKnown, Text: field.Text, Sensitive: true}},
					Grants: []session.ApprovalGrant{{OptionID: decision.Approval.Options[0].ID, Scope: field}}}}
			}
			handle := newTestTurnHandle()
			handle.sessionRef = active.SessionRef
			gateway := &Gateway{sessions: store}
			handle.persistApproval = gateway.approvalPersister(active.SessionRef, handle.turnID)
			handle.settleApproval = gateway.approvalSettler(active.SessionRef, handle.turnID)
			pending, err := handle.enqueueApproval(request, true)
			if err != nil {
				t.Fatal(err)
			}
			live := testObservedTurnEvents(handle)[0]
			state := handle.controlClientRuntimeState("running", true).Approval.Active
			if state == nil || !reflect.DeepEqual(live.ApprovalSubject, state.Subject) || live.ApprovalRequestID != pending.id || state.RequestID != pending.id || live.Permission.ToolCall.ToolCallID != call.ID || state.Permission.ToolCall.ID != call.ID || !reflect.DeepEqual(state.Permission.Options, decision.Approval.Options) {
				t.Fatalf("live/state identity or subject mismatch: %+v / %+v", live, state)
			}
			if child && (state.ScopeID != "child-task" || live.ScopeID != state.ScopeID || state.ParentTool == nil || state.ParentTool.ToolCallID != "spawn-call") {
				t.Fatalf("child ownership lost: %+v", state)
			}
			// Reopen the physical Store and project its canonical event, without
			// consulting the process FIFO or original Runtime request.
			reloaded := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			page, err := reloaded.EventsPage(t.Context(), session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageAllDurable, Limit: 20})
			if err != nil || len(page.Events) != 1 {
				t.Fatalf("file replay = %+v, %v", page, err)
			}
			event := page.Events[0]
			replay := projection.ProjectSessionEventEnvelope(projection.EnvelopeBaseFromSessionEvent(active.SessionRef, event, projection.SessionEventTransport{}), event)
			if len(replay) != 1 || !reflect.DeepEqual(live.ApprovalSubject, replay[0].ApprovalSubject) || replay[0].ApprovalRequestID != pending.id || session.IsCanonicalHistoryEvent(event) || session.IsMainInvocationVisibleEvent(event) {
				t.Fatalf("display changed through durable replay or entered model context: %+v", replay)
			}
			for _, value := range []any{live, state, replay} {
				raw, _ := json.Marshal(value)
				if strings.Contains(string(raw), "MARKED-APPROVAL-SECRET") {
					t.Fatalf("marked input leaked in presentation: %s", raw)
				}
			}
			wire, err := wirev1.MarshalEnvelope(live)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := wirev1.UnmarshalEnvelope(wire)
			if err != nil || !reflect.DeepEqual(restored.ApprovalSubject, state.Subject) {
				t.Fatalf("HTTP subject round trip = %+v, %v", restored, err)
			}
			option := decision.Approval.Options[0]
			if err := handle.submitApproval(t.Context(), ApprovalDecision{RequestID: pending.id, OptionID: option.ID, Outcome: "selected", Approved: true}); err != nil {
				t.Fatal(err)
			}
			if got := <-pending.decisions; got.RequestID != pending.id || got.OptionID != option.ID {
				t.Fatalf("native receipt = %+v", got)
			}
			if err := handle.submitApproval(t.Context(), ApprovalDecision{RequestID: pending.id, OptionID: option.ID, Outcome: "selected", Approved: true}); err == nil {
				t.Fatal("stale display restored a settled grant")
			}
			if handle.controlClientRuntimeState("running", false).Approval.Active != nil {
				t.Fatal("settled subject remained active")
			}
		})
	}
}

func TestGuardianReceivesExactMarkedActionWhileLiveReviewIsSafe(t *testing.T) {
	const secret = "GUARDIAN-MARKED-SECRET"
	input := json.RawMessage(`{"command":"GUARDIAN-MARKED-SECRET","justification":"GUARDIAN-MARKED-SECRET"}`)
	original := &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "call", Name: "RunCommand", Title: secret, RawInput: map[string]any{"command": secret, "justification": secret}}, Options: []session.ProtocolApprovalOption{{ID: "native-allow", Name: "Allow", Kind: "allow_once"}, {ID: "native-reject", Name: "Reject", Kind: "reject_once"}}}
	req := &agent.ApprovalRequest{SessionRef: session.SessionRef{SessionID: "session"}, PauseTokenID: "native-request", Tool: tool.Definition{Name: "RunCommand", InputSchema: map[string]any{"properties": map[string]any{"command": map[string]any{"writeOnly": true}, "justification": map[string]any{"writeOnly": true}}}},
		Call: tool.Call{ID: "call", Name: "RunCommand", Input: input}, Approval: original, Metadata: map[string]any{"approval_reason": secret, "justification": secret}}
	reviewer := &recordingApprovalReviewer{result: ApprovalReviewResult{Approved: true, OptionID: "native-allow", DisplayText: secret, Rationale: secret, Risk: secret, Authorization: secret}}
	gateway := &Gateway{approvalReviewer: reviewer}
	handle := newTestTurnHandle()
	handle.sessionRef = req.SessionRef
	response, err := gateway.resolveActiveAutoApproval(t.Context(), t.Context(), handle, req, nil, ApprovalModeAutoReview)
	if err != nil || !response.Approved || response.OptionID != "native-allow" {
		t.Fatalf("Guardian decision = %+v, %v", response, err)
	}
	evidence := reviewer.req.Approval
	if evidence == nil || evidence.RawInput["command"] != secret || evidence.ToolTitle != secret || evidence.Reason != secret || evidence.Justification != secret || string(reviewer.req.RuntimeRequest.Call.Input) != string(input) || !reflect.DeepEqual(reviewer.req.RuntimeRequest.Approval, original) {
		t.Fatalf("Guardian received display in place of action evidence: %+v", reviewer.req)
	}
	events := testObservedTurnEvents(handle)
	if len(events) != 2 {
		t.Fatalf("live review events = %+v", events)
	}
	for _, event := range events {
		raw, _ := wirev1.MarshalEnvelope(event)
		if strings.Contains(string(raw), secret) || event.ApprovalRequestID != "native-request" || event.ApprovalSubject == nil || !event.ApprovalSubject.SensitiveInput {
			t.Fatalf("live review leaked or changed identity: %s", raw)
		}
	}
	if original.ToolCall.RawInput["command"] != secret || string(req.Call.Input) != string(input) {
		t.Fatal("display projection mutated executable action")
	}
}
