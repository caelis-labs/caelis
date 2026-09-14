package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestSessionHistoryCommitsExactNarrativeOnlyWhenReady(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	m.handleUserMessageMsg(UserMessageMsg{Text: "previous view"})
	previous := m.doc
	feed := newObservationTestFeed("restored")
	feed.feed = make(chan appserver.FeedDelivery, 256)
	started := time.Unix(100, 0)
	feed.publish(eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: "restored", TurnID: "turn", Scope: eventstream.ScopeMain, Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}, Lifecycle: &eventstream.Lifecycle{State: "running"}, OccurredAt: started})
	want := ""
	for i := range 140 {
		chunk := fmt.Sprintf("%d ", i)
		want += chunk
		feed.publish(eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "restored", TurnID: "turn", Scope: eventstream.ScopeMain, Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}, Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "final", Content: eventstream.TextContent{Type: "text", Text: chunk}}})
	}
	feed.publish(eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: "restored", TurnID: "turn", Scope: eventstream.ScopeMain, Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}, Lifecycle: &eventstream.Lifecycle{State: "completed"}, OccurredAt: started.Add(time.Second)})
	feed.feed <- appserver.FeedDelivery{Kind: appserver.FeedDeliverySync, Source: appserver.FeedSourceExact}
	batches := 0
	sender := &ProgramSender{Send: func(msg tea.Msg) {
		m.Update(msg)
		if _, ok := unwrapSessionViewMessage(msg).(TranscriptEventsMsg); ok {
			batches++
			if m.doc != previous || !strings.Contains(ansi.Strip(m.View().Content), "previous view") {
				t.Fatal("history was exposed before sync")
			}
		}
	}}
	defer sender.Close()
	observeSelectedSession(context.Background(), sender, feed, false)
	if batches < 2 || m.doc == previous || m.sessionHistory != nil {
		t.Fatal("history did not commit")
	}
	var got strings.Builder
	for _, raw := range m.doc.Blocks() {
		if block, ok := raw.(*MainACPTurnBlock); ok {
			for _, event := range block.Events {
				if event.Kind == SEAssistant {
					got.WriteString(event.Text)
				}
			}
		}
	}
	if got.String() != want {
		t.Fatalf("restored text=%q, want %q", got.String(), want)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "139") {
		t.Fatal("final response is not in the physical viewport")
	}
}

func TestSessionHistoryFailureKeepsPreviousDocument(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.handleUserMessageMsg(UserMessageMsg{Text: "retained"})
	old := m.doc
	m.Update(sessionViewStartMsg{generation: 1, state: appserver.SessionState{SessionID: "new"}})
	m.Update(TranscriptEventsMsg{Events: longHistoryTranscript(4), ReconnectReplay: true})
	m.Update(sessionObservationErrorMsg{err: fmt.Errorf("incomplete replacement")})
	if m.doc != old || m.sessionHistory != nil || m.currentSessionID != "" || !m.sessionHistoryFailed {
		t.Fatal("failed history replaced the old document")
	}
}

func TestSessionHistoryReplacementPreservesCurrentInteraction(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.currentSessionID = "session"
	m.textarea.SetValue("draft")
	m.beginLiveTurn(SubmissionModeDefault, true, time.Now())
	turn := m.liveTurn
	child := m.ensureSubagentOutputView("spawn")
	child.taskHandle = "child"
	m.Update(sessionHistoryReplacementMsg{state: appserver.SessionState{SessionID: "session"}})
	m.Update(TranscriptEventsMsg{ReconnectReplay: true, Events: append(longHistoryTranscript(2), TranscriptEvent{Kind: TranscriptEventTool, Scope: ACPProjectionMain, ToolCallID: "spawn", ToolName: surfaceToolSpawn, ToolTaskHandle: "child"})})
	m.Update(sessionHistoryReadyMsg{})
	if m.textarea.Value() != "draft" || m.liveTurn != turn || m.subagentOutputViews["spawn"] != child {
		t.Fatal("replacement reset an active interaction or independently observed child")
	}
}

func TestResumeAtomicPhysicalFrames(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {35, 16}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.handleUserMessageMsg(UserMessageMsg{Text: "previous session"})
			frames := []string{m.View().Content}
			m.Update(sessionViewStartMsg{generation: 1, state: appserver.SessionState{SessionID: "restored"}})
			m.Update(TranscriptEventsMsg{ReconnectReplay: true, Events: longHistoryTranscript(4)})
			frames = append(frames, m.View().Content)
			if !strings.Contains(ansi.Strip(frames[1]), "previous session") {
				t.Fatal("partial history exposed")
			}
			m.Update(sessionHistoryReadyMsg{})
			frames = append(frames, m.View().Content)
			if strings.Contains(ansi.Strip(frames[2]), "previous session") || m.hint == sessionHistoryLoadingHint {
				t.Fatal("old history retained at commit")
			}
			terminal := vt.NewSafeEmulator(size[0], size[1])
			t.Cleanup(func() { _ = terminal.Close() })
			for i, output := range renderFullscreenFramesForTest(t, size[0], size[1], frames...) {
				if _, err := terminal.Write([]byte(output)); err != nil {
					t.Fatal(err)
				}
				got := trimPhysicalFramePadding(ansi.Strip(terminal.Render()))
				want := trimPhysicalFramePadding(ansi.Strip(frames[i]))
				if got != want {
					t.Fatalf("physical frame %d mismatch:\ngot:\n%s\nwant:\n%s", i, got, want)
				}
			}
		})
	}
}
