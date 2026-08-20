package runtime

import (
	"context"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type controllerRecoveryFunc func(context.Context, controller.RecoveryRequest) (session.Session, error)

func (f controllerRecoveryFunc) ReattachController(ctx context.Context, req controller.RecoveryRequest) (session.Session, error) {
	return f(ctx, req)
}

func TestRuntimeMainControllerSteeringWaitsForAdmissionAndCommitsCanonicalInput(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sessions, active := newTestSessionService(t, "main-controller-steering")
	var err error
	active, err = sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: active.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "controller-1", AgentName: "remote-main",
			EpochID: "epoch-1", RemoteSessionID: "remote-session-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	controllerHandle := newTestControllerTurnHandle(nil)
	runTurnStarted := make(chan struct{})
	releaseRunTurn := make(chan struct{})
	steerStarted := make(chan controller.ControllerSteerRequest, 1)
	backend := steeringACPController{
		stubACPController: stubACPController{runTurn: func(context.Context, controller.TurnRequest) (controller.TurnResult, error) {
			close(runTurnStarted)
			<-releaseRunTurn
			return controller.TurnResult{Handle: controllerHandle}, nil
		}},
		steerController: func(_ context.Context, req controller.ControllerSteerRequest) error {
			steerStarted <- req
			return req.Commit()
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, Controllers: backend,
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runTurnStarted:
	case <-ctx.Done():
		t.Fatal("main controller Turn did not reach admission")
	}
	inputRunner, ok := result.Handle.(agent.ContextSubmissionRunner)
	if !ok {
		t.Fatalf("main controller runner = %T, want ContextSubmissionRunner", result.Handle)
	}
	steerResult := make(chan error, 1)
	go func() {
		steerResult <- inputRunner.SubmitContext(ctx, agent.Submission{
			Kind: agent.SubmissionKindConversation, Text: "guide active controller",
			Actor: session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-1", Name: "@child"},
		})
	}()
	select {
	case req := <-steerStarted:
		t.Fatalf("steering reached controller before Turn admission: %#v", req)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseRunTurn)
	var steer controller.ControllerSteerRequest
	select {
	case steer = <-steerStarted:
	case <-ctx.Done():
		t.Fatal("steering did not reach active main controller")
	}
	if err := <-steerResult; err != nil {
		t.Fatalf("SubmitContext() error = %v", err)
	}
	if steer.SessionRef.SessionID != active.SessionID || steer.ControllerID != "controller-1" ||
		steer.ControllerEpoch != "epoch-1" || steer.RemoteSessionID != "remote-session-1" || steer.TurnID == "" {
		t.Fatalf("main steering target = %#v, want exact controller generation and Turn", steer)
	}

	controllerHandle.finish()
	var inputs []*session.Event
	for event, eventErr := range result.Handle.Events() {
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		if event != nil && session.EventTypeOf(event) == session.EventTypeUser {
			inputs = append(inputs, event)
		}
	}
	if len(inputs) != 2 || session.EventText(inputs[0]) != "initial" || session.EventText(inputs[1]) != "guide active controller" {
		t.Fatalf("published user inputs = %#v, want initial then steering", inputs)
	}
	if inputs[1].Actor.Kind != session.ActorKindParticipant || inputs[1].Actor.ID != "child-1" {
		t.Fatalf("steering Actor = %#v, want trusted participant provenance", inputs[1].Actor)
	}
	durable, err := sessions.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	var durableInputs []string
	for _, event := range durable {
		if event != nil && session.EventTypeOf(event) == session.EventTypeUser {
			durableInputs = append(durableInputs, session.EventText(event))
		}
	}
	if len(durableInputs) != 2 || durableInputs[0] != "initial" || durableInputs[1] != "guide active controller" {
		t.Fatalf("durable inputs = %#v, want canonical FIFO", durableInputs)
	}
}

func TestRuntimeMainControllerSteeringUsesReattachedBinding(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sessions, active := newTestSessionService(t, "main-controller-steering-reattach")
	oldBinding := session.ControllerBinding{
		Kind: session.ControllerKindACP, ControllerID: "controller-old", AgentName: "remote-main",
		EpochID: "epoch-old", RemoteSessionID: "remote-session-old",
	}
	var err error
	active, err = sessions.BindController(ctx, session.BindControllerRequest{SessionRef: active.SessionRef, Binding: oldBinding})
	if err != nil {
		t.Fatal(err)
	}
	newBinding := session.CloneControllerBinding(oldBinding)
	newBinding.ControllerID = "controller-new"
	newBinding.EpochID = "epoch-new"
	newBinding.RemoteSessionID = "remote-session-new"

	controllerHandle := newTestControllerTurnHandle(nil)
	secondRun := make(chan controller.TurnRequest, 1)
	steered := make(chan controller.ControllerSteerRequest, 1)
	runCalls := 0
	backend := steeringACPController{
		stubACPController: stubACPController{runTurn: func(_ context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
			runCalls++
			if runCalls == 1 {
				return controller.TurnResult{}, controller.ErrNotActive
			}
			secondRun <- req
			return controller.TurnResult{Handle: controllerHandle}, nil
		}},
		steerController: func(_ context.Context, req controller.ControllerSteerRequest) error {
			steered <- req
			return req.Commit()
		},
	}
	recovery := controllerRecoveryFunc(func(recoveryCtx context.Context, req controller.RecoveryRequest) (session.Session, error) {
		return sessions.BindController(recoveryCtx, session.BindControllerRequest{
			SessionRef: req.SessionRef, Binding: newBinding,
		})
	})
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, Controllers: backend, ControllerRecovery: recovery,
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-secondRun:
		if req.Session.Controller.ControllerID != newBinding.ControllerID || req.Session.Controller.EpochID != newBinding.EpochID {
			t.Fatalf("reattached RunTurn binding = %#v, want %#v", req.Session.Controller, newBinding)
		}
	case <-ctx.Done():
		t.Fatal("reattached controller Turn did not start")
	}
	inputRunner, ok := result.Handle.(agent.ContextSubmissionRunner)
	if !ok {
		t.Fatalf("main controller runner = %T, want ContextSubmissionRunner", result.Handle)
	}
	if err := inputRunner.SubmitContext(ctx, agent.Submission{Kind: agent.SubmissionKindConversation, Text: "after reattach"}); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-steered:
		if req.ControllerID != newBinding.ControllerID || req.ControllerEpoch != newBinding.EpochID || req.RemoteSessionID != newBinding.RemoteSessionID {
			t.Fatalf("steering target = %#v, want refreshed binding %#v", req, newBinding)
		}
	case <-ctx.Done():
		t.Fatal("steering did not reach reattached controller")
	}
	controllerHandle.finish()
	for range result.Handle.Events() {
	}
}
