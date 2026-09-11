package tuiapp

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestTurnObservationDoesNotDuplicateSessionPresenceNotice(t *testing.T) {
	events := make(chan eventstream.Envelope, 2)
	events <- eventstream.Envelope{Kind: eventstream.KindNotice, SessionID: "session-1", Notice: "MCP unavailable", Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}}
	events <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Now())
	close(events)
	var messages []tea.Msg
	forwardTurnEventStream(context.Background(), &errorReportingControlTurn{events: events}, &ProgramSender{Send: func(message tea.Msg) { messages = append(messages, message) }})
	if len(messages) != 1 {
		t.Fatalf("Turn view duplicated Session notice: %#v", messages)
	}
	if terminal, ok := messages[0].(eventstream.Envelope); !ok || !eventstream.IsTurnTerminalLifecycle(terminal) {
		t.Fatalf("lost Turn terminal: %#v", messages)
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
