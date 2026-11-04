package tuiapp

import (
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"testing"
	"time"
)

func TestActivityRetiredTurnCannotAcquireApprovalHint(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scope    eventstream.Scope
		approval eventstream.ApprovalRequestID
		parent   *eventstream.ParentToolRelation
	}{
		{"approval", eventstream.ScopeMain, "original-request", nil},
		{"parent", eventstream.ScopeMain, "", &eventstream.ParentToolRelation{ToolCallID: "parent"}},
		{"participant", eventstream.ScopeParticipant, "", nil},
		{"subagent", eventstream.ScopeSubagent, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
			for _, id := range []string{"A", "B"} {
				m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindLifecycle, TurnID: id, Scope: eventstream.ScopeMain, Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateRunning}})
			}
			m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindApprovalReview, TurnID: "A", Scope: tc.scope, ApprovalRequestID: tc.approval, ParentTool: tc.parent, ApprovalReview: &eventstream.ApprovalReview{ToolCallID: "old", Status: "in_progress"}})
			if m.runningActivity.Phase == runningPhaseReview || m.runningHintTracker.turnID != "B" {
				t.Fatalf("retired review acquired current foreground: %+v", m.runningActivity)
			}
		})
	}
}

func TestActivityToolCompletionCannotCloseOtherTurnOwner(t *testing.T) {
	tracker := newRunningHintTracker()
	tracker.beginTurn(time.Unix(100, 0))
	for _, turn := range []string{"A", "B"} {
		key := "tool:" + turn + ":shared"
		tracker.observeOwner("handle-"+turn, runningActivityOwner{Key: key, CallID: "shared", Target: runningTargetShell})
		tracker.start(key, runningPhaseToolWait, runningTargetShell, time.Unix(101, 0), "shared")
	}
	tracker.completeTool("tool:A:shared", "shared", time.Unix(102, 0))
	if _, active := tracker.active["tool:B:shared"]; !active {
		t.Fatal("old terminal completed a different Turn owner with shared CallID")
	}
}

func TestActivityRetiredApprovalSettlementKeepsOriginalRequestID(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	m.runningHintTracker.turnID = "B"
	m.runningHintTracker.retiredTurns["A"] = struct{}{}
	m.pendingPrompt = []PromptRequestMsg{{ApprovalRequestID: "old-request"}, {ApprovalRequestID: "current-request"}}
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindLifecycle, TurnID: "A", ApprovalRequestID: "old-request", Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted}})
	if len(m.pendingPrompt) != 1 || m.pendingPrompt[0].ApprovalRequestID != "current-request" || !m.turnRunning() || m.runningHintTracker.turnID != "B" {
		t.Fatal("old approval settlement lost original identity or ended current Turn")
	}
}

func TestActivityTaskTerminalUsesOriginalParentHandle(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	m.runningHintTracker.turnID = "B"
	for _, turn := range []string{"A", "B"} {
		key := "tool:" + turn + ":shared"
		m.runningHintTracker.observeOwner("handle-"+turn, runningActivityOwner{Key: key, CallID: "shared", Target: runningTargetShell})
		m.runningHintTracker.start(key, runningPhaseToolWait, runningTargetShell, time.Unix(101, 0), "shared")
	}
	terminal := func(handle string) eventstream.Envelope {
		return eventstream.Envelope{Kind: eventstream.KindSessionUpdate, TurnID: "task-source", Scope: eventstream.ScopeMain, ScopeID: "session", ParentTool: &eventstream.ParentToolRelation{ToolCallID: "shared", ToolName: "RunCommand"}, Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}, Final: true, Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "shared", Status: stringPtr(eventstream.ToolStatusCompleted), Meta: runningSnapshotTerminalMeta("RunCommand", handle, "terminal", "done", "final")}}
	}
	m = applyACPEnvelopeForTest(t, m, terminal("handle-A"))
	if _, active := m.runningHintTracker.active["tool:B:shared"]; !active {
		t.Fatal("old Task terminal cleared new Turn's tool")
	}
	m = applyACPEnvelopeForTest(t, m, terminal("handle-B"))
	if _, active := m.runningHintTracker.active["tool:B:shared"]; active {
		t.Fatalf("typed parent plus exact handle did not settle legitimate owner: active=%+v owners=%+v projection=%+v", m.runningHintTracker.active, m.runningHintTracker.ownersByCallID, m.projectACPEnvelopePresentation(terminal("handle-B")))
	}
}

func TestActivityAmbiguousTaskObservationCannotChooseByCallID(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	m.runningHintTracker.turnID = "B"
	for _, turn := range []string{"A", "B"} {
		key := "tool:" + turn + ":shared"
		m.runningHintTracker.observeOwner("ambiguous", runningActivityOwner{Key: key, CallID: "shared", Target: runningTargetShell})
		m.runningHintTracker.start(key, runningPhaseToolWait, runningTargetShell, time.Unix(101, 0), "shared")
	}
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, TurnID: "task-source", Scope: eventstream.ScopeMain, ParentTool: &eventstream.ParentToolRelation{ToolCallID: "shared", ToolName: "RunCommand"}, Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}, Final: true, Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "shared", Status: stringPtr(eventstream.ToolStatusCompleted), Meta: runningSnapshotTerminalMeta("RunCommand", "ambiguous", "terminal", "done", "final")}})
	if _, active := m.runningHintTracker.active["tool:B:shared"]; !active {
		t.Fatal("ambiguous Task observation selected another Turn by bare CallID")
	}
}
