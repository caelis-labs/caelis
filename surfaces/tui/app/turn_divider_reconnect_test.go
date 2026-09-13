package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestTurnDividerSurvivesSessionAttachment(t *testing.T) {
	for _, tc := range []struct {
		name      string
		active    bool
		automatic bool
		history   bool
		terminal  string
	}{
		{name: "first_prompt_running", active: true, automatic: true},
		{name: "first_prompt_already_completed", automatic: true},
		{name: "resume_running_after_history", active: true, history: true},
		{name: "resume_idle_after_history", history: true},
		{name: "resume_cancelled", terminal: eventstream.LifecycleStateCancelled},
		{name: "resume_failed", terminal: eventstream.LifecycleStateFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := time.Unix(100, 0)
			feed := newObservationTestFeed("session")
			<-feed.feed // Backfill precedes the synchronization boundary.
			feed.state.Run = appserver.RunState{Active: tc.active, TurnID: "current", StartedAt: started}
			turn := func(id, prompt, answer string, at time.Time, complete bool) {
				feed.publish(eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: "session", Scope: eventstream.ScopeMain, TurnID: id, OccurredAt: at, Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateRunning}})
				for _, chunk := range []struct{ kind, text string }{{eventstream.UpdateUserMessage, prompt}, {eventstream.UpdateAgentMessage, answer}} {
					feed.publish(eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session", Scope: eventstream.ScopeMain, TurnID: id, OccurredAt: at.Add(time.Second), Final: true, Update: eventstream.ContentChunk{SessionUpdate: chunk.kind, Content: eventstream.TextContent{Type: "text", Text: chunk.text}}})
				}
				if complete {
					terminal := eventstream.TurnCompleted("handle", "run", id, at.Add(3*time.Second))
					terminal.SessionID = "session"
					if tc.terminal != "" {
						terminal.Lifecycle.State = tc.terminal
					}
					feed.publish(terminal)
					feed.publish(terminal)
				}
			}
			if tc.history {
				turn("previous", "earlier question", "earlier answer", started.Add(-20*time.Second), true)
			}
			turn("current", "hi", "Hi! How can I help?", started, !tc.active)
			feed.feed <- appserver.FeedDelivery{Kind: appserver.FeedDeliverySync, Source: appserver.FeedSourceExact}
			messages := make(chan tea.Msg, 32)
			sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
			t.Cleanup(sender.Close)
			observeSelectedSession(t.Context(), sender, feed, tc.automatic)
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			if tc.automatic {
				m.beginLiveTurn(SubmissionModeDefault, true, started)
			}
			deadline := time.After(5 * time.Second)
			for ready := false; !ready; {
				select {
				case msg := <-messages:
					m.Update(msg)
					_, ready = unwrapSessionViewMessage(msg).(sessionHistoryReadyMsg)
				case <-deadline:
					t.Fatal("Session backfill did not finish")
				}
			}
			m.drainPendingRenderEvents(time.Now())
			m.flushPendingViewportSync()
			before := m.View().Content
			if tc.active {
				if !m.liveTurn.StartedAt.Equal(started) || m.liveTurn.HasLastDuration {
					t.Fatalf("history changed active Turn start: %v", m.liveTurn.StartedAt)
				}
				terminal := eventstream.TurnCompleted("handle", "run", "current", started.Add(3*time.Second))
				terminal.SessionID = "session"
				m = applyACPEnvelopeForTest(t, m, terminal)
				m = applyACPEnvelopeForTest(t, m, terminal)
			}
			m.handleUserMessageMsg(UserMessageMsg{Text: "next question"})
			m.syncViewportContent()
			var labels []string
			for _, block := range m.doc.Blocks() {
				if divider, ok := block.(*DividerBlock); ok {
					labels = append(labels, divider.Label)
				}
			}
			want := 1
			if tc.history {
				want++
			}
			if len(labels) != want || labels[len(labels)-1] != "3.0s" {
				t.Fatalf("Turn divider labels = %v, want %d correctly timed dividers", labels, want)
			}
			after := m.View().Content
			assertFrameContainsInOrder(t, tc.name, after, []string{"Hi! How can I help?", "3.0s", "next question"})
			if strings.Count(after, "3.0s") != want {
				t.Fatalf("duration divider missing or duplicated:\n%s", after)
			}
			updates := renderFullscreenFramesForTest(t, 80, 24, before, after)
			assertPhysicalFullscreenFrame(t, 80, 24, after, updates)
		})
	}
}

func TestReplayParticipantFooterSuppressesOuterTurnDivider(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	started := time.Unix(100, 0)
	terminal := eventstream.TurnCompleted("handle", "run", "main", started.Add(3*time.Second))
	terminal.SessionID = "session"
	events := []eventstream.Envelope{{
		Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeParticipant,
		ScopeID: "participant-turn", TurnID: "participant-turn", ParticipantID: "ivy", Actor: "@ivy",
		OccurredAt: started, Final: true,
		Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentThought, Content: eventstream.TextContent{Type: "text", Text: "checking"}},
	}, {
		Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeParticipant,
		ScopeID: "participant-turn", TurnID: "participant-turn", ParticipantID: "ivy", Actor: "@ivy",
		OccurredAt: started.Add(3 * time.Second), Final: true,
		Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "done"}},
	}, terminal, terminal}
	m.Update(TranscriptEventsMsg{Events: projectResumeReplayEvents(events), ReconnectReplay: true})
	m.drainPendingRenderEvents(time.Now())
	m.flushPendingViewportSync()
	participant := m.findParticipantTurnBlock("participant-turn")
	if participant == nil || !participantTurnHasFooter(participant) {
		t.Fatal("participant's duration footer missing after replay")
	}
	for _, block := range m.doc.Blocks() {
		switch block.(type) {
		case *DividerBlock, *MainACPTurnBlock:
			t.Fatalf("main lifecycle duplicated the participant's footer: %T", block)
		}
	}
	frame := m.View().Content
	if strings.Count(frame, "3.0s") != 1 {
		t.Fatalf("participant duration missing or duplicated:\n%s", frame)
	}
	assertPhysicalFullscreenFrame(t, 80, 24, frame, renderFullscreenFramesForTest(t, 80, 24, frame))
}
