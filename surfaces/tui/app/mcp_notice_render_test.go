package tuiapp

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestTurnObservationIncludesSessionNotice(t *testing.T) {
	events := make(chan eventstream.Envelope, 2)
	events <- eventstream.Envelope{Kind: eventstream.KindNotice, SessionID: "session-1", Notice: "MCP unavailable", Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}}
	events <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Now())
	close(events)
	var messages []tea.Msg
	forwardTurnEventStream(context.Background(), &errorReportingControlTurn{events: events}, &ProgramSender{Send: func(message tea.Msg) {
		message = unwrapSessionViewMessage(message)
		messages = append(messages, message)
	}})
	if len(messages) != 2 {
		t.Fatalf("Turn view lost Session notice: %#v", messages)
	}
	if terminal, ok := messages[1].(eventstream.Envelope); !ok || !eventstream.IsTurnTerminalLifecycle(terminal) {
		t.Fatalf("lost Turn terminal: %#v", messages)
	}
}

func TestSessionObservationPresentsNoticePublishedBeforeAttachment(t *testing.T) {
	const notice = "MCP unavailable during admission"
	for _, automatic := range []bool{true, false} {
		feed := newObservationTestFeed("session")
		<-feed.feed
		envelope := eventstream.Envelope{
			Kind: eventstream.KindNotice, SessionID: "session", Notice: notice,
			Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		}
		feed.publish(envelope)
		feed.feed <- appserver.FeedDelivery{Kind: appserver.FeedDeliverySync, Source: appserver.FeedSourceExact}
		messages := make(chan tea.Msg, 16)
		sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
		defer sender.Close()
		observeSelectedSession(t.Context(), sender, feed, automatic)
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		for ready := false; !ready; {
			select {
			case msg := <-messages:
				model.Update(msg)
				_, ready = unwrapSessionViewMessage(msg).(sessionHistoryReadyMsg)
			case <-time.After(time.Second):
				t.Fatal("Session attachment did not finish")
			}
		}
		model.drainPendingRenderEvents(time.Now())
		model.flushPendingViewportSync()
		frame := model.View().Content
		if count := strings.Count(frame, notice); count != 1 {
			t.Fatalf("automatic=%v: notice count=%d, want one:\n%s", automatic, count, frame)
		}
		if events := projectResumeReplayEvents([]eventstream.Envelope{envelope}); len(events) != 0 {
			t.Fatal("transient Session notice became canonical replay history")
		}
		updates := renderFullscreenFramesForTest(t, model.width, model.height, frame)
		assertPhysicalFullscreenFrame(t, model.width, model.height, frame, updates)
	}
}

func mcpNoticeForTest() eventstream.Envelope {
	return eventstream.Envelope{Kind: eventstream.KindNotice, SessionID: "session-1", NoticeKind: eventstream.NoticeKindMCPStartupFailed, Notice: "MCP context7 unavailable · /mcp for details", OccurredAt: time.Now(), Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}}
}

func TestMCPStartupNoticeIsDeduplicatedExpiringHint(t *testing.T) {
	for _, width := range []int{60, 120} {
		m := NewModel(Config{NoColor: true, NoAnimation: true})
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		env := mcpNoticeForTest()
		_, cmd := m.handleACPEventEnvelope(env)
		if cmd == nil || m.hint != env.Notice || len(mainACPTurnBlocksForTest(m)) != 0 {
			t.Fatalf("hint=%q blocks=%d", m.hint, len(mainACPTurnBlocksForTest(m)))
		}
		_, cmd = m.handleACPEventEnvelope(env)
		if cmd != nil || len(m.hintEntries) != 1 {
			t.Fatal("duplicate hint/timer")
		}
		if policy := renderEventPolicyForACPEnvelope(env); policy.dismissHints || policy.flushSmoothing {
			t.Fatal("hint disturbed activity")
		}
		m.Update(clearHintMsg{id: m.hintEntries[0].id})
		if m.hint != "" {
			t.Fatal("hint did not expire")
		}
		env.OccurredAt = time.Now().Add(-mcpStartupHintLifetime - time.Second)
		m.handleACPEventEnvelope(env)
		if m.hint != "" {
			t.Fatal("expired notice replayed")
		}
		if events := projectResumeReplayEvents([]eventstream.Envelope{env}); len(events) != 0 {
			t.Fatal("transient notice persisted")
		}
	}
}

func TestMCPStartupHintDoesNotReplaceWorkAndClearsOnSessionChange(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	env := mcpNoticeForTest()
	m.showHint("Current activity", hintOptions{priority: HintPriorityNormal})
	m.handleACPEventEnvelope(env)
	if m.hint != "Current activity" {
		t.Fatal("overwrote higher priority hint")
	}
	m.applySessionReconnectState(appserver.SessionState{SessionID: "session-2"})
	if len(m.hintEntries) != 0 {
		t.Fatal("hint crossed Session")
	}
	m.handleACPEventEnvelope(env)
	if len(m.hintEntries) != 0 {
		t.Fatal("foreign Session hint")
	}
	env.SessionID = "session-2"
	m.beginLiveTurn(SubmissionModeDefault, true, time.Now())
	m.handleACPEventEnvelope(env)
	if len(m.hintEntries) != 0 || !m.turnRunning() {
		t.Fatal("startup hint disturbed running Turn")
	}
}

func TestMCPStartupHintDuringHistoryAttachment(t *testing.T) {
	for _, expired := range []bool{false, true} {
		m := NewModel(Config{NoColor: true, NoAnimation: true})
		m.beginSessionHistory(sessionViewStartMsg{state: appserver.SessionState{SessionID: "session-1"}})
		env := mcpNoticeForTest()
		if expired {
			env.OccurredAt = time.Now().Add(-time.Minute)
		}
		m.Update(env)
		m.Update(env)
		m.commitSessionHistory()
		if len(mainACPTurnBlocksForTest(m)) != 0 {
			t.Fatal("startup notice entered transcript")
		}
		if expired && len(m.hintEntries) != 0 || !expired && len(m.hintEntries) != 1 {
			t.Fatalf("expired=%v hints=%#v", expired, m.hintEntries)
		}
	}
}
