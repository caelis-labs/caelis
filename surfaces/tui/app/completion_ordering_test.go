package tuiapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
)

func TestExecuteLineViaDriverForwardsTerminalStreamEvents(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan gateway.EventEnvelope, 1),
	}
	turn.events <- gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "RUN_COMMAND",
				Content:  testTerminalContentWithID("seed\n", "terminal-1"),
				Status:   gateway.ToolStatusRunning,
			},
			Meta: terminalTaskMeta("task-1", "terminal-1", 5),
		},
	}
	close(turn.events)
	terminalEvents := make(chan gateway.EventEnvelope, 1)
	terminalEvents <- terminalStreamEnvelope("call-1", "terminal-1", "streamed\n")
	close(terminalEvents)

	driver := &bridgeSubmitDriver{turn: turn, terminalEvents: terminalEvents}
	var msgs []tea.Msg
	var msgsMu sync.Mutex
	result := executeLineViaControlService(driver, &ProgramSender{Send: func(msg tea.Msg) {
		msgsMu.Lock()
		defer msgsMu.Unlock()
		msgs = append(msgs, msg)
	}}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaControlService() err = %v", result.Err)
	}
	assertNoLocalTurnCompletion(t, result)

	snapshot := snapshotTeaMessages(&msgsMu, &msgs)
	assertTerminalStreamBeforeSenderCompletion(t, snapshot, "streamed\n")
	if driver.terminalSubscribeCalls != 1 {
		t.Fatalf("terminalSubscribeCalls = %d, want 1", driver.terminalSubscribeCalls)
	}
}

func TestControlServiceExecuteLineCmdReturnsNilWhenSenderQueuesCompletion(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan gateway.EventEnvelope, 1),
	}
	turn.events <- gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:  gateway.NarrativeRoleAssistant,
				Text:  "queued through sender",
				Final: true,
				Scope: gateway.EventScopeMain,
			},
		},
	}
	close(turn.events)

	driver := &bridgeSubmitDriver{turn: turn}
	var msgs []tea.Msg
	cfg := ConfigFromControlService(driver, &ProgramSender{Send: func(msg tea.Msg) {
		msgs = append(msgs, msg)
	}}, Config{})
	if cfg.executeLineCmd == nil {
		t.Fatal("ConfigFromControlService() executeLineCmd = nil, want command adapter")
	}
	if msg := cfg.executeLineCmd(Submission{Text: "hello"}); msg != nil {
		t.Fatalf("executeLineCmd() = %#v, want nil after queued completion", msg)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %#v, want streamed event plus queued lifecycle completion", msgs)
	}
	requireTerminalLifecycle(t, msgs[1], eventstream.LifecycleStateCompleted)
}

func TestExecuteLineViaDriverDoesNotWaitForTerminalStreamToClose(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan gateway.EventEnvelope, 1),
	}
	turn.events <- gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "RUN_COMMAND",
				Content:  testTerminalContentWithID("seed\n", "terminal-1"),
				Status:   gateway.ToolStatusRunning,
			},
			Meta: terminalTaskMeta("task-1", "terminal-1", 5),
		},
	}
	close(turn.events)
	terminalEvents := make(chan gateway.EventEnvelope, 1)
	terminalEvents <- terminalStreamEnvelope("call-1", "terminal-1", "streamed\n")

	driver := &bridgeSubmitDriver{turn: turn, terminalEvents: terminalEvents}
	var msgs []tea.Msg
	var msgsMu sync.Mutex
	resultCh := make(chan TaskResultMsg, 1)
	go func() {
		resultCh <- executeLineViaControlService(driver, &ProgramSender{Send: func(msg tea.Msg) {
			msgsMu.Lock()
			defer msgsMu.Unlock()
			msgs = append(msgs, msg)
		}}, Submission{Text: "hello"})
	}()

	waitForTerminalStreamBeforeReturn(t, resultCh, &msgsMu, &msgs, "streamed\n")
	select {
	case result := <-resultCh:
		if result.Err != nil {
			t.Fatalf("executeLineViaControlService() err = %v", result.Err)
		}
		assertNoLocalTurnCompletion(t, result)
	case <-time.After(2 * time.Second):
		t.Fatal("executeLineViaControlService() waited for terminal stream close")
	}
	close(terminalEvents)

	snapshot := snapshotTeaMessages(&msgsMu, &msgs)
	assertTerminalStreamBeforeSenderCompletion(t, snapshot, "streamed\n")
}

func TestExecuteLineViaDriverCancelsTerminalForwarderAfterCompletionDrainTimeout(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan gateway.EventEnvelope, 1),
	}
	turn.events <- gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "RUN_COMMAND",
				Content:  testTerminalContentWithID("seed\n", "terminal-1"),
				Status:   gateway.ToolStatusRunning,
			},
			Meta: terminalTaskMeta("task-1", "terminal-1", 5),
		},
	}
	close(turn.events)
	terminalEvents := make(chan gateway.EventEnvelope, 2)
	terminalEvents <- terminalStreamEnvelope("call-1", "terminal-1", "streamed\n")
	defer close(terminalEvents)

	driver := &bridgeSubmitDriver{turn: turn, terminalEvents: terminalEvents}
	var msgs []tea.Msg
	var msgsMu sync.Mutex
	result := executeLineViaControlService(driver, &ProgramSender{Send: func(msg tea.Msg) {
		msgsMu.Lock()
		defer msgsMu.Unlock()
		msgs = append(msgs, msg)
	}}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaControlService() err = %v", result.Err)
	}
	assertNoLocalTurnCompletion(t, result)

	snapshot := snapshotTeaMessages(&msgsMu, &msgs)
	assertTerminalStreamBeforeSenderCompletion(t, snapshot, "streamed\n")
	terminalEvents <- terminalStreamEnvelope("call-1", "terminal-1", "late\n")
	time.Sleep(3 * eventStreamBatchInterval)
	snapshot = snapshotTeaMessages(&msgsMu, &msgs)
	if containsTerminalStream(snapshot, "late\n") {
		t.Fatalf("messages = %#v, want terminal stream canceled before late chunk", snapshot)
	}
}

func TestExecuteLineViaDriverQueuesErrorCompletion(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan gateway.EventEnvelope, 1),
	}
	turn.events <- gateway.EventEnvelope{Err: gateway.EventError(errors.New("provider failed"))}
	close(turn.events)

	driver := &bridgeSubmitDriver{turn: turn}
	var msgs []tea.Msg
	result := executeLineViaControlService(driver, &ProgramSender{Send: func(msg tea.Msg) {
		msgs = append(msgs, msg)
	}}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaControlService() err = %v", result.Err)
	}
	assertNoLocalTurnCompletion(t, result)
	if len(msgs) != 2 {
		t.Fatalf("messages = %#v, want provider error event plus failure lifecycle", msgs)
	}
	requireTerminalLifecycle(t, msgs[1], eventstream.LifecycleStateFailed)
}

func TestForwardTurnEventStreamCancelQueuesInterruptedCompletion(t *testing.T) {
	events := make(chan gateway.EventEnvelope)
	turn := &bridgeTestTurn{events: events}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var msgs []tea.Msg
	result := forwardTurnEventStream(ctx, nil, turn, &ProgramSender{Send: func(msg tea.Msg) {
		msgs = append(msgs, msg)
	}})
	close(events)

	if !result.queued || result.completion != (TaskResultMsg{}) {
		t.Fatalf("forwardTurnEventStream() result = %#v, want queued command no-op", result)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %#v, want cancelled lifecycle only", msgs)
	}
	requireTerminalLifecycle(t, msgs[0], eventstream.LifecycleStateCancelled)
}

func terminalStreamEnvelope(callID string, terminalID string, text string) gateway.EventEnvelope {
	return gateway.EventEnvelope{
		Event: gateway.Event{
			Kind: gateway.EventKindToolResult,
			ToolResult: &gateway.ToolResultPayload{
				CallID:   callID,
				ToolName: "RUN_COMMAND",
				Content:  testTerminalContentWithID(text, terminalID),
				Status:   gateway.ToolStatusRunning,
			},
		},
	}
}

func terminalTaskMeta(taskID string, terminalID string, cursor int64) map[string]any {
	return map[string]any{
		"caelis": map[string]any{
			"runtime": map[string]any{
				"task": map[string]any{
					"task_id":       taskID,
					"terminal_id":   terminalID,
					"running":       true,
					"state":         "running",
					"output_cursor": cursor,
				},
			},
		},
	}
}

func assertNoLocalTurnCompletion(t *testing.T, result TaskResultMsg) {
	t.Helper()
	if result != (TaskResultMsg{}) {
		t.Fatalf("TaskResultMsg = %#v, want local command no-op after sender-queued completion", result)
	}
}

func snapshotTeaMessages(mu *sync.Mutex, msgs *[]tea.Msg) []tea.Msg {
	mu.Lock()
	defer mu.Unlock()
	return append([]tea.Msg(nil), (*msgs)...)
}

func waitForTerminalStreamBeforeReturn(t *testing.T, resultCh <-chan TaskResultMsg, mu *sync.Mutex, msgs *[]tea.Msg, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		snapshot := snapshotTeaMessages(mu, msgs)
		if containsTerminalStream(snapshot, want) {
			return
		}
		select {
		case result := <-resultCh:
			t.Fatalf("executeLineViaControlService() returned before terminal stream drained: %#v", result)
		case <-deadline:
			t.Fatalf("messages = %#v, want terminal stream before completion", snapshot)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func assertTerminalStreamBeforeSenderCompletion(t *testing.T, msgs []tea.Msg, want string) {
	t.Helper()
	streamIndex := -1
	completionIndex := -1
	for i, msg := range msgs {
		if env, ok := msg.(eventstream.Envelope); ok && acpUpdateTerminalText(env.Update) == want {
			streamIndex = i
		}
		if env, ok := msg.(eventstream.Envelope); ok && eventstream.IsTerminalLifecycle(env) {
			completionIndex = i
			break
		}
	}
	if streamIndex < 0 {
		t.Fatalf("messages = %#v, want forwarded terminal stream event before completion", msgs)
	}
	if completionIndex < 0 {
		t.Fatalf("messages = %#v, want sender terminal lifecycle completion", msgs)
	}
	if completionIndex < streamIndex {
		t.Fatalf("messages = %#v, want terminal stream before sender terminal lifecycle", msgs)
	}
}

func requireTerminalLifecycle(t *testing.T, msg tea.Msg, state string) eventstream.Envelope {
	t.Helper()
	env, ok := msg.(eventstream.Envelope)
	if !ok {
		t.Fatalf("message = %#v, want terminal lifecycle envelope", msg)
	}
	if !eventstream.IsTerminalLifecycle(env) {
		t.Fatalf("envelope = %#v, want terminal lifecycle", env)
	}
	if env.Lifecycle == nil || env.Lifecycle.State != state {
		t.Fatalf("lifecycle = %#v, want state %q", env.Lifecycle, state)
	}
	return env
}

func containsTerminalStream(msgs []tea.Msg, want string) bool {
	for _, msg := range msgs {
		env, ok := msg.(eventstream.Envelope)
		if !ok {
			continue
		}
		if text := acpUpdateTerminalText(env.Update); text == want {
			return true
		}
	}
	return false
}
