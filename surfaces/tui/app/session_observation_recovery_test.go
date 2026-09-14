package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestSessionObservationRetryClassification(t *testing.T) {
	for _, err := range []error{nil, context.DeadlineExceeded, io.EOF, io.ErrUnexpectedEOF, net.ErrClosed, syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE,
		errorcode.New(errorcode.Unavailable, "new Host not discovered"), &url.Error{Op: "Get", URL: "http://host", Err: syscall.ECONNRESET}} {
		if !retrySessionObservation(err) {
			t.Errorf("did not retry %v", err)
		}
	}
	for _, err := range []error{context.Canceled, appserver.ErrSessionClosed, errors.New("connection reset by peer"),
		errorcode.New(errorcode.Unauthenticated, "bad token"), errorcode.New(errorcode.Unsupported, "protocol"),
		errorcode.New(errorcode.InvalidArgument, "schema"), errorcode.New(errorcode.NotFound, "missing Session"),
		errorcode.Wrap(errorcode.PermissionDenied, "forbidden", syscall.ECONNRESET)} {
		if retrySessionObservation(err) {
			t.Errorf("retried permanent/unclassified error %v", err)
		}
	}
}

func recoveryTestRead(t *testing.T, messages <-chan tea.Msg, m *Model) tea.Msg {
	t.Helper()
	select {
	case msg := <-messages:
		if m != nil {
			m.Update(msg)
			m.drainPendingRenderEvents(time.Now())
			m.flushPendingViewportSync()
		}
		return unwrapSessionViewMessage(msg)
	case <-time.After(sessionObservationRecoveryBudget + 5*time.Second):
		t.Fatal("missing observation message")
		return nil
	}
}

func recoveryTestReady(t *testing.T, messages <-chan tea.Msg, m *Model) {
	t.Helper()
	for {
		if _, ok := recoveryTestRead(t, messages, m).(sessionHistoryReadyMsg); ok {
			return
		}
	}
}

func TestSessionObservationRecoveryIsBoundedAndDoesNotFakeTurnCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		feed := newObservationTestFeed("session")
		feed.state.Run.Active = true
		feed.err = errorcode.New(errorcode.Unavailable, "Host unavailable")
		messages := make(chan tea.Msg, 32)
		attempts := 0
		sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }, resumeSession: func(context.Context, string) (controlprompt.SessionSnapshot, error) {
			attempts++
			return controlprompt.SessionSnapshot{}, errorcode.New(errorcode.Unavailable, "discovery gap")
		}}
		defer sender.Close()
		m := NewModel(Config{NoColor: true, NoAnimation: true})
		observeSelectedSession(t.Context(), sender, feed, false)
		recoveryTestReady(t, messages, m)
		m.textarea.SetValue("draft")
		started := time.Now()
		close(feed.feed)
		for {
			if _, ok := recoveryTestRead(t, messages, m).(sessionObservationErrorMsg); ok {
				break
			}
		}
		synctest.Wait()
		if attempts < 2 || time.Since(started) != sessionObservationRecoveryBudget || !m.turnRunning() || !m.sessionHistoryFailed || m.textarea.Value() != "draft" {
			t.Fatalf("attempts=%d running=%v failed=%v draft=%q", attempts, m.turnRunning(), m.sessionHistoryFailed, m.textarea.Value())
		}
		if !sender.waitForwarders(time.Second) {
			t.Fatal("recovery did not stop")
		}
	})
}

func TestSessionObservationRecoveryStopsAtFiniteOrClosedSession(t *testing.T) {
	for _, closedHistory := range []bool{false, true} {
		t.Run(fmt.Sprint(closedHistory), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				feed := newObservationTestFeed("session")
				restored := newObservationTestFeed("session")
				if closedHistory {
					<-restored.feed
					restored.publish(eventstream.Envelope{SessionID: "session", Kind: eventstream.KindLifecycle, Lifecycle: &eventstream.Lifecycle{State: "closed"}})
					restored.feed <- appserver.FeedDelivery{Kind: appserver.FeedDeliverySync, Source: appserver.FeedSourceExact}
				}
				close(restored.feed)
				messages := make(chan tea.Msg, 32)
				attempts := 0
				sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }, resumeSession: func(context.Context, string) (controlprompt.SessionSnapshot, error) {
					attempts++
					return controlprompt.SessionSnapshot{SessionID: "session", Reconnect: restored}, nil
				}}
				defer sender.Close()
				observeSelectedSession(t.Context(), sender, feed, false)
				recoveryTestReady(t, messages, nil)
				close(feed.feed)
				for {
					if failure, ok := recoveryTestRead(t, messages, nil).(sessionObservationErrorMsg); ok {
						if closedHistory && !errors.Is(failure.err, appserver.ErrSessionClosed) {
							t.Fatalf("closed history error = %v", failure.err)
						}
						break
					}
				}
				synctest.Wait()
				if attempts != 1 {
					t.Fatalf("finite replay resumed %d times", attempts)
				}
			})
		})
	}
}

func TestSessionObservationRecoveryCancellationFencesLateResume(t *testing.T) {
	for _, action := range []string{"switch", "quit", "selection"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				feed := newObservationTestFeed("a")
				late := newObservationTestFeed("a")
				entered, release := make(chan context.Context, 1), make(chan struct{})
				messages := make(chan tea.Msg, 32)
				sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }, resumeSession: func(ctx context.Context, id string) (controlprompt.SessionSnapshot, error) {
					entered <- ctx
					<-release // Deliberately return a late successful response after cancellation.
					return controlprompt.SessionSnapshot{SessionID: id, Reconnect: late}, nil
				}}
				defer sender.Close()
				observeSelectedSession(t.Context(), sender, feed, false)
				recoveryTestReady(t, messages, nil)
				close(feed.feed)
				ctx := <-entered
				switch action {
				case "switch":
					sender.replaceSessionView(t.Context(), "b")
				case "quit":
					sender.Close()
				case "selection":
					sender.cancelSessionRecovery()
				}
				<-ctx.Done()
				close(release)
				synctest.Wait()
				select {
				case <-late.done:
				default:
					t.Fatal("late observation leaked")
				}
				if selected, _ := sender.sessionView(); action == "switch" && selected != "b" {
					t.Fatalf("late result replaced selection with %q", selected)
				}
				for len(messages) > 0 {
					if start, ok := (<-messages).(sessionViewStartMsg); ok && start.recovery {
						t.Fatal("late successful response published history")
					}
				}
			})
		})
	}
}

func TestSessionObservationRecoveryPreservesInteractionAndPhysicalFrames(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {35, 16}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := NewModel(Config{NoColor: true, NoAnimation: true})
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.Update(sessionViewStartMsg{generation: 1, state: appserver.SessionState{SessionID: "session", Run: appserver.RunState{Active: true}}})
			m.Update(sessionHistoryReadyMsg{})
			m.handleUserMessageMsg(UserMessageMsg{Text: "original input"})
			m.textarea.SetValue("preserved draft")
			m.pendingQueue.enqueue(pendingPromptEnqueueOptions{execLine: "queued", deferUntilIdle: true})
			responses := make(chan PromptResponse, 1)
			m.enqueuePrompt(PromptRequestMsg{Prompt: "local prompt", Response: responses})
			prompt := m.activePrompt
			old := m.doc
			m.Update(sessionViewMessage{generation: 1, message: sessionObservationRecoveringMsg{}})
			frames := []string{m.View().Content}
			m.Update(sessionViewStartMsg{generation: 2, state: appserver.SessionState{SessionID: "session"}, recovery: true})
			m.Update(sessionViewMessage{generation: 2, message: TranscriptEventsMsg{ReconnectReplay: true, Events: []TranscriptEvent{
				{Kind: TranscriptEventNarrative, NarrativeKind: TranscriptNarrativeUser, Text: "original input", TurnID: "old"},
				{Kind: TranscriptEventLifecycle, State: "interrupted", TurnID: "old"},
			}}})
			if m.doc != old {
				t.Fatal("partial recovered history became visible")
			}
			m.textarea.SetValue("draft edited during recovery")
			m.Update(sessionViewMessage{generation: 2, message: sessionHistoryReadyMsg{}})
			for _, stale := range []tea.Msg{sessionObservationErrorMsg{err: errors.New("stale")}, sessionHistoryReadyMsg{}, TaskResultMsg{}, LogChunkMsg{Chunk: "stale output"}, sessionApprovalRefreshMsg{}} {
				m.Update(sessionViewMessage{generation: 1, message: stale})
			}
			if m.doc == old || m.turnRunning() || m.sessionObservationRecovering || m.sessionHistoryFailed || m.textarea.Value() != "draft edited during recovery" || len(m.pendingQueue) != 1 || m.activePrompt != prompt || len(responses) != 0 {
				t.Fatal("recovery changed interaction, dispatched input, or kept stale activity")
			}
			frames = append(frames, m.View().Content)
			if strings.Contains(ansi.Strip(frames[1]), "stale output") || strings.Contains(ansi.Strip(frames[1]), sessionObservationRecoveryHint) {
				t.Fatal("stale recovery output remained visible")
			}
			updates := renderFullscreenFramesForTest(t, size[0], size[1], frames...)
			assertPhysicalFullscreenFrame(t, size[0], size[1], frames[len(frames)-1], updates)
		})
	}
}

func TestSessionObservationRecoveryRebindsOnlyCurrentApprovalHead(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.currentSessionID = "session"
	oldResponse, newResponse := make(chan PromptResponse, 1), make(chan PromptResponse, 1)
	dismissed := 0
	m.enqueuePrompt(PromptRequestMsg{ApprovalRequestID: "head", Response: oldResponse, dismiss: func() { dismissed++ }})
	m.activePrompt.input = []rune("edited decision")
	m.enqueuePrompt(PromptRequestMsg{ApprovalRequestID: "settled", Response: oldResponse, dismiss: func() { dismissed++ }})
	m.enqueuePrompt(PromptRequestMsg{Prompt: "local", Response: oldResponse})
	m.sessionObservationRecovering = true
	m.finishPrompt("approve", nil)
	if len(oldResponse) != 0 || m.activePrompt == nil {
		t.Fatal("disconnected approval submitted a decision")
	}
	m.restoreSessionObservationState(appserver.SessionState{SessionID: "session", Approval: appserver.ApprovalState{Active: &appserver.ActiveApproval{RequestID: "head"}}})
	m.finishPrompt("approve", nil)
	if len(oldResponse) != 0 {
		t.Fatal("old approval response channel remained writable")
	}
	m.refreshSessionApproval(PromptRequestMsg{ApprovalRequestID: "head", Response: newResponse})
	if string(m.activePrompt.input) != "edited decision" || len(m.pendingPrompt) != 1 || m.pendingPrompt[0].Prompt != "local" || dismissed != 2 {
		t.Fatal("approval restore lost local interaction or retained an obsolete head")
	}
	m.finishPrompt("approve", nil)
	if len(newResponse) != 1 || len(oldResponse) != 0 {
		t.Fatal("user decision did not target only the new observation")
	}
	local := m.activePrompt
	m.restoreSessionObservationState(appserver.SessionState{SessionID: "session"})
	if m.activePrompt == nil || m.activePrompt != local || len(oldResponse) != 0 {
		t.Fatal("head removal answered or dismissed unrelated local prompt")
	}
}

func TestSessionObservationRecoveryKeepsPartialHistoryPrivate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original := newObservationTestFeed("session")
		partial := newObservationTestFeed("session")
		<-partial.feed
		partial.feed <- appserver.FeedDelivery{Kind: appserver.FeedDeliveryReplaceBegin, Source: appserver.FeedSourceReplacement, SnapshotID: "partial"}
		partial.feed <- appserver.FeedDelivery{Kind: appserver.FeedDeliveryReplacePage, Source: appserver.FeedSourceReplacement, SnapshotID: "partial", Events: []eventstream.Envelope{{
			SessionID: "session", Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateUserMessage, Content: eventstream.TextContent{Type: "text", Text: "partial input"}},
		}}}
		partial.err = errorcode.New(errorcode.Unavailable, "replacement interrupted")
		close(partial.feed)
		restored := newObservationTestFeed("session")
		messages := make(chan tea.Msg, 32)
		attempts := 0
		sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }, resumeSession: func(context.Context, string) (controlprompt.SessionSnapshot, error) {
			attempts++
			if attempts == 1 {
				return controlprompt.SessionSnapshot{Reconnect: partial}, nil
			}
			return controlprompt.SessionSnapshot{Reconnect: restored}, nil
		}}
		defer sender.Close()
		m := NewModel(Config{NoColor: true, NoAnimation: true})
		observeSelectedSession(t.Context(), sender, original, false)
		recoveryTestReady(t, messages, m)
		m.handleUserMessageMsg(UserMessageMsg{Text: "retained document"})
		old := m.doc
		close(original.feed)
		for {
			msg := recoveryTestRead(t, messages, m)
			if _, ok := msg.(sessionHistoryReadyMsg); ok {
				break
			}
			if m.doc != old {
				t.Fatal("interrupted replacement became visible")
			}
		}
		if attempts != 2 || m.viewGeneration != 3 || m.doc == old || m.sessionHistoryFailed || m.sessionSwitchPending {
			t.Fatalf("attempts=%d generation=%d failed=%v switching=%v", attempts, m.viewGeneration, m.sessionHistoryFailed, m.sessionSwitchPending)
		}
	})
}

func TestSessionObservationRecoveryDoesNotRedispatchPendingInputs(t *testing.T) {
	for _, duringRecovery := range []bool{true, false} {
		t.Run(fmt.Sprint(duringRecovery), func(t *testing.T) {
			calls := 0
			m := NewModel(Config{NoColor: true, NoAnimation: true, ExecuteLine: func(Submission) TaskResultMsg { calls++; return TaskResultMsg{} }})
			m.viewGeneration, m.currentSessionID = 1, "session"
			m.beginLiveTurn(SubmissionModeDefault, true, time.Now())
			m.pendingQueue = pendingPromptQueue{
				{localID: 1, execLine: "unknown outcome", state: pendingPromptDispatched},
				{localID: 2, execLine: "accepted input", state: pendingPromptAwaitingActiveDisplay},
				{localID: 3, execLine: "still queued", state: pendingPromptQueued},
				{localID: 4, execLine: "not sent", state: pendingPromptDispatchScheduled},
			}
			submission := Submission{Text: "not sent", localID: 4, Mode: SubmissionModeActiveTurn, viewGeneration: 1}
			turnGeneration := m.liveTurn.generation
			m.registerSubmissionDispatch(submission, turnGeneration)
			m.Update(sessionObservationRecoveringMsg{})
			if !duringRecovery {
				m.Update(sessionViewStartMsg{generation: 2, state: appserver.SessionState{SessionID: "session"}, recovery: true})
				m.Update(sessionHistoryReadyMsg{})
			}
			_, cmd := m.handleSubmissionDispatch(submissionDispatchMsg{submission: submission, turnGeneration: turnGeneration})
			if cmd != nil || calls != 0 || len(m.pendingQueue) != 4 || m.pendingQueue[0].state != pendingPromptDispatched || m.pendingQueue[1].state != pendingPromptAwaitingActiveDisplay || m.pendingQueue[3].state != pendingPromptQueued {
				t.Fatal("recovery reissued input or changed admission certainty")
			}
		})
	}
}

func TestSessionObservationRecoveryPreservesNavigationDuringHistory(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true, ExecuteLine: func(Submission) TaskResultMsg { return TaskResultMsg{} }})
	m.currentSessionID = "a"
	m.Update(sessionViewStartMsg{generation: 1, state: appserver.SessionState{SessionID: "a"}, recovery: true})
	if m.executeLineCmd(Submission{Text: "/resume b"}) == nil {
		t.Fatal("recovery blocked Session selection")
	}
	m.Update(sessionHistoryResetMsg{})
	m.Update(sessionHistoryReadyMsg{})
	if !m.sessionSwitchPending {
		t.Fatal("recovery cleared user navigation")
	}
	m.Update(sessionObservationRecoveringMsg{})
	m.Update(sessionObservationErrorMsg{err: errors.New("disconnected")})
	if !m.sessionSwitchPending {
		t.Fatal("recovery failure cleared user navigation")
	}
}

func TestSessionObservationRecoveryRejectsSuccessAfterRequestDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original, late := newObservationTestFeed("session"), newObservationTestFeed("session")
		messages := make(chan tea.Msg, 32)
		sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }, resumeSession: func(ctx context.Context, id string) (controlprompt.SessionSnapshot, error) {
			<-ctx.Done()
			return controlprompt.SessionSnapshot{SessionID: id, Reconnect: late}, nil
		}}
		defer sender.Close()
		observeSelectedSession(t.Context(), sender, original, false)
		recoveryTestReady(t, messages, nil)
		close(original.feed)
		for {
			msg := recoveryTestRead(t, messages, nil)
			if _, ok := msg.(sessionViewStartMsg); ok {
				t.Fatal("late response published a replacement")
			}
			if failure, ok := msg.(sessionObservationErrorMsg); ok {
				if !errors.Is(failure.err, context.DeadlineExceeded) {
					t.Fatalf("late request error = %v", failure.err)
				}
				break
			}
		}
		synctest.Wait()
		select {
		case <-late.done:
		default:
			t.Fatal("late observation leaked")
		}
	})
}

func TestSessionObservationRecoveryReleasesSelectionLockBeforeHistory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original, partial, selected := newObservationTestFeed("a"), newObservationTestFeed("a"), newObservationTestFeed("b")
		<-partial.feed
		partial.feed <- appserver.FeedDelivery{Kind: appserver.FeedDeliveryReplaceBegin, Source: appserver.FeedSourceReplacement, SnapshotID: "partial"}
		messages := make(chan tea.Msg, 32)
		sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }, resumeSession: func(context.Context, string) (controlprompt.SessionSnapshot, error) {
			return controlprompt.SessionSnapshot{Reconnect: partial}, nil
		}}
		defer sender.Close()
		m := NewModel(Config{NoColor: true, NoAnimation: true})
		observeSelectedSession(t.Context(), sender, original, false)
		recoveryTestReady(t, messages, m)
		close(original.feed)
		for {
			if _, ok := recoveryTestRead(t, messages, m).(sessionHistoryResetMsg); ok {
				break
			}
		}
		sender.sessionCommands.Lock()
		observeSelectedSession(t.Context(), sender, selected, false)
		sender.sessionCommands.Unlock()
		recoveryTestReady(t, messages, m)
		synctest.Wait()
		m.Update(sessionViewMessage{generation: 2, message: sessionHistoryReadyMsg{}})
		m.Update(sessionViewMessage{generation: 2, message: sessionObservationErrorMsg{err: errors.New("late history")}})
		if m.currentSessionID != "b" || m.viewGeneration != 3 || m.sessionHistoryFailed {
			t.Fatal("partial recovered history crossed Session selection")
		}
		select {
		case <-partial.done:
		default:
			t.Fatal("cancelled partial history leaked its subscription")
		}
	})
}

func TestSessionObservationRecoveryBudgetCoversHostSwitchAndResetsAfterStableFollowing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		feed := newObservationTestFeed("session")
		messages := make(chan tea.Msg, 64)
		unavailableUntil := time.Now().Add(25 * time.Second)
		var resumed *observationTestFeed
		sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }, resumeSession: func(context.Context, string) (controlprompt.SessionSnapshot, error) {
			if time.Now().Before(unavailableUntil) {
				return controlprompt.SessionSnapshot{}, errorcode.New(errorcode.Unavailable, "Host stopping/starting")
			}
			resumed = newObservationTestFeed("session")
			return controlprompt.SessionSnapshot{Reconnect: resumed}, nil
		}}
		defer sender.Close()
		m := NewModel(Config{NoColor: true, NoAnimation: true})
		observeSelectedSession(t.Context(), sender, feed, false)
		recoveryTestReady(t, messages, m)
		for episode := range 2 {
			close(feed.feed)
			recoveryTestReady(t, messages, m)
			if time.Now().Before(unavailableUntil) || m.viewGeneration != uint64(episode+2) || m.sessionHistoryFailed {
				t.Fatalf("episode=%d generation=%d failed=%v", episode, m.viewGeneration, m.sessionHistoryFailed)
			}
			if episode == 0 {
				// This is virtual time inside the bubble, not a wall-clock sleep.
				time.Sleep(sessionObservationStableDuration)
				unavailableUntil = time.Now().Add(25 * time.Second)
				feed = resumed
			}
		}
	})
}

func TestSessionObservationRecoveryBackfillTimeoutCannotRenewBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original := newObservationTestFeed("session")
		messages := make(chan tea.Msg, 128)
		var partials []*observationTestFeed
		sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }, resumeSession: func(context.Context, string) (controlprompt.SessionSnapshot, error) {
			partial := newObservationTestFeed("session")
			<-partial.feed // Every new Host stream stalls before sync.
			partial.feed <- appserver.FeedDelivery{Kind: appserver.FeedDeliveryReplaceBegin, Source: appserver.FeedSourceReplacement, SnapshotID: "partial"}
			partials = append(partials, partial)
			return controlprompt.SessionSnapshot{Reconnect: partial}, nil
		}}
		defer sender.Close()
		m := NewModel(Config{NoColor: true, NoAnimation: true})
		observeSelectedSession(t.Context(), sender, original, false)
		recoveryTestReady(t, messages, m)
		old := m.doc
		started := time.Now()
		close(original.feed)
		for {
			if _, ok := recoveryTestRead(t, messages, m).(sessionObservationErrorMsg); ok {
				break
			}
		}
		synctest.Wait()
		if time.Since(started) != sessionObservationRecoveryBudget || len(partials) < 2 || m.doc != old || !m.sessionHistoryFailed {
			t.Fatalf("elapsed=%s partials=%d failed=%v", time.Since(started), len(partials), m.sessionHistoryFailed)
		}
		for _, partial := range partials {
			select {
			case <-partial.done:
			default:
				t.Fatal("timed out history leaked a subscription")
			}
		}
	})
}

func TestSessionObservationRecoveryBudgetIncludesCommandQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		feed := newObservationTestFeed("session")
		messages := make(chan tea.Msg, 32)
		calls := 0
		sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }, resumeSession: func(context.Context, string) (controlprompt.SessionSnapshot, error) {
			calls++
			return controlprompt.SessionSnapshot{}, nil
		}}
		defer sender.Close()
		observeSelectedSession(t.Context(), sender, feed, false)
		recoveryTestReady(t, messages, nil)
		sender.sessionCommands.Lock()
		defer sender.sessionCommands.Unlock()
		started := time.Now()
		close(feed.feed)
		for {
			if _, ok := recoveryTestRead(t, messages, nil).(sessionObservationErrorMsg); ok {
				break
			}
		}
		synctest.Wait()
		if calls != 0 || time.Since(started) != sessionObservationRecoveryBudget {
			t.Fatalf("calls=%d elapsed=%s", calls, time.Since(started))
		}
	})
}

func TestSessionObservationRecoveryLiveReplacementTimeoutIsNotStableFollowing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		original := newObservationTestFeed("session")
		messages := make(chan tea.Msg, 128)
		calls := 0
		sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }, resumeSession: func(context.Context, string) (controlprompt.SessionSnapshot, error) {
			calls++
			partial := newObservationTestFeed("session")
			// Sync succeeds, then an atomic live replacement stalls. Its 5s
			// timeout must not be mistaken for 5s of stable following.
			partial.feed <- appserver.FeedDelivery{Kind: appserver.FeedDeliveryReplaceBegin, Source: appserver.FeedSourceReplacement, SnapshotID: "partial"}
			return controlprompt.SessionSnapshot{Reconnect: partial}, nil
		}}
		defer sender.Close()
		observeSelectedSession(t.Context(), sender, original, false)
		recoveryTestReady(t, messages, nil)
		started := time.Now()
		close(original.feed)
		for {
			if _, ok := recoveryTestRead(t, messages, nil).(sessionObservationErrorMsg); ok {
				break
			}
		}
		synctest.Wait()
		if time.Since(started) != sessionObservationRecoveryBudget || calls < 2 {
			t.Fatalf("elapsed=%s calls=%d", time.Since(started), calls)
		}
	})
}
