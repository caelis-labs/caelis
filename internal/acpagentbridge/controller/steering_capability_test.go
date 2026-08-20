package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
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
				AgentCapabilities: schema.AgentCapabilities{SessionCapabilities: map[string]json.RawMessage{
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
