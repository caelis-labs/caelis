//go:build e2e

package eval

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
	"github.com/OnslaughtSnail/caelis/protocol/acp/projector"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
	"github.com/OnslaughtSnail/caelis/surfaces/headless"
)

func TestLocalStackGatewayACPMainE2E(t *testing.T) {
	repo := repoRootForGatewayAppTest(t)
	root := t.TempDir()
	workdir := t.TempDir()

	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "user-1",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "auto-review",
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

	updated, err := stack.KernelControlPlane().HandoffController(context.Background(), gateway.HandoffControllerRequest{
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

	state, err := stack.KernelControlPlane().ControlPlaneState(context.Background(), gateway.ControlPlaneStateRequest{
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

	result, err := headless.RunOnce(ctx, stack.KernelTurns(), gateway.BeginTurnRequest{
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

func TestLocalStackGatewayACPCommandEventShapeE2E(t *testing.T) {
	repo := repoRootForGatewayAppTest(t)
	root := t.TempDir()
	workdir := t.TempDir()

	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "user-1",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "auto-review",
		Assembly: assembly.ResolvedAssembly{
			Agents: []assembly.AgentConfig{{
				Name:        "codex",
				Description: "ACP main controller.",
				Command:     "go",
				Args:        []string{"run", "./internal/acpe2eagent"},
				WorkDir:     repo,
				Env: map[string]string{
					"SDK_ACP_SCRIPTED_MODE": "async_command",
					"SDK_ACP_SESSION_ROOT":  filepath.Join(root, "controller-sessions"),
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
	activeSession, err := stack.StartSession(context.Background(), "gateway-acp-command", "surface-acp-command")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	updated, err := stack.KernelControlPlane().HandoffController(context.Background(), gateway.HandoffControllerRequest{
		SessionRef: activeSession.SessionRef,
		Kind:       session.ControllerKindACP,
		Agent:      "codex",
		Source:     "test",
		Reason:     "delegate main control",
	})
	if err != nil {
		t.Fatalf("HandoffController() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := stack.KernelTurns().BeginTurn(ctx, gateway.BeginTurnRequest{
		SessionRef: updated.SessionRef,
		Input:      "run a simple command",
		Surface:    "headless-acp-command-shape-e2e",
	})
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if result.Handle == nil {
		t.Fatal("BeginTurn() returned nil handle")
	}
	defer result.Handle.Close()

	var sawCommandCall bool
	var sawCommandUpdate bool
	var sawTaskFinal bool
	for env := range projector.ACPEventsFromGatewayHandle(result.Handle) {
		switch update := env.Update.(type) {
		case schema.ToolCall:
			if update.ToolCallID == "command-async-1" &&
				update.Kind == schema.ToolKindExecute &&
				toolContentHasTerminal(update.Content) &&
				terminalInfoID(update.Meta) == "command-async-1" {
				sawCommandCall = true
			}
			t.Logf("tool_call call_id=%q title=%q kind=%q status=%q raw_input=%s meta=%s content=%s",
				update.ToolCallID,
				update.Title,
				update.Kind,
				update.Status,
				debugJSON(update.RawInput),
				debugJSON(update.Meta),
				debugJSON(update.Content),
			)
		case schema.ToolCallUpdate:
			if update.ToolCallID == "command-async-1" &&
				stringPtrDebug(update.Kind) == schema.ToolKindExecute &&
				stringPtrDebug(update.Status) == schema.ToolStatusInProgress &&
				toolContentHasTerminal(update.Content) &&
				terminalInfoID(update.Meta) == "command-async-1" {
				sawCommandUpdate = true
			}
			if update.ToolCallID == "task-wait-1" &&
				stringPtrDebug(update.Kind) == schema.ToolKindExecute &&
				stringPtrDebug(update.Status) == schema.ToolStatusCompleted &&
				toolContentHasTerminal(update.Content) &&
				terminalInfoID(update.Meta) == "task-wait-1" &&
				terminalExitID(update.Meta) == "task-wait-1" {
				sawTaskFinal = true
			}
			t.Logf("tool_update call_id=%q title=%q kind=%q status=%q raw_input=%s raw_output=%s meta=%s content=%s",
				update.ToolCallID,
				stringPtrDebug(update.Title),
				stringPtrDebug(update.Kind),
				stringPtrDebug(update.Status),
				debugJSON(update.RawInput),
				debugJSON(update.RawOutput),
				debugJSON(update.Meta),
				debugJSON(update.Content),
			)
		}
	}
	if !sawCommandCall {
		t.Fatal("did not capture command tool_call with ACP execute terminal shape")
	}
	if !sawCommandUpdate {
		t.Fatal("did not capture command tool_update with ACP execute terminal shape")
	}
	if !sawTaskFinal {
		t.Fatal("did not capture TASK final with ACP execute terminal exit shape")
	}
}

func debugJSON(value any) string {
	if value == nil {
		return "null"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "<json error: " + err.Error() + ">"
	}
	return string(raw)
}

func stringPtrDebug(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

func toolContentHasTerminal(content []schema.ToolCallContent) bool {
	for _, item := range content {
		if item.Type == "terminal" && strings.TrimSpace(item.TerminalID) != "" {
			return true
		}
	}
	return false
}

func terminalInfoID(meta map[string]any) string {
	info, ok := metautil.TerminalInfo(meta)
	if !ok {
		return ""
	}
	return strings.TrimSpace(info.TerminalID)
}

func terminalExitID(meta map[string]any) string {
	exit, ok := metautil.TerminalExit(meta)
	if !ok {
		return ""
	}
	return strings.TrimSpace(exit.TerminalID)
}
