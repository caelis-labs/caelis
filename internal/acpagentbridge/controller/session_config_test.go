package controller

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
)

func TestControlPreparationPreservesExternalACPDefaults(t *testing.T) {
	for _, role := range []string{"main", "side", "resumed-side"} {
		t.Run(role, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			registry, err := subagent.NewRegistry([]subagent.AgentConfig{sessionConfigTestAgent()})
			if err != nil {
				t.Fatal(err)
			}
			prepared := false
			manager, err := NewManager(Config{
				Registry: registry, PlacementResolver: testPlacementResolver(registry),
				SessionPreparer: func(_ context.Context, id string, cfg subagent.AgentConfig) (agents.SessionOptions, error) {
					if id != "session-defaults" || cfg.PinnedModel != nil || cfg.SessionOptions.ModelID != "opus" || cfg.SessionOptions.ConfigValues["effort"] != "max" {
						t.Fatalf("external preparation = %q, %#v", id, cfg)
					}
					prepared = true
					return cfg.SessionOptions, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := manager.Quiesce(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			frozen, err := placement.Seal(placement.Placement{
				Kind: placement.KindAgent, ProfileID: "acp:helper:opus", Agent: "helper", Model: "opus",
				ReasoningEffort: "xhigh", ReasoningEffortConfigID: "effort",
				SessionConfigValues: map[string]string{"effort": "max"}, ConfigFingerprint: "sha256:test",
			})
			if err != nil {
				t.Fatal(err)
			}
			parent := session.Session{SessionRef: session.SessionRef{SessionID: "parent"}, CWD: t.TempDir()}
			var turn controller.TurnResult
			if role == "main" {
				parent.Controller, err = manager.Activate(ctx, controller.HandoffRequest{Session: parent, Agent: "helper", Placement: frozen})
				if err == nil {
					turn, err = manager.RunTurn(ctx, controller.TurnRequest{Session: parent, Input: "review"})
				}
			} else {
				request := controller.AttachRequest{Session: parent, Agent: "helper", Placement: frozen}
				if role == "resumed-side" {
					request.Binding = session.ParticipantBinding{ID: "side", SessionID: "session-defaults", Placement: frozen}
				}
				var binding session.ParticipantBinding
				binding, err = manager.Attach(ctx, request)
				if err == nil {
					turn, err = manager.PromptParticipant(ctx, controller.ParticipantPromptRequest{Session: parent, ParticipantID: binding.ID, Input: "review"})
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if !prepared || turn.Handle == nil {
				t.Fatalf("prepared=%v, turn=%#v", prepared, turn)
			}
			if err := turn.Handle.WaitCompletion(ctx); err != nil {
				t.Fatal(err)
			}
			_ = turn.Handle.Close()
		})
	}
}

func TestControllerSessionPreparationFailureStopsStartup(t *testing.T) {
	for _, remoteID := range []string{"", "session-defaults"} {
		t.Run("resume="+remoteID, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			want := errors.New("provider configuration revoked")
			manager := &Manager{sessionPreparer: func(context.Context, string, subagent.AgentConfig) (agents.SessionOptions, error) {
				return agents.SessionOptions{}, want
			}}
			acpClient, id, _, err := manager.startACPClient(ctx, t.TempDir(), sessionConfigTestAgent(), remoteID, nil,
				func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
					return client.RequestPermissionResponse{}, nil
				})
			if !errors.Is(err, want) || acpClient != nil || id != "" {
				t.Fatalf("startup = %v, %q, %v; want preparation failure", acpClient, id, err)
			}
		})
	}
}

func sessionConfigTestAgent() subagent.AgentConfig {
	return subagent.AgentConfig{
		Name: "helper", Command: os.Args[0],
		Args: []string{"-test.run=^TestManagerSessionDefaultsHelperProcess$", "--"},
		Env:  map[string]string{"CAELIS_ACP_HELPER": "session-defaults"},
	}
}
