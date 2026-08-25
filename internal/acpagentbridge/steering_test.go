package acpagentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestRuntimeAgentAdvertisesSteeringOnlyWithAppServerBackend(t *testing.T) {
	t.Parallel()

	client := &steeringTestSessionClient{state: activeSteeringTestState()}
	typed := steeringTestAgent(client)
	response, err := typed.Initialize(context.Background(), acp.InitializeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var capability acp.SessionSteeringCapability
	if err := json.Unmarshal(response.Meta[acp.SessionSteeringMetaKey], &capability); err != nil {
		t.Fatalf("decode steering capability: %v", err)
	}
	if !capability.Supported {
		t.Fatalf("steering capability = %#v, want supported", capability)
	}

	direct := steeringTestAgent(nil)
	directResponse, err := direct.Initialize(context.Background(), acp.InitializeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := directResponse.Meta[acp.SessionSteeringMetaKey]; ok {
		t.Fatalf("direct Runtime initialize _meta = %#v, want no steering capability", directResponse.Meta)
	}
	if _, err := direct.SteerSession(context.Background(), acp.SessionSteeringRequest{}); !errors.Is(err, acp.ErrCapabilityUnsupported) {
		t.Fatalf("direct Runtime SteerSession error = %v, want capability unsupported", err)
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
		jsonrpc.MustMarshalRaw(acp.TextContent{Type: "text", Text: "adjust the plan"}),
		jsonrpc.MustMarshalRaw(schema.ImageContent{Type: "image", MimeType: "image/png", Data: "aW1hZ2U=", Name: "plan.png"}),
	}
	response, err := agent.SteerSession(context.Background(), acp.SessionSteeringRequest{
		SessionID: state.SessionID,
		Prompt:    prompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != acp.SessionSteeringInjected {
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
		wantOutcome acp.SessionSteeringOutcome
		wantReason  string
	}{
		{
			name:        "idle default",
			run:         appserver.RunState{},
			wantOutcome: acp.SessionSteeringFailed,
		},
		{
			name: "idle prompt required",
			meta: map[string]json.RawMessage{
				acp.SessionSteeringMetaKey: json.RawMessage(`{"idleBehavior":"promptRequired"}`),
			},
			wantOutcome: acp.SessionSteeringPromptRequired,
			wantReason:  "noRunningTurn",
		},
		{
			name: "active participant",
			run: appserver.RunState{
				Active: true, Kind: appserver.RunKindParticipant,
				HandleID: "participant-handle", RunID: "participant-run", TurnID: "participant-turn",
			},
			meta: map[string]json.RawMessage{
				acp.SessionSteeringMetaKey: json.RawMessage(`{"idleBehavior":"promptRequired"}`),
			},
			wantOutcome: acp.SessionSteeringPromptRequired,
			wantReason:  "noRunningTurn",
		},
		{
			name: "incomplete main target",
			run: appserver.RunState{
				Active: true, Kind: appserver.RunKindKernel,
				HandleID: "main-handle", RunID: "main-run",
			},
			wantOutcome: acp.SessionSteeringFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state := activeSteeringTestState()
			state.Run = tt.run
			client := &steeringTestSessionClient{state: state}
			agent := steeringTestAgent(client)
			response, err := agent.SteerSession(context.Background(), acp.SessionSteeringRequest{
				SessionID: state.SessionID,
				Prompt: []json.RawMessage{
					jsonrpc.MustMarshalRaw(acp.TextContent{Type: "text", Text: "continue"}),
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
			response, err := steeringTestAgent(client).SteerSession(context.Background(), acp.SessionSteeringRequest{
				SessionID: state.SessionID,
				Prompt: []json.RawMessage{
					jsonrpc.MustMarshalRaw(acp.TextContent{Type: "text", Text: "continue"}),
				},
			})
			if err == nil {
				t.Fatalf("SteerSession response = %#v, want %q error", response, tt.outcome)
			}
			var receipt *appserver.CommandReceiptError
			if !errors.As(err, &receipt) || receipt.Receipt.Outcome != tt.outcome {
				t.Fatalf("SteerSession error = %T %v, want %q receipt", err, err, tt.outcome)
			}
			if response.Outcome == acp.SessionSteeringInjected {
				t.Fatalf("SteerSession response = %#v, %q command reported injected", response, tt.outcome)
			}
		})
	}
}

type steeringTestSessionClient struct {
	appserver.SessionClient
	state           appserver.SessionState
	currentRevision uint64
	result          appserver.CommandResult
	steerErr        error
	steerRequests   []appserver.SteerRequest
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
