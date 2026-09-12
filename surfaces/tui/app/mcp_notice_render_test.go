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

func TestMCPStartupNoticeRendersAsTimelineNotice(t *testing.T) {
	const notice = "MCP server \"context7\" did not initialize within 30 seconds. Its tools are unavailable; the conversation can continue."
	for _, width := range []int{60, 120} {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{Kind: eventstream.KindNotice, SessionID: "session-1", Scope: eventstream.ScopeMain, Notice: notice, Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}})
		blocks := mainACPTurnBlocksForTest(model)
		if len(blocks) != 1 || len(blocks[0].Events) != 1 || blocks[0].Events[0].Kind != SENotice {
			t.Fatalf("notice blocks=%#v", blocks)
		}
		text := joinRenderedPlain(blocks[0].Render(BlockRenderContext{Width: width}))
		normalized := strings.Join(strings.Fields(text), " ")
		for _, part := range []string{"context7", "30 seconds", "conversation can continue"} {
			if !strings.Contains(normalized, part) {
				t.Fatalf("render omitted %q: %s", part, text)
			}
		}
		t.Logf("width %d:\n%s", width, text)
	}
}
