package acpagentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/steeringwire"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
)

func TestRuntimeAgentAdvertisesSteeringOnlyWithAppServerBackend(t *testing.T) {
	t.Parallel()

	client := &steeringTestSessionClient{state: activeSteeringTestState()}
	typed := steeringTestAgent(client)
	response, err := typed.Initialize(context.Background(), acpsdk.InitializeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var capability steeringwire.SessionSteeringCapability
	if err := json.Unmarshal(response.Meta[steeringwire.SessionSteeringMetaKey], &capability); err != nil {
		t.Fatalf("decode steering capability: %v", err)
	}
	if !capability.Supported {
		t.Fatalf("steering capability = %#v, want supported", capability)
	}

	direct := steeringTestAgent(nil)
	directResponse, err := direct.Initialize(context.Background(), acpsdk.InitializeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := directResponse.Meta[steeringwire.SessionSteeringMetaKey]; ok {
		t.Fatalf("direct Runtime initialize _meta = %#v, want no steering capability", directResponse.Meta)
	}
	if _, err := direct.SteerSession(context.Background(), steeringwire.SessionSteeringRequest{}); !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("direct Runtime SteerSession error = %v, want capability unsupported", err)
	}
}

func TestRuntimeAgentDoesNotEncodeControllerAuthorityInSessionMetadata(t *testing.T) {
	t.Parallel()

	client := &steeringTestSessionClient{
		state: appserver.SessionState{
			SessionID:    "session-inbound",
			WorkspaceKey: "workspace-inbound",
			Revision:     7,
			Controller: session.ControllerBinding{
				Kind: session.ControllerKindKernel, EpochID: "kernel-epoch", Source: "acp_ingress",
			},
		},
		result: appserver.CommandResult{
			Outcome:   appserver.OutcomeCommitted,
			SessionID: "session-inbound",
		},
	}
	agent := steeringTestAgent(client)
	if _, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if len(client.createRequests) != 1 {
		t.Fatalf("CreateSession calls = %d, want 1", len(client.createRequests))
	}
	if metadata := client.createRequests[0].Metadata; len(metadata) != 0 {
		t.Fatalf("CreateSession metadata = %#v, want no controller authority hint", metadata)
	}
}

func TestRuntimeAgentRejectsSessionWhenHostDoesNotAcceptIngressCredential(t *testing.T) {
	t.Parallel()

	client := &steeringTestSessionClient{
		state: appserver.SessionState{
			SessionID: "session-rejected", Revision: 3,
			Controller: session.ControllerBinding{
				Kind: session.ControllerKindACP, EpochID: "external-epoch", Source: "host_default",
			},
		},
		result: appserver.CommandResult{Outcome: appserver.OutcomeCommitted, SessionID: "session-rejected"},
	}
	agent := steeringTestAgent(client)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	_, err := agent.NewSession(requestCtx, acpsdk.NewSessionRequest{Cwd: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "did not accept the ACP ingress credential") {
		t.Fatalf("NewSession() error = %v, want rejected ingress credential", err)
	}
	if len(client.closeRequests) != 1 || client.closeRequests[0].SessionID != "session-rejected" {
		t.Fatalf("CloseSession requests = %#v, want rejected Session cleanup", client.closeRequests)
	}
	if client.closeContextErrors[0] != nil {
		t.Fatalf("CloseSession cleanup context error = %v, want cancellation-detached cleanup", client.closeContextErrors[0])
	}
}

func TestRuntimeAgentSteersExactActiveMainTurnWithoutRevisionCAS(t *testing.T) {
	t.Parallel()

	state := activeSteeringTestState()
	state.Revision = 11
	client := &steeringTestSessionClient{
		state:           state,
		currentRevision: 12,
		result: appserver.CommandResult{
			Outcome: appserver.OutcomeCommitted,
		},
	}
	agent := steeringTestAgent(client)
	prompt := []json.RawMessage{
		jsonrpc.MustMarshalRaw(eventstream.TextContent{Type: "text", Text: "adjust the plan"}),
		json.RawMessage(`{"type":"image","mimeType":"image/png","data":"aW1hZ2U=","name":"plan.png"}`),
	}
	response, err := agent.SteerSession(context.Background(), steeringwire.SessionSteeringRequest{
		SessionID: state.SessionID,
		Prompt:    prompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != steeringwire.SessionSteeringInjected {
		t.Fatalf("steering response = %#v, want injected", response)
	}
	if len(client.steerRequests) != 1 {
		t.Fatalf("Control steer calls = %d, want 1", len(client.steerRequests))
	}
	request := client.steerRequests[0]
	if request.ExpectedRevision != nil {
		t.Fatalf("steering ExpectedRevision = %v, want nil while revision advanced from 11 to 12", *request.ExpectedRevision)
	}
	if request.SessionID != state.SessionID || request.ExpectedControllerEpoch != state.Controller.EpochID {
		t.Fatalf("steering write fence = %#v", request.WriteBase)
	}
	wantTarget := appserver.TurnTarget{
		HandleID: state.Run.HandleID,
		RunID:    state.Run.RunID,
		TurnID:   state.Run.TurnID,
	}
	if request.Target != wantTarget {
		t.Fatalf("steering target = %#v, want %#v", request.Target, wantTarget)
	}
	if request.Input != "adjust the plan" {
		t.Fatalf("steering input = %q", request.Input)
	}
	wantParts := []model.ContentPart{
		{Type: model.ContentPartText, Text: "adjust the plan"},
		{Type: model.ContentPartImage, MimeType: "image/png", Data: "aW1hZ2U=", FileName: "plan.png"},
	}
	if !reflect.DeepEqual(request.ContentParts, wantParts) {
		t.Fatalf("steering content parts = %#v, want %#v", request.ContentParts, wantParts)
	}
}

func TestRuntimeAgentSteeringWithoutActiveMainTurnHasNoControlEffect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		run         appserver.RunState
		meta        map[string]json.RawMessage
		wantOutcome steeringwire.SessionSteeringOutcome
		wantReason  string
	}{
		{
			name:        "idle default",
			run:         appserver.RunState{},
			wantOutcome: steeringwire.SessionSteeringFailed,
		},
		{
			name: "idle prompt required",
			meta: map[string]json.RawMessage{
				steeringwire.SessionSteeringMetaKey: json.RawMessage(`{"idleBehavior":"promptRequired"}`),
			},
			wantOutcome: steeringwire.SessionSteeringPromptRequired,
			wantReason:  "noRunningTurn",
		},
		{
			name: "active participant",
			run: appserver.RunState{
				Active: true, Kind: appserver.RunKindParticipant,
				HandleID: "participant-handle", RunID: "participant-run", TurnID: "participant-turn",
			},
			meta: map[string]json.RawMessage{
				steeringwire.SessionSteeringMetaKey: json.RawMessage(`{"idleBehavior":"promptRequired"}`),
			},
			wantOutcome: steeringwire.SessionSteeringPromptRequired,
			wantReason:  "noRunningTurn",
		},
		{
			name: "incomplete main target",
			run: appserver.RunState{
				Active: true, Kind: appserver.RunKindKernel,
				HandleID: "main-handle", RunID: "main-run",
			},
			wantOutcome: steeringwire.SessionSteeringFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state := activeSteeringTestState()
			state.Run = tt.run
			client := &steeringTestSessionClient{state: state}
			agent := steeringTestAgent(client)
			response, err := agent.SteerSession(context.Background(), steeringwire.SessionSteeringRequest{
				SessionID: state.SessionID,
				Prompt: []json.RawMessage{
					jsonrpc.MustMarshalRaw(eventstream.TextContent{Type: "text", Text: "continue"}),
				},
				Meta: tt.meta,
			})
			if err != nil {
				t.Fatal(err)
			}
			if response.Outcome != tt.wantOutcome || response.Reason != tt.wantReason {
				t.Fatalf("steering response = %#v, want outcome %q reason %q", response, tt.wantOutcome, tt.wantReason)
			}
			if len(client.steerRequests) != 0 {
				t.Fatalf("Control steer calls = %d, want 0", len(client.steerRequests))
			}
		})
	}
}

func TestRuntimeAgentReportsOnlyCommittedSteeringAsInjected(t *testing.T) {
	t.Parallel()

	state := activeSteeringTestState()
	tests := []struct {
		outcome appserver.Outcome
		err     error
	}{
		{outcome: appserver.OutcomeAccepted},
		{outcome: appserver.OutcomeConflicted, err: appserver.NewOutcomeError(appserver.OutcomeConflicted, errors.New("stale target"))},
		{outcome: appserver.OutcomeRejected, err: appserver.NewOutcomeError(appserver.OutcomeRejected, errors.New("rejected"))},
		{outcome: appserver.OutcomeUnknown, err: appserver.NewOutcomeError(appserver.OutcomeUnknown, errors.New("unknown outcome"))},
	}
	for _, tt := range tests {
		t.Run(string(tt.outcome), func(t *testing.T) {
			t.Parallel()
			client := &steeringTestSessionClient{
				state: state,
				result: appserver.CommandResult{
					OperationID: "steer-" + string(tt.outcome),
					Outcome:     tt.outcome,
					SessionID:   state.SessionID,
				},
				steerErr: tt.err,
			}
			response, err := steeringTestAgent(client).SteerSession(context.Background(), steeringwire.SessionSteeringRequest{
				SessionID: state.SessionID,
				Prompt: []json.RawMessage{
					jsonrpc.MustMarshalRaw(eventstream.TextContent{Type: "text", Text: "continue"}),
				},
			})
			if err == nil {
				t.Fatalf("SteerSession response = %#v, want %q error", response, tt.outcome)
			}
			var receipt *appserver.CommandReceiptError
			if !errors.As(err, &receipt) || receipt.Receipt.Outcome != tt.outcome {
				t.Fatalf("SteerSession error = %T %v, want %q receipt", err, err, tt.outcome)
			}
			if response.Outcome == steeringwire.SessionSteeringInjected {
				t.Fatalf("SteerSession response = %#v, %q command reported injected", response, tt.outcome)
			}
		})
	}
}

type steeringTestSessionClient struct {
	appserver.SessionClient
	state              appserver.SessionState
	currentRevision    uint64
	result             appserver.CommandResult
	steerErr           error
	createRequests     []appserver.CreateSessionRequest
	closeRequests      []appserver.CloseSessionRequest
	closeContextErrors []error
	steerRequests      []appserver.SteerRequest
}

func (c *steeringTestSessionClient) CreateSession(_ context.Context, request appserver.CreateSessionRequest) (appserver.CommandResult, error) {
	c.createRequests = append(c.createRequests, request)
	return c.result, nil
}

func (c *steeringTestSessionClient) CloseSession(ctx context.Context, request appserver.CloseSessionRequest) (appserver.CommandResult, error) {
	c.closeRequests = append(c.closeRequests, request)
	c.closeContextErrors = append(c.closeContextErrors, ctx.Err())
	return c.result, nil
}

func (c *steeringTestSessionClient) InspectSession(context.Context, appserver.StateRequest) (appserver.SessionState, error) {
	return c.state, nil
}

func (c *steeringTestSessionClient) Steer(_ context.Context, request appserver.SteerRequest) (appserver.CommandResult, error) {
	c.steerRequests = append(c.steerRequests, request)
	if request.ExpectedRevision != nil && *request.ExpectedRevision != c.currentRevision {
		return appserver.CommandResult{Outcome: appserver.OutcomeConflicted}, errors.New("revision conflict")
	}
	return c.result, c.steerErr
}

func steeringTestAgent(client appserver.SessionClient) *RuntimeAgent {
	return &RuntimeAgent{
		appName:         "caelis",
		userID:          "user-1",
		sessionClient:   client,
		managedSessions: map[string]struct{}{},
	}
}

func activeSteeringTestState() appserver.SessionState {
	return appserver.SessionState{
		SessionID:    "session-1",
		WorkspaceKey: "workspace-1",
		Controller: session.ControllerBinding{
			ControllerID: "controller-1",
			EpochID:      "epoch-1",
		},
		Run: appserver.RunState{
			Active:   true,
			Kind:     appserver.RunKindKernel,
			HandleID: "handle-1",
			RunID:    "run-1",
			TurnID:   "turn-1",
		},
	}
}
