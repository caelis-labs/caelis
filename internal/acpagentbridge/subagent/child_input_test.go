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
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
)

type childInputTestEvent struct {
	ActivityID string
	Cursor     uint64
	Frame      *output.Event
	Result     *delegation.Result
}

type childInputTestSink struct {
	activityID string
	events     chan<- childInputTestEvent
	cursor     atomic.Uint64
}

func (s *childInputTestSink) ObserveTaskOutput(_ context.Context, event output.Event) error {
	if s == nil || s.events == nil || (event.Closed && event.Event == nil && event.Text == "") {
		return nil
	}
	canonical := session.CloneEvent(event.Event)
	if canonical == nil && event.Text != "" {
		canonical = &session.Event{Text: event.Text}
	}
	frame := &output.Event{
		Text: event.Text, State: event.State, Running: event.Running, Closed: event.Closed,
		ExitCode: event.ExitCode, Event: canonical, OccurredAt: event.OccurredAt,
	}
	s.events <- childInputTestEvent{ActivityID: s.activityID, Cursor: s.cursor.Add(1), Frame: frame}
	return nil
}

func (s *childInputTestSink) PublishSubagentCompletion(result delegation.Result) {
	if s == nil || s.events == nil {
		return
	}
	cloned := delegation.CloneResult(result)
	s.events <- childInputTestEvent{ActivityID: s.activityID, Cursor: s.cursor.Add(1), Result: &cloned}
}

var childInputTestActivity atomic.Uint64

func TestActiveChildInputPreservesIngressOrderAndPublishesAcceptedInput(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "active")
	events := make(chan childInputTestEvent, 8)
	spawn := childInputSpawnContext(t, "task-active-input", events)
	anchor, initial, err := runner.Spawn(ctx, spawn, delegation.Request{
		Agent: "helper", Prompt: "initial",
	})
	if err != nil || !initial.Running {
		t.Fatalf("Spawn() = (%#v, %v), want running prompt", initial, err)
	}
	consumeSpawnInitialInput(t, ctx, events, spawn.ActivityID, "initial")
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	target := run.slot.target
	result, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
		Target: target,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		Input:  "guide now",
	})
	if err != nil || result.StartedActivity || result.ActivityID == "" {
		t.Fatalf("SubmitChildInput() = (%#v, %v), want active steering", result, err)
	}
	frames, terminal := waitChildActivityFramesUntilTerminalFor(t, ctx, events, result.ActivityID)
	if len(frames) != 2 || frames[0].Event == nil || frames[1].Event == nil {
		t.Fatalf("active input frames = %#v, want Agent output then accepted input", frames)
	}
	// The provider emits output before acknowledging steering. It reaches the
	// Control observer immediately; acceptance is projected only after the reply.
	outputFrame, acceptedFrame := frames[0], frames[1]
	if communication := session.ProtocolAgentCommunicationOf(acceptedFrame.Event); communication == nil ||
		communication.Text != "guide now" || acceptedFrame.Event.Actor.ID != "controller-1" ||
		!strings.HasPrefix(acceptedFrame.Event.ID, "agent-input:"+result.ActivityID+":") {
		t.Fatalf("accepted active input = %#v, want stable standard Agent communication", acceptedFrame.Event)
	}
	if got := outputFrame.Event.Text; got != "steered output" {
		t.Fatalf("steering output = %q, want notification received before acceptance", got)
	}
	if terminal.ActivityID != result.ActivityID || terminal.Cursor <= 2 {
		t.Fatalf("terminal order = %#v after frames %#v", terminal, frames)
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
	events := make(chan childInputTestEvent, 8)
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
	result, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
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

func TestActiveChildInputUnsupportedErrorIsModelFacing(t *testing.T) {
	t.Parallel()

	run := &childRun{}
	slot := newChildSlot(agent.ChildEndpointRef{EndpointKey: "child"}, run)
	_, err := (&Runner{}).submitActiveChildInputLocked(
		context.Background(), slot, run, agent.ChildInputRequest{Target: slot.target}, nil,
	)
	if !errorcode.Is(err, errorcode.Unsupported) {
		t.Fatalf("error = %v, want unsupported", err)
	}
	want := "ACP Agent @child does not support additional messages while its current turn is running. You can send a message after this turn finishes."
	if got := err.Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestBuildAgentCommunicationPromptCarriesTrustedSource(t *testing.T) {
	t.Parallel()

	prompt := buildAgentCommunicationPrompt(agent.ChildInputRequest{
		Source: session.ActorRef{
			Kind: session.ActorKindController, ID: "sdk-kernel", Role: "kernel", Name: "local",
		},
		Input: "review this change",
	})
	if len(prompt) != 2 {
		t.Fatalf("prompt = %#v, want sender header and message", prompt)
	}
	var header client.TextContent
	var message client.TextContent
	if err := json.Unmarshal(prompt[0], &header); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(prompt[1], &message); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(header.Text, "[Internal agent message]") ||
		!strings.Contains(header.Text, "Sender: parent") ||
		strings.Contains(header.Text, "Reply-To:") ||
		strings.Contains(header.Text, "local") ||
		strings.Contains(header.Text, "kernel") ||
		message.Text != "review this change" {
		t.Fatalf("prompt header = %q message = %q", header.Text, message.Text)
	}
}

func TestRejectedChildSteeringReleasesOriginalActivityUpdates(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "failed-echo")
	events := make(chan childInputTestEvent, 8)
	spawn := childInputSpawnContext(t, "task-rejected-input", events)
	anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{
		Agent: "helper", Prompt: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	consumeSpawnInitialInput(t, ctx, events, spawn.ActivityID, "initial")
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
		Target: run.slot.target,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		Input:  "cannot inject",
	})
	if !errorcode.Is(err, errorcode.FailedPrecondition) {
		t.Fatalf("rejected SubmitChildInput() error = %v, want failed precondition", err)
	}
	communicationCount := 0
	var frame childInputTestEvent
	var terminal childInputTestEvent
	for terminal.Result == nil {
		select {
		case event := <-events:
			if event.Frame != nil {
				frame = event
				if session.ProtocolAgentCommunicationOf(event.Frame.Event) != nil {
					communicationCount++
				}
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
	if communicationCount != 0 {
		t.Fatalf("rejected input projected %d Agent communication events, want none", communicationCount)
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
	events := make(chan childInputTestEvent, 12)
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
	result, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
		Target: run.slot.target,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
		Input:  "second prompt",
	})
	if err != nil || !result.StartedActivity || result.ActivityID == "" || result.ActivityID == firstTerminal.ActivityID {
		t.Fatalf("SubmitChildInput() = (%#v, %v), want new prompt activity", result, err)
	}
	frames, terminal := waitChildActivityFramesUntilTerminalFor(t, ctx, events, result.ActivityID)
	if len(frames) != 2 || frames[0].Event == nil || frames[1].Event == nil {
		t.Fatalf("idle input frames = %#v, want accepted input and Agent output", frames)
	}
	acceptedFrame, outputFrame := frames[0], frames[1]
	if session.ProtocolAgentCommunicationOf(acceptedFrame.Event) == nil {
		acceptedFrame, outputFrame = outputFrame, acceptedFrame
	}
	if communication := session.ProtocolAgentCommunicationOf(acceptedFrame.Event); communication == nil ||
		communication.Text != "second prompt" || acceptedFrame.Event.Actor.ID != "controller-1" {
		t.Fatalf("accepted idle input = %#v, want standard Agent communication", acceptedFrame.Event)
	}
	if outputFrame.Event.Text != "prompt output 2" {
		t.Fatalf("idle prompt output = %#v", outputFrame)
	}
	if terminal.Result.State != delegation.StateCompleted {
		t.Fatalf("idle prompt terminal = %#v", terminal.Result)
	}
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptedChildInputDeduplicatesProviderEcho(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		mode string
		idle bool
		text string
	}{
		{name: "active", mode: "active-echo", text: "guide now"},
		{name: "idle", mode: "idle-echo", idle: true, text: "second prompt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			runner := childInputTestRunner(t, test.mode)
			events := make(chan childInputTestEvent, 12)
			spawn := childInputSpawnContext(t, "task-echo-"+test.name, events)
			anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{
				Agent: "helper", Prompt: "initial",
			})
			if err != nil {
				t.Fatal(err)
			}
			consumeSpawnInitialInput(t, ctx, events, spawn.ActivityID, "initial")
			if test.idle {
				waitChildActivityTerminal(t, ctx, events)
			}
			run, err := runner.lookup(anchor)
			if err != nil {
				t.Fatal(err)
			}
			result, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
				Target: run.slot.target,
				Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"},
				Input:  test.text,
			})
			if err != nil {
				t.Fatal(err)
			}
			frames, _ := waitChildActivityFramesUntilTerminalFor(t, ctx, events, result.ActivityID)
			communicationCount := 0
			for _, frame := range frames {
				if communication := session.ProtocolAgentCommunicationOf(frame.Event); communication != nil {
					communicationCount++
					if communication.Text != test.text {
						t.Fatalf("communication text = %q, want %q", communication.Text, test.text)
					}
				}
			}
			if communicationCount != 1 {
				t.Fatalf("Agent communication count = %d in %#v, want one", communicationCount, frames)
			}
			if err := runner.Quiesce(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIdleChildInputCancellationBeforeWriteRestoresCompleteRunState(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "idle")
	events := make(chan childInputTestEvent, 8)
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
	_, err = submitChildInputTest(runner, events, cancelled, agent.ChildInputRequest{
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
			events := make(chan childInputTestEvent, 16)
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
				started, inputErr := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
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
				_, waitErr := submitChildInputTest(runner, events, cancelledCtx, agent.ChildInputRequest{
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
				result, inputErr := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
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
	events := make(chan childInputTestEvent, 12)
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
	result, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
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

func TestResumeSetupUsesNewChildOutputBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "resume-update")
	defer func() { _ = runner.Quiesce(context.Background()) }()
	events := make(chan childInputTestEvent, 16)
	spawn := childInputSpawnContext(t, "task-resume-binding", events)
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
	run.state, run.running = delegation.StateFailed, false
	run.mu.Unlock()
	sink := &childInputTestSink{activityID: "resumed-activity", events: events}
	spawn.ActivityID, spawn.Output, spawn.Completion = sink.activityID, sink, sink
	if err := runner.BindChildEndpoint(ctx, run.slot.target, spawn); err != nil {
		t.Fatal(err)
	}
	result, err := runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "resume",
		ActivityID: sink.activityID, Output: sink, Completion: sink,
	})
	if err != nil || !result.StartedActivity {
		t.Fatalf("resume = %#v, %v", result, err)
	}
	if count := waitChildActivityTextBeforeTerminalFor(t, ctx, events, sink.activityID, "resume setup output"); count != 1 {
		t.Fatalf("new activity setup notifications = %d, want 1", count)
	}
}

func TestActiveChildInputKeepsProducerBindingBeforeRuntimeObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	runner := childInputTestRunner(t, "active")
	defer func() { _ = runner.Quiesce(context.Background()) }()
	events := make(chan childInputTestEvent, 16)
	spawn := childInputSpawnContext(t, "task-active-binding", events)
	anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	unexpected := make(chan childInputTestEvent, 16)
	candidate := &childInputTestSink{activityID: "not-started", events: unexpected}
	spawn.ActivityID, spawn.Output, spawn.Completion = candidate.activityID, candidate, candidate
	if err := runner.BindChildEndpoint(ctx, run.slot.target, spawn); err != nil {
		t.Fatal(err)
	}
	result, err := runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "steer",
		ActivityID: candidate.activityID, Output: candidate, Completion: candidate,
	})
	if err != nil || result.StartedActivity || result.ActivityID == candidate.activityID {
		t.Fatalf("steering = %#v, %v", result, err)
	}
	waitChildActivityTerminalFor(t, ctx, events, result.ActivityID)
	select {
	case event := <-unexpected:
		t.Fatalf("unstarted candidate received producer output: %#v", event)
	default:
	}
}

func TestRehydratedEndpointResumesWithoutProcessLocalRun(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstRunner := childInputTestRunner(t, "idle")
	events := make(chan childInputTestEvent, 12)
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
	result, err := submitChildInputTest(rehydrated, events, ctx, agent.ChildInputRequest{
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
	events := make(chan childInputTestEvent, 12)
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
	first, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
		Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "prompt two",
	})
	if err != nil || !first.StartedActivity {
		t.Fatalf("first input = (%#v, %v)", first, err)
	}
	second, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
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
	events := make(chan childInputTestEvent, 12)
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
	_, err = submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
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
	events := make(chan childInputTestEvent, 12)
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
	_, err = submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
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
	events := make(chan childInputTestEvent, 12)
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
	started, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
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

func childInputSpawnContext(t *testing.T, taskID string, events chan<- childInputTestEvent) tasksubagent.SpawnContext {
	t.Helper()
	activityID := fmt.Sprintf("spawn-activity-%d", childInputTestActivity.Add(1))
	sink := &childInputTestSink{activityID: activityID, events: events}
	return tasksubagent.SpawnContext{
		SessionRef: session.SessionRef{SessionID: "parent-session"},
		TaskID:     taskID, CWD: t.TempDir(), Role: session.ParticipantRoleDelegated,
		ActivityID: activityID, Output: sink, Completion: sink,
	}
}

func submitChildInputTest(runner *Runner, events chan<- childInputTestEvent, ctx context.Context, req agent.ChildInputRequest) (agent.ChildInputResult, error) {
	activityID := strings.TrimSpace(req.ActivityID)
	if slot, err := runner.lookupChildSlot(req.Target); err == nil {
		run := slot.currentRun()
		running := false
		if run != nil {
			run.mu.RLock()
			running = run.running
			run.mu.RUnlock()
		}
		if running {
			activityID = slot.activityCheckpoint().activityID
		}
	}
	if activityID == "" {
		activityID = fmt.Sprintf("input-activity-%d", childInputTestActivity.Add(1))
	}
	sink := &childInputTestSink{activityID: activityID, events: events}
	req.ActivityID = activityID
	req.Output = sink
	req.Completion = sink
	return runner.SubmitChildInput(ctx, req)
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

func consumeSpawnInitialInput(t *testing.T, ctx context.Context, events <-chan childInputTestEvent, activityID, wantText string) {
	t.Helper()
	for {
		select {
		case event := <-events:
			if event.ActivityID != activityID {
				continue
			}
			if event.Result != nil {
				t.Fatalf("Spawn completed before initial input projection: %#v", event.Result)
			}
			if event.Frame == nil || event.Frame.Event == nil {
				continue
			}
			communication := session.ProtocolAgentCommunicationOf(event.Frame.Event)
			if communication == nil || communication.Text != wantText || event.Frame.Event.Actor != session.ParentCommunicationActor() {
				t.Fatalf("Spawn initial input = %#v, want typed parent message %q", event.Frame.Event, wantText)
			}
			return
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
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

func waitChildActivityTerminal(t *testing.T, ctx context.Context, events <-chan childInputTestEvent) childInputTestEvent {
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

func waitChildActivityTerminalFor(t *testing.T, ctx context.Context, events <-chan childInputTestEvent, activityID string) childInputTestEvent {
	t.Helper()
	for {
		event := waitChildActivityTerminal(t, ctx, events)
		if event.ActivityID == activityID {
			return event
		}
	}
}

func waitChildActivityFramesUntilTerminalFor(
	t *testing.T,
	ctx context.Context,
	events <-chan childInputTestEvent,
	activityID string,
) ([]output.Event, childInputTestEvent) {
	t.Helper()
	var frames []output.Event
	for {
		select {
		case event := <-events:
			if event.ActivityID != activityID {
				continue
			}
			if event.Frame != nil {
				frames = append(frames, *event.Frame)
			}
			if event.Result != nil {
				return frames, event
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func waitChildActivityTextBeforeTerminal(t *testing.T, ctx context.Context, events <-chan childInputTestEvent, text string) int {
	t.Helper()
	return waitChildActivityTextBeforeTerminalFor(t, ctx, events, "", text)
}

func waitChildActivityTextBeforeTerminalFor(t *testing.T, ctx context.Context, events <-chan childInputTestEvent, activityID string, text string) int {
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
				AgentCapabilities: client.AgentCapabilities{
					PromptCapabilities:  acpsdk.PromptCapabilities{Image: true},
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
			if err := json.Unmarshal(message.Params, &request); err != nil || request.MethodId != "agent-login" {
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
				return client.PromptResponse{StopReason: string(acpsdk.StopReasonEndTurn)}, nil
			}
			if (mode == "active" || mode == "active-echo" || mode == "unknown" || mode == "image" || mode == "failed" || mode == "failed-echo" || mode == "malformed-steering") && count == 1 || mode == "second-active" && count == 2 {
				select {
				case <-steered:
				case <-time.After(4 * time.Second):
					return nil, &jsonrpc.RPCError{Code: -32000, Message: "steering timeout"}
				}
			} else if count > 1 {
				if mode == "idle-echo" {
					if err := childInputNotifyUpdate(conn, client.UpdateUserMessage, "second prompt"); err != nil {
						return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
					}
				}
				if err := childInputNotify(conn, fmt.Sprintf("prompt output %d", count)); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
				}
				if mode == "malformed-idle" {
					return map[string]any{"stopReason": 42}, nil
				}
			}
			return client.PromptResponse{StopReason: string(acpsdk.StopReasonEndTurn)}, nil
		case client.MethodSessionSteering:
			if strings.HasPrefix(mode, "auth-") {
				if err := appendChildInputTestFile(os.Getenv("CAELIS_ACP_CHILD_INPUT_STEER_MARKER"), "steer\n"); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
				}
			}
			output := "steered output"
			if mode == "active-echo" || mode == "failed-echo" {
				if err := childInputNotifyUpdate(conn, client.UpdateUserMessage, "guide now"); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
				}
			}
			switch mode {
			case "unknown":
				output = "ambiguous output"
			case "failed", "failed-echo":
				output = "not injected output"
			case "malformed-steering":
				output = "malformed steering output"
			case "image":
				var request client.SessionSteeringRequest
				if err := json.Unmarshal(message.Params, &request); err != nil || len(request.Prompt) != 2 {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "missing image prompt"}
				}
				var header client.TextContent
				if err := json.Unmarshal(request.Prompt[0], &header); err != nil || !strings.Contains(header.Text, "[Internal agent message]") {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "missing Agent sender header"}
				}
				var image struct {
					Type     string `json:"type"`
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
					Name     string `json:"name"`
				}
				if err := json.Unmarshal(request.Prompt[1], &image); err != nil || image.Type != "image" || image.MimeType != "image/png" || image.Data != "aW1hZ2U=" || image.Name != "guide.png" {
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
			case "failed", "failed-echo":
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
	return childInputNotifyUpdate(conn, client.UpdateAgentMessage, text)
}

func childInputNotifyUpdate(conn *jsonrpc.Conn, updateType string, text string) error {
	return conn.Notify(client.MethodSessionUpdate, client.SessionNotification{
		SessionID: "child-input-session",
		Update: jsonrpc.MustMarshalRaw(client.ContentChunk{
			SessionUpdate: updateType,
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
