package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type childActivityObserverFunc func(context.Context, agent.ChildActivityEvent) error

func (fn childActivityObserverFunc) ObserveChildActivity(ctx context.Context, event agent.ChildActivityEvent) error {
	return fn(ctx, event)
}

func TestActiveChildInputUsesSteeringAndReleasesBufferedUpdate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "active")
	events := make(chan agent.ChildActivityEvent, 8)
	anchor, initial, err := runner.Spawn(ctx, childInputSpawnContext(t, "task-active-input", events), delegation.Request{
		Agent: "helper", Prompt: "initial",
	})
	if err != nil || !initial.Running {
		t.Fatalf("Spawn() = (%#v, %v), want running prompt", initial, err)
	}
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	target := run.slot.target
	result, err := runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: target,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		Input:  "guide now",
	})
	if err != nil || result.StartedActivity || result.ActivityID == "" {
		t.Fatalf("SubmitChildInput() = (%#v, %v), want active steering", result, err)
	}
	var frameEvent agent.ChildActivityEvent
	var terminal agent.ChildActivityEvent
	for terminal.Result == nil {
		select {
		case event := <-events:
			if event.Frame != nil {
				frameEvent = event
			}
			if event.Result != nil {
				terminal = event
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if frameEvent.Frame == nil || frameEvent.ActivityID != result.ActivityID {
		t.Fatalf("buffered steering frame = %#v, want original activity %q", frameEvent, result.ActivityID)
	}
	if got := frameEvent.Frame.Event.Text; got != "steered output" {
		t.Fatalf("steering output = %q, want buffered notification", got)
	}
	if terminal.ActivityID != result.ActivityID || terminal.Cursor <= frameEvent.Cursor {
		t.Fatalf("terminal order = %#v after frame %#v", terminal, frameEvent)
	}
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestActiveChildInputPreservesStandardACPImageContent(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "image")
	events := make(chan agent.ChildActivityEvent, 8)
	anchor, _, err := runner.Spawn(ctx, childInputSpawnContext(t, "task-image-input", events), delegation.Request{
		Agent: "helper", Prompt: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: run.slot.target,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		ContentParts: []model.ContentPart{{
			Type: model.ContentPartImage, MimeType: "image/png", Data: "aW1hZ2U=", FileName: "guide.png",
		}},
	})
	if err != nil || result.StartedActivity {
		t.Fatalf("image SubmitChildInput() = (%#v, %v), want active steering", result, err)
	}
	waitChildActivityTerminalFor(t, ctx, events, result.ActivityID)
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedChildSteeringReleasesOriginalActivityUpdates(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "failed")
	events := make(chan agent.ChildActivityEvent, 8)
	anchor, _, err := runner.Spawn(ctx, childInputSpawnContext(t, "task-rejected-input", events), delegation.Request{
		Agent: "helper", Prompt: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: run.slot.target,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		Input:  "cannot inject",
	})
	if !errorcode.Is(err, errorcode.FailedPrecondition) {
		t.Fatalf("rejected SubmitChildInput() error = %v, want failed precondition", err)
	}
	var frame agent.ChildActivityEvent
	var terminal agent.ChildActivityEvent
	for terminal.Result == nil {
		select {
		case event := <-events:
			if event.Frame != nil {
				frame = event
			}
			if event.Result != nil {
				terminal = event
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if frame.Frame == nil || frame.Frame.Event.Text != "not injected output" || terminal.Cursor <= frame.Cursor {
		t.Fatalf("released rejection events = frame %#v terminal %#v", frame, terminal)
	}
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIdleChildInputStartsPromptOnExistingSession(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "idle")
	events := make(chan agent.ChildActivityEvent, 12)
	spawn := childInputSpawnContext(t, "task-idle-input", events)
	anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	firstTerminal := waitChildActivityTerminal(t, ctx, events)
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: run.slot.target,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		Input:  "second prompt",
	})
	if err != nil || !result.StartedActivity || result.ActivityID == "" || result.ActivityID == firstTerminal.ActivityID {
		t.Fatalf("SubmitChildInput() = (%#v, %v), want new prompt activity", result, err)
	}
	var output agent.ChildActivityEvent
	var terminal agent.ChildActivityEvent
	for terminal.Result == nil {
		select {
		case event := <-events:
			if event.ActivityID != result.ActivityID {
				continue
			}
			if event.Frame != nil {
				output = event
			}
			if event.Result != nil {
				terminal = event
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if output.Frame == nil || output.Frame.Event.Text != "prompt output 2" {
		t.Fatalf("idle prompt output = %#v", output)
	}
	if terminal.Result.State != delegation.StateCompleted {
		t.Fatalf("idle prompt terminal = %#v", terminal.Result)
	}
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIdleChildInputCancellationBeforeWriteRestoresCompleteRunState(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "idle")
	events := make(chan agent.ChildActivityEvent, 8)
	anchor, _, err := runner.Spawn(ctx, childInputSpawnContext(t, "task-idle-rollback", events), delegation.Request{
		Agent: "helper", Prompt: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitChildActivityTerminal(t, ctx, events)
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	run.mu.RLock()
	initialDone := run.done
	run.mu.RUnlock()
	select {
	case <-initialDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	run.mu.Lock()
	run.outputPreview = "previous preview"
	run.result = "previous result"
	run.agentText = "previous assistant"
	run.actionSummary.observeAssistant("previous-message", "previous assistant")
	run.finalAssistant.ObserveFrame("previous-message", "previous assistant")
	wantRun := checkpointIdleRun(run)
	run.mu.Unlock()
	wantActivity := run.slot.activityCheckpoint()

	cancelled, cancelInput := context.WithCancel(context.Background())
	cancelInput()
	_, err = runner.SubmitChildInput(cancelled, agent.ChildInputRequest{
		Target: run.slot.target,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		Input:  "must not be written",
	})
	if !errors.Is(err, context.Canceled) || errorcode.Is(err, errorcode.UnknownOutcome) {
		t.Fatalf("SubmitChildInput() error = %v, want proven pre-write cancellation", err)
	}
	run.mu.RLock()
	gotRun := checkpointIdleRun(run)
	run.mu.RUnlock()
	if !reflect.DeepEqual(gotRun, wantRun) {
		t.Fatalf("idle run after pre-write cancellation = %#v, want %#v", gotRun, wantRun)
	}
	if gotActivity := run.slot.activityCheckpoint(); !reflect.DeepEqual(gotActivity, wantActivity) {
		t.Fatalf("activity after pre-write cancellation = %#v, want %#v", gotActivity, wantActivity)
	}
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPromptAuthenticationRetryKeepsChildInputAdmissionFence(t *testing.T) {
	for _, mode := range []string{"auth-initial", "auth-idle"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			stateDir := t.TempDir()
			authReady := stateDir + "/auth-ready"
			authRelease := stateDir + "/auth-release"
			steeringMarker := stateDir + "/steering"
			runner := childInputAuthTestRunner(t, mode, authReady, authRelease, steeringMarker)
			events := make(chan agent.ChildActivityEvent, 16)
			anchor, _, err := runner.Spawn(ctx, childInputSpawnContext(t, "task-"+mode, events), delegation.Request{
				Agent: "helper", Prompt: "initial",
			})
			if err != nil {
				t.Fatal(err)
			}
			run, err := runner.lookup(anchor)
			if err != nil {
				t.Fatal(err)
			}
			activityID := run.slot.activityCheckpoint().activityID
			if mode == "auth-idle" {
				waitChildActivityTerminalFor(t, ctx, events, activityID)
				started, inputErr := runner.SubmitChildInput(ctx, agent.ChildInputRequest{
					Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "requires authentication",
				})
				if inputErr != nil || !started.StartedActivity {
					t.Fatalf("authenticated idle SubmitChildInput() = (%#v, %v)", started, inputErr)
				}
				activityID = started.ActivityID
			}
			waitForChildInputFile(t, ctx, authReady)
			waitForPromptDispatchFence(t, ctx, run.slot)

			cancelledCtx, cancelWaiter := context.WithCancel(ctx)
			cancelledResult := make(chan error, 1)
			go func() {
				_, waitErr := runner.SubmitChildInput(cancelledCtx, agent.ChildInputRequest{
					Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "cancel this waiter",
				})
				cancelledResult <- waitErr
			}()
			time.Sleep(20 * time.Millisecond)
			cancelWaiter()
			select {
			case waitErr := <-cancelledResult:
				if !errors.Is(waitErr, context.Canceled) {
					t.Fatalf("cancelled waiter error = %v, want context canceled", waitErr)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}

			liveResult := make(chan struct {
				result agent.ChildInputResult
				err    error
			}, 1)
			go func() {
				result, inputErr := runner.SubmitChildInput(ctx, agent.ChildInputRequest{
					Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "steer after authenticated prompt",
				})
				liveResult <- struct {
					result agent.ChildInputResult
					err    error
				}{result: result, err: inputErr}
			}()
			select {
			case got := <-liveResult:
				t.Fatalf("input crossed authenticated prompt fence early: (%#v, %v)", got.result, got.err)
			case <-time.After(75 * time.Millisecond):
			}
			if _, statErr := os.Stat(steeringMarker); !os.IsNotExist(statErr) {
				t.Fatalf("steering reached peer before authenticated prompt retry: %v", statErr)
			}
			if err := os.WriteFile(authRelease, []byte("release"), 0o600); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-liveResult:
				if got.err != nil || got.result.StartedActivity || got.result.ActivityID != activityID {
					t.Fatalf("post-auth steering = (%#v, %v), want original activity %q", got.result, got.err, activityID)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			waitChildActivityTerminalFor(t, ctx, events, activityID)
			waitForChildInputFile(t, ctx, steeringMarker)
			time.Sleep(50 * time.Millisecond)
			marker, err := os.ReadFile(steeringMarker)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(string(marker), "steer\n"); got != 1 {
				t.Fatalf("peer steering count = %d, want one live input and no cancelled late write", got)
			}
			if err := runner.Quiesce(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGenericReconnectDoesNotRequirePrivateMessageCapability(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "idle")
	events := make(chan agent.ChildActivityEvent, 12)
	anchor, _, err := runner.Spawn(ctx, childInputSpawnContext(t, "task-reconnect-input", events), delegation.Request{
		Agent: "helper", Prompt: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitChildActivityTerminal(t, ctx, events)
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.client.Close(ctx)
	run.mu.Lock()
	run.state = delegation.StateFailed
	run.running = false
	run.mu.Unlock()
	result, err := runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: run.slot.target,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		Input:  "resume prompt",
	})
	if err != nil || !result.StartedActivity {
		t.Fatalf("SubmitChildInput() after reconnect = (%#v, %v)", result, err)
	}
	terminal := waitChildActivityTerminalFor(t, ctx, events, result.ActivityID)
	if terminal.Result == nil || terminal.Result.State != delegation.StateCompleted {
		t.Fatalf("reconnected terminal = %#v", terminal)
	}
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRehydratedEndpointResumesWithoutProcessLocalRun(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstRunner := childInputTestRunner(t, "idle")
	events := make(chan agent.ChildActivityEvent, 12)
	spawn := childInputSpawnContext(t, "task-rehydrated-input", events)
	anchor, _, err := firstRunner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	waitChildActivityTerminal(t, ctx, events)
	firstRun, err := firstRunner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	target := firstRun.slot.target
	if err := firstRunner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}

	rehydrated := childInputTestRunner(t, "idle")
	if err := rehydrated.BindChildEndpoint(ctx, target, spawn); err != nil {
		t.Fatal(err)
	}
	result, err := rehydrated.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "after activation",
	})
	if err != nil || !result.StartedActivity {
		t.Fatalf("rehydrated SubmitChildInput() = (%#v, %v)", result, err)
	}
	terminal := waitChildActivityTerminalFor(t, ctx, events, result.ActivityID)
	if terminal.Result == nil || terminal.Result.State != delegation.StateCompleted {
		t.Fatalf("rehydrated terminal = %#v", terminal)
	}
	if err := rehydrated.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIdlePromptOwnershipMakesImmediateNextInputSteering(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "second-active")
	events := make(chan agent.ChildActivityEvent, 12)
	anchor, _, err := runner.Spawn(ctx, childInputSpawnContext(t, "task-prompt-then-steer", events), delegation.Request{
		Agent: "helper", Prompt: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitChildActivityTerminal(t, ctx, events)
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "prompt two",
	})
	if err != nil || !first.StartedActivity {
		t.Fatalf("first input = (%#v, %v)", first, err)
	}
	second, err := runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "steer two",
	})
	if err != nil || second.StartedActivity || second.ActivityID != first.ActivityID {
		t.Fatalf("immediate second input = (%#v, %v), want same-activity steering", second, err)
	}
	waitChildActivityTerminalFor(t, ctx, events, first.ActivityID)
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownSteeringOutcomeDropsBufferedRemoteTurnOutput(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "unknown")
	events := make(chan agent.ChildActivityEvent, 12)
	anchor, _, err := runner.Spawn(ctx, childInputSpawnContext(t, "task-unknown-steering", events), delegation.Request{
		Agent: "helper", Prompt: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "ambiguous steer",
	})
	if !errorcode.Is(err, errorcode.UnknownOutcome) {
		t.Fatalf("SubmitChildInput() error = %v, want unknown outcome", err)
	}
	terminal := waitChildActivityTerminal(t, ctx, events)
	if terminal.Result == nil || terminal.Result.State != delegation.StateUnknownOutcome {
		t.Fatalf("unknown terminal = %#v", terminal)
	}
	select {
	case event := <-events:
		if event.Frame != nil && event.Frame.Event != nil && event.Frame.Event.Text == "ambiguous output" {
			t.Fatalf("ambiguous remote-Turn output was released: %#v", event)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func TestMalformedSteeringResultIsUnknownAndDropsBufferedOutput(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "malformed-steering")
	defer func() { _ = runner.Quiesce(context.Background()) }()
	events := make(chan agent.ChildActivityEvent, 12)
	anchor, _, err := runner.Spawn(ctx, childInputSpawnContext(t, "task-malformed-steering", events), delegation.Request{
		Agent: "helper", Prompt: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: run.slot.target,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		Input:  "malformed steering result",
	})
	if !errorcode.Is(err, errorcode.UnknownOutcome) {
		t.Fatalf("SubmitChildInput() error = %v, want unknown outcome", err)
	}
	terminal := waitChildActivityTerminal(t, ctx, events)
	if terminal.Result == nil || terminal.Result.State != delegation.StateUnknownOutcome {
		t.Fatalf("malformed steering terminal = %#v", terminal)
	}
	select {
	case event := <-events:
		if event.Frame != nil && event.Frame.Event != nil && event.Frame.Event.Text == "malformed steering output" {
			t.Fatalf("malformed steering output was released: %#v", event)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func TestMalformedIdlePromptResultFinishesUnknownAndIsolatesTransport(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "malformed-idle")
	defer func() { _ = runner.Quiesce(context.Background()) }()
	events := make(chan agent.ChildActivityEvent, 12)
	anchor, _, err := runner.Spawn(ctx, childInputSpawnContext(t, "task-malformed-idle", events), delegation.Request{
		Agent: "helper", Prompt: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitChildActivityTerminal(t, ctx, events)
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	started, err := runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: run.slot.target,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		Input:  "prompt with malformed terminal result",
	})
	if err != nil || !started.StartedActivity {
		t.Fatalf("SubmitChildInput() = (%#v, %v), want started activity", started, err)
	}
	terminal := waitChildActivityTerminalFor(t, ctx, events, started.ActivityID)
	if terminal.Result == nil || terminal.Result.State != delegation.StateUnknownOutcome {
		t.Fatalf("malformed prompt terminal = %#v, want unknown", terminal)
	}
	run.mu.RLock()
	state := run.state
	remote := run.client
	run.mu.RUnlock()
	if state != delegation.StateUnknownOutcome || remote == nil {
		t.Fatalf("malformed prompt run state=%q client=%v", state, remote != nil)
	}
}

func TestInitializeUpdateUsesActivityObserverFromFirstNotification(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "initialize-update")
	defer func() { _ = runner.Quiesce(context.Background()) }()
	events := make(chan agent.ChildActivityEvent, 12)
	legacy := &recordingStreams{}
	spawn := childInputSpawnContext(t, "task-initialize-update", events)
	spawn.Streams = legacy
	_, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	count := waitChildActivityTextBeforeTerminal(t, ctx, events, "initialize setup output")
	if count != 1 || len(legacy.frames) != 0 {
		t.Fatalf("initialize output count=%d legacy frames=%d, want sole observer delivery", count, len(legacy.frames))
	}
}

func TestResumeUpdateUsesActivityObserverFromFirstNotification(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "resume-update")
	defer func() { _ = runner.Quiesce(context.Background()) }()
	events := make(chan agent.ChildActivityEvent, 16)
	legacy := &recordingStreams{}
	spawn := childInputSpawnContext(t, "task-resume-update", events)
	spawn.Streams = legacy
	anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	waitChildActivityTerminal(t, ctx, events)
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.client.Close(ctx)
	run.mu.Lock()
	run.state = delegation.StateFailed
	run.running = false
	run.mu.Unlock()
	started, err := runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: run.slot.target,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		Input:  "resume with setup output",
	})
	if err != nil || !started.StartedActivity {
		t.Fatalf("SubmitChildInput() = (%#v, %v)", started, err)
	}
	count := waitChildActivityTextBeforeTerminalFor(t, ctx, events, started.ActivityID, "resume setup output")
	if count != 1 || len(legacy.frames) != 0 {
		t.Fatalf("resume output count=%d legacy frames=%d, want sole observer delivery", count, len(legacy.frames))
	}
}

func childInputSpawnContext(t *testing.T, taskID string, events chan<- agent.ChildActivityEvent) tasksubagent.SpawnContext {
	t.Helper()
	return tasksubagent.SpawnContext{
		SessionRef: session.SessionRef{SessionID: "parent-session"},
		TaskID:     taskID, CWD: t.TempDir(), Role: session.ParticipantRoleDelegated,
		ActivityObserver: childActivityObserverFunc(func(_ context.Context, event agent.ChildActivityEvent) error {
			events <- agent.CloneChildActivityEvent(event)
			return nil
		}),
	}
}

func childInputTestRunner(t *testing.T, mode string) *Runner {
	t.Helper()
	return newChildInputTestRunner(t, mode, nil, controlagents.Authentication{})
}

func childInputAuthTestRunner(t *testing.T, mode string, ready string, release string, steering string) *Runner {
	t.Helper()
	return newChildInputTestRunner(t, mode, map[string]string{
		"CAELIS_ACP_CHILD_INPUT_AUTH_READY":   ready,
		"CAELIS_ACP_CHILD_INPUT_AUTH_RELEASE": release,
		"CAELIS_ACP_CHILD_INPUT_STEER_MARKER": steering,
	}, controlagents.Authentication{MethodID: "agent-login", Type: controlagents.AuthenticationAgent})
}

func newChildInputTestRunner(t *testing.T, mode string, extraEnv map[string]string, authentication controlagents.Authentication) *Runner {
	t.Helper()
	env := map[string]string{"CAELIS_ACP_CHILD_INPUT_HELPER": "1", "CAELIS_ACP_CHILD_INPUT_MODE": mode}
	for key, value := range extraEnv {
		env[key] = value
	}
	registry, err := NewRegistry([]AgentConfig{{
		Name: "helper", Command: os.Args[0],
		Args: []string{"-test.run=TestChildInputHelperProcess", "--"},
		Env:  env, Authentication: authentication,
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func waitForPromptDispatchFence(t *testing.T, ctx context.Context, slot *childSlot) {
	t.Helper()
	for {
		if slot != nil && slot.pendingPromptDispatch() != nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func waitForChildInputFile(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func waitChildActivityTerminal(t *testing.T, ctx context.Context, events <-chan agent.ChildActivityEvent) agent.ChildActivityEvent {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event.Result != nil {
				return event
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func waitChildActivityTerminalFor(t *testing.T, ctx context.Context, events <-chan agent.ChildActivityEvent, activityID string) agent.ChildActivityEvent {
	t.Helper()
	for {
		event := waitChildActivityTerminal(t, ctx, events)
		if event.ActivityID == activityID {
			return event
		}
	}
}

func waitChildActivityTextBeforeTerminal(t *testing.T, ctx context.Context, events <-chan agent.ChildActivityEvent, text string) int {
	t.Helper()
	return waitChildActivityTextBeforeTerminalFor(t, ctx, events, "", text)
}

func waitChildActivityTextBeforeTerminalFor(t *testing.T, ctx context.Context, events <-chan agent.ChildActivityEvent, activityID string, text string) int {
	t.Helper()
	count := 0
	for {
		select {
		case event := <-events:
			if activityID != "" && event.ActivityID != activityID {
				continue
			}
			if event.Frame != nil && event.Frame.Event != nil && event.Frame.Event.Text == text {
				count++
			}
			if event.Result != nil {
				return count
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestChildInputHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_CHILD_INPUT_HELPER") != "1" {
		return
	}
	mode := os.Getenv("CAELIS_ACP_CHILD_INPUT_MODE")
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	var mu sync.Mutex
	promptCount := 0
	authenticated := false
	steered := make(chan struct{})
	var steerOnce sync.Once
	err := conn.Serve(context.Background(), func(_ context.Context, message jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch message.Method {
		case client.MethodInitialize:
			if mode == "initialize-update" {
				if err := childInputNotify(conn, "initialize setup output"); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
				}
			}
			response := client.InitializeResponse{
				ProtocolVersion: 1,
				Meta: map[string]json.RawMessage{
					client.SessionSteeringMetaKey: json.RawMessage(`{"supported":true}`),
				},
				AgentCapabilities: schema.AgentCapabilities{
					PromptCapabilities:  schema.PromptCapabilities{Image: true},
					SessionCapabilities: map[string]json.RawMessage{"resume": json.RawMessage(`{}`)},
				},
			}
			if strings.HasPrefix(mode, "auth-") {
				response.AuthMethods = []json.RawMessage{json.RawMessage(`{"id":"agent-login","name":"Agent login"}`)}
			}
			return response, nil
		case client.MethodSessionNew:
			return client.NewSessionResponse{SessionID: "child-input-session"}, nil
		case client.MethodSessionResume:
			if mode == "resume-update" {
				if err := childInputNotify(conn, "resume setup output"); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
				}
			}
			return client.ResumeSessionResponse{}, nil
		case client.MethodAuthenticate:
			if !strings.HasPrefix(mode, "auth-") {
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
			var request client.AuthenticateRequest
			if err := json.Unmarshal(message.Params, &request); err != nil || request.MethodID != "agent-login" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected authentication request"}
			}
			if err := os.WriteFile(os.Getenv("CAELIS_ACP_CHILD_INPUT_AUTH_READY"), []byte("ready"), 0o600); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
			}
			if err := waitForChildInputRelease(os.Getenv("CAELIS_ACP_CHILD_INPUT_AUTH_RELEASE")); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
			}
			mu.Lock()
			authenticated = true
			mu.Unlock()
			return client.AuthenticateResponse{}, nil
		case client.MethodSessionPrompt:
			mu.Lock()
			promptCount++
			count := promptCount
			isAuthenticated := authenticated
			mu.Unlock()
			if strings.HasPrefix(mode, "auth-") {
				authPrompt := 1
				if mode == "auth-idle" {
					authPrompt = 2
				}
				if count == authPrompt && !isAuthenticated {
					return nil, &jsonrpc.RPCError{Code: client.ErrorCodeAuthRequired, Message: "Authentication required"}
				}
				if count == authPrompt+1 {
					select {
					case <-steered:
					case <-time.After(4 * time.Second):
						return nil, &jsonrpc.RPCError{Code: -32000, Message: "post-auth steering timeout"}
					}
				}
				return client.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
			}
			if (mode == "active" || mode == "unknown" || mode == "image" || mode == "failed" || mode == "malformed-steering") && count == 1 || mode == "second-active" && count == 2 {
				select {
				case <-steered:
				case <-time.After(4 * time.Second):
					return nil, &jsonrpc.RPCError{Code: -32000, Message: "steering timeout"}
				}
			} else if count > 1 {
				if err := childInputNotify(conn, fmt.Sprintf("prompt output %d", count)); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
				}
				if mode == "malformed-idle" {
					return map[string]any{"stopReason": 42}, nil
				}
			}
			return client.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
		case client.MethodSessionSteering:
			if strings.HasPrefix(mode, "auth-") {
				if err := appendChildInputTestFile(os.Getenv("CAELIS_ACP_CHILD_INPUT_STEER_MARKER"), "steer\n"); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
				}
			}
			output := "steered output"
			switch mode {
			case "unknown":
				output = "ambiguous output"
			case "failed":
				output = "not injected output"
			case "malformed-steering":
				output = "malformed steering output"
			case "image":
				var request client.SessionSteeringRequest
				if err := json.Unmarshal(message.Params, &request); err != nil || len(request.Prompt) != 1 {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "missing image prompt"}
				}
				var image client.ImageContent
				if err := json.Unmarshal(request.Prompt[0], &image); err != nil || image.Type != "image" || image.MimeType != "image/png" || image.Data != "aW1hZ2U=" || image.Name != "guide.png" {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "invalid image prompt"}
				}
			}
			if err := childInputNotify(conn, output); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
			}
			steerOnce.Do(func() { close(steered) })
			if mode == "malformed-steering" {
				return map[string]any{"outcome": 42}, nil
			}
			outcome := client.SessionSteeringInjected
			switch mode {
			case "unknown":
				outcome = client.SessionSteeringStartedNewTurn
			case "failed":
				outcome = client.SessionSteeringFailed
			}
			return client.SessionSteeringResponse{Outcome: outcome}, nil
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func childInputNotify(conn *jsonrpc.Conn, text string) error {
	return conn.Notify(client.MethodSessionUpdate, client.SessionNotification{
		SessionID: "child-input-session",
		Update: jsonrpc.MustMarshalRaw(client.ContentChunk{
			SessionUpdate: client.UpdateAgentMessage,
			MessageID:     "message-" + text,
			Content:       jsonrpc.MustMarshalRaw(client.TextContent{Type: "text", Text: text}),
		}),
	})
}

func waitForChildInputRelease(path string) error {
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for auth release")
}

func appendChildInputTestFile(path string, text string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(text); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
