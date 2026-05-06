//go:build e2e

package gatewayapp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	headlessadapter "github.com/OnslaughtSnail/caelis/headless"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestLocalStackGatewayACPMainE2E(t *testing.T) {
	repo := repoRootForGatewayAppTest(t)
	root := t.TempDir()
	workdir := t.TempDir()

	stack, err := NewLocalStack(Config{
		AppName:        "caelis",
		UserID:         "user-1",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
		Assembly: sdkplugin.ResolvedAssembly{
			Agents: []sdkplugin.AgentConfig{{
				Name:        "codex",
				Description: "ACP main controller.",
				Command:     "go",
				Args:        []string{"run", "./acpbridge/cmd/e2eagent"},
				WorkDir:     repo,
				Env: map[string]string{
					"SDK_ACP_STUB_REPLY":         "gateway acp main ok",
					"SDK_ACP_ENABLE_MODE_CONFIG": "1",
					"SDK_ACP_SESSION_ROOT":       filepath.Join(root, "controller-sessions"),
					"SDK_ACP_TASK_ROOT":          filepath.Join(root, "controller-tasks"),
				},
			}},
		},
		Model: ModelConfig{
			Provider: "minimax",
			Model:    "MiniMax-M2",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}

	session, err := stack.StartSession(context.Background(), "gateway-acp-main", "surface-acp-main")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	updated, err := stack.Gateway.HandoffController(context.Background(), appgateway.HandoffControllerRequest{
		SessionRef: session.SessionRef,
		Kind:       sdksession.ControllerKindACP,
		Agent:      "codex",
		Source:     "test",
		Reason:     "delegate main control",
	})
	if err != nil {
		t.Fatalf("HandoffController() error = %v", err)
	}
	if updated.Controller.Kind != sdksession.ControllerKindACP {
		t.Fatalf("controller kind = %q, want %q", updated.Controller.Kind, sdksession.ControllerKindACP)
	}

	state, err := stack.Gateway.ControlPlaneState(context.Background(), appgateway.ControlPlaneStateRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("ControlPlaneState() error = %v", err)
	}
	if state.Controller.Kind != sdksession.ControllerKindACP || strings.TrimSpace(state.Controller.EpochID) == "" {
		t.Fatalf("control state = %+v", state)
	}
	controllerStatus, found, err := stack.ACPControllerStatus(context.Background(), session.SessionRef)
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
	updatedStatus, err := stack.SetACPControllerMode(context.Background(), session.SessionRef, "plan")
	if err != nil {
		t.Fatalf("SetACPControllerMode(plan) error = %v", err)
	}
	if got := strings.TrimSpace(updatedStatus.Mode); got != "plan" {
		t.Fatalf("SetACPControllerMode(plan).Mode = %q, want plan", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	result, err := headlessadapter.RunOnce(ctx, stack.Gateway, appgateway.BeginTurnRequest{
		SessionRef: session.SessionRef,
		Input:      "run through acp controller",
		Surface:    "headless-acp-main-e2e",
	}, headlessadapter.Options{})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if got := strings.TrimSpace(result.Output); got != "gateway acp main ok" {
		t.Fatalf("RunOnce() output = %q, want %q", got, "gateway acp main ok")
	}

	loaded, err := stack.Sessions.LoadSession(ctx, sdksession.LoadSessionRequest{
		SessionRef: session.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var sawACPAssistant bool
	for _, event := range loaded.Events {
		if event == nil || sdksession.EventTypeOf(event) != sdksession.EventTypeAssistant || event.Scope == nil {
			continue
		}
		if event.Scope.Controller.Kind == sdksession.ControllerKindACP && strings.TrimSpace(event.Text) == "gateway acp main ok" {
			sawACPAssistant = true
			break
		}
	}
	if !sawACPAssistant {
		t.Fatalf("loaded events missing ACP-scoped assistant reply: %#v", loaded.Events)
	}
}
