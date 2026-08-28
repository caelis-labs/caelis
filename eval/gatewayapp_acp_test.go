//go:build e2e

package eval

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/surfaces/headless"
)

func TestLocalStackGatewayACPMainE2E(t *testing.T) {
	repo := repoRootForGatewayAppTest(t)
	root := privateEvalTempDir(t)
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
	})
	if err != nil {
		t.Fatalf("gatewayapp.NewLocalStack() error = %v", err)
	}

	activeSession := startEvalSession(t, context.Background(), stack, "gateway-acp-main")
	clients := evalAppServerClients(t, stack, activeSession.UserID)
	runtimeLease, err := stack.ControlRuntimes().Acquire(
		context.Background(),
		appserver.Principal{ID: activeSession.UserID},
		appserver.ActionSessionInspect,
		activeSession.SessionID,
		true,
	)
	if err != nil {
		t.Fatalf("ControlRuntimes().Acquire() error = %v", err)
	}
	t.Cleanup(func() { _ = runtimeLease.Close(context.Background()) })

	updated := handoffEvalController(t, context.Background(), stack, activeSession, "codex", "gateway-acp-main-e2e")
	if updated.Controller.Kind != session.ControllerKindACP {
		t.Fatalf("controller kind = %q, want %q", updated.Controller.Kind, session.ControllerKindACP)
	}

	state := inspectEvalSession(t, context.Background(), stack, activeSession)
	if state.Controller.Kind != session.ControllerKindACP || strings.TrimSpace(state.Controller.EpochID) == "" {
		t.Fatalf("control state = %+v", state)
	}
	controllerStatus, err := clients.Status.SessionStatus(context.Background(), appserver.StatusRequest{
		SessionID: activeSession.SessionID,
		Surface:   "gateway-acp-main-e2e",
	})
	if err != nil {
		t.Fatalf("SessionStatus() error = %v", err)
	}
	if got := strings.TrimSpace(controllerStatus.Session.ModeLabel); got != "Default" {
		t.Fatalf("SessionStatus().Session.ModeLabel = %q, want Default", got)
	}
	current, err := clients.Sessions.InspectSession(context.Background(), appserver.StateRequest{SessionID: activeSession.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	revision := current.Revision
	modeResult, err := clients.Configuration.ConfigureSessionControllerMode(
		context.Background(),
		appserver.SessionControllerModeRequest{
			WriteBase: appserver.WriteBase{
				OperationID:             "eval-controller-mode-plan",
				SessionID:               activeSession.SessionID,
				ExpectedRevision:        &revision,
				ExpectedControllerEpoch: current.Controller.EpochID,
			},
			Mode: "plan",
		},
	)
	if err != nil || modeResult.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConfigureSessionControllerMode(plan) = %#v, %v", modeResult, err)
	}
	updatedStatus, err := clients.Status.SessionStatus(context.Background(), appserver.StatusRequest{
		SessionID: activeSession.SessionID,
		Surface:   "gateway-acp-main-e2e",
	})
	if err != nil {
		t.Fatalf("SessionStatus(after mode) error = %v", err)
	}
	if got := strings.TrimSpace(updatedStatus.Session.ModeLabel); got != "Plan" {
		t.Fatalf("SessionStatus(after mode).Session.ModeLabel = %q, want Plan", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	result, err := runEvalHeadlessOnce(t, ctx, stack, activeSession, "run through acp controller", headless.Options{})
	if err != nil {
		t.Fatalf("RunSessionOnce() error = %v", err)
	}
	if got := strings.TrimSpace(result.Output); got != "gateway acp main ok" {
		t.Fatalf("RunSessionOnce() output = %q, want %q", got, "gateway acp main ok")
	}
	loaded, err := stack.Sessions().LoadSession(ctx, session.LoadSessionRequest{
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
	root := privateEvalTempDir(t)
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
	})
	if err != nil {
		t.Fatalf("gatewayapp.NewLocalStack() error = %v", err)
	}
	activeSession := startEvalSession(t, context.Background(), stack, "gateway-acp-command")
	handoffEvalController(t, context.Background(), stack, activeSession, "codex", "gateway-acp-command-shape-e2e")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	turn, err := startEvalSessionTurn(t, ctx, stack, activeSession, "run a simple command")
	if err != nil {
		t.Fatalf("SessionTurnClient.Start() error = %v", err)
	}
	if turn == nil {
		t.Fatal("SessionTurnClient.Start() returned nil Turn")
	}
	defer turn.Close()

	var sawCommandCall bool
	var sawCommandUpdate bool
	var sawCommandOutput bool
	var sawCommandFinal bool
	var sawTaskFinal bool
	for env := range turn.Events() {
		switch update := env.Update.(type) {
		case eventstream.ToolCall:
			if update.ToolCallID == "command-async-1" &&
				update.Kind == eventstream.ToolKindExecute &&
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
		case eventstream.ToolCallUpdate:
			if update.ToolCallID == "command-async-1" &&
				stringPtrDebug(update.Kind) == eventstream.ToolKindExecute &&
				stringPtrDebug(update.Status) == eventstream.ToolStatusInProgress &&
				toolContentHasTerminal(update.Content) &&
				terminalInfoID(update.Meta) == "command-async-1" {
				sawCommandUpdate = true
			}
			if output, ok := evalTerminalOutput(update.Meta); update.ToolCallID == "command-async-1" &&
				ok &&
				output.TerminalID == "command-async-1" &&
				strings.Contains(output.Data, "acpx async command ok") &&
				toolContentHasTerminal(update.Content) &&
				terminalInfoID(update.Meta) == "command-async-1" {
				sawCommandOutput = true
			}
			if update.ToolCallID == "command-async-1" &&
				stringPtrDebug(update.Status) == eventstream.ToolStatusCompleted &&
				toolContentHasTerminal(update.Content) &&
				terminalInfoID(update.Meta) == "command-async-1" &&
				terminalExitID(update.Meta) == "command-async-1" {
				sawCommandFinal = true
			}
			if update.ToolCallID == "task-wait-1" &&
				stringPtrDebug(update.Kind) == eventstream.ToolKindOther &&
				stringPtrDebug(update.Status) == eventstream.ToolStatusCompleted &&
				!toolContentHasTerminal(update.Content) &&
				terminalInfoID(update.Meta) == "" &&
				terminalExitID(update.Meta) == "" &&
				isCompletedCommandTaskObservation(update.RawOutput) {
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
	if !sawCommandOutput {
		t.Fatal("did not capture RunCommand output on its mounted ACP terminal")
	}
	if !sawCommandFinal {
		t.Fatal("did not capture RunCommand completed status with terminal_exit")
	}
	if !sawTaskFinal {
		t.Fatal("did not capture TASK completed observation without a duplicate terminal")
	}
}

func TestLocalStackGatewayACPInteractiveTaskReadWriteE2E(t *testing.T) {
	repo := repoRootForGatewayAppTest(t)
	root := privateEvalTempDir(t)
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
					"SDK_ACP_SCRIPTED_MODE": "interactive_command",
					"SDK_ACP_SESSION_ROOT":  filepath.Join(root, "controller-sessions"),
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("gatewayapp.NewLocalStack() error = %v", err)
	}
	activeSession := startEvalSession(t, context.Background(), stack, "gateway-acp-interactive")
	handoffEvalController(t, context.Background(), stack, activeSession, "codex", "gateway-acp-interactive-e2e")

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	turn, err := startEvalSessionTurn(t, ctx, stack, activeSession, "exercise Task write and read")
	if err != nil {
		t.Fatalf("SessionTurnClient.Start() error = %v", err)
	}
	if turn == nil {
		t.Fatal("SessionTurnClient.Start() returned nil Turn")
	}
	defer turn.Close()

	var (
		commandOutput strings.Builder
		sawWrite      bool
		sawRead       bool
		sawCancel     bool
	)
	for env := range turn.Events() {
		update, ok := env.Update.(eventstream.ToolCallUpdate)
		if !ok {
			continue
		}
		if output, ok := evalTerminalOutput(update.Meta); ok && update.ToolCallID == "command-interactive-1" {
			commandOutput.WriteString(output.Data)
		}
		raw, _ := update.RawOutput.(map[string]any)
		if stringPtrDebug(update.Status) != eventstream.ToolStatusCompleted {
			continue
		}
		switch update.ToolCallID {
		case "task-write-interactive-1":
			sawWrite = mapStringValue(raw, "state") == "running" &&
				strings.Contains(mapStringValue(raw, "latest_output"), "echo:ping")
		case "task-read-interactive-1":
			sawRead = mapStringValue(raw, "state") == "running" &&
				strings.Contains(mapStringValue(raw, "latest_output"), "later:ping")
		case "task-cancel-interactive-1":
			sawCancel = mapStringValue(raw, "state") == "cancelled"
		}
	}
	if !sawWrite {
		t.Fatal("did not capture non-terminal Task write response")
	}
	if !sawRead {
		t.Fatal("did not capture output-driven Task read response")
	}
	if !sawCancel {
		t.Fatal("did not capture interactive command cancellation")
	}
	for _, want := range []string{"interactive ready", "echo:ping", "later:ping"} {
		if !strings.Contains(commandOutput.String(), want) {
			t.Fatalf("command terminal output = %q, want %q", commandOutput.String(), want)
		}
	}
}

func isCompletedCommandTaskObservation(raw any) bool {
	output, ok := raw.(map[string]any)
	if !ok ||
		strings.TrimSpace(mapStringValue(output, "handle")) != "command" ||
		strings.TrimSpace(mapStringValue(output, "target_kind")) != "command" ||
		strings.TrimSpace(mapStringValue(output, "state")) != "completed" ||
		!strings.Contains(mapStringValue(output, "result"), "acpx async command ok") {
		return false
	}
	switch exitCode := output["exit_code"].(type) {
	case int:
		return exitCode == 0
	case int64:
		return exitCode == 0
	case float64:
		return exitCode == 0
	default:
		return false
	}
}

func mapStringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
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

func toolContentHasTerminal(content []eventstream.ToolCallContent) bool {
	for _, item := range content {
		if item.Type == "terminal" && strings.TrimSpace(item.TerminalID) != "" {
			return true
		}
	}
	return false
}

func terminalInfoID(meta map[string]any) string {
	return strings.TrimSpace(evalTerminalID(meta, "terminal_info"))
}

func terminalExitID(meta map[string]any) string {
	return strings.TrimSpace(evalTerminalID(meta, "terminal_exit"))
}
