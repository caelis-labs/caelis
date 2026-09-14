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
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

type newSessionControlService struct {
	observingControlService
	resetCalls int
}

func (s *newSessionControlService) ResetSession(context.Context) error {
	s.resetCalls++
	s.selected.Store(false)
	return nil
}

func TestNewCommandCommitsEmptySessionView(t *testing.T) {
	feed := newObservationTestFeed("session-old")
	service := &newSessionControlService{observingControlService: observingControlService{feed: feed}}
	messages := make(chan tea.Msg, 32)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	t.Cleanup(sender.Close)
	cfg := ConfigFromControlService(service, sender, Config{
		Context: t.Context(), PromptRouterFactory: controlprompt.New,
		NoColor: true, NoAnimation: true, ShowWelcomeCard: true,
	})
	m := NewModel(cfg)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	applyMessages := func() {
		for len(messages) > 0 {
			m.Update(<-messages)
		}
	}
	if msg := cfg.executeLineCmd(Submission{Text: "/resume session-old"}); msg != nil {
		t.Fatalf("resume completion = %#v, want following observation", msg)
	}
	applyMessages()
	if m.currentSessionID != "session-old" || m.sessionHistory != nil {
		t.Fatal("initial Session did not attach through the command bridge")
	}
	m.handleUserMessageMsg(UserMessageMsg{Text: "previous session transcript"})
	m.textarea.SetValue("previous session draft")
	frames := []string{m.View().Content}
	responses := make(chan PromptResponse, 1)
	dismissed := false
	m.enqueuePrompt(PromptRequestMsg{
		ApprovalRequestID: "approval-old", Response: responses,
		dismiss: func() { dismissed = true },
	})
	subscription := newTUIProtocolTaskSubscription()
	t.Cleanup(func() { _ = subscription.Close() })
	m.taskStreamSubscriptions["task-old"] = subscription
	m.pendingQueue.enqueue(pendingPromptEnqueueOptions{execLine: "old queued input", deferUntilIdle: true})

	cmd := m.executeLineCmd(Submission{Text: "/new"})
	if cmd == nil {
		t.Fatal("/new did not dispatch")
	}
	completion := cmd()
	applyMessages()
	if service.resetCalls != 1 || service.SessionID() != "" {
		t.Fatal("/new did not reset the Control selection")
	}
	// The command bridge must commit the empty view before command completion;
	// there is no reconnect feed to provide a later history-ready message.
	if m.currentSessionID != "" || m.sessionHistory != nil || m.sessionSwitchPending {
		t.Fatalf("/new left Session=%q historyPending=%v switching=%v", m.currentSessionID, m.sessionHistory != nil, m.sessionSwitchPending)
	}
	m.Update(completion)
	if result, ok := unwrapSessionViewMessage(completion).(TaskResultMsg); !ok || result.Err != nil {
		t.Fatalf("/new completion = %#v", completion)
	}
	if len(m.doc.Blocks()) != 1 || len(m.doc.FindByKind(BlockWelcome)) != 1 {
		t.Fatal("/new did not replace the old document with the launch view")
	}
	if !dismissed || m.activePrompt != nil || len(responses) != 0 || len(m.pendingQueue) != 0 || m.textarea.Value() != "" {
		t.Fatal("/new retained old interaction or answered an approval")
	}
	if subscription.closeCalls.Load() != 1 || len(m.taskStreamSubscriptions) != 0 {
		t.Fatal("/new left old Task subscriptions open")
	}
	select {
	case <-feed.done:
	case <-time.After(time.Second):
		t.Fatal("/new left the old Session observation open")
	}
	m.Update(sessionViewMessage{generation: 1, message: LogChunkMsg{Chunk: "late old output"}})
	frames = append(frames, m.View().Content)
	plain := ansi.Strip(frames[1])
	if !welcomeFrameVisible(plain) || strings.Contains(plain, "previous session transcript") || strings.Contains(plain, "late old output") || strings.Contains(plain, sessionHistoryLoadingHint) {
		t.Fatalf("/new rendered stale Session content:\n%s", plain)
	}
	updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
	assertPhysicalFullscreenFrame(t, m.width, m.height, frames[1], updates)
}

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
