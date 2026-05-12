//go:build e2e

package eval

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/surfaces/headless"
)

func TestLocalStackGatewayACPMainE2E(t *testing.T) {
	repo := repoRootForGatewayAppTest(t)
	root := t.TempDir()
	workdir := t.TempDir()

	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "user-1",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "auto-review",
		Assembly: assembly.ResolvedAssembly{
			Agents: []assembly.AgentConfig{{
				Name:        "codex",
				Description: "ACP main controller.",
				Command:     "go",
				Args:        []string{"run", "./internal/acpe2eagent"},
				WorkDir:     repo,
				Env: map[string]string{
					"SDK_ACP_STUB_REPLY":         "gateway acp main ok",
					"SDK_ACP_ENABLE_MODE_CONFIG": "1",
					"SDK_ACP_SESSION_ROOT":       filepath.Join(root, "controller-sessions"),
					"SDK_ACP_TASK_ROOT":          filepath.Join(root, "controller-tasks"),
				},
			}},
		},
		Model: gatewayapp.ModelConfig{
			Provider: "minimax",
			Model:    "MiniMax-M2",
		},
	})
	if err != nil {
		t.Fatalf("gatewayapp.NewLocalStack() error = %v", err)
	}

	activeSession, err := stack.StartSession(context.Background(), "gateway-acp-main", "surface-acp-main")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	updated, err := stack.Gateway.HandoffController(context.Background(), kernel.HandoffControllerRequest{
		SessionRef: activeSession.SessionRef,
		Kind:       session.ControllerKindACP,
		Agent:      "codex",
		Source:     "test",
		Reason:     "delegate main control",
	})
	if err != nil {
		t.Fatalf("HandoffController() error = %v", err)
	}
	if updated.Controller.Kind != session.ControllerKindACP {
		t.Fatalf("controller kind = %q, want %q", updated.Controller.Kind, session.ControllerKindACP)
	}

	state, err := stack.Gateway.ControlPlaneState(context.Background(), kernel.ControlPlaneStateRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("ControlPlaneState() error = %v", err)
	}
	if state.Controller.Kind != session.ControllerKindACP || strings.TrimSpace(state.Controller.EpochID) == "" {
		t.Fatalf("control state = %+v", state)
	}
	controllerStatus, found, err := stack.ACPControllerStatus(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("ACPControllerStatus() error = %v", err)
	}
	if !found {
		t.Fatal("ACPControllerStatus() found = false")
	}
	if got := strings.TrimSpace(controllerStatus.Mode); got != "default" {
		t.Fatalf("ACPControllerStatus().Mode = %q, want default", got)
	}
	if got := len(controllerStatus.ModeOptions); got != 2 {
		t.Fatalf("len(ACPControllerStatus().ModeOptions) = %d, want 2", got)
	}
	updatedStatus, err := stack.SetACPControllerMode(context.Background(), activeSession.SessionRef, "plan")
	if err != nil {
		t.Fatalf("SetACPControllerMode(plan) error = %v", err)
	}
	if got := strings.TrimSpace(updatedStatus.Mode); got != "plan" {
		t.Fatalf("SetACPControllerMode(plan).Mode = %q, want plan", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	result, err := headless.RunOnce(ctx, stack.Gateway, kernel.BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "run through acp controller",
		Surface:    "headless-acp-main-e2e",
	}, headless.Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if got := strings.TrimSpace(result.Output); got != "gateway acp main ok" {
		t.Fatalf("RunOnce() output = %q, want %q", got, "gateway acp main ok")
	}

	loaded, err := stack.Sessions.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var sawACPAssistant bool
	for _, event := range loaded.Events {
		if event == nil || session.EventTypeOf(event) != session.EventTypeAssistant || event.Scope == nil {
			continue
		}
		if event.Scope.Controller.Kind == session.ControllerKindACP && strings.TrimSpace(session.EventText(event)) == "gateway acp main ok" {
			sawACPAssistant = true
			break
		}
	}
	if !sawACPAssistant {
		t.Fatalf("loaded events missing ACP-scoped assistant reply: %#v", loaded.Events)
	}
}
