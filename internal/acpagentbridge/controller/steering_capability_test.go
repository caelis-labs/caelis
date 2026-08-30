package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
)

func TestStartACPClientNegotiatesSteeringForNewAndResumedSessions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		meta       string
		resumeID   string
		supported  bool
		wantRemote string
	}{
		{name: "new supported", meta: `{"supported":true}`, supported: true, wantRemote: "new-session"},
		{name: "new unsupported", meta: `{"supported":false}`, wantRemote: "new-session"},
		{name: "resume supported", meta: `{"supported":true}`, resumeID: "existing-session", supported: true, wantRemote: "existing-session"},
		{name: "resume unsupported", meta: `{"supported":false}`, resumeID: "existing-session", wantRemote: "existing-session"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			acpClient, remoteID, state, err := (&Manager{}).startACPClient(
				ctx,
				t.TempDir(),
				steeringControllerTestConfig(tt.meta, ""),
				tt.resumeID,
				nil,
				func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
					return client.RequestPermissionResponse{}, nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer acpClient.Close(context.Background())
			if remoteID != tt.wantRemote || state.supportsSteering != tt.supported {
				t.Fatalf("startACPClient() remote=%q steering=%v, want %q/%v", remoteID, state.supportsSteering, tt.wantRemote, tt.supported)
			}
		})
	}
}

func TestStartACPClientRejectsMalformedSteeringBeforeSessionEffect(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	marker := filepath.Join(t.TempDir(), "session-called")
	acpClient, remoteID, state, err := (&Manager{}).startACPClient(
		ctx,
		t.TempDir(),
		steeringControllerTestConfig(`{"supported":null}`, marker),
		"existing-session",
		nil,
		func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return client.RequestPermissionResponse{}, nil
		},
	)
	if err == nil {
		t.Fatal("startACPClient() error = nil, want malformed steering capability")
	}
	if acpClient != nil || remoteID != "" || state.supportsSteering {
		t.Fatalf("malformed startup leaked result: client=%v remote=%q state=%#v", acpClient, remoteID, state)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("session operation ran before capability rejection: stat error = %v", statErr)
	}
}

func TestControllerAndParticipantRunsRetainConnectionSteeringCapability(t *testing.T) {
	t.Parallel()

	mainRun := &controllerRun{}
	mainRun.applyStartupStateLocked(nil, "remote-1", controllerClientState{supportsSteering: true}, 0)
	if !mainRun.supportsSteering {
		t.Fatal("main controller did not retain supported steering capability")
	}
	mainRun.applyStartupStateLocked(nil, "remote-1", controllerClientState{supportsSteering: false}, 0)
	if mainRun.supportsSteering {
		t.Fatal("main controller reconnect retained stale steering=true capability")
	}
	mainRun.applyStartupStateLocked(nil, "remote-1", controllerClientState{supportsSteering: true}, 0)
	if !mainRun.supportsSteering {
		t.Fatal("main controller reconnect did not refresh steering=false to true")
	}

	manager := &Manager{
		clock:        time.Now,
		participants: map[participantRunKey]*participantRun{},
	}
	manager.startClient = func(
		context.Context,
		string,
		subagent.AgentConfig,
		string,
		func(client.UpdateEnvelope),
		func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
	) (*client.Client, string, controllerClientState, error) {
		return nil, "participant-remote", controllerClientState{supportsSteering: true}, nil
	}
	participant, err := manager.startParticipant(context.Background(), session.Session{
		SessionRef: session.SessionRef{SessionID: "parent-session"},
	}, subagent.AgentConfig{Name: "helper"}, controller.AttachRequest{
		Agent:     "helper",
		Placement: mustParticipantPlacement(t, "helper"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !participant.supportsSteering {
		t.Fatal("participant did not retain its connection steering capability")
	}
}

func TestParticipantSteeringOrdersPriorAndBufferedRemoteEventsAroundCanonicalInput(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	requestMarker := filepath.Join(t.TempDir(), "steering-requested")
	cfg := steeringControllerTestConfig(`{"supported":true}`, "")
	cfg.Env["CAELIS_ACP_STEERING_OUTCOME"] = string(client.SessionSteeringInjected)
	cfg.Env["CAELIS_ACP_STEERING_NOTIFY_TEXT"] = "after steer"
	cfg.Env["CAELIS_ACP_STEERING_REQUEST_MARKER"] = requestMarker

	handle := newTurnHandle(nil)
	run := &participantRun{
		id:              "participant-1",
		parentSessionID: "parent-session",
		agent:           "steering-helper",
		binding: session.ParticipantBinding{
			ID: "participant-1", Kind: session.ParticipantKindACP,
			Label: "@helper", ControllerRef: "participant-controller",
		},
		supportsSteering: true,
		turnID:           "participant-turn",
		turnStream:       true,
		handle:           handle,
	}
	acpClient, remoteID, state, err := (&Manager{}).startACPClient(
		ctx,
		t.TempDir(),
		cfg,
		"",
		func(env client.UpdateEnvelope) { run.handleUpdate(time.Now, env) },
		func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return client.RequestPermissionResponse{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = run.closeClient(context.Background())
	}()
	if !state.supportsSteering {
		t.Fatal("test ACP client did not negotiate steering")
	}
	run.client = acpClient
	run.remoteSessionID = remoteID
	key := participantKey(run.parentSessionID, run.id)
	manager := &Manager{participants: map[participantRunKey]*participantRun{key: run}}

	priorMessage := model.NewTextMessage(model.RoleAssistant, "before steer")
	handle.publishEvent(&session.Event{Type: session.EventTypeAssistant, Message: &priorMessage})
	priorStarted := make(chan struct{})
	releasePrior := make(chan struct{})
	ordered := make(chan string, 3)
	consumerErrors := make(chan error, 1)
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for source, sourceErr := range handle.SourceEvents() {
			if sourceErr != nil {
				consumerErrors <- sourceErr
				return
			}
			if source.Canonical == nil || source.Canonical.Message == nil {
				continue
			}
			text := source.Canonical.Message.TextContent()
			if text == "before steer" {
				close(priorStarted)
				<-releasePrior
			}
			ordered <- text
		}
	}()
	select {
	case <-priorStarted:
	case <-ctx.Done():
		t.Fatal("prior event did not reach consumer")
	}
	steerResult := make(chan error, 1)
	go func() {
		steerResult <- manager.SteerParticipant(ctx, controller.ParticipantSteerRequest{
			SessionRef:    session.SessionRef{SessionID: run.parentSessionID},
			TurnID:        run.turnID,
			ParticipantID: run.id,
			Input:         "guide",
			Commit: func() error {
				ordered <- "user:guide"
				return nil
			},
		})
	}()
	select {
	case err := <-steerResult:
		t.Fatalf("SteerParticipant() crossed persistence barrier early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, statErr := os.Stat(requestMarker); !os.IsNotExist(statErr) {
		t.Fatalf("steering RPC reached peer before prior event completed: stat error = %v", statErr)
	}
	close(releasePrior)
	if err := <-steerResult; err != nil {
		t.Fatalf("SteerParticipant() error = %v", err)
	}
	var got []string
	for len(got) < 3 {
		select {
		case item := <-ordered:
			got = append(got, item)
		case err := <-consumerErrors:
			t.Fatalf("SourceEvents() error = %v", err)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for ordered steering events: %#v", got)
		}
	}
	if got[0] != "before steer" || got[1] != "user:guide" || got[2] != "after steer" {
		t.Fatalf("steering event order = %#v, want prior/canonical input/buffered output", got)
	}
	if _, statErr := os.Stat(requestMarker); statErr != nil {
		t.Fatalf("steering RPC marker missing after completion: %v", statErr)
	}
	handle.finish()
	select {
	case <-consumerDone:
	case <-ctx.Done():
		t.Fatal("SourceEvents() consumer did not stop")
	}
}

func TestMainControllerSteeringOrdersPriorAndBufferedRemoteEventsAroundCanonicalInput(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := steeringControllerTestConfig(`{"supported":true}`, "")
	cfg.Env["CAELIS_ACP_STEERING_OUTCOME"] = string(client.SessionSteeringInjected)
	cfg.Env["CAELIS_ACP_STEERING_NOTIFY_TEXT"] = "after main steer"
	handle := newTurnHandle(nil)
	run := &controllerRun{
		parentSessionID: "parent-session", agent: "steering-helper", cfg: cfg,
		binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "controller-1", EpochID: "epoch-1",
		},
		supportsSteering: true, turnID: "main-turn", turnStream: true, handle: handle,
	}
	acpClient, remoteID, state, err := (&Manager{}).startACPClient(
		ctx,
		t.TempDir(),
		cfg,
		"",
		func(env client.UpdateEnvelope) { run.handleUpdate(time.Now, env) },
		func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return client.RequestPermissionResponse{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = acpClient.Close(context.Background()) })
	if !state.supportsSteering {
		t.Fatal("test ACP client did not negotiate steering")
	}
	run.client = acpClient
	run.remoteSessionID = remoteID
	run.binding.RemoteSessionID = remoteID
	manager := &Manager{controllers: map[string]*controllerRun{run.parentSessionID: run}}

	priorMessage := model.NewTextMessage(model.RoleAssistant, "before main steer")
	handle.publishEvent(&session.Event{Type: session.EventTypeAssistant, Message: &priorMessage})
	priorStarted := make(chan struct{})
	releasePrior := make(chan struct{})
	ordered := make(chan string, 3)
	consumerErrors := make(chan error, 1)
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for source, sourceErr := range handle.SourceEvents() {
			if sourceErr != nil {
				consumerErrors <- sourceErr
				return
			}
			if source.Canonical == nil || source.Canonical.Message == nil {
				continue
			}
			text := source.Canonical.Message.TextContent()
			if text == "before main steer" {
				close(priorStarted)
				<-releasePrior
			}
			ordered <- text
		}
	}()
	select {
	case <-priorStarted:
	case <-ctx.Done():
		t.Fatal("prior main event did not reach consumer")
	}
	steerResult := make(chan error, 1)
	go func() {
		steerResult <- manager.SteerController(ctx, controller.ControllerSteerRequest{
			SessionRef:   session.SessionRef{SessionID: run.parentSessionID},
			ControllerID: run.binding.ControllerID, ControllerEpoch: run.binding.EpochID,
			RemoteSessionID: remoteID, TurnID: run.turnID, Input: "guide main",
			Commit: func() error {
				ordered <- "user:guide main"
				return nil
			},
		})
	}()
	select {
	case err := <-steerResult:
		t.Fatalf("SteerController() crossed persistence barrier early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releasePrior)
	if err := <-steerResult; err != nil {
		t.Fatalf("SteerController() error = %v", err)
	}
	var got []string
	for len(got) < 3 {
		select {
		case item := <-ordered:
			got = append(got, item)
		case err := <-consumerErrors:
			t.Fatalf("SourceEvents() error = %v", err)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for ordered main steering events: %#v", got)
		}
	}
	if got[0] != "before main steer" || got[1] != "user:guide main" || got[2] != "after main steer" {
		t.Fatalf("main steering event order = %#v, want prior/canonical input/buffered output", got)
	}
	handle.finish()
	select {
	case <-consumerDone:
	case <-ctx.Done():
		t.Fatal("main SourceEvents() consumer did not stop")
	}
}

func TestMainControllerSteeringAmbiguousOutcomeIsolatesExactRun(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := steeringControllerTestConfig(`{"supported":true}`, "")
	cfg.Env["CAELIS_ACP_STEERING_OUTCOME"] = string(client.SessionSteeringStartedNewTurn)
	handle := newTurnHandle(nil)
	run := &controllerRun{
		parentSessionID: "parent-session", agent: "steering-helper", cfg: cfg,
		binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "controller-1", EpochID: "epoch-1",
		},
		supportsSteering: true, turnID: "main-turn", turnStream: true, handle: handle,
	}
	acpClient, remoteID, _, err := (&Manager{}).startACPClient(
		ctx,
		t.TempDir(),
		cfg,
		"",
		func(env client.UpdateEnvelope) { run.handleUpdate(time.Now, env) },
		func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return client.RequestPermissionResponse{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	run.client = acpClient
	run.remoteSessionID = remoteID
	run.binding.RemoteSessionID = remoteID
	manager := &Manager{controllers: map[string]*controllerRun{run.parentSessionID: run}}
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for range handle.SourceEvents() {
		}
	}()
	committed := false
	err = manager.SteerController(ctx, controller.ControllerSteerRequest{
		SessionRef:   session.SessionRef{SessionID: run.parentSessionID},
		ControllerID: run.binding.ControllerID, ControllerEpoch: run.binding.EpochID,
		RemoteSessionID: remoteID, TurnID: run.turnID, Input: "ambiguous guide",
		Commit: func() error {
			committed = true
			return nil
		},
	})
	if errorcode.CodeOf(err) != errorcode.UnknownOutcome {
		t.Fatalf("SteerController() error = %v, want unknown_outcome", err)
	}
	if committed {
		t.Fatal("ambiguous main steering committed canonical input")
	}
	manager.mu.RLock()
	retained := manager.controllers[run.parentSessionID]
	manager.mu.RUnlock()
	if retained != nil {
		t.Fatal("ambiguous main controller remained addressable")
	}
	handle.mu.Lock()
	cancelled := handle.cancelled
	handle.mu.Unlock()
	if !cancelled {
		t.Fatal("ambiguous main controller Turn was not cancelled")
	}
	handle.finish()
	select {
	case <-consumerDone:
	case <-ctx.Done():
		t.Fatal("ambiguous main controller consumer did not stop")
	}
}

func TestMainControllerSteeringReportsClosedRunBeforeRuntimeRunnerCloses(t *testing.T) {
	t.Parallel()

	newFixture := func() (*Manager, *controllerRun, *turnHandle, controller.ControllerSteerRequest) {
		handle := newTurnHandle(nil)
		run := &controllerRun{
			parentSessionID: "parent-session", supportsSteering: true,
			binding: session.ControllerBinding{
				Kind: session.ControllerKindACP, ControllerID: "controller-1", EpochID: "epoch-1",
			},
			remoteSessionID: "remote-session-1", turnID: "main-turn", turnStream: true, handle: handle,
		}
		manager := &Manager{controllers: map[string]*controllerRun{run.parentSessionID: run}}
		request := controller.ControllerSteerRequest{
			SessionRef:   session.SessionRef{SessionID: run.parentSessionID},
			ControllerID: run.binding.ControllerID, ControllerEpoch: run.binding.EpochID,
			RemoteSessionID: run.remoteSessionID, TurnID: run.turnID, Input: "after terminal",
			Commit: func() error { t.Fatal("closed Run committed steering input"); return nil },
		}
		return manager, run, handle, request
	}

	t.Run("turn owner cleared before handle close", func(t *testing.T) {
		manager, run, handle, request := newFixture()
		run.finishTurn(handle)
		if err := manager.SteerController(context.Background(), request); !errors.Is(err, agent.ErrRunInputClosed) {
			t.Fatalf("SteerController() error = %v, want ErrRunInputClosed", err)
		}
	})

	t.Run("event stream barrier closed before runner close", func(t *testing.T) {
		manager, _, handle, request := newFixture()
		handle.closeBarrierAdmission(controller.ErrNotActive)
		if err := manager.SteerController(context.Background(), request); !errors.Is(err, agent.ErrRunInputClosed) {
			t.Fatalf("SteerController() error = %v, want ErrRunInputClosed", err)
		}
	})
}

func TestParticipantSteeringAmbiguousOutcomeIsolatesParticipant(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := steeringControllerTestConfig(`{"supported":true}`, "")
	cfg.Env["CAELIS_ACP_STEERING_OUTCOME"] = string(client.SessionSteeringStartedNewTurn)
	handle := newTurnHandle(nil)
	run := &participantRun{
		id: "participant-1", parentSessionID: "parent-session", agent: "steering-helper",
		binding:          session.ParticipantBinding{ID: "participant-1", Kind: session.ParticipantKindACP},
		supportsSteering: true,
		turnID:           "participant-turn",
		turnStream:       true,
		handle:           handle,
	}
	acpClient, remoteID, _, err := (&Manager{}).startACPClient(
		ctx,
		t.TempDir(),
		cfg,
		"",
		func(env client.UpdateEnvelope) { run.handleUpdate(time.Now, env) },
		func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return client.RequestPermissionResponse{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.closeClient(context.Background()) })
	run.client = acpClient
	run.remoteSessionID = remoteID
	key := participantKey(run.parentSessionID, run.id)
	manager := &Manager{participants: map[participantRunKey]*participantRun{key: run}}
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for range handle.SourceEvents() {
		}
	}()
	committed := false
	err = manager.SteerParticipant(ctx, controller.ParticipantSteerRequest{
		SessionRef:    session.SessionRef{SessionID: run.parentSessionID},
		TurnID:        run.turnID,
		ParticipantID: run.id,
		Input:         "guide",
		Commit: func() error {
			committed = true
			return nil
		},
	})
	if errorcode.CodeOf(err) != errorcode.UnknownOutcome {
		t.Fatalf("SteerParticipant() error = %v, want unknown_outcome", err)
	}
	if committed {
		t.Fatal("ambiguous startedNewTurn outcome committed canonical input")
	}
	manager.mu.RLock()
	retained := manager.participants[key]
	manager.mu.RUnlock()
	if retained != nil {
		t.Fatal("ambiguous participant remained addressable")
	}
	handle.mu.Lock()
	cancelled := handle.cancelled
	handle.mu.Unlock()
	if !cancelled {
		t.Fatal("ambiguous participant Turn was not cancelled")
	}
	handle.finish()
	select {
	case <-consumerDone:
	case <-ctx.Done():
		t.Fatal("ambiguous participant event consumer did not stop")
	}
}

func TestParticipantSteeringCallerCancelBeforeRPCIsProvenNoEffect(t *testing.T) {
	t.Parallel()

	handle := newTurnHandle(nil)
	run := &participantRun{
		id: "participant-1", parentSessionID: "parent-session",
		binding:          session.ParticipantBinding{ID: "participant-1", Kind: session.ParticipantKindACP},
		supportsSteering: true,
		turnID:           "participant-turn",
		turnStream:       true,
		handle:           handle,
	}
	key := participantKey(run.parentSessionID, run.id)
	manager := &Manager{participants: map[participantRunKey]*participantRun{key: run}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	committed := false
	err := manager.SteerParticipant(ctx, controller.ParticipantSteerRequest{
		SessionRef:    session.SessionRef{SessionID: run.parentSessionID},
		TurnID:        run.turnID,
		ParticipantID: run.id,
		Input:         "guide",
		Commit: func() error {
			committed = true
			return nil
		},
	})
	if errorcode.CodeOf(err) != errorcode.FailedPrecondition || !errors.Is(err, context.Canceled) {
		t.Fatalf("SteerParticipant() error = %v, want failed_precondition retaining cancellation", err)
	}
	if committed {
		t.Fatal("pre-RPC cancellation committed canonical input")
	}
	manager.mu.RLock()
	retained := manager.participants[key]
	manager.mu.RUnlock()
	if retained != run {
		t.Fatal("pre-RPC cancellation isolated participant despite proven no effect")
	}
	handle.finish()
}

func TestParticipantSteeringProvenNoInjectionReleasesBufferedUpdate(t *testing.T) {
	t.Parallel()

	for _, outcome := range []client.SessionSteeringOutcome{
		client.SessionSteeringFailed,
		client.SessionSteeringPromptRequired,
	} {
		outcome := outcome
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			harness := newParticipantSteeringTestHarness(t, ctx, outcome, "ordinary output")
			var texts []string
			var sourceErrs []error
			consumerDone := make(chan struct{})
			go func() {
				defer close(consumerDone)
				for source, sourceErr := range harness.handle.SourceEvents() {
					if sourceErr != nil {
						sourceErrs = append(sourceErrs, sourceErr)
						continue
					}
					if source.Canonical != nil && source.Canonical.Message != nil {
						texts = append(texts, source.Canonical.Message.TextContent())
					}
				}
			}()
			committed := false
			err := harness.manager.SteerParticipant(ctx, controller.ParticipantSteerRequest{
				SessionRef:    session.SessionRef{SessionID: harness.run.parentSessionID},
				TurnID:        harness.run.turnID,
				ParticipantID: harness.run.id,
				Input:         "guide",
				Commit: func() error {
					committed = true
					return nil
				},
			})
			if errorcode.CodeOf(err) != errorcode.FailedPrecondition {
				t.Fatalf("SteerParticipant() error = %v, want failed_precondition", err)
			}
			if committed {
				t.Fatal("non-injected outcome committed canonical input")
			}
			harness.manager.mu.RLock()
			retained := harness.manager.participants[harness.key]
			harness.manager.mu.RUnlock()
			if retained != harness.run {
				t.Fatal("proven no-injection outcome isolated participant")
			}
			harness.handle.finish()
			<-consumerDone
			if len(sourceErrs) != 0 {
				t.Fatalf("released source errors = %#v, want none", sourceErrs)
			}
			if len(texts) != 1 || texts[0] != "ordinary output" {
				t.Fatalf("released buffered updates = %#v, want ordinary output", texts)
			}
		})
	}
}

func TestParticipantSteeringCommitFailureIsolatesAndDropsBufferedUpdate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	harness := newParticipantSteeringTestHarness(t, ctx, client.SessionSteeringInjected, "must be quarantined")
	var texts []string
	var sourceErrs []error
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for source, sourceErr := range harness.handle.SourceEvents() {
			if sourceErr != nil {
				sourceErrs = append(sourceErrs, sourceErr)
				continue
			}
			if source.Canonical != nil && source.Canonical.Message != nil {
				texts = append(texts, source.Canonical.Message.TextContent())
			}
		}
	}()
	commitErr := errors.New("canonical append failed")
	err := harness.manager.SteerParticipant(ctx, controller.ParticipantSteerRequest{
		SessionRef:    session.SessionRef{SessionID: harness.run.parentSessionID},
		TurnID:        harness.run.turnID,
		ParticipantID: harness.run.id,
		Input:         "guide",
		Commit:        func() error { return commitErr },
	})
	if errorcode.CodeOf(err) != errorcode.UnknownOutcome || !errors.Is(err, commitErr) {
		t.Fatalf("SteerParticipant() error = %v, want unknown_outcome retaining append failure", err)
	}
	harness.manager.mu.RLock()
	retained := harness.manager.participants[harness.key]
	harness.manager.mu.RUnlock()
	if retained != nil {
		t.Fatal("commit failure left divergent participant addressable")
	}
	harness.handle.finish()
	<-consumerDone
	if len(texts) != 0 {
		t.Fatalf("commit failure projected quarantined updates = %#v", texts)
	}
	if len(sourceErrs) == 0 || errorcode.CodeOf(sourceErrs[0]) != errorcode.UnknownOutcome {
		t.Fatalf("commit failure source errors = %#v, want unknown outcome", sourceErrs)
	}
}

func TestParticipantSteeringTransportLossIsUnknownAndIsolates(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	harness := newParticipantSteeringTestHarnessWithEnv(
		t,
		ctx,
		client.SessionSteeringInjected,
		"",
		map[string]string{"CAELIS_ACP_STEERING_EXIT_BEFORE_RESPONSE": "1"},
	)
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for range harness.handle.SourceEvents() {
		}
	}()
	committed := false
	err := harness.manager.SteerParticipant(ctx, controller.ParticipantSteerRequest{
		SessionRef:    session.SessionRef{SessionID: harness.run.parentSessionID},
		TurnID:        harness.run.turnID,
		ParticipantID: harness.run.id,
		Input:         "guide",
		Commit: func() error {
			committed = true
			return nil
		},
	})
	if errorcode.CodeOf(err) != errorcode.UnknownOutcome {
		t.Fatalf("SteerParticipant() transport error = %v, want unknown_outcome", err)
	}
	if committed {
		t.Fatal("transport loss committed canonical input")
	}
	harness.manager.mu.RLock()
	retained := harness.manager.participants[harness.key]
	harness.manager.mu.RUnlock()
	if retained != nil {
		t.Fatal("transport loss left participant addressable")
	}
	harness.handle.finish()
	<-consumerDone
}

func TestParticipantDetachCancelsInflightSteering(t *testing.T) {
	t.Parallel()

	// Detach deliberately gives an unresponsive owned process three seconds to
	// exit before forced cleanup. Keep that production grace distinct from the
	// test's scheduling budget so parallel process tests cannot exhaust the same
	// deadline before the post-detach assertions run.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	requestMarker := filepath.Join(t.TempDir(), "steering-requested")
	harness := newParticipantSteeringTestHarnessWithEnv(
		t,
		ctx,
		client.SessionSteeringInjected,
		"",
		map[string]string{
			"CAELIS_ACP_STEERING_BLOCK_BEFORE_RESPONSE": "1",
			"CAELIS_ACP_STEERING_REQUEST_MARKER":        requestMarker,
		},
	)
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for range harness.handle.SourceEvents() {
		}
	}()
	steerResult := make(chan error, 1)
	go func() {
		steerResult <- harness.manager.SteerParticipant(ctx, controller.ParticipantSteerRequest{
			SessionRef:    session.SessionRef{SessionID: harness.run.parentSessionID},
			TurnID:        harness.run.turnID,
			ParticipantID: harness.run.id,
			Input:         "guide",
			Commit:        func() error { return nil },
		})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(requestMarker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("steering RPC did not reach blocking peer")
		}
		time.Sleep(time.Millisecond)
	}
	if err := harness.manager.Detach(ctx, controller.DetachRequest{
		SessionRef:           session.SessionRef{SessionID: harness.run.parentSessionID},
		ParticipantID:        harness.run.id,
		DelegationID:         harness.run.binding.DelegationID,
		AttachmentGeneration: harness.run.binding.AttachmentGeneration,
	}); err != nil {
		t.Fatalf("Detach() error = %v", err)
	}
	select {
	case err := <-steerResult:
		if errorcode.CodeOf(err) != errorcode.UnknownOutcome {
			t.Fatalf("in-flight SteerParticipant() error = %v, want unknown_outcome", err)
		}
	case <-ctx.Done():
		t.Fatal("Detach() did not settle in-flight steering")
	}
	harness.handle.finish()
	consumerDeadline := time.NewTimer(5 * time.Second)
	defer consumerDeadline.Stop()
	select {
	case <-consumerDone:
	case <-consumerDeadline.C:
		t.Fatal("detached participant consumer did not stop")
	}
}

func TestParticipantDetachAbortsBlockedSteeringWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	marker := filepath.Join(t.TempDir(), "partial-write-started")
	acpClient := newBlockedSteeringWriteClient(t, marker)
	handle := newTurnHandle(nil)
	run := &participantRun{
		id: "participant-blocked-write", parentSessionID: "parent-session", agent: "blocked-writer",
		client: acpClient, remoteSessionID: "remote-session", supportsSteering: true,
		binding: session.ParticipantBinding{
			ID: "participant-blocked-write", Kind: session.ParticipantKindACP,
			DelegationID: "delegation-blocked-write", AttachmentGeneration: "generation-blocked-write",
		},
		turnID: "participant-turn", turnStream: true, handle: handle,
	}
	key := participantKey(run.parentSessionID, run.id)
	manager := &Manager{participants: map[participantRunKey]*participantRun{key: run}}
	consumerDone := drainControllerTurnHandle(handle)
	committed := make(chan struct{}, 1)
	steerDone := make(chan error, 1)
	go func() {
		steerDone <- manager.SteerParticipant(ctx, controller.ParticipantSteerRequest{
			SessionRef: session.SessionRef{SessionID: run.parentSessionID},
			TurnID:     run.turnID, ParticipantID: run.id,
			Input: strings.Repeat("blocked participant steering ", 1<<16),
			Commit: func() error {
				committed <- struct{}{}
				return nil
			},
		})
	}()
	waitForBlockedSteeringWrite(t, ctx, marker)
	if err := manager.Detach(ctx, controller.DetachRequest{
		SessionRef:    session.SessionRef{SessionID: run.parentSessionID},
		ParticipantID: run.id, DelegationID: run.binding.DelegationID,
		AttachmentGeneration: run.binding.AttachmentGeneration,
	}); err != nil {
		t.Fatalf("Detach() error = %v", err)
	}
	select {
	case err := <-steerDone:
		if errorcode.CodeOf(err) != errorcode.UnknownOutcome {
			t.Fatalf("blocked SteerParticipant() error = %v, want unknown_outcome", err)
		}
	case <-ctx.Done():
		t.Fatal("participant detach did not abort the blocked steering write")
	}
	manager.mu.RLock()
	retained := manager.participants[key]
	manager.mu.RUnlock()
	if retained != nil {
		t.Fatal("blocked-write participant remained addressable")
	}
	select {
	case <-committed:
		t.Fatal("blocked participant steering committed canonical input")
	default:
	}
	handle.finish()
	select {
	case <-consumerDone:
	case <-ctx.Done():
		t.Fatal("blocked-write participant consumer did not stop")
	}
}

func TestControllerCloseAdmissionAbortsBlockedSteeringWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	marker := filepath.Join(t.TempDir(), "partial-write-started")
	acpClient := newBlockedSteeringWriteClient(t, marker)
	handle := newTurnHandle(nil)
	run := &controllerRun{
		parentSessionID: "parent-session", agent: "blocked-writer", client: acpClient,
		remoteSessionID: "remote-session", supportsSteering: true,
		binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "controller-blocked-write",
			EpochID: "epoch-blocked-write", RemoteSessionID: "remote-session",
		},
		turnID: "main-turn", turnStream: true, handle: handle,
	}
	manager := &Manager{controllers: map[string]*controllerRun{run.parentSessionID: run}}
	consumerDone := drainControllerTurnHandle(handle)
	committed := make(chan struct{}, 1)
	steerDone := make(chan error, 1)
	go func() {
		steerDone <- manager.SteerController(ctx, controller.ControllerSteerRequest{
			SessionRef:   session.SessionRef{SessionID: run.parentSessionID},
			ControllerID: run.binding.ControllerID, ControllerEpoch: run.binding.EpochID,
			RemoteSessionID: run.remoteSessionID, TurnID: run.turnID,
			Input: strings.Repeat("blocked controller steering ", 1<<16),
			Commit: func() error {
				committed <- struct{}{}
				return nil
			},
		})
	}()
	waitForBlockedSteeringWrite(t, ctx, marker)
	closeDone := make(chan struct{})
	go func() {
		run.closeTurnAdmission()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-ctx.Done():
		t.Fatal("controller close admission did not abort the blocked steering write")
	}
	select {
	case err := <-steerDone:
		if errorcode.CodeOf(err) != errorcode.UnknownOutcome {
			t.Fatalf("blocked SteerController() error = %v, want unknown_outcome", err)
		}
	case <-ctx.Done():
		t.Fatal("blocked controller steering did not settle")
	}
	manager.mu.RLock()
	retained := manager.controllers[run.parentSessionID]
	manager.mu.RUnlock()
	if retained != nil {
		t.Fatal("blocked-write controller remained addressable")
	}
	select {
	case <-committed:
		t.Fatal("blocked controller steering committed canonical input")
	default:
	}
	handle.finish()
	select {
	case <-consumerDone:
	case <-ctx.Done():
		t.Fatal("blocked-write controller consumer did not stop")
	}
}

func newBlockedSteeringWriteClient(t *testing.T, marker string) *client.Client {
	t.Helper()
	acpClient, err := client.Start(context.Background(), client.Config{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerBlockedSteeringWriteHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_HELPER":               "blocked-steering-write",
			"CAELIS_ACP_PARTIAL_WRITE_MARKER": marker,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = acpClient.Close(closeCtx)
	})
	return acpClient
}

func waitForBlockedSteeringWrite(t *testing.T, ctx context.Context, marker string) {
	t.Helper()
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("steering request did not begin its partial transport write")
		case <-time.After(time.Millisecond):
		}
	}
}

func drainControllerTurnHandle(handle *turnHandle) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range handle.SourceEvents() {
		}
	}()
	return done
}

func TestManagerBlockedSteeringWriteHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_HELPER") != "blocked-steering-write" {
		return
	}
	buffer := make([]byte, 64)
	if _, err := os.Stdin.Read(buffer); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv("CAELIS_ACP_PARTIAL_WRITE_MARKER"), []byte("started"), 0o600); err != nil {
		os.Exit(3)
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

type participantSteeringTestHarness struct {
	manager *Manager
	run     *participantRun
	handle  *turnHandle
	client  *client.Client
	key     participantRunKey
}

func newParticipantSteeringTestHarness(
	t *testing.T,
	ctx context.Context,
	outcome client.SessionSteeringOutcome,
	notifyText string,
) participantSteeringTestHarness {
	t.Helper()
	return newParticipantSteeringTestHarnessWithEnv(t, ctx, outcome, notifyText, nil)
}

func newParticipantSteeringTestHarnessWithEnv(
	t *testing.T,
	ctx context.Context,
	outcome client.SessionSteeringOutcome,
	notifyText string,
	extraEnv map[string]string,
) participantSteeringTestHarness {
	t.Helper()
	cfg := steeringControllerTestConfig(`{"supported":true}`, "")
	cfg.Env["CAELIS_ACP_STEERING_OUTCOME"] = string(outcome)
	cfg.Env["CAELIS_ACP_STEERING_NOTIFY_TEXT"] = notifyText
	for key, value := range extraEnv {
		cfg.Env[key] = value
	}
	handle := newTurnHandle(nil)
	run := &participantRun{
		id: "participant-1", parentSessionID: "parent-session", agent: "steering-helper",
		binding: session.ParticipantBinding{
			ID: "participant-1", Kind: session.ParticipantKindACP,
			DelegationID: "delegation-1", AttachmentGeneration: "generation-1",
		},
		supportsSteering: true,
		turnID:           "participant-turn",
		turnStream:       true,
		handle:           handle,
	}
	acpClient, remoteID, state, err := (&Manager{}).startACPClient(
		ctx,
		t.TempDir(),
		cfg,
		"",
		func(env client.UpdateEnvelope) { run.handleUpdate(time.Now, env) },
		func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return client.RequestPermissionResponse{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.closeClient(context.Background()) })
	if !state.supportsSteering {
		t.Fatal("test ACP client did not negotiate steering")
	}
	run.client = acpClient
	run.remoteSessionID = remoteID
	key := participantKey(run.parentSessionID, run.id)
	return participantSteeringTestHarness{
		manager: &Manager{participants: map[participantRunKey]*participantRun{key: run}},
		run:     run, handle: handle, client: acpClient, key: key,
	}
}

func steeringControllerTestConfig(meta string, marker string) subagent.AgentConfig {
	return subagent.AgentConfig{
		Name:    "steering-helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerSteeringCapabilityHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_HELPER":                "steering-capability",
			"CAELIS_ACP_STEERING_META":         meta,
			"CAELIS_ACP_SESSION_EFFECT_MARKER": marker,
		},
	}
}

func TestManagerSteeringCapabilityHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_HELPER") != "steering-capability" {
		return
	}
	marker := os.Getenv("CAELIS_ACP_SESSION_EFFECT_MARKER")
	markSessionEffect := func() {
		if marker != "" {
			_ = os.WriteFile(marker, []byte("called"), 0o600)
		}
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(_ context.Context, message jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch message.Method {
		case client.MethodInitialize:
			return client.InitializeResponse{
				ProtocolVersion: 1,
				AgentCapabilities: client.AgentCapabilities{SessionCapabilities: map[string]json.RawMessage{
					"resume": json.RawMessage(`{}`),
				}},
				Meta: map[string]json.RawMessage{
					client.SessionSteeringMetaKey: json.RawMessage(os.Getenv("CAELIS_ACP_STEERING_META")),
				},
			}, nil
		case client.MethodSessionNew:
			markSessionEffect()
			return client.NewSessionResponse{SessionID: "new-session"}, nil
		case client.MethodSessionResume:
			markSessionEffect()
			return client.ResumeSessionResponse{}, nil
		case client.MethodSessionSteering:
			marker := os.Getenv("CAELIS_ACP_STEERING_REQUEST_MARKER")
			if marker != "" {
				_ = os.WriteFile(marker, []byte("called"), 0o600)
			}
			var request client.SessionSteeringRequest
			if err := json.Unmarshal(message.Params, &request); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if request.SessionID == "" || len(request.Prompt) == 0 {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "missing steering target or prompt"}
			}
			if os.Getenv("CAELIS_ACP_STEERING_EXIT_BEFORE_RESPONSE") == "1" {
				os.Exit(0)
			}
			if os.Getenv("CAELIS_ACP_STEERING_BLOCK_BEFORE_RESPONSE") == "1" {
				select {}
			}
			if notifyText := os.Getenv("CAELIS_ACP_STEERING_NOTIFY_TEXT"); notifyText != "" {
				update := jsonrpc.MustMarshalRaw(client.ContentChunk{
					SessionUpdate: client.UpdateAgentMessage,
					Content: jsonrpc.MustMarshalRaw(client.TextContent{
						Type: "text",
						Text: notifyText,
					}),
				})
				if err := conn.Notify(client.MethodSessionUpdate, client.SessionNotification{
					SessionID: request.SessionID,
					Update:    update,
				}); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
				}
			}
			outcome := client.SessionSteeringOutcome(os.Getenv("CAELIS_ACP_STEERING_OUTCOME"))
			if outcome == "" {
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
