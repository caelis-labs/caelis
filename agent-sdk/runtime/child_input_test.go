package runtime

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
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

func TestRuntimeChildInputLeavesTaskUnchangedUntilOutputThenObservesGeneration(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := &runtimeChildInputRunner{spawnResult: delegation.Result{
		State: delegation.StateCompleted, Result: "initial result",
	}}
	runtime, active := newSubagentTaskTestRuntime(t, runner)
	baseStore := runtime.tasks.store
	audit := &activityTaskStoreAudit{Store: baseStore, cas: baseStore.(taskapi.CASStore)}
	runtime.tasks.store = audit
	var err error
	active, err = runtime.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef:    active.SessionRef,
		MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindKernel, ControllerID: "controller-1", AgentName: "main",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.StartSubagentWithOptions(ctx, active.SessionRef, "helper", "initial", "spawn-activity-test", StartSubagentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := runtime.tasks.store.Get(ctx, started.Ref.TaskID)
	if err != nil || before == nil {
		t.Fatalf("load Task before input = %#v, %v", before, err)
	}
	result, err := runtime.SubmitChildInput(ctx, active.SessionRef, agent.ChildInputCommand{
		Target: started.Handle,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		Input:  "continue from output",
	})
	if err != nil || !result.StartedActivity || result.ActivityID != "activity-2" {
		t.Fatalf("SubmitChildInput() = (%#v, %v)", result, err)
	}
	currentSession, err := runtime.sessions.Session(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	var childBinding session.ParticipantBinding
	for _, binding := range currentSession.Participants {
		if binding.DelegationID == started.Ref.TaskID {
			childBinding = binding
			break
		}
	}
	runner.mu.Lock()
	delivered := agent.CloneChildInputRequest(runner.request)
	deliveryCalls := runner.calls
	runner.mu.Unlock()
	if deliveryCalls != 1 || delivered.Source != session.ControllerExecutor(currentSession.Controller) ||
		delivered.Target.ParticipantID != childBinding.ID || delivered.Target.SessionID != childBinding.SessionID ||
		delivered.Target.EndpointKey != childBinding.DelegationID ||
		!reflect.DeepEqual(delivered.Target.Placement, childBinding.Placement) {
		t.Fatalf("resolved child input = %#v calls=%d, want exact trusted binding %#v", delivered, deliveryCalls, childBinding)
	}
	if _, selfErr := runtime.SubmitChildInput(ctx, active.SessionRef, agent.ChildInputCommand{
		Target: started.Handle,
		Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: childBinding.ID},
		Input:  "self input",
	}); !errorcode.Is(selfErr, errorcode.Conflict) {
		t.Fatalf("self SubmitChildInput() error = %v, want conflict", selfErr)
	}
	runner.mu.Lock()
	deliveryCalls = runner.calls
	runner.mu.Unlock()
	if deliveryCalls != 1 {
		t.Fatalf("self input reached runner; delivery calls = %d", deliveryCalls)
	}
	afterAdmission, err := runtime.tasks.store.Get(ctx, started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, afterAdmission) {
		t.Fatalf("input admission mutated Task\nbefore: %#v\nafter:  %#v", before, afterAdmission)
	}

	if err := runner.publish(agent.ChildActivityEvent{
		ActivityID: result.ActivityID,
		Cursor:     1,
		Frame: &stream.Frame{
			Ref: stream.Ref{TaskID: started.Ref.TaskID, SessionID: "child-1"},
			Event: &session.Event{
				Type: session.EventTypeAssistant, Visibility: session.VisibilityUIOnly,
				Text: "new observed output", Time: time.Now(),
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	runtime.tasks.mu.RLock()
	runningObservation := runtime.tasks.subagents[started.Ref.TaskID]
	runtime.tasks.mu.RUnlock()
	if runningObservation == nil {
		t.Fatal("observed Task disappeared after first output")
	}
	runningSnapshot := runningObservation.snapshot()
	if !runningSnapshot.Running || runningSnapshot.State != taskapi.StateRunning {
		t.Fatalf("first output observation = %#v, want running generation", runningSnapshot)
	}
	if _, exists := runningSnapshot.Result["final_message"]; exists {
		t.Fatalf("new running generation retained previous final result: %#v", runningSnapshot.Result)
	}
	if err := runner.publish(agent.ChildActivityEvent{
		ActivityID: result.ActivityID,
		Cursor:     2,
		Result: &delegation.Result{
			TaskID: started.Ref.TaskID, State: delegation.StateCompleted,
			Result: "new final result", UpdatedAt: time.Now(),
		},
	}); err != nil {
		t.Fatal(err)
	}

	runtime.tasks.mu.RLock()
	observed := runtime.tasks.subagents[started.Ref.TaskID]
	runtime.tasks.mu.RUnlock()
	if observed == nil {
		t.Fatal("observed Task disappeared")
	}
	snapshot := observed.snapshot()
	generation, _ := taskInt64Value(snapshot.Metadata[subagentActivityGenerationMeta])
	if snapshot.Running || snapshot.State != taskapi.StateCompleted ||
		taskStringValue(snapshot.Result["final_message"]) != "new final result" || generation != 2 {
		t.Fatalf("observed Task = %#v, want completed output generation 2", snapshot)
	}
	observed.mu.Lock()
	frames := append([]stream.Frame(nil), observed.streamFrames...)
	activityCursor := observed.activityDurableCursor
	observed.mu.Unlock()
	count := 0
	for _, frame := range frames {
		if frame.Event != nil && strings.TrimSpace(frame.Event.Text) == "new observed output" {
			count++
			if frame.Ref.TerminalID != subagentTurnID(started.Ref.TaskID, 2) {
				t.Fatalf("observed frame Turn = %q", frame.Ref.TerminalID)
			}
		}
	}
	if count != 1 || activityCursor != 2 {
		t.Fatalf("observed frame count=%d durable activity cursor=%d", count, activityCursor)
	}
	audit.mu.Lock()
	runningTerminalCursor := audit.runningTerminalCursor
	terminalCursorCommitted := audit.terminalCursorCommitted
	audit.mu.Unlock()
	if runningTerminalCursor || !terminalCursorCommitted {
		t.Fatalf("terminal cursor persistence running=%v terminal=%v, want one terminal-state commit", runningTerminalCursor, terminalCursorCommitted)
	}
}

func TestRuntimeChildActivityBatchUsesOneRunningWriteAndKeepsGapNonTerminal(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := &runtimeChildInputRunner{spawnResult: delegation.Result{
		State: delegation.StateCompleted, Result: "initial result",
	}}
	runtime, active := newSubagentTaskTestRuntime(t, runner)
	started, err := runtime.StartSubagentWithOptions(
		ctx, active.SessionRef, "helper", "initial", "batch-activity-test", StartSubagentOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	baseStore := runtime.tasks.store
	audit := &activityTaskStoreAudit{Store: baseStore, cas: baseStore.(taskapi.CASStore)}
	runtime.tasks.store = audit

	runner.mu.Lock()
	observer := runner.observer
	runner.mu.Unlock()
	batchObserver, ok := observer.(agent.ChildActivityBatchObserver)
	if !ok {
		t.Fatalf("activity observer = %T, want batch observer", observer)
	}
	runtime.tasks.mu.RLock()
	task := runtime.tasks.subagents[started.Ref.TaskID]
	runtime.tasks.mu.RUnlock()
	if task == nil {
		t.Fatal("started Task is unavailable")
	}
	task.mu.Lock()
	target := agent.ChildEndpointRef{
		ParticipantID: task.anchor.AgentID,
		SessionID:     task.anchor.SessionID,
		EndpointKey:   task.ref.TaskID,
		Role:          subagentParticipantRole(task),
		Placement:     task.target.Placement,
	}
	task.mu.Unlock()
	const frames = 300
	events := make([]agent.ChildActivityEvent, 0, frames)
	for index := range frames {
		events = append(events, agent.ChildActivityEvent{
			Target: target, ActivityID: "activity-batch", Cursor: uint64(index + 1),
			Frame: &stream.Frame{Text: fmt.Sprintf("frame-%03d\n", index), Running: true},
		})
	}
	if err := batchObserver.ObserveChildActivityBatch(ctx, events); err != nil {
		t.Fatal(err)
	}
	audit.mu.Lock()
	runningWrites := audit.puts
	audit.mu.Unlock()
	if runningWrites != 1 {
		t.Fatalf("Task writes for %d-frame activity batch = %d, want 1", frames, runningWrites)
	}

	if err := batchObserver.ObserveChildActivityBatch(ctx, []agent.ChildActivityEvent{
		{
			Target: target, ActivityID: "activity-batch", Cursor: frames + 1,
			Gap: true, Dropped: 400,
		},
		{
			Target: target, ActivityID: "activity-batch", Cursor: frames + 2,
			Result: &delegation.Result{
				TaskID: started.Ref.TaskID, State: delegation.StateCompleted,
				Result: "exact final result", UpdatedAt: time.Now(),
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	runtime.tasks.mu.RLock()
	observed := runtime.tasks.subagents[started.Ref.TaskID]
	runtime.tasks.mu.RUnlock()
	if observed == nil {
		t.Fatal("observed Task disappeared")
	}
	snapshot := observed.snapshot()
	observed.mu.Lock()
	durableCursor := observed.activityDurableCursor
	streamBase := observed.streamEventBase
	observed.mu.Unlock()
	if snapshot.Running || snapshot.State != taskapi.StateCompleted ||
		taskStringValue(snapshot.Result["final_message"]) != "exact final result" {
		t.Fatalf("Task after recoverable gap = %#v, want authoritative completion", snapshot)
	}
	if durableCursor != frames+2 || streamBase == 0 {
		t.Fatalf("durable activity cursor/base = %d/%d, want %d and recoverable stream gap", durableCursor, streamBase, frames+2)
	}
	audit.mu.Lock()
	terminalCursorCommitted := audit.terminalCursorCommitted
	audit.mu.Unlock()
	if !terminalCursorCommitted {
		t.Fatal("batched terminal did not commit its durable activity cursor")
	}
}

func TestInitialActivityReplayedAfterFastTerminalStaysOnGenerationOne(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := &fastTerminalActivityRunner{}
	runtime, active := newSubagentTaskTestRuntime(t, runner)
	started, err := runtime.StartSubagentWithOptions(
		ctx, active.SessionRef, "helper", "finish immediately", "fast-terminal", StartSubagentOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if runner.preinstallErr == nil {
		t.Fatal("initial activity unexpectedly observed before Task installation")
	}
	if err := runner.BindChildActivityObserver(ctx, runner.events[0].Target, 0, newSubagentActivityObserver(runtime.tasks, started.Ref.TaskID)); err != nil {
		t.Fatal(err)
	}
	for _, event := range runner.events {
		if err := runner.observer.ObserveChildActivity(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	runtime.tasks.mu.RLock()
	task := runtime.tasks.subagents[started.Ref.TaskID]
	runtime.tasks.mu.RUnlock()
	if task == nil {
		t.Fatal("initial Task is unavailable")
	}
	snapshot := task.snapshot()
	generation, _ := taskInt64Value(snapshot.Metadata[subagentActivityGenerationMeta])
	if generation != 1 || snapshot.Running || snapshot.State != taskapi.StateCompleted ||
		taskStringValue(snapshot.Result["final_message"]) != "fast terminal result" {
		t.Fatalf("replayed initial activity = %#v generation=%d, want completed generation 1", snapshot, generation)
	}
	task.mu.Lock()
	frames := append([]stream.Frame(nil), task.streamFrames...)
	task.mu.Unlock()
	count := 0
	for _, frame := range frames {
		if frame.Event == nil || frame.Event.Text != "fast terminal output" {
			continue
		}
		count++
		if frame.Ref.TerminalID != subagentTurnID(started.Ref.TaskID, 1) {
			t.Fatalf("initial frame terminal = %q, want generation 1", frame.Ref.TerminalID)
		}
	}
	if count != 1 {
		t.Fatalf("initial output frame count = %d, want exactly once", count)
	}
	durable, err := runtime.tasks.store.Get(ctx, started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	durableGeneration, _ := taskInt64Value(durable.Metadata[subagentActivityGenerationMeta])
	durableCursor, _ := taskInt64Value(durable.Metadata[subagentActivityCursorMeta])
	if durableGeneration != 1 || durableCursor != 2 {
		t.Fatalf("durable initial activity generation=%d cursor=%d, want 1/2", durableGeneration, durableCursor)
	}
	for _, event := range []agent.ChildActivityEvent{
		{
			Target: runner.events[0].Target, ActivityID: "generic-after-rebind", Cursor: 3,
			Frame: &stream.Frame{Event: &session.Event{
				Type: session.EventTypeAssistant, Visibility: session.VisibilityUIOnly,
				Text: "generic output", Time: time.Now(),
			}},
		},
		{
			Target: runner.events[0].Target, ActivityID: "generic-after-rebind", Cursor: 4,
			Result: &delegation.Result{
				TaskID: started.Ref.TaskID, State: delegation.StateCompleted,
				Result: "generic result", UpdatedAt: time.Now(),
			},
		},
	} {
		if err := runner.observer.ObserveChildActivity(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	final := task.snapshot()
	finalGeneration, _ := taskInt64Value(final.Metadata[subagentActivityGenerationMeta])
	if finalGeneration != 2 || taskStringValue(final.Result["final_message"]) != "generic result" {
		t.Fatalf("post-rebind generic activity = %#v generation=%d, want generation 2", final, finalGeneration)
	}
}

func TestObservedTerminalRetainsTaskOperationAcrossPersistenceRetry(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := &fastTerminalActivityRunner{}
	runtime, active := newSubagentTaskTestRuntime(t, runner)
	base := runtime.tasks.store
	flaky := &failOnceObservedTerminalStore{
		Store: base, cas: base.(taskapi.CASStore), failed: make(chan struct{}),
	}
	runtime.tasks.store = flaky
	started, err := runtime.StartSubagentWithOptions(
		ctx, active.SessionRef, "helper", "finish immediately", "terminal-retry", StartSubagentOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.BindChildActivityObserver(ctx, runner.events[0].Target, 0, newSubagentActivityObserver(runtime.tasks, started.Ref.TaskID)); err != nil {
		t.Fatal(err)
	}
	if err := runner.observer.ObserveChildActivity(ctx, runner.events[0]); err != nil {
		t.Fatal(err)
	}
	terminalDone := make(chan error, 1)
	go func() {
		terminalDone <- runner.observer.ObserveChildActivity(ctx, runner.events[1])
	}()
	select {
	case <-flaky.failed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if release, claimed := runtime.tasks.tryClaimSubagentOperation(active.SessionRef, started.Ref.TaskID); claimed {
		release()
		t.Fatal("terminal persistence retry released its Task operation claim")
	}
	select {
	case err := <-terminalDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if release, claimed := runtime.tasks.tryClaimSubagentOperation(active.SessionRef, started.Ref.TaskID); !claimed {
		t.Fatal("terminal persistence success did not release its Task operation claim")
	} else {
		release()
	}
}

func TestRuntimeChildInputRejectsSelfAndStaleSourceBeforeRunner(t *testing.T) {
	t.Parallel()

	active := session.Session{
		Controller: session.ControllerBinding{Kind: session.ControllerKindKernel, ControllerID: "controller-1"},
		Participants: []session.ParticipantBinding{{
			ID: "child-1", SessionID: "child-session", DelegationID: "task-1",
			Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated,
		}},
	}
	if _, _, err := resolveTrustedChildInputSource(active, session.ActorRef{Kind: session.ActorKindController, ID: "old-controller"}); err == nil {
		t.Fatal("stale controller source was accepted")
	}
	trusted, binding, err := resolveTrustedChildInputSource(active, session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-session"})
	if err != nil || binding == nil || trusted.ID != "child-1" {
		t.Fatalf("participant source = (%#v, %#v, %v)", trusted, binding, err)
	}
}

type runtimeChildInputRunner struct {
	mu          sync.Mutex
	spawnResult delegation.Result
	observer    agent.ChildActivityObserver
	target      agent.ChildEndpointRef
	request     agent.ChildInputRequest
	calls       int
}

type fastTerminalActivityRunner struct {
	observer      agent.ChildActivityObserver
	events        []agent.ChildActivityEvent
	preinstallErr error
}

func (r *fastTerminalActivityRunner) Spawn(ctx context.Context, spawn subagent.SpawnContext, req delegation.Request) (delegation.Anchor, delegation.Result, error) {
	return r.SpawnTarget(ctx, spawn, delegation.TargetRequest{Target: delegation.AgentTarget(req.Agent), Prompt: req.Prompt})
}

func (r *fastTerminalActivityRunner) SpawnTarget(_ context.Context, spawn subagent.SpawnContext, req delegation.TargetRequest) (delegation.Anchor, delegation.Result, error) {
	target := agent.ChildEndpointRef{
		ParticipantID: spawn.TaskID, SessionID: "child-fast-terminal", EndpointKey: spawn.TaskID,
		Role: spawn.Role, Placement: req.Target.Placement,
	}
	r.observer = spawn.ActivityObserver
	r.events = []agent.ChildActivityEvent{
		{
			Target: target, ActivityID: "initial-fast-activity", Cursor: 1, Initial: true,
			Frame: &stream.Frame{Event: &session.Event{
				Type: session.EventTypeAssistant, Visibility: session.VisibilityUIOnly,
				Text: "fast terminal output", Time: time.Now(),
			}},
		},
		{
			Target: target, ActivityID: "initial-fast-activity", Cursor: 2, Initial: true,
			Result: &delegation.Result{
				TaskID: spawn.TaskID, State: delegation.StateCompleted,
				Result: "fast terminal result", UpdatedAt: time.Now(),
			},
		},
	}
	r.preinstallErr = r.observer.ObserveChildActivity(context.Background(), r.events[0])
	return delegation.Anchor{
			TaskID: spawn.TaskID, SessionID: target.SessionID, AgentID: target.ParticipantID,
		}, delegation.Result{
			TaskID: spawn.TaskID, State: delegation.StateCompleted,
			Result: "fast terminal result", UpdatedAt: time.Now(),
		}, nil
}

func (*fastTerminalActivityRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	return delegation.Result{}, nil
}

func (*fastTerminalActivityRunner) Cancel(context.Context, delegation.Anchor) error { return nil }

func (r *fastTerminalActivityRunner) BindChildActivityObserver(_ context.Context, _ agent.ChildEndpointRef, _ uint64, observer agent.ChildActivityObserver) error {
	r.observer = observer
	return nil
}

type activityTaskStoreAudit struct {
	taskapi.Store
	cas taskapi.CASStore

	mu                      sync.Mutex
	puts                    int
	runningTerminalCursor   bool
	terminalCursorCommitted bool
}

type failOnceObservedTerminalStore struct {
	taskapi.Store
	cas    taskapi.CASStore
	failed chan struct{}
	once   sync.Once
}

func (s *failOnceObservedTerminalStore) Put(ctx context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	fail := false
	if req.Entry != nil && req.Entry.Kind == taskapi.KindSubagent && !req.Entry.Running {
		cursor, _ := taskInt64Value(req.Entry.Metadata[subagentActivityCursorMeta])
		if cursor < 2 {
			return s.cas.Put(ctx, req)
		}
		s.once.Do(func() {
			fail = true
			close(s.failed)
		})
	}
	if fail {
		return nil, errors.New("forced observed terminal persistence retry")
	}
	return s.cas.Put(ctx, req)
}

func (s *activityTaskStoreAudit) Put(ctx context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	if req.Entry != nil {
		cursor, _ := taskInt64Value(req.Entry.Metadata[subagentActivityCursorMeta])
		s.mu.Lock()
		if req.Entry.Kind == taskapi.KindSubagent {
			s.puts++
		}
		if cursor >= 2 && req.Entry.Running {
			s.runningTerminalCursor = true
		}
		if cursor >= 2 && !req.Entry.Running && req.Entry.State == taskapi.StateCompleted {
			s.terminalCursorCommitted = true
		}
		s.mu.Unlock()
	}
	return s.cas.Put(ctx, req)
}

func (r *runtimeChildInputRunner) Spawn(ctx context.Context, spawn subagent.SpawnContext, req delegation.Request) (delegation.Anchor, delegation.Result, error) {
	return r.SpawnTarget(ctx, spawn, delegation.TargetRequest{Target: delegation.AgentTarget(req.Agent), Prompt: req.Prompt})
}

func (r *runtimeChildInputRunner) SpawnTarget(_ context.Context, spawn subagent.SpawnContext, req delegation.TargetRequest) (delegation.Anchor, delegation.Result, error) {
	r.mu.Lock()
	r.observer = spawn.ActivityObserver
	r.mu.Unlock()
	return delegation.Anchor{
		TaskID: spawn.TaskID, SessionID: "child-1", AgentID: spawn.TaskID,
	}, delegation.CloneResult(r.spawnResult), nil
}

func (*runtimeChildInputRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	return delegation.Result{}, nil
}

func (*runtimeChildInputRunner) Cancel(context.Context, delegation.Anchor) error { return nil }

func (r *runtimeChildInputRunner) BindChildActivityObserver(_ context.Context, target agent.ChildEndpointRef, _ uint64, observer agent.ChildActivityObserver) error {
	r.mu.Lock()
	r.target = agent.NormalizeChildEndpointRef(target)
	r.observer = observer
	r.mu.Unlock()
	return nil
}

func (r *runtimeChildInputRunner) SubmitChildInput(_ context.Context, req agent.ChildInputRequest) (agent.ChildInputResult, error) {
	r.mu.Lock()
	r.request = agent.CloneChildInputRequest(req)
	r.calls++
	r.mu.Unlock()
	return agent.ChildInputResult{ActivityID: "activity-2", StartedActivity: true}, nil
}

func (r *runtimeChildInputRunner) publish(event agent.ChildActivityEvent) error {
	r.mu.Lock()
	observer := r.observer
	target := r.target
	r.mu.Unlock()
	event.Target = target
	return observer.ObserveChildActivity(context.Background(), event)
}

var (
	_ subagent.PlacementRunner          = (*runtimeChildInputRunner)(nil)
	_ agent.ChildInputRunner            = (*runtimeChildInputRunner)(nil)
	_ agent.ChildActivityObserverBinder = (*runtimeChildInputRunner)(nil)
)
