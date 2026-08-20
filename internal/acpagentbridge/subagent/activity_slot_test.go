package subagent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

func TestChildActivityObserverSwapReplaysUnacknowledgedJournalExactlyOnce(t *testing.T) {
	t.Parallel()

	run := &childRun{taskID: "task-swap"}
	slot := newChildSlot(agent.ChildEndpointRef{EndpointKey: run.taskID}, run)
	slot.beginActivity("activity-swap", run)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	slot.bindObserver(0, childActivityObserverFunc(func(context.Context, agent.ChildActivityEvent) error {
		once.Do(func() { close(entered) })
		<-release
		return errors.New("observer retired before durable ack")
	}))
	slot.publishFrame(stream.Frame{Text: "first"})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old observer did not receive journal item")
	}

	got := make(chan agent.ChildActivityEvent, 4)
	bound := make(chan struct{})
	go func() {
		slot.opMu.Lock()
		slot.bindObserver(0, childActivityObserverFunc(func(_ context.Context, event agent.ChildActivityEvent) error {
			got <- event
			return nil
		}))
		slot.opMu.Unlock()
		close(bound)
	}()
	slot.publishFrame(stream.Frame{Text: "second"})
	close(release)
	select {
	case <-bound:
	case <-time.After(time.Second):
		t.Fatal("observer swap did not finish")
	}

	seen := map[uint64]int{}
	deadline := time.After(time.Second)
	for len(seen) < 2 {
		select {
		case event := <-got:
			seen[event.Cursor]++
		case <-deadline:
			t.Fatalf("replayed cursors = %#v, want 1 and 2", seen)
		}
	}
	if seen[1] != 1 || seen[2] != 1 {
		t.Fatalf("replayed cursors = %#v, want exactly once", seen)
	}
}

func TestChildActivityJournalOverflowRetainsGapTerminal(t *testing.T) {
	t.Parallel()

	run := &childRun{
		taskID: "task-overflow", state: delegation.StateRunning, running: true,
		done: make(chan struct{}),
	}
	slot := newChildSlot(agent.ChildEndpointRef{
		ParticipantID: "child-overflow", SessionID: "child-session", EndpointKey: run.taskID,
		Role: session.ParticipantRoleDelegated,
	}, run)
	slot.beginActivity("activity-overflow", run)
	slot.bindObserver(0, childActivityObserverFunc(func(context.Context, agent.ChildActivityEvent) error {
		return errors.New("observer unavailable")
	}))
	for i := 0; i < childActivityJournalMaxEvents+32; i++ {
		slot.publishFrame(stream.Frame{Text: "output"})
	}

	replayed := make(chan agent.ChildActivityEvent, 1)
	slot.bindObserver(0, childActivityObserverFunc(func(_ context.Context, event agent.ChildActivityEvent) error {
		replayed <- event
		return nil
	}))
	select {
	case event := <-replayed:
		if !event.Gap || event.Result == nil || event.Result.State != delegation.StateUnknownOutcome {
			t.Fatalf("overflow replay = %#v, want terminal gap", event)
		}
	case <-time.After(time.Second):
		t.Fatal("overflow gap was not replayed")
	}
	slot.mu.Lock()
	remaining := len(slot.journal)
	overflowed := slot.overflowed
	slot.mu.Unlock()
	if !overflowed || remaining != 0 {
		t.Fatalf("overflow state = %v journal=%d, want acknowledged gap", overflowed, remaining)
	}
}

func TestChildActivityObserverResumeAdvancesSlotCursor(t *testing.T) {
	t.Parallel()

	run := &childRun{taskID: "task-resume-cursor"}
	slot := newChildSlot(agent.ChildEndpointRef{EndpointKey: run.taskID}, run)
	slot.beginActivity("activity-resume-cursor", run)
	events := make(chan agent.ChildActivityEvent, 1)
	slot.bindObserver(17, childActivityObserverFunc(func(_ context.Context, event agent.ChildActivityEvent) error {
		events <- event
		return nil
	}))
	slot.publishFrame(stream.Frame{Text: "after rehydration"})

	select {
	case event := <-events:
		if event.Cursor != 18 {
			t.Fatalf("resumed event cursor = %d, want 18", event.Cursor)
		}
	case <-time.After(time.Second):
		t.Fatal("resumed observer did not receive output")
	}
}

func TestSteeringSettlementPreservesIngressOrderAndQuarantinesUnknownOutput(t *testing.T) {
	t.Parallel()

	run := &childRun{taskID: "task-steering-order"}
	slot := newChildSlot(agent.ChildEndpointRef{EndpointKey: run.taskID}, run)
	slot.beginActivity("activity-steering-order", run)
	events := make(chan agent.ChildActivityEvent, 4)
	slot.bindObserver(0, childActivityObserverFunc(func(_ context.Context, event agent.ChildActivityEvent) error {
		events <- event
		return nil
	}))
	if !slot.beginSteering(func() {}) {
		t.Fatal("beginSteering() = false")
	}
	slot.publishFrame(stream.Frame{Text: "before-response"})

	settled := make(chan struct{})
	go func() {
		slot.settleSteeringFrames(true)
		close(settled)
	}()
	go func() {
		slot.ingressMu.Lock()
		slot.publishFrame(stream.Frame{Text: "after-response"})
		slot.ingressMu.Unlock()
	}()

	got := make([]string, 0, 2)
	for len(got) < 2 {
		select {
		case event := <-events:
			if event.Frame != nil {
				got = append(got, event.Frame.Text)
			}
		case <-time.After(time.Second):
			t.Fatalf("steering frames = %#v, want ordered pair", got)
		}
	}
	<-settled
	if want := []string{"before-response", "after-response"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("steering frames = %#v, want %#v", got, want)
	}

	if !slot.beginSteering(func() {}) {
		t.Fatal("second beginSteering() = false")
	}
	slot.publishFrame(stream.Frame{Text: "ambiguous-before-response"})
	slot.settleSteeringFrames(false)
	slot.ingressMu.Lock()
	slot.publishFrame(stream.Frame{Text: "ambiguous-after-response"})
	slot.ingressMu.Unlock()
	select {
	case event := <-events:
		t.Fatalf("quarantined steering output leaked: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTerminalObserverCanBeReplacedWhileProducerWaitsForAcknowledgement(t *testing.T) {
	t.Parallel()

	target := agent.ChildEndpointRef{
		ParticipantID: "child-terminal-swap", SessionID: "session-terminal-swap",
		EndpointKey: "task-terminal-swap", Role: session.ParticipantRoleDelegated,
		Placement: delegation.AgentTarget("helper").Placement,
	}
	run := &childRun{
		anchor: delegation.Anchor{TaskID: target.EndpointKey, SessionID: target.SessionID, AgentID: target.ParticipantID},
		taskID: target.EndpointKey, state: delegation.StateRunning, running: true,
		done: make(chan struct{}), updatedAt: time.Now(),
	}
	slot := newChildSlot(target, run)
	slot.beginActivity("activity-terminal-swap", run)
	oldObserverCalled := make(chan struct{})
	allowOldObserverReturn := make(chan struct{})
	var oldOnce sync.Once
	slot.bindObserver(0, childActivityObserverFunc(func(context.Context, agent.ChildActivityEvent) error {
		oldOnce.Do(func() { close(oldObserverCalled) })
		<-allowOldObserverReturn
		return errors.New("temporary Task persistence failure")
	}))
	runner := &Runner{
		clock: time.Now, runs: map[string]*childRun{target.EndpointKey: run},
		slots: map[string]*childSlot{target.EndpointKey: slot},
	}
	producerDone := make(chan struct{})
	go func() {
		runner.finishDrive(context.Background(), run, "end_turn", nil)
		close(producerDone)
	}()
	select {
	case <-oldObserverCalled:
	case <-time.After(time.Second):
		t.Fatal("old observer did not receive terminal event")
	}
	messageReturned := make(chan error, 1)
	go func() {
		_, err := runner.Message(context.Background(), run.anchor, tasksubagent.MessageRequest{Request: agentmessage.Request{
			MessageID: "message-during-terminal-settlement", To: "child", Text: "continue",
		}})
		messageReturned <- err
	}()
	select {
	case err := <-messageReturned:
		if !errorcode.Is(err, errorcode.Conflict) {
			t.Fatalf("Message() during terminal settlement error = %v, want conflict", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Message() waited behind terminal durable acknowledgement")
	}
	close(allowOldObserverReturn)

	newObserverCalled := make(chan struct{})
	if err := runner.BindChildActivityObserver(context.Background(), target, 0,
		childActivityObserverFunc(func(context.Context, agent.ChildActivityEvent) error {
			close(newObserverCalled)
			return nil
		})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-newObserverCalled:
	case <-time.After(time.Second):
		t.Fatal("replacement observer did not replay terminal journal item")
	}
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("terminal producer remained blocked after observer replacement")
	}
	select {
	case <-run.done:
	default:
		t.Fatal("terminal producer returned before closing run.done")
	}
}

func TestChildSlotRevokesPromptDispatchBeforeJoiningOperation(t *testing.T) {
	t.Parallel()

	run := &childRun{taskID: "task-prompt-cancel"}
	slot := newChildSlot(agent.ChildEndpointRef{EndpointKey: run.taskID}, run)
	dispatchCtx, cancelDispatch := context.WithCancel(context.Background())
	done := slot.beginPromptDispatch(cancelDispatch)
	_, revoke := slot.revokeActiveInput()
	if revoke == nil {
		t.Fatal("prompt dispatch did not publish its cancellation owner")
	}
	revoke()
	select {
	case <-dispatchCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("prompt dispatch was not cancelled")
	}
	slot.finishPromptDispatch(done)
}

func TestBeginActivityKeepsPublishedRunSlotImmutable(t *testing.T) {
	t.Parallel()

	run := &childRun{taskID: "task-slot-immutable"}
	slot := newChildSlot(agent.ChildEndpointRef{EndpointKey: run.taskID}, run)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			slot.beginActivity(fmt.Sprintf("activity-%d", i), run)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			if got := run.childSlot(); got != slot {
				t.Errorf("childSlot() = %p, want %p", got, slot)
				return
			}
		}
	}()
	wg.Wait()
}
