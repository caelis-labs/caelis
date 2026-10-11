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
			Kind: agent.SubmissionKindAgentCommunication, Text: "guide active controller",
			Actor: session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-1", Role: "delegated", Name: "@child"},
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
	if steer.Input != "" || len(steer.ContentParts) != 1 ||
		steer.ContentParts[0].Text != "guide active controller\n\nFrom: @child" {
		t.Fatalf("main steering prompt = input %q parts %#v, want trusted Agent header", steer.Input, steer.ContentParts)
	}
	if err := inputRunner.SubmitContext(ctx, agent.Submission{
		Kind: agent.SubmissionKindConversation, Text: "guide from user", DisplayInput: "display user guidance",
		Metadata: map[string]any{
			"operation_id": "main-steer-operation", "steering_operation_id": "main-steer-operation",
			"actor": "forged-child", "message_id": "forged-message", "caelis": map[string]any{"agent_communication": true},
		},
	}); err != nil {
		t.Fatalf("conversation SubmitContext() error = %v", err)
	}

	controllerHandle.finish()
	if err := result.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := sessions.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var userInputs []*session.Event
	var communications []*session.Event
	for _, event := range events {
		if event != nil && session.EventTypeOf(event) == session.EventTypeUser {
			userInputs = append(userInputs, event)
		}
		if session.ProtocolAgentCommunicationOf(event) != nil {
			communications = append(communications, event)
		}
	}
	if len(userInputs) != 2 || session.EventText(userInputs[0]) != "initial" || session.EventText(userInputs[1]) != "guide from user" || len(communications) != 1 ||
		session.ProtocolAgentCommunicationOf(communications[0]).Text != "guide active controller" {
		t.Fatalf("published inputs = users %#v communications %#v", userInputs, communications)
	}
	if communications[0].Actor.Kind != session.ActorKindParticipant || communications[0].Actor.ID != "child-1" {
		t.Fatalf("steering Actor = %#v, want trusted participant provenance", communications[0].Actor)
	}
	assertCommittedSteeringCorrelation(t, userInputs[1], "main-steer-operation")
	if userInputs[1].Actor.Kind != session.ActorKindUser || userInputs[1].Actor.ID != "" {
		t.Fatalf("user steering adopted metadata Actor: %#v", userInputs[1].Actor)
	}
	durable, err := sessions.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	var durableUsers []string
	var durableCommunications []string
	for _, event := range durable {
		if event != nil && session.EventTypeOf(event) == session.EventTypeUser {
			durableUsers = append(durableUsers, session.EventText(event))
			if session.EventText(event) == "guide from user" {
				assertCommittedSteeringCorrelation(t, event, "main-steer-operation")
				if event.ID != userInputs[1].ID {
					t.Fatalf("recovered steering event identity %q differs from live %q", event.ID, userInputs[1].ID)
				}
			}
		}
		if communication := session.ProtocolAgentCommunicationOf(event); communication != nil {
			durableCommunications = append(durableCommunications, communication.Text)
		}
	}
	if len(durableUsers) != 2 || durableUsers[0] != "initial" || durableUsers[1] != "guide from user" ||
		len(durableCommunications) != 1 || durableCommunications[0] != "guide active controller" {
		t.Fatalf("durable inputs = users %#v communications %#v, want typed canonical FIFO", durableUsers, durableCommunications)
	}
}

func TestRuntimeACPControllerInitialAgentCommunicationCarriesIdentity(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sessions, active := newTestSessionService(t, "main-controller-agent-communication")
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
	turns := make(chan controller.TurnRequest, 1)
	backend := stubACPController{runTurn: func(_ context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
		turns <- req
		return controller.TurnResult{Handle: controllerHandle}, nil
	}}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, Controllers: backend,
	}))
	if err != nil {
		t.Fatal(err)
	}
	actor := session.ActorRef{
		Kind: session.ActorKindParticipant, ID: "child-1", Role: "delegated", Name: "@child",
	}
	result, err := runtime.Run(ctx, agent.RunRequest{
		SessionRef: active.SessionRef, InputKind: agent.SubmissionKindAgentCommunication,
		Input: "start from child", InputActor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case turn := <-turns:
		if turn.Input != "" || len(turn.ContentParts) != 1 ||
			turn.ContentParts[0].Text != "start from child\n\nFrom: @child" {
			t.Fatalf("controller prompt = input %q parts %#v", turn.Input, turn.ContentParts)
		}
	case <-ctx.Done():
		t.Fatal("controller Turn did not start")
	}
	controllerHandle.finish()
	if err := result.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	events, err := sessions.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var communication *session.Event
	for _, event := range events {
		if session.ProtocolAgentCommunicationOf(event) != nil {
			communication = event
		}
		if event != nil && session.EventTypeOf(event) == session.EventTypeUser {
			t.Fatalf("Agent communication emitted User event: %#v", event)
		}
	}
	if communication == nil || communication.Actor != actor ||
		session.ProtocolAgentCommunicationOf(communication).Text != "start from child" {
		t.Fatalf("Agent communication event = %#v", communication)
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
	if err := result.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
}
