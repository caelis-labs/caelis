package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	appserver "github.com/caelis-labs/caelis/control/appserver"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestActivityRecoveryReplacesMissingCompactTerminal(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	for _, env := range []eventstream.Envelope{
		{Kind: eventstream.KindLifecycle, SessionID: "s", TurnID: "old", Scope: eventstream.ScopeMain, OccurredAt: time.Unix(100, 0), Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateRunning}},
		{Kind: eventstream.KindLifecycle, SessionID: "s", TurnID: "old", Scope: eventstream.ScopeMain, OccurredAt: time.Unix(101, 0), Lifecycle: &eventstream.Lifecycle{State: session.LifecycleStatusContextCompacting}},
		// The new Turn is authoritative even if the old terminal was lost.
		{Kind: eventstream.KindLifecycle, SessionID: "s", TurnID: "new", Scope: eventstream.ScopeMain, OccurredAt: time.Unix(200, 0), Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateRunning}},
		{Kind: eventstream.KindSessionUpdate, SessionID: "s", TurnID: "new", Scope: eventstream.ScopeMain, OccurredAt: time.Unix(201, 0), Update: eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "git", Kind: eventstream.ToolKindExecute, Title: "git status", Status: eventstream.ToolStatusInProgress}},
	} {
		m = applyACPEnvelopeForTest(t, m, env)
	}
	if got := m.runningActivity; got.Phase != runningPhaseToolWait || got.CallID != "git" {
		t.Fatalf("new tool masked by stale activity: %#v; hint=%s", got, m.buildHintText())
	}
	// A late terminal from the old Turn cannot stop the new one.
	m = applyACPEnvelopeForTest(t, m, eventstream.TurnFailed("s", "r", "old", "fixture deadline", time.Unix(102, 0)))
	if !m.turnRunning() || m.runningActivity.CallID != "git" {
		t.Fatalf("old terminal ended current work: %#v", m.runningActivity)
	}
}

func TestActivityRecoveryOrderReplayStopAndReconnect(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(*Model)
	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	compact := func(turn string, at int64) eventstream.Envelope {
		return eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: "s", TurnID: turn, OccurredAt: time.Unix(at, 0), Lifecycle: &eventstream.Lifecycle{State: session.LifecycleStatusContextCompacting}}
	}
	m = applyACPEnvelopeForTest(t, m, compact("t", 101))
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "s", TurnID: "t", OccurredAt: time.Unix(103, 0), Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "new response"}}})
	m = applyACPEnvelopeForTest(t, m, compact("t", 102))
	if m.runningActivity.Phase != runningPhaseResponding {
		t.Fatal("out-of-order compact start revived a superseded activity")
	}
	m = applyACPEnvelopeForTest(t, m, compact("t", 104))
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindNotice, SessionID: "s", TurnID: "t", OccurredAt: time.Unix(102, 0), NoticeKind: eventstream.NoticeKindCompactFailed, Notice: "fixture compact failed"})
	if m.runningActivity.Phase != runningPhaseCompact {
		t.Fatal("old compact terminal cleared a newer compact")
	}
	m = applyACPEnvelopeForTest(t, m, eventstream.TurnCancelled("s", "r", "t", "stopped", time.Unix(105, 0)))
	if m.turnRunning() || m.runningActivity.Phase != "" || !m.runningActivity.StartedAt.IsZero() {
		t.Fatalf("Stop retained activity clock: %#v", m.runningActivity)
	}
	m = applyACPEnvelopeForTest(t, m, compact("t", 106))
	if m.runningActivity.Phase != "" {
		t.Fatal("late start revived stopped Turn")
	}
	m.applySessionReconnectState(appserver.SessionState{SessionID: "s", Run: appserver.RunState{Active: true, TurnID: "new", StartedAt: time.Unix(200, 0)}})
	m = applyACPEnvelopeForTest(t, m, compact("t", 106))
	m.handleTranscriptEventsMsg(TranscriptEventsMsg{ReconnectReplay: true, Events: []TranscriptEvent{{Kind: TranscriptEventLifecycle, Scope: ACPProjectionMain, TurnID: "t", State: session.LifecycleStatusContextCompacting, OccurredAt: time.Unix(101, 0)}}})
	if m.runningActivity.Phase != runningPhaseModelWait || strings.Contains(m.buildHintText(), "Compacting") {
		t.Fatal("reconnect/replay restored obsolete compaction")
	}
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "s", TurnID: "new", OccurredAt: time.Unix(201, 0), Update: eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "git", Kind: eventstream.ToolKindExecute, Title: "git status", Status: eventstream.ToolStatusInProgress}})
	frame := m.View().Content
	if !strings.Contains(frame, "Waiting on shell") || strings.Contains(frame, "Compacting context") {
		t.Fatalf("rendered current action did not converge:\n%s", frame)
	}
	m.applySessionReconnectState(appserver.SessionState{SessionID: "s"})
	if m.runningActivity.Phase != "" || m.runningIndicatorActive() {
		t.Fatal("idle reconnect retained activity/timer")
	}
}

func TestActivityRecoveryPositionsAndApprovalPriority(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	env := eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: "s", TurnID: "t", Lifecycle: &eventstream.Lifecycle{State: session.LifecycleStatusContextCompacting}, Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{Generation: "g", Sequence: 2}}}
	m = applyACPEnvelopeForTest(t, m, env)
	tool := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "s", TurnID: "t", Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{Generation: "g", Sequence: 3}}, Update: eventstream.ToolCall{SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "git", Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress}}
	m = applyACPEnvelopeForTest(t, m, tool)
	m.startRunningAnimation()
	if m.runningActivity.CallID != "git" || m.runningHintTracker.turnID != "t" {
		t.Fatal("animation/loading restart retired or reset a live Turn")
	}
	m = applyACPEnvelopeForTest(t, m, env)
	if m.runningActivity.CallID != "git" {
		t.Fatal("duplicate old start replaced current tool")
	}
	m.runningHintTracker.start("approval:t:git", runningPhaseReview, "", time.Unix(105, 0), "git")
	m.runningHintTracker.setFocus(runningPhaseRetrying, "", "retry", time.Unix(106, 0))
	m.refreshRunningActivity()
	if m.runningActivity.Phase != runningPhaseReview {
		t.Fatal("retry/log replaced an active approval")
	}
	m.runningHintTracker.completeTool("tool:t:git", "git", time.Unix(107, 0))
	m.refreshRunningActivity()
	if m.runningActivity.Phase == runningPhaseReview {
		t.Fatal("canonical tool result did not clear its missing review terminal")
	}
}

func TestActivityRecoveryCurrentActionSupersedesCompact(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: "s", TurnID: "t", OccurredAt: time.Unix(101, 0), Lifecycle: &eventstream.Lifecycle{State: session.LifecycleStatusContextCompacting}})
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "s", TurnID: "t", OccurredAt: time.Unix(102, 0), Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "continued"}}})
	if got := m.runningActivity; got.Phase != runningPhaseResponding || strings.Contains(m.buildHintText(), "Compacting") {
		t.Fatalf("new response did not supersede stale compaction: %#v", got)
	}
}

func TestActivityRecoveryParticipantTerminalCannotRetireMain(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: "s", TurnID: "main", Scope: eventstream.ScopeMain, Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateRunning}})
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: "s", TurnID: "peer-turn", Scope: eventstream.ScopeParticipant, ScopeID: "peer", ParticipantID: "peer", Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateFailed}})
	m = applyACPEnvelopeForTest(t, m, eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "s", TurnID: "main", Scope: eventstream.ScopeMain, Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "main still working"}}})
	if !m.turnRunning() || m.runningHintTracker.turnID != "main" || m.runningActivity.Phase != runningPhaseResponding {
		t.Fatalf("participant retired main activity: %#v", m.runningActivity)
	}
}
