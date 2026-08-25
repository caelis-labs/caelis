package subagent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
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

type childActivityBatchObserverFunc func(context.Context, []agent.ChildActivityEvent) error

func (fn childActivityBatchObserverFunc) ObserveChildActivity(ctx context.Context, event agent.ChildActivityEvent) error {
	return fn(ctx, []agent.ChildActivityEvent{event})
}

func (fn childActivityBatchObserverFunc) ObserveChildActivityBatch(ctx context.Context, events []agent.ChildActivityEvent) error {
	return fn(ctx, events)
}

type childActivityLiveBatchObserver struct {
	live  func(context.Context, agent.ChildActivityEvent) error
	batch func(context.Context, []agent.ChildActivityEvent) error
}

func (o *childActivityLiveBatchObserver) ObserveChildActivity(ctx context.Context, event agent.ChildActivityEvent) error {
	return o.batch(ctx, []agent.ChildActivityEvent{event})
}

func (o *childActivityLiveBatchObserver) ObserveChildActivityBatch(ctx context.Context, events []agent.ChildActivityEvent) error {
	return o.batch(ctx, events)
}

func (o *childActivityLiveBatchObserver) ObserveChildActivityLive(ctx context.Context, event agent.ChildActivityEvent) error {
	return o.live(ctx, event)
}

func TestChildActivityJournalDeliversLiveFramesWhileDurableObserverPersists(t *testing.T) {
	t.Parallel()

	run := &childRun{taskID: "task-live-persist", state: delegation.StateRunning, running: true, done: make(chan struct{})}
	slot := newChildSlot(agent.ChildEndpointRef{
		ParticipantID: "child-live", SessionID: "child-session", EndpointKey: run.taskID,
		Role: session.ParticipantRoleDelegated,
	}, run)
	slot.beginActivity("activity-live", run)
	entered := make(chan struct{})
	release := make(chan struct{})
	live := make(chan agent.ChildActivityEvent, 4)
	durable := make(chan uint64, 4)
	var once sync.Once
	observer := &childActivityLiveBatchObserver{
		live: func(_ context.Context, event agent.ChildActivityEvent) error {
			live <- agent.CloneChildActivityEvent(event)
			return nil
		},
		batch: func(_ context.Context, events []agent.ChildActivityEvent) error {
			once.Do(func() {
				close(entered)
				<-release
			})
			durable <- events[len(events)-1].Cursor
			return nil
		},
	}
	slot.bindObserver(0, observer)
	slot.publishFrame(childAssistantActivityFrame("message-1", "first "))
	select {
	case event := <-live:
		if event.Cursor != 1 || session.EventText(event.Frame.Event) != "first " {
			t.Fatalf("first live frame = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("first frame did not reach live observer")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("durable observer did not enter first persistence callback")
	}

	slot.publishFrame(childAssistantActivityFrame("message-1", "second"))
	select {
	case event := <-live:
		if event.Cursor != 2 || session.EventText(event.Frame.Event) != "second" {
			t.Fatalf("second live frame = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("second frame waited for durable persistence")
	}
	close(release)

	deadline := time.After(time.Second)
	lastDurable := uint64(0)
	for lastDurable < 2 {
		select {
		case cursor := <-durable:
			lastDurable = max(lastDurable, cursor)
		case <-deadline:
			t.Fatalf("durable cursor = %d, want 2", lastDurable)
		}
	}
}

func TestChildActivityJournalDoesNotMergeAcceptedLivePrefixIntoDurableReplay(t *testing.T) {
	t.Parallel()

	run := &childRun{taskID: "task-live-replay", state: delegation.StateRunning, running: true, done: make(chan struct{})}
	slot := newChildSlot(agent.ChildEndpointRef{
		ParticipantID: "child-live", SessionID: "child-session", EndpointKey: run.taskID,
		Role: session.ParticipantRoleDelegated,
	}, run)
	slot.beginActivity("activity-live", run)
	entered := make(chan struct{})
	release := make(chan struct{})
	durable := make(chan uint64, 4)
	var blockFirst sync.Once
	var appliedMu sync.Mutex
	var applied strings.Builder
	var appliedCursor uint64
	apply := func(event agent.ChildActivityEvent) {
		if event.Frame == nil || event.Cursor <= appliedCursor {
			return
		}
		applied.WriteString(session.EventText(event.Frame.Event))
		appliedCursor = event.Cursor
	}
	observer := &childActivityLiveBatchObserver{
		live: func(_ context.Context, event agent.ChildActivityEvent) error {
			if event.Cursor == 3 {
				return errors.New("live preview unavailable")
			}
			appliedMu.Lock()
			apply(event)
			appliedMu.Unlock()
			return nil
		},
		batch: func(_ context.Context, events []agent.ChildActivityEvent) error {
			blockFirst.Do(func() {
				close(entered)
				<-release
			})
			appliedMu.Lock()
			for _, event := range events {
				apply(event)
			}
			appliedMu.Unlock()
			durable <- events[len(events)-1].Cursor
			return nil
		},
	}
	slot.bindObserver(0, observer)
	slot.publishFrame(childAssistantActivityFrame("message-1", "first "))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("durable observer did not block on the first frame")
	}

	slot.publishFrame(childAssistantActivityFrame("message-1", "second "))
	slot.publishFrame(childAssistantActivityFrame("message-1", "third"))
	close(release)

	deadline := time.After(time.Second)
	lastDurable := uint64(0)
	for lastDurable < 3 {
		select {
		case cursor := <-durable:
			lastDurable = max(lastDurable, cursor)
		case <-deadline:
			t.Fatalf("durable cursor = %d, want 3", lastDurable)
		}
	}
	appliedMu.Lock()
	got := applied.String()
	appliedMu.Unlock()
	if got != "first second third" {
		t.Fatalf("live plus durable assistant text = %q, want each delta exactly once", got)
	}
}

func TestChildActivityJournalCoalescesStreamingAssistantChunksWhileObserverPersists(t *testing.T) {
	t.Parallel()

	run := &childRun{
		taskID: "task-stream-burst", state: delegation.StateRunning, running: true,
		done: make(chan struct{}),
	}
	slot := newChildSlot(agent.ChildEndpointRef{
		ParticipantID: "child-stream-burst", SessionID: "child-session", EndpointKey: run.taskID,
		Role: session.ParticipantRoleDelegated,
	}, run)
	slot.beginActivity("activity-stream-burst", run)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var observed []agent.ChildActivityEvent
	slot.bindObserver(0, childActivityBatchObserverFunc(func(_ context.Context, events []agent.ChildActivityEvent) error {
		mu.Lock()
		for _, event := range events {
			observed = append(observed, agent.CloneChildActivityEvent(event))
		}
		mu.Unlock()
		once.Do(func() {
			close(entered)
			<-release
		})
		return nil
	}))

	const chunks = childActivityJournalMaxEvents + 64
	var expected strings.Builder
	for index := range chunks {
		text := fmt.Sprintf("%03d\n", index)
		expected.WriteString(text)
		slot.publishFrame(childAssistantActivityFrame("message-1", text))
		if index == 0 {
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("observer did not start first durable callback")
			}
		}
	}
	want := expected.String()
	wantFinal := strings.TrimSpace(want)
	terminalAck := publishChildRunResult(slot, run, delegation.Result{
		TaskID: run.taskID, State: delegation.StateCompleted, Result: wantFinal,
	})
	close(release)
	select {
	case <-terminalAck:
	case <-time.After(time.Second):
		t.Fatal("real terminal result was not durably acknowledged")
	}
	mu.Lock()
	events := append([]agent.ChildActivityEvent(nil), observed...)
	mu.Unlock()
	var got strings.Builder
	var terminal *delegation.Result
	for _, event := range events {
		if event.Gap {
			t.Fatalf("coalesced %d-chunk stream produced observation gap: %#v", chunks, event)
		}
		if event.Frame != nil {
			got.WriteString(session.EventText(event.Frame.Event))
		}
		if event.Result != nil {
			result := delegation.CloneResult(*event.Result)
			terminal = &result
		}
	}
	if got.String() != want {
		t.Fatalf("observed assistant stream bytes = %d, want %d", got.Len(), len(want))
	}
	if terminal == nil || terminal.State != delegation.StateCompleted || terminal.Result != wantFinal {
		if terminal == nil {
			t.Fatal("terminal result is nil")
		}
		t.Fatalf("terminal state/result = %q equal:%v bytes:%d, want %q/%d bytes", terminal.State, terminal.Result == wantFinal, len(terminal.Result), delegation.StateCompleted, len(wantFinal))
	}
	slot.mu.Lock()
	remaining := len(slot.journal)
	slot.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("journal retained %d acknowledged items", remaining)
	}
}

func TestChildActivityJournalBoundsStreamingMergeWork(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		chunks int
		text   string
	}{
		{
			name:   "frame_count",
			chunks: childActivityMergedMaxFrames*4 + 1,
			text:   "x",
		},
		{
			name:   "text_bytes",
			chunks: 25,
			text:   strings.Repeat("x", childActivityMergedMaxBytes/8),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := &childRun{
				taskID: "task-bounded-merge-" + test.name, state: delegation.StateRunning, running: true,
				done: make(chan struct{}),
			}
			slot := newChildSlot(agent.ChildEndpointRef{
				ParticipantID: "child-bounded-merge", SessionID: "child-session", EndpointKey: run.taskID,
				Role: session.ParticipantRoleDelegated,
			}, run)
			slot.beginActivity("activity-bounded-merge", run)
			entered := make(chan struct{})
			release := make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			var once sync.Once
			slot.bindObserver(0, childActivityBatchObserverFunc(func(context.Context, []agent.ChildActivityEvent) error {
				once.Do(func() {
					close(entered)
					<-release
				})
				return nil
			}))

			for index := range test.chunks {
				slot.publishFrame(childAssistantActivityFrame("bounded-message", test.text))
				if index == 0 {
					select {
					case <-entered:
					case <-time.After(time.Second):
						t.Fatal("observer did not enter slow callback")
					}
				}
			}

			type journalSummary struct {
				frames    uint64
				textBytes int
			}
			slot.mu.Lock()
			summaries := make([]journalSummary, 0, len(slot.journal))
			for _, item := range slot.journal {
				if item != nil && item.event.Frame != nil {
					summaries = append(summaries, journalSummary{
						frames: item.frameCount, textBytes: len(session.EventText(item.event.Frame.Event)),
					})
				}
			}
			slot.mu.Unlock()

			var retainedFrames uint64
			for _, summary := range summaries {
				retainedFrames += summary.frames
				if summary.frames == 0 || summary.frames > childActivityMergedMaxFrames {
					t.Fatalf("merged journal item frame count = %d, want 1..%d", summary.frames, childActivityMergedMaxFrames)
				}
				if summary.textBytes > childActivityMergedMaxBytes {
					t.Fatalf("merged journal item text bytes = %d, want <= %d", summary.textBytes, childActivityMergedMaxBytes)
				}
			}
			if retainedFrames != uint64(test.chunks) {
				t.Fatalf("retained frame count = %d, want %d", retainedFrames, test.chunks)
			}

			terminalAck := publishChildRunResult(slot, run, delegation.Result{
				TaskID: run.taskID, State: delegation.StateCompleted, Result: "done",
			})
			close(release)
			released = true
			select {
			case <-terminalAck:
			case <-time.After(time.Second):
				t.Fatal("bounded journal did not deliver authoritative terminal result")
			}
		})
	}
}

func TestChildActivityJournalReplaysPreinstallBurstWhenTaskObserverBecomesReady(t *testing.T) {
	t.Parallel()

	run := &childRun{
		taskID: "task-preinstall-burst", state: delegation.StateRunning, running: true,
		done: make(chan struct{}),
	}
	slot := newChildSlot(agent.ChildEndpointRef{
		ParticipantID: "child-preinstall", SessionID: "child-session", EndpointKey: run.taskID,
		Role: session.ParticipantRoleDelegated,
	}, run)
	slot.beginInitialActivity("activity-preinstall", run)
	observerAttempted := make(chan struct{})
	var attemptOnce sync.Once
	slot.bindObserver(0, childActivityObserverFunc(func(context.Context, agent.ChildActivityEvent) error {
		attemptOnce.Do(func() { close(observerAttempted) })
		return errors.New("child Task is not installed yet")
	}))

	const chunks = childActivityJournalMaxEvents + 64
	var expected strings.Builder
	for index := range chunks {
		text := fmt.Sprintf("preinstall-%03d\n", index)
		expected.WriteString(text)
		slot.publishFrame(childAssistantActivityFrame("preinstall-message", text))
	}
	select {
	case <-observerAttempted:
	case <-time.After(time.Second):
		t.Fatal("preinstall observer was not attempted")
	}
	want := expected.String()
	terminalAck := publishChildRunResult(slot, run, delegation.Result{
		TaskID: run.taskID, State: delegation.StateCompleted, Result: strings.TrimSpace(want),
	})
	var mu sync.Mutex
	var observed []agent.ChildActivityEvent
	slot.bindObserver(0, childActivityBatchObserverFunc(func(_ context.Context, events []agent.ChildActivityEvent) error {
		mu.Lock()
		for _, event := range events {
			observed = append(observed, agent.CloneChildActivityEvent(event))
		}
		mu.Unlock()
		return nil
	}))
	select {
	case <-terminalAck:
	case <-time.After(time.Second):
		t.Fatal("preinstall terminal did not replay after Task observer installation")
	}

	mu.Lock()
	events := append([]agent.ChildActivityEvent(nil), observed...)
	mu.Unlock()
	var got strings.Builder
	var terminal *delegation.Result
	for _, event := range events {
		if event.Gap {
			t.Fatalf("bounded preinstall text burst produced gap: %#v", event)
		}
		if event.Frame != nil {
			got.WriteString(session.EventText(event.Frame.Event))
		}
		if event.Result != nil {
			result := delegation.CloneResult(*event.Result)
			terminal = &result
		}
	}
	if got.String() != want {
		t.Fatalf("preinstall replay bytes = %d, want %d", got.Len(), len(want))
	}
	if terminal == nil || terminal.State != delegation.StateCompleted {
		t.Fatalf("preinstall terminal = %#v, want completed", terminal)
	}
}

func TestChildActivityJournalOverflowIsRecoverableAndRetainsRealTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		frames []stream.Frame
	}{
		{
			name: "event_limit",
			frames: func() []stream.Frame {
				frames := make([]stream.Frame, 0, childActivityJournalMaxEvents+32)
				for index := 0; index < childActivityJournalMaxEvents+32; index++ {
					frames = append(frames, stream.Frame{Text: fmt.Sprintf("output-%d", index)})
				}
				return frames
			}(),
		},
		{
			name: "byte_limit",
			frames: []stream.Frame{
				{Text: strings.Repeat("a", childActivityJournalMaxBytes/2)},
				{Text: strings.Repeat("b", childActivityJournalMaxBytes/2)},
				{Text: strings.Repeat("c", childActivityJournalMaxBytes/2)},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runCtx, cancelRun := context.WithCancel(context.Background())
			defer cancelRun()
			run := &childRun{
				taskID: "task-overflow-" + test.name, state: delegation.StateRunning, running: true,
				done: make(chan struct{}), cancel: cancelRun,
			}
			slot := newChildSlot(agent.ChildEndpointRef{
				ParticipantID: "child-overflow", SessionID: "child-session", EndpointKey: run.taskID,
				Role: session.ParticipantRoleDelegated,
			}, run)
			slot.beginActivity("activity-overflow", run)
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			var mu sync.Mutex
			var observed []agent.ChildActivityEvent
			slot.bindObserver(0, childActivityObserverFunc(func(_ context.Context, event agent.ChildActivityEvent) error {
				once.Do(func() {
					close(entered)
					<-release
				})
				mu.Lock()
				observed = append(observed, agent.CloneChildActivityEvent(event))
				mu.Unlock()
				return nil
			}))

			for index, frame := range test.frames {
				slot.publishFrame(frame)
				if index == 0 {
					select {
					case <-entered:
					case <-time.After(time.Second):
						t.Fatal("observer did not enter slow callback")
					}
				}
			}
			terminalAck := publishChildRunResult(slot, run, delegation.Result{
				TaskID: run.taskID, State: delegation.StateCompleted, Result: "exact final result",
			})
			select {
			case <-runCtx.Done():
				t.Fatal("presentation backlog cancelled child execution")
			default:
			}
			close(release)
			select {
			case <-terminalAck:
			case <-time.After(time.Second):
				t.Fatal("real terminal result was not acknowledged after observation gap")
			}

			mu.Lock()
			events := append([]agent.ChildActivityEvent(nil), observed...)
			mu.Unlock()
			var gap *agent.ChildActivityEvent
			var terminal *delegation.Result
			for index := range events {
				if events[index].Gap {
					cloned := agent.CloneChildActivityEvent(events[index])
					gap = &cloned
				}
				if events[index].Result != nil {
					result := delegation.CloneResult(*events[index].Result)
					terminal = &result
				}
			}
			if gap == nil || gap.Dropped == 0 || gap.Result != nil {
				t.Fatalf("recoverable gap = %#v, want non-terminal dropped-frame boundary", gap)
			}
			if terminal == nil || terminal.State != delegation.StateCompleted || terminal.Result != "exact final result" {
				t.Fatalf("terminal result = %#v, want authoritative completion", terminal)
			}
			select {
			case <-runCtx.Done():
				t.Fatal("completed observation gap eventually cancelled child execution")
			default:
			}
		})
	}
}

func TestChildActivityTerminalResultIsReferencedOutsideBoundedJournal(t *testing.T) {
	t.Parallel()

	run := &childRun{
		taskID: "task-large-terminal", state: delegation.StateRunning, running: true,
		done: make(chan struct{}),
	}
	slot := newChildSlot(agent.ChildEndpointRef{
		ParticipantID: "child-large-terminal", SessionID: "child-session", EndpointKey: run.taskID,
		Role: session.ParticipantRoleDelegated,
	}, run)
	slot.beginActivity("activity-large-terminal", run)
	attempted := make(chan int, 1)
	var once sync.Once
	slot.bindObserver(0, childActivityObserverFunc(func(_ context.Context, event agent.ChildActivityEvent) error {
		once.Do(func() {
			if event.Result == nil {
				attempted <- -1
				return
			}
			attempted <- len(event.Result.Result)
		})
		return errors.New("observer unavailable")
	}))

	largeFinal := strings.Repeat("x", 2*childActivityJournalMaxBytes)
	terminalAck := publishChildRunResult(slot, run, delegation.Result{
		TaskID: run.taskID, State: delegation.StateCompleted, Result: largeFinal,
	})
	select {
	case size := <-attempted:
		if size != len(largeFinal) {
			t.Fatalf("delivered terminal bytes = %d, want %d", size, len(largeFinal))
		}
	case <-time.After(time.Second):
		t.Fatal("large terminal was not attempted")
	}

	slot.mu.Lock()
	journalBytes := slot.journalBytes
	journalItems := append([]*childActivityJournalItem(nil), slot.journal...)
	slot.mu.Unlock()
	if journalBytes > childActivityJournalMaxBytes || len(journalItems) != 1 {
		t.Fatalf("terminal journal = %d bytes/%d items, want one bounded reference", journalBytes, len(journalItems))
	}
	if journalItems[0].event.Result != nil || journalItems[0].terminal == nil ||
		len(journalItems[0].terminal.Result) != len(largeFinal) {
		t.Fatalf("terminal journal item = %#v, want result-free immutable terminal snapshot", journalItems[0])
	}
	select {
	case <-terminalAck:
		t.Fatal("failed terminal observation was acknowledged")
	default:
	}

	delivered := make(chan int, 1)
	slot.bindObserver(0, childActivityBatchObserverFunc(func(_ context.Context, events []agent.ChildActivityEvent) error {
		if len(events) != 1 || events[0].Result == nil {
			return errors.New("terminal result missing")
		}
		delivered <- len(events[0].Result.Result)
		return nil
	}))
	select {
	case size := <-delivered:
		if size != len(largeFinal) {
			t.Fatalf("replayed terminal bytes = %d, want %d", size, len(largeFinal))
		}
	case <-time.After(time.Second):
		t.Fatal("large terminal was not replayed")
	}
	select {
	case <-terminalAck:
	case <-time.After(time.Second):
		t.Fatal("large terminal was not acknowledged after replay")
	}
}

func TestChildActivityTerminalSnapshotRejectsLateUpdates(t *testing.T) {
	t.Parallel()

	terminalAt := time.Unix(100, 0)
	lateAt := terminalAt.Add(time.Minute)
	run := &childRun{
		anchor: delegation.Anchor{TaskID: "task-frozen-terminal", SessionID: "child-session", AgentID: "child-1"},
		taskID: "task-frozen-terminal", state: delegation.StateRunning, running: true,
		updatedAt: terminalAt, done: make(chan struct{}),
	}
	slot := newChildSlot(agent.ChildEndpointRef{
		ParticipantID: "child-1", SessionID: "child-session", EndpointKey: run.taskID,
		Role: session.ParticipantRoleDelegated,
	}, run)
	slot.beginActivity("activity-frozen-terminal", run)
	firstAttempt := make(chan delegation.Result, 1)
	var firstOnce sync.Once
	slot.bindObserver(0, childActivityObserverFunc(func(_ context.Context, event agent.ChildActivityEvent) error {
		if event.Result != nil {
			firstOnce.Do(func() { firstAttempt <- delegation.CloneResult(*event.Result) })
		}
		return errors.New("observer unavailable")
	}))

	terminalAck := publishChildRunResult(slot, run, delegation.Result{
		TaskID: run.taskID, State: delegation.StateCompleted, Result: "exact terminal", UpdatedAt: terminalAt,
	})
	var first delegation.Result
	select {
	case first = <-firstAttempt:
	case <-time.After(time.Second):
		t.Fatal("terminal result was not attempted")
	}

	runner := &Runner{clock: func() time.Time { return lateAt }}
	runner.handleUpdate(run, contentUpdate(t, client.UpdateAgentThought, "late notification"))
	run.mu.RLock()
	updatedAt := run.updatedAt
	run.mu.RUnlock()
	if !updatedAt.Equal(terminalAt) {
		t.Fatalf("run updated_at = %v, want frozen terminal time %v", updatedAt, terminalAt)
	}

	replayed := make(chan agent.ChildActivityEvent, 2)
	slot.bindObserver(0, childActivityBatchObserverFunc(func(_ context.Context, events []agent.ChildActivityEvent) error {
		for _, event := range events {
			replayed <- agent.CloneChildActivityEvent(event)
		}
		return nil
	}))
	var second delegation.Result
	select {
	case event := <-replayed:
		if event.Result == nil {
			t.Fatalf("replayed event = %#v, want terminal result", event)
		}
		second = delegation.CloneResult(*event.Result)
	case <-time.After(time.Second):
		t.Fatal("terminal result was not replayed")
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("replayed terminal = %#v, want immutable first generation %#v", second, first)
	}
	select {
	case <-terminalAck:
	case <-time.After(time.Second):
		t.Fatal("terminal result was not acknowledged")
	}
	select {
	case event := <-replayed:
		t.Fatalf("post-terminal activity event = %#v, want late notification quarantined", event)
	case <-time.After(100 * time.Millisecond):
	}
}

func childAssistantActivityFrame(messageID string, text string) stream.Frame {
	message := model.NewTextMessage(model.RoleAssistant, text)
	return stream.Frame{
		Running: true,
		Event: &session.Event{
			Type: session.EventTypeAssistant, Visibility: session.VisibilityUIOnly,
			Text: text, Message: &message,
			Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				MessageID:     messageID, Content: session.ProtocolTextContent(text),
			}},
		},
	}
}

func publishChildRunResult(slot *childSlot, run *childRun, result delegation.Result) <-chan struct{} {
	if run == nil {
		return slot.publishRunResult(nil)
	}
	run.mu.Lock()
	run.state = result.State
	run.running = result.Running
	run.outputPreview = result.OutputPreview
	run.failureDetail = result.Error
	run.result = result.Result
	run.updatedAt = result.UpdatedAt
	run.mu.Unlock()
	return slot.publishRunResult(run)
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
