package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func unwrapSessionViewMessage(msg tea.Msg) tea.Msg {
	if scoped, ok := msg.(sessionViewMessage); ok {
		return scoped.message
	}
	return msg
}

type observationTestFeed struct {
	tuiReconnect
	feed     chan appserver.FeedDelivery
	done     chan struct{}
	once     sync.Once
	sequence uint64
}

func newObservationTestFeed(id string) *observationTestFeed {
	r := &observationTestFeed{
		tuiReconnect: tuiReconnect{state: appserver.SessionState{SessionID: id}},
		feed:         make(chan appserver.FeedDelivery, 16), done: make(chan struct{}),
	}
	r.feed <- appserver.FeedDelivery{Kind: appserver.FeedDeliverySync, Source: appserver.FeedSourceExact}
	return r
}

func (r *observationTestFeed) Deliveries() <-chan appserver.FeedDelivery { return r.feed }
func (r *observationTestFeed) Close() error {
	r.once.Do(func() { close(r.done) })
	return nil
}

func (r *observationTestFeed) publish(env eventstream.Envelope) {
	r.sequence++
	env.Cursor = fmt.Sprintf("observation-%d", r.sequence)
	env.Position = &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{Generation: "observation", Sequence: r.sequence}}
	r.feed <- appserver.FeedDelivery{Kind: appserver.FeedDeliveryAppendPage, Source: appserver.FeedSourceExact, Events: []eventstream.Envelope{env}, NextCursor: env.Cursor}
}

func TestSessionObservationContinuesAcrossTurnsAndIdleNotices(t *testing.T) {
	feed := newObservationTestFeed("session-a")
	messages := make(chan tea.Msg, 32)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	t.Cleanup(sender.Close)
	if result := observeSelectedSession(context.Background(), sender, feed, false); !result.queued {
		t.Fatal("observation did not attach")
	}
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	for range 2 {
		m.Update(<-messages)
	}
	for turn := range 2 {
		id := []string{"turn-a", "turn-b"}[turn]
		for _, state := range []string{eventstream.LifecycleStateRunning, eventstream.LifecycleStateCompleted} {
			env := eventstream.Envelope{
				Kind: eventstream.KindLifecycle, SessionID: "session-a", Scope: eventstream.ScopeMain,
				HandleID: "handle", RunID: "run-" + id, TurnID: id,
				Lifecycle: &eventstream.Lifecycle{State: state}, OccurredAt: time.Now(),
			}
			feed.publish(env)
			select {
			case msg := <-messages:
				m.Update(msg)
				m.drainPendingRenderEvents(time.Now())
			case <-time.After(time.Second):
				t.Fatal("observer stopped at the previous Turn terminal")
			}
			if m.turnRunning() != (state == eventstream.LifecycleStateRunning) {
				t.Fatalf("Turn %s state %s: running=%v", id, state, m.turnRunning())
			}
		}
		feed.publish(eventstream.Envelope{Kind: eventstream.KindNotice, SessionID: "session-a", Notice: "idle notice"})
		select {
		case msg := <-messages:
			if env, ok := unwrapSessionViewMessage(msg).(eventstream.Envelope); !ok || env.Notice != "idle notice" {
				t.Fatalf("idle notice = %#v", msg)
			}
		case <-time.After(time.Second):
			t.Fatal("idle observer did not receive Session notice")
		}
	}
	select {
	case <-feed.done:
		t.Fatal("Turn terminal closed Session observation")
	default:
	}
	sender.Close()
	select {
	case <-feed.done:
	case <-time.After(time.Second):
		t.Fatal("surface close did not release observation")
	}
}

func TestSessionSwitchRejectsQueuedOldOutputAndPreservesDrafts(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.Update(sessionViewStartMsg{generation: 1, state: appserver.SessionState{SessionID: "a", Run: appserver.RunState{Active: true}}})
	m.Update(sessionHistoryReadyMsg{})
	m.textarea.SetValue("A draft")
	m.syncInputFromTextarea()
	responses := make(chan PromptResponse, 1)
	dismissed := false
	m.enqueuePrompt(PromptRequestMsg{ApprovalRequestID: "approval-a", Response: responses, dismiss: func() { dismissed = true }})
	m.Update(sessionViewStartMsg{generation: 2, state: appserver.SessionState{SessionID: "b", Run: appserver.RunState{Active: true}}})
	m.Update(sessionHistoryReadyMsg{})
	if !dismissed || len(responses) != 0 || m.textarea.Value() != "" {
		t.Fatal("switch must dismiss without answering and isolate the composer")
	}
	m.textarea.SetValue("B draft")
	for _, old := range []tea.Msg{
		TaskResultMsg{}, LogChunkMsg{Chunk: "old output"},
		PromptRequestMsg{ApprovalRequestID: "approval-a", Response: responses},
		SetStatusMsg{Workspace: "old workspace"},
		eventstream.TurnCompleted("h", "r", "t", time.Now()),
	} {
		m.Update(sessionViewMessage{generation: 1, message: old})
	}
	if !m.turnRunning() || m.activePrompt != nil || m.currentSessionID != "b" || m.doc.Len() != 0 {
		t.Fatal("queued old Session messages crossed the view boundary")
	}
	m.Update(sessionViewStartMsg{generation: 3, state: appserver.SessionState{SessionID: "a"}})
	m.Update(sessionHistoryReadyMsg{})
	if m.textarea.Value() != "A draft" {
		t.Fatalf("A draft = %q", m.textarea.Value())
	}
	m.Update(sessionViewStartMsg{generation: 4, state: appserver.SessionState{SessionID: "b"}})
	m.Update(sessionHistoryReadyMsg{})
	if m.textarea.Value() != "B draft" {
		t.Fatalf("B draft = %q", m.textarea.Value())
	}
}

func TestPendingSessionSwitchKeepsNewInputAsDraftAndFailureKeepsQueue(t *testing.T) {
	m := NewModel(Config{NoColor: true, ExecuteLine: func(Submission) TaskResultMsg {
		return TaskResultMsg{Err: fmt.Errorf("Session unavailable")}
	}})
	m.currentSessionID = "a"
	m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	m.pendingQueue.enqueue(pendingPromptEnqueueOptions{execLine: "queued input", deferUntilIdle: true})
	cmd := m.executeLineCmd(Submission{Text: "/resume b"})
	m.textarea.SetValue("draft for a")
	m.syncInputFromTextarea()
	m.submitLine("draft for a")
	if m.textarea.Value() != "draft for a" || len(m.pendingQueue) != 1 {
		t.Fatal("pending switch consumed another Session's input")
	}
	m.Update(cmd())
	if m.sessionSwitchPending || m.currentSessionID != "a" || !m.turnRunning() || len(m.pendingQueue) != 1 {
		t.Fatal("failed switch changed the observed Session or discarded its pending input")
	}
}

func TestSessionObservationOwnsRunningStateAfterConcurrentSubmissionFailure(t *testing.T) {
	for _, observed := range []string{"live", "reconnect", "none"} {
		t.Run(observed, func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			state := appserver.SessionState{SessionID: "session"}
			state.Run.Active = observed == "reconnect"
			m.Update(sessionViewStartMsg{generation: 1, state: state})
			m.Update(sessionHistoryReadyMsg{})
			if observed != "reconnect" {
				// Local input starts a provisional spinner before admission. A
				// different observer may win admission during that request.
				m.beginLiveTurn(SubmissionModeDefault, true, time.Now())
			}
			if observed == "live" {
				m.handleACPEventEnvelope(eventstream.Envelope{
					Kind: eventstream.KindLifecycle, SessionID: "session", TurnID: "other-turn",
					Scope: eventstream.ScopeMain, Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateRunning},
				})
			}
			m.handleTaskResultMsg(TaskResultMsg{Err: fmt.Errorf("Session already running")})
			if m.turnRunning() != (observed != "none") {
				t.Fatal("local admission failure changed the Host-observed Turn state")
			}
			if observed != "none" {
				m.handleACPEventEnvelope(eventstream.TurnCompleted("host", "run", "other-turn", time.Now()))
				if m.turnRunning() {
					t.Fatal("Host terminal event did not finish the observed Turn")
				}
			}
		})
	}
}

func TestFirstPromptAttachPreservesDraftAndPendingNavigation(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	m.sessionSwitchPending = true
	m.textarea.SetValue("draft typed while first prompt was admitted")
	m.Update(sessionViewStartMsg{generation: 1, state: appserver.SessionState{SessionID: "a"}, automatic: true})
	m.Update(sessionHistoryReadyMsg{})
	if !m.sessionSwitchPending || m.textarea.Value() == "" {
		t.Fatal("automatic attach lost the draft or pending user navigation")
	}
	m.Update(sessionViewStartMsg{generation: 2, state: appserver.SessionState{SessionID: "b"}})
	m.Update(sessionHistoryReadyMsg{})
	if m.sessionSwitchPending || m.textarea.Value() != "" {
		t.Fatal("explicit navigation did not finish on a separate composer")
	}
	m.Update(sessionViewStartMsg{generation: 3, state: appserver.SessionState{SessionID: "a"}})
	m.Update(sessionHistoryReadyMsg{})
	if m.textarea.Value() != "draft typed while first prompt was admitted" {
		t.Fatal("first Session draft was lost")
	}
}

func TestQueuedNavigationSurvivesAutomaticFirstPromptAttach(t *testing.T) {
	feed := newObservationTestFeed("session")
	service := &observingControlService{feed: feed}
	sender := &ProgramSender{Send: func(tea.Msg) {}}
	defer sender.Close()
	cfg := ConfigFromControlService(service, sender, Config{PromptRouterFactory: controlprompt.New})
	// The user's navigation was queued while first-prompt admission still had
	// no Session identity. Its automatic attachment must not invalidate the
	// already requested navigation when the command acquires the serial queue.
	sender.replaceSessionView(context.Background(), "first-session")
	cfg.executeLineCmd(Submission{Text: "/resume session", viewGeneration: 0})
	if service.resumes.Load() != 1 {
		t.Fatal("first-prompt attachment discarded queued user navigation")
	}
	if selected, _ := sender.sessionView(); selected != "session" {
		t.Fatalf("selected=%q", selected)
	}
}

func TestQueuedNavigationFailureClearsPendingAfterFirstPromptAttach(t *testing.T) {
	sender := &ProgramSender{}
	defer sender.Close()
	m := NewModel(Config{NoColor: true, ProgramSender: sender, ExecuteLine: func(Submission) TaskResultMsg {
		return TaskResultMsg{Err: fmt.Errorf("Session unavailable")}
	}})
	cmd := m.executeLineCmd(Submission{Text: "/resume missing"})
	sender.replaceSessionView(context.Background(), "a")
	m.Update(sessionViewStartMsg{generation: 1, state: appserver.SessionState{SessionID: "a", Run: appserver.RunState{Active: true}}, automatic: true})
	m.Update(sessionHistoryReadyMsg{})
	m.Update(cmd())
	if m.sessionSwitchPending || m.currentSessionID != "a" || !m.turnRunning() {
		t.Fatal("failed queued navigation left the composer blocked after automatic attachment")
	}
}

func TestApprovalSettlementDismissesOnlyMatchingPromptsWithoutDecision(t *testing.T) {
	m := NewModel(Config{NoColor: true})
	responses := make(chan PromptResponse, 3)
	var dismissed []string
	for _, id := range []string{"a", "b", "c"} {
		m.enqueuePrompt(PromptRequestMsg{ApprovalRequestID: id, Response: responses, dismiss: func() { dismissed = append(dismissed, id) }})
	}
	m.handleACPEventEnvelope(eventstream.Envelope{Kind: eventstream.KindLifecycle, ApprovalRequestID: "b", Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted}})
	if m.activePrompt.approvalRequestID != "a" || len(m.pendingPrompt) != 1 || m.pendingPrompt[0].ApprovalRequestID != "c" {
		t.Fatal("settlement did not remove only the matching queued approval")
	}
	m.handleACPEventEnvelope(eventstream.Envelope{Kind: eventstream.KindLifecycle, ApprovalRequestID: "a", Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted}})
	if m.activePrompt.approvalRequestID != "c" || len(responses) != 0 || len(dismissed) != 2 {
		t.Fatal("settlement must close the matching modal and advance without submitting a decision")
	}
	duplicateDismissed := false
	m.enqueuePrompt(PromptRequestMsg{ApprovalRequestID: "c", Response: responses, dismiss: func() { duplicateDismissed = true }})
	if !duplicateDismissed || len(m.pendingPrompt) != 0 {
		t.Fatal("bootstrap/live duplicate approval was queued twice")
	}
}

func TestQuitDetachesWhileEscExplicitlyInterrupts(t *testing.T) {
	for _, action := range []string{"/quit", "/exit", "ctrl+d", "ctrl+c"} {
		t.Run(action, func(t *testing.T) {
			interrupts := 0
			m := NewModel(Config{NoColor: true, NoAnimation: true, CancelRunning: func() bool { interrupts++; return true }})
			m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
			var cmd tea.Cmd
			if action[0] == '/' {
				_, cmd = m.submitLine(action)
			} else {
				if action == "ctrl+c" {
					m.requestSurfaceQuit()
				}
				key := tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
				if action == "ctrl+c" {
					key.Code = 'c'
				}
				_, cmd = m.Update(key)
			}
			if !m.quit || cmd == nil || interrupts != 0 || !m.turnRunning() {
				t.Fatalf("quit=%v cmd=%v interrupts=%d running=%v", m.quit, cmd != nil, interrupts, m.turnRunning())
			}
		})
	}
}

type observingControlService struct {
	interruptBridgeStub
	feed     *observationTestFeed
	selected atomic.Bool
	submits  atomic.Int32
	resumes  atomic.Int32
}

func (s *observingControlService) SessionID() string {
	if s.selected.Load() {
		return s.feed.state.SessionID
	}
	return ""
}
func (s *observingControlService) Submit(context.Context, controlprompt.Submission) (controlprompt.Turn, error) {
	s.submits.Add(1)
	s.selected.Store(true)
	return &surfaceDetachTurn{events: make(chan eventstream.Envelope)}, nil
}
func (s *observingControlService) ResumeSession(_ context.Context, id string) (controlprompt.SessionSnapshot, error) {
	s.resumes.Add(1)
	s.selected.Store(true)
	return controlprompt.SessionSnapshot{SessionID: id, Reconnect: s.feed}, nil
}

func TestTUIInitialAttachAndFirstPromptUsePersistentSessionObservation(t *testing.T) {
	for _, initialAttach := range []bool{true, false} {
		t.Run(fmt.Sprintf("initial_attach_%v", initialAttach), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			feed := newObservationTestFeed("host-session")
			service := &observingControlService{feed: feed}
			sender := &ProgramSender{}
			defer sender.Close()
			cfg := Config{Context: ctx, NoColor: true, NoAnimation: true, PromptRouterFactory: controlprompt.New}
			if initialAttach {
				cfg.InitialSessionID = "host-session"
			}
			cfg = ConfigFromControlService(service, sender, cfg)
			ready := make(chan struct{}, 1)
			program := tea.NewProgram(NewModel(cfg), tea.WithInput(nil), tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithContext(ctx),
				tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
					if _, ok := unwrapSessionViewMessage(msg).(sessionHistoryReadyMsg); ok {
						ready <- struct{}{}
					}
					return msg
				}))
			sender.Send = program.Send
			type result struct {
				model tea.Model
				err   error
			}
			finished := make(chan result, 1)
			go func() { m, err := program.Run(); finished <- result{m, err} }()
			if !initialAttach {
				program.Send(tea.KeyPressMsg{Code: 'h', Text: "hello"})
				program.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal("TUI did not attach through its actual startup/submission entry point")
			}
			program.Send(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
			got := <-finished
			if got.err != nil {
				t.Fatal(got.err)
			}
			if got.model.(*Model).currentSessionID != "host-session" || service.resumes.Load() != 1 {
				t.Fatal("selected Session was not installed exactly once")
			}
			wantSubmits := int32(1)
			if initialAttach {
				wantSubmits = 0
			}
			if service.submits.Load() != wantSubmits {
				t.Fatalf("submits=%d, want %d", service.submits.Load(), wantSubmits)
			}
			sender.Close()
			select {
			case <-feed.done:
			case <-time.After(time.Second):
				t.Fatal("terminal exit did not detach its Session observation")
			}
		})
	}
}

func TestSessionPickerApprovalSettlementPhysicalFrames(t *testing.T) {
	for _, width := range []int{80, 120} {
		m := newSessionPickerTestModel(t, width, 24, []ResumeCandidate{
			{SessionID: "session-current", Title: "优化审批与沙箱路由", Running: true},
			{SessionID: "session-other", Title: "评估 TUI 交互"},
		}, nil)
		m.currentSessionID = "session-current"
		m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
		responses := make(chan PromptResponse, 1)
		m.enqueuePrompt(PromptRequestMsg{ApprovalRequestID: "approval", Title: "Approval required", Response: responses, Choices: []PromptChoice{{Label: "Allow once", Value: "allow"}}})
		frames := []string{m.View().Content}
		openSessionPickerForTest(t, m)
		frames = append(frames, m.View().Content)
		plain := ansi.Strip(frames[len(frames)-1])
		if !strings.Contains(plain, "ID session-current") {
			t.Fatal("selected Session address is unavailable for another terminal to attach")
		}
		if strings.Contains(plain, "Approval required") || strings.Contains(plain, "Allow once") {
			t.Fatal("underlying approval dialog leaked through the standalone Session list")
		}
		oneRow := false
		for _, line := range strings.Split(plain, "\n") {
			if strings.Contains(line, "优化审批") && strings.Contains(line, "running · current") {
				oneRow = true
			}
		}
		if !oneRow {
			t.Fatal("selected running Session wrapped and displaced mouse hit rows")
		}
		if m.View().Cursor != nil {
			t.Fatal("Session list left an underlying composer cursor visible")
		}
		m.handleACPEventEnvelope(eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: m.currentSessionID, ApprovalRequestID: "approval", Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted}})
		frames = append(frames, m.View().Content)
		m.Update(keyPress("esc"))
		frames = append(frames, m.View().Content)
		updates := renderFullscreenFramesForTest(t, width, 24, frames...)
		assertPhysicalFullscreenFrame(t, width, 24, frames[len(frames)-1], updates)
		if m.activePrompt != nil || !m.turnRunning() || len(responses) != 0 || strings.Contains(ansi.Strip(frames[len(frames)-1]), "Approval required") {
			t.Fatal("completed approval was restored after closing the Session list")
		}
		t.Logf("Session overlay %dx24:\n%s", width, ansi.Strip(frames[1]))
	}
}
