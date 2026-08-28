package acpagentbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
)

func TestACPTaskStreamMuxDoesNotTreatChildLifecycleAsObservationControl(t *testing.T) {
	t.Parallel()

	service := &acpMuxTestService{requests: make(chan taskstream.SubscribeRequest, 1)}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()

	mux.Observe(eventstream.Envelope{
		Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
		ScopeID: "task-1", TurnID: "child-turn-2",
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"},
		Lifecycle:  &eventstream.Lifecycle{State: eventstream.LifecycleStateRunning, Reason: "turn_started"},
	})

	select {
	case request := <-service.requests:
		t.Fatalf("child lifecycle restarted Task observation: %#v", request)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestACPTaskStreamMuxFollowsMessageAuthoredSubagentActivityUntilSeal(t *testing.T) {
	t.Parallel()

	subscription := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 8)}
	service := &acpMuxTestService{
		requests: make(chan taskstream.SubscribeRequest, 1),
		sub:      subscription,
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "orbit", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "Spawn"},
		}}},
	}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	mux.Observe(acpMuxSubagentAnchor("orbit"))

	request := receiveACPTaskStreamRequest(t, service.requests)
	if !request.Follow {
		t.Fatalf("subagent Subscribe request = %#v, want Follow across observed activities", request)
	}

	subscription.events <- acpMuxSubagentLifecycleEnvelope("cursor-1", "child-turn-1", eventstream.LifecycleStateRunning)
	subscription.events <- acpMuxSubagentMessageEnvelope("cursor-2", "child-turn-1", "message-1", "first turn")
	subscription.events <- acpMuxSubagentLifecycleEnvelope("cursor-3", "child-turn-1", eventstream.LifecycleStateCompleted)
	for index := 0; index < 3; index++ {
		select {
		case <-mux.Events():
		case <-time.After(time.Second):
			t.Fatalf("first activity envelope %d was not forwarded", index+1)
		}
	}
	select {
	case request := <-service.requests:
		t.Fatalf("child lifecycle restarted observation instead of using Follow: %#v", request)
	case <-time.After(25 * time.Millisecond):
	}

	subscription.events <- acpMuxSubagentLifecycleEnvelope("cursor-4", "child-turn-2", eventstream.LifecycleStateRunning)
	subscription.events <- acpMuxSubagentMessageEnvelope("cursor-5", "child-turn-2", "message-2", "second turn")
	for index := 0; index < 2; index++ {
		select {
		case envelope := <-mux.Events():
			if envelope.TurnID != "child-turn-2" {
				t.Fatalf("second activity envelope = %#v", envelope)
			}
		case <-time.After(time.Second):
			t.Fatalf("later activity envelope %d was not forwarded", index+1)
		}
	}

	// Seal stops discovery but lets the already-running activity reach its typed
	// terminal. Future child activities belong to a later ACP Prompt.
	mux.Seal()
	subscription.events <- acpMuxSubagentLifecycleEnvelope("cursor-6", "child-turn-2", eventstream.LifecycleStateCompleted)
	select {
	case envelope := <-mux.Events():
		if envelope.TurnID != "child-turn-2" || envelope.Lifecycle == nil ||
			envelope.Lifecycle.State != eventstream.LifecycleStateCompleted {
			t.Fatalf("sealed current-activity terminal = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("Seal cut off the current child activity before terminal")
	}
	select {
	case _, open := <-mux.Events():
		if open {
			t.Fatal("sealed mux remained open after the followed activity reached terminal")
		}
	case <-time.After(time.Second):
		t.Fatal("sealed mux did not close at the followed activity boundary")
	}
}

func TestACPTaskStreamMuxResumesActiveStreamFromLastCursorAndFiltersGap(t *testing.T) {
	t.Parallel()

	first := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 2)}
	resumed := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 3)}
	service := newACPMuxReconnectService(
		[]acpMuxSubscribeStep{{sub: first}, {sub: resumed}},
	)
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	mux.Observe(acpMuxCommandAnchor("command"))

	initial := receiveACPTaskStreamRequest(t, service.requests)
	if initial.Cursor != "" || initial.TaskID != "task-1" {
		t.Fatalf("initial Subscribe request = %#v, want cursorless first mount", initial)
	}
	first.events <- acpMuxCommandOutputEnvelope("cursor-1", "before disconnect\n")
	select {
	case envelope := <-mux.Events():
		output, ok := acpmeta.ReadTerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "before disconnect\n" {
			t.Fatalf("initial Task output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("initial Task output was not forwarded")
	}
	first.finish(errorcode.New(errorcode.Unavailable, "active backend disconnected"), "cursor-1")

	select {
	case request := <-service.requests:
		if request.TaskID != "task-1" || request.Cursor != "cursor-1" {
			t.Fatalf("resume Subscribe request = %#v, want LastCursor cursor-1", request)
		}
	case <-time.After(time.Second):
		t.Fatal("active Task stream was not resumed")
	}
	resumed.events <- eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Cursor: "cursor-gap",
		Notice: "transient Task output before this boundary is no longer available",
		Meta:   map[string]any{"task_stream": map[string]any{"transient_gap": true}},
	}
	resumed.events <- acpMuxCommandOutputEnvelope("cursor-2", "after resume\n")
	select {
	case envelope := <-mux.Events():
		output, ok := acpmeta.ReadTerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "after resume\n" {
			t.Fatalf("post-resume envelope = %#v, want typed gap filtered before output", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("resumed Task output was not forwarded")
	}
	if calls := service.listCallCount(); calls != 1 {
		t.Fatalf("Task directory List calls = %d, want identity resolved only before the first subscription", calls)
	}
}

func TestACPTaskStreamMuxResumesAfterUnexpectedEOFFromLastAcceptedCursor(t *testing.T) {
	t.Parallel()

	first := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 2)}
	resumed := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 2)}
	service := newACPMuxReconnectService(
		[]acpMuxSubscribeStep{{sub: first}, {sub: resumed}},
	)
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	mux.Observe(acpMuxCommandAnchor("command"))

	_ = receiveACPTaskStreamRequest(t, service.requests)
	first.events <- acpMuxCommandOutputEnvelope("cursor-accepted", "accepted\n")
	select {
	case envelope := <-mux.Events():
		output, ok := acpmeta.ReadTerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "accepted\n" {
			t.Fatalf("accepted Task output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted Task output was not forwarded")
	}
	// Abrupt transport truncation classified as Unavailable must resume, not stop.
	first.finish(errorcode.New(errorcode.Unavailable, "control http client: Task stream ended without a done event"), "cursor-accepted")

	select {
	case request := <-service.requests:
		if request.TaskID != "task-1" || request.Cursor != "cursor-accepted" {
			t.Fatalf("unexpected-EOF resume request = %#v, want LastCursor cursor-accepted", request)
		}
	case <-time.After(time.Second):
		t.Fatal("active Task stream was not resumed after abrupt transport failure")
	}
	resumed.events <- acpMuxCommandOutputEnvelope("cursor-after", "after abrupt disconnect\n")
	select {
	case envelope := <-mux.Events():
		output, ok := acpmeta.ReadTerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "after abrupt disconnect\n" {
			t.Fatalf("post-abrupt-disconnect output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("resumed Task output after abrupt disconnect was not forwarded")
	}
}

func TestACPTaskStreamMuxResumesAfterSlowConsumerFromLastAcceptedCursor(t *testing.T) {
	t.Parallel()

	first := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 2)}
	resumed := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 2)}
	service := newACPMuxReconnectService(
		[]acpMuxSubscribeStep{{sub: first}, {sub: resumed}},
	)
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	mux.Observe(acpMuxCommandAnchor("command"))

	_ = receiveACPTaskStreamRequest(t, service.requests)
	first.events <- acpMuxCommandOutputEnvelope("cursor-accepted", "accepted\n")
	select {
	case envelope := <-mux.Events():
		output, ok := acpmeta.ReadTerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "accepted\n" {
			t.Fatalf("accepted Task output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted Task output was not forwarded")
	}
	// Slow-consumer failure must reconnect from the last accepted cursor, not stop.
	first.finish(taskstream.ErrSlowConsumer, "cursor-accepted")

	select {
	case request := <-service.requests:
		if request.TaskID != "task-1" || request.Cursor != "cursor-accepted" {
			t.Fatalf("slow-consumer resume request = %#v, want LastCursor cursor-accepted", request)
		}
	case <-time.After(time.Second):
		t.Fatal("active Task stream was not resumed after slow consumer")
	}
	resumed.events <- acpMuxCommandOutputEnvelope("cursor-after", "after slow consumer\n")
	select {
	case envelope := <-mux.Events():
		output, ok := acpmeta.ReadTerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "after slow consumer\n" {
			t.Fatalf("post-slow-consumer output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("resumed Task output after slow consumer was not forwarded")
	}
}

func TestACPTaskStreamMuxGivesEachInterruptionAFreshResumeBudget(t *testing.T) {
	t.Parallel()

	const interruptions = acpTaskStreamResumeMaxAttempts + 1
	subscriptions := make([]*acpMuxTestSubscription, interruptions+1)
	steps := make([]acpMuxSubscribeStep, 0, len(subscriptions))
	for index := range subscriptions {
		subscriptions[index] = &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
		steps = append(steps, acpMuxSubscribeStep{sub: subscriptions[index]})
	}
	service := newACPMuxReconnectService(steps)
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	mux.Observe(acpMuxCommandAnchor("command"))

	initial := receiveACPTaskStreamRequest(t, service.requests)
	if initial.Cursor != "" {
		t.Fatalf("initial Subscribe cursor = %q, want empty", initial.Cursor)
	}
	for index := range interruptions {
		cursor := fmt.Sprintf("cursor-%d", index+1)
		output := fmt.Sprintf("before disconnect %d\n", index+1)
		subscriptions[index].events <- acpMuxCommandOutputEnvelope(cursor, output)
		select {
		case envelope := <-mux.Events():
			terminalOutput, ok := acpmeta.ReadTerminalOutput(eventstream.UpdateMeta(envelope.Update))
			if !ok || terminalOutput.Data != output {
				t.Fatalf("interruption %d output = %#v", index+1, envelope)
			}
		case <-time.After(time.Second):
			t.Fatalf("interruption %d output was not forwarded", index+1)
		}
		subscriptions[index].finish(errorcode.New(errorcode.Unavailable, "active stream disconnected"), cursor)
		request := receiveACPTaskStreamRequest(t, service.requests)
		if request.TaskID != "task-1" || request.Cursor != cursor {
			t.Fatalf("resume request %d = %#v, want Task task-1 cursor %s", index+1, request, cursor)
		}
	}

	subscriptions[interruptions].events <- acpMuxCommandOutputEnvelope("cursor-final", "after reconnects\n")
	select {
	case envelope := <-mux.Events():
		terminalOutput, ok := acpmeta.ReadTerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || terminalOutput.Data != "after reconnects\n" {
			t.Fatalf("final reconnected output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("Task stream stopped after several independently successful reconnects")
	}
	subscriptions[interruptions].finish(nil, "cursor-final")
	select {
	case <-mux.parentBoundary("command-1"):
	case <-time.After(time.Second):
		t.Fatal("normally completed Task stream did not signal its observation boundary")
	}
	select {
	case envelope := <-mux.Events():
		t.Fatalf("successful reconnect windows emitted an interruption notice: %#v", envelope)
	case <-time.After(2 * acpTaskStreamResolveRetryDelay):
	}
}

func TestACPTaskStreamMuxActiveResumeExhaustionReportsSanitizedGap(t *testing.T) {
	t.Parallel()

	const internalTaskID = "95537455f400"
	first := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
	steps := []acpMuxSubscribeStep{{sub: first}}
	for range acpTaskStreamResumeMaxAttempts {
		steps = append(steps, acpMuxSubscribeStep{
			err: errorcode.New(errorcode.Unavailable, `agent-sdk/runtime: task "`+internalTaskID+`" not found during resume`),
		})
	}
	service := newACPMuxReconnectService(steps)
	service.handle = "command-43"
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	mux.Observe(acpMuxCommandAnchor("command-43"))

	_ = receiveACPTaskStreamRequest(t, service.requests)
	first.events <- acpMuxCommandOutputEnvelope("cursor-1", "retained output\n")
	select {
	case <-mux.Events():
	case <-time.After(time.Second):
		t.Fatal("initial retained Task output was not forwarded")
	}
	first.finish(errorcode.New(errorcode.Unavailable, "active stream disconnected"), "cursor-1")

	for attempt := 0; attempt < acpTaskStreamResumeMaxAttempts; attempt++ {
		select {
		case request := <-service.requests:
			if request.Cursor != "cursor-1" {
				t.Fatalf("resume request %d cursor = %q, want retained LastCursor", attempt+1, request.Cursor)
			}
		case <-time.After(time.Second):
			t.Fatalf("resume attempt %d was not made", attempt+1)
		}
	}
	select {
	case envelope := <-mux.Events():
		if envelope.Kind != eventstream.KindNotice ||
			!taskstream.IsTransientGapEnvelope(envelope) ||
			strings.Contains(envelope.Notice, internalTaskID) ||
			strings.Contains(envelope.Notice, "not found") ||
			!strings.Contains(envelope.Notice, "was interrupted") ||
			!strings.Contains(envelope.Notice, "final Task result remains authoritative") {
			t.Fatalf("active resume exhaustion notice = %#v, want sanitized transient gap", envelope)
		}
		meta, _ := envelope.Meta["task_stream"].(map[string]any)
		if meta["transient_gap"] != true ||
			meta["active_stream_interrupted"] != true ||
			meta["resume_exhausted"] != true ||
			meta["resume_cursor_available"] != true {
			t.Fatalf("active resume exhaustion metadata = %#v", meta)
		}
		if _, exists := meta["retry_exhausted"]; exists {
			t.Fatalf("active failure reused resolve retry metadata: %#v", meta)
		}
	case <-time.After(time.Second):
		t.Fatal("active resume exhaustion emitted no gap notice")
	}
}

func TestACPTaskStreamMuxDoesNotReconnectActiveStreamWithoutCursor(t *testing.T) {
	t.Parallel()

	first := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
	service := newACPMuxReconnectService([]acpMuxSubscribeStep{{sub: first}})
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	mux.Observe(acpMuxCommandAnchor("command"))

	_ = receiveACPTaskStreamRequest(t, service.requests)
	first.finish(errorcode.New(errorcode.Unavailable, "active stream disconnected"), "")
	select {
	case envelope := <-mux.Events():
		meta, _ := envelope.Meta["task_stream"].(map[string]any)
		if meta["transient_gap"] != true ||
			meta["resume_cursor_available"] != false ||
			meta["resume_exhausted"] != false ||
			!strings.Contains(envelope.Notice, "before a safe resume cursor was available") {
			t.Fatalf("cursorless active failure = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("cursorless active failure emitted no gap notice")
	}
	select {
	case request := <-service.requests:
		t.Fatalf("active Task stream reconnected without a cursor: %#v", request)
	case <-time.After(2 * acpTaskStreamResolveRetryDelay):
	}
}

func TestACPTaskStreamMuxSealPreservesActiveResumeToTerminal(t *testing.T) {
	t.Parallel()

	first := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
	resumed := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 2)}
	service := newACPMuxReconnectService([]acpMuxSubscribeStep{{sub: first}, {sub: resumed}})
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	mux.Observe(acpMuxCommandAnchor("command"))

	_ = receiveACPTaskStreamRequest(t, service.requests)
	first.events <- acpMuxCommandOutputEnvelope("cursor-before-seal", "before seal\n")
	select {
	case envelope := <-mux.Events():
		output, ok := acpmeta.ReadTerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "before seal\n" {
			t.Fatalf("pre-Seal Task output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("initial Task stream did not attach before Seal")
	}

	mux.Seal()
	first.finish(errorcode.New(errorcode.Unavailable, "active stream disconnected after prompt"), "cursor-before-seal")
	resume := receiveACPTaskStreamRequest(t, service.requests)
	if resume.TaskID != "task-1" || resume.Cursor != "cursor-before-seal" {
		t.Fatalf("post-Seal resume request = %#v, want retained Task and cursor", resume)
	}

	resumed.events <- acpMuxCommandOutputEnvelope("cursor-after-seal", "after seal\n")
	exitCode := 0
	resumed.events <- eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Cursor: "cursor-terminal",
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "command-1",
			Meta:          acpmeta.WithTerminalExit(nil, "command-1", &exitCode, nil),
		},
	}
	select {
	case envelope := <-mux.Events():
		output, ok := acpmeta.ReadTerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "after seal\n" {
			t.Fatalf("post-Seal resumed output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("post-Seal resumed output was not forwarded")
	}
	select {
	case envelope := <-mux.Events():
		exit, ok := acpmeta.ReadTerminalExit(eventstream.UpdateMeta(envelope.Update))
		if !ok || exit.TerminalID != "command-1" || exit.ExitCode == nil || *exit.ExitCode != 0 {
			t.Fatalf("post-Seal terminal delivery = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("post-Seal terminal delivery was not forwarded")
	}

	resumed.finish(nil, "cursor-terminal")
	select {
	case <-mux.parentBoundary("command-1"):
	case <-time.After(time.Second):
		t.Fatal("post-Seal completed stream did not signal its observation boundary")
	}
	select {
	case _, ok := <-mux.Events():
		if ok {
			t.Fatal("sealed mux emitted an unexpected event after Task stream completion")
		}
	case <-time.After(time.Second):
		t.Fatal("sealed mux did not close after its active Task stream completed")
	}
	if calls := service.listCallCount(); calls != 1 {
		t.Fatalf("Task directory List calls = %d, want no discovery during active resume", calls)
	}
}

func TestRetryACPTaskStreamChecksBoundaryBeforeFirstAttempt(t *testing.T) {
	t.Parallel()

	calls := 0
	_, attempts, err := retryACPTaskStream(
		context.Background(),
		acpTaskStreamResumeMaxAttempts,
		func() bool { return false },
		func() (taskstream.SubscribeResult, error) {
			calls++
			return taskstream.SubscribeResult{}, nil
		},
	)
	if !errors.Is(err, context.Canceled) || attempts != 0 || calls != 0 {
		t.Fatalf("stopped retry = attempts %d calls %d error %v, want no first operation", attempts, calls, err)
	}
}

func TestACPTaskStreamMuxParentTerminalCancelsResolvingSubscription(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	release := make(chan struct{})
	service := newACPMuxReconnectService([]acpMuxSubscribeStep{{
		sub:       &acpMuxTestSubscription{events: make(chan eventstream.Envelope)},
		started:   started,
		release:   release,
		cancelled: cancelled,
	}})
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()

	mux.Observe(acpMuxCommandAnchor("command"))
	_ = receiveACPTaskStreamRequest(t, service.requests)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("resolving Subscribe did not reach its in-flight boundary")
	}

	mux.Observe(acpMuxTerminalCommandAnchor("command"))
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("parent terminal did not cancel the in-flight resolving Subscribe")
	}
	requireACPMuxObservationSettled(t, mux, "command-1", acpTaskStreamObservationClosed)
}

func TestACPTaskStreamMuxParentTerminalCancelsAttachedSubscription(t *testing.T) {
	t.Parallel()

	cancelled := make(chan struct{}, 1)
	sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
	service := newACPMuxReconnectService([]acpMuxSubscribeStep{{
		sub:           sub,
		cancelled:     cancelled,
		closeOnCancel: true,
	}})
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()

	mux.Observe(acpMuxCommandAnchor("command"))
	_ = receiveACPTaskStreamRequest(t, service.requests)
	sub.events <- acpMuxCommandOutputEnvelope("cursor-attached", "attached\n")
	select {
	case <-mux.Events():
	case <-time.After(time.Second):
		t.Fatal("Task stream did not become attached")
	}

	mux.Observe(acpMuxTerminalCommandAnchor("command"))
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("parent terminal did not cancel the attached subscription")
	}
	requireACPMuxObservationSettled(t, mux, "command-1", acpTaskStreamObservationClosed)
}

func TestACPTaskStreamMuxParentTerminalCancelsInFlightResume(t *testing.T) {
	t.Parallel()

	first := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	release := make(chan struct{})
	service := newACPMuxReconnectService([]acpMuxSubscribeStep{
		{sub: first},
		{
			sub:       &acpMuxTestSubscription{events: make(chan eventstream.Envelope)},
			started:   started,
			release:   release,
			cancelled: cancelled,
		},
	})
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()

	mux.Observe(acpMuxCommandAnchor("command"))
	_ = receiveACPTaskStreamRequest(t, service.requests)
	first.events <- acpMuxCommandOutputEnvelope("cursor-before-resume", "before resume\n")
	select {
	case <-mux.Events():
	case <-time.After(time.Second):
		t.Fatal("initial Task stream output was not forwarded")
	}
	first.finish(errorcode.New(errorcode.Unavailable, "active stream disconnected"), "cursor-before-resume")
	resume := receiveACPTaskStreamRequest(t, service.requests)
	if resume.Cursor != "cursor-before-resume" {
		t.Fatalf("resume cursor = %q, want retained cursor", resume.Cursor)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("resume Subscribe did not reach its in-flight boundary")
	}

	mux.Observe(acpMuxTerminalCommandAnchor("command"))
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("parent terminal did not cancel the in-flight resume Subscribe")
	}
	requireACPMuxObservationSettled(t, mux, "command-1", acpTaskStreamObservationClosed)
	if calls := service.subscribeCallCount(); calls != 2 {
		t.Fatalf("Subscribe calls = %d, want no retry after parent terminal", calls)
	}
}

func TestACPTaskStreamMuxParentTerminalCancelsBackpressuredForward(t *testing.T) {
	t.Parallel()

	cancelled := make(chan struct{}, 1)
	sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
	service := newACPMuxReconnectService([]acpMuxSubscribeStep{{
		sub:           sub,
		cancelled:     cancelled,
		closeOnCancel: true,
	}})
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()

	mux.Observe(acpMuxCommandAnchor("command"))
	_ = receiveACPTaskStreamRequest(t, service.requests)
	for range cap(mux.events) {
		mux.events <- eventstream.Envelope{Kind: eventstream.KindNotice}
	}
	sub.events <- acpMuxCommandOutputEnvelope("cursor-backpressured", "blocked output\n")
	deadline := time.Now().Add(time.Second)
	for len(sub.events) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(sub.events) != 0 {
		t.Fatal("Task stream forward did not reach the full mux event buffer")
	}

	mux.Observe(acpMuxTerminalCommandAnchor("command"))
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("parent terminal did not cancel the backpressured attached subscription")
	}
	requireACPMuxObservationSettled(t, mux, "command-1", acpTaskStreamObservationClosed)
}

func TestACPTaskStreamMuxParentTerminalCancelsBackpressuredNotice(t *testing.T) {
	t.Parallel()

	service := &acpMuxTestService{
		listErr: errorcode.New(errorcode.PermissionDenied, "Task stream access denied"),
	}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	for range cap(mux.events) {
		mux.events <- eventstream.Envelope{Kind: eventstream.KindNotice}
	}

	mux.Observe(acpMuxCommandAnchor("command"))
	deadline := time.Now().Add(time.Second)
	for {
		mux.mu.Lock()
		observation := mux.observations["command-1"]
		blocked := observation != nil &&
			observation.phase == acpTaskStreamObservationResolving &&
			observation.notices&acpTaskStreamNoticeAttachFailed != 0
		mux.mu.Unlock()
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Task stream resolve failure did not reach the full mux event buffer")
		}
		time.Sleep(time.Millisecond)
	}

	mux.Observe(acpMuxTerminalCommandAnchor("command"))
	requireACPMuxObservationSettled(t, mux, "command-1", acpTaskStreamObservationClosed)
}

func TestACPTaskStreamMuxSealKeepsInFlightResumeAlive(t *testing.T) {
	t.Parallel()

	first := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
	resumed := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	release := make(chan struct{})
	service := newACPMuxReconnectService([]acpMuxSubscribeStep{
		{sub: first},
		{sub: resumed, started: started, release: release, cancelled: cancelled},
	})
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()

	mux.Observe(acpMuxCommandAnchor("command"))
	_ = receiveACPTaskStreamRequest(t, service.requests)
	first.events <- acpMuxCommandOutputEnvelope("cursor-before-seal", "before seal\n")
	select {
	case <-mux.Events():
	case <-time.After(time.Second):
		t.Fatal("initial Task stream output was not forwarded")
	}
	first.finish(errorcode.New(errorcode.Unavailable, "active stream disconnected"), "cursor-before-seal")
	_ = receiveACPTaskStreamRequest(t, service.requests)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("resume Subscribe did not reach its in-flight boundary")
	}

	mux.Seal()
	select {
	case <-cancelled:
		t.Fatal("Seal cancelled an in-flight active resume")
	case <-time.After(2 * acpTaskStreamResolveRetryDelay):
	}
	close(release)
	resumed.events <- acpMuxCommandOutputEnvelope("cursor-after-seal", "after seal\n")
	select {
	case envelope := <-mux.Events():
		output, ok := acpmeta.ReadTerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "after seal\n" {
			t.Fatalf("resumed output after Seal = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("Seal prevented the in-flight resume from attaching")
	}
	resumed.finish(nil, "cursor-after-seal")
	select {
	case _, open := <-mux.Events():
		if open {
			t.Fatal("sealed mux emitted an unexpected event after resumed stream completion")
		}
	case <-time.After(time.Second):
		t.Fatal("sealed mux did not close after resumed stream completion")
	}
}

func requireACPMuxObservationSettled(
	t *testing.T,
	mux *acpTaskStreamMux,
	callID string,
	wantPhase acpTaskStreamObservationPhase,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		mux.mu.Lock()
		observation := mux.observations[callID]
		active := mux.active
		var phase acpTaskStreamObservationPhase
		var generation *acpTaskStreamObservationGeneration
		if observation != nil {
			phase = observation.phase
			generation = observation.generation
		}
		mux.mu.Unlock()
		generationStopped := generation == nil || generation.ctx == nil || generation.ctx.Err() != nil
		if observation != nil && phase == wantPhase && generationStopped && active == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"observation = phase %v generation %p stopped %t active %d, want phase %v settled",
				phase, generation, generationStopped, active, wantPhase,
			)
		}
		time.Sleep(time.Millisecond)
	}
}
