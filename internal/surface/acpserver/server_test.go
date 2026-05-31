package acpserver

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/plugin"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
	"github.com/OnslaughtSnail/caelis/internal/app/local"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/internal/engine/approval"
	"github.com/OnslaughtSnail/caelis/protocol/acp/jsonrpc"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestServeStdioRunsPromptThroughCoreEngine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stack, err := local.New(local.Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
		},
		Provider: &testProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("pong")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := stack.Services().Engine()

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, Config{
			Engine:  engine,
			AppName: "caelis",
			UserID:  "tester",
			Implementation: schema.Implementation{
				Name:    "caelis",
				Version: "test",
			},
		}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	updates := make(chan updateNotification, 8)
	go func() {
		_ = conn.Serve(ctx, nil, func(_ context.Context, msg jsonrpc.Message) {
			if msg.Method != schema.MethodSessionUpdate {
				return
			}
			var notification updateNotification
			if err := json.Unmarshal(msg.Params, &notification); err == nil {
				updates <- notification
			}
		})
	}()

	var initResp schema.InitializeResponse
	if err := conn.Call(ctx, schema.MethodInitialize, schema.InitializeRequest{
		ProtocolVersion:    schema.CurrentProtocolVersion,
		ClientCapabilities: map[string]any{},
	}, &initResp); err != nil {
		t.Fatalf("initialize call error = %v", err)
	}
	if initResp.ProtocolVersion != schema.CurrentProtocolVersion {
		t.Fatalf("protocol version = %d, want %d", initResp.ProtocolVersion, schema.CurrentProtocolVersion)
	}

	var newResp schema.NewSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionNew, schema.NewSessionRequest{CWD: "/tmp/project"}, &newResp); err != nil {
		t.Fatalf("session/new call error = %v", err)
	}
	if newResp.SessionID == "" {
		t.Fatal("session/new returned empty session id")
	}

	var promptResp schema.PromptResponse
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "ping"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt call error = %v", err)
	}
	if promptResp.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want %q", promptResp.StopReason, schema.StopReasonEndTurn)
	}

	kinds := make([]string, 0, 2)
	for len(kinds) < 2 {
		select {
		case update := <-updates:
			if update.SessionID != newResp.SessionID {
				t.Fatalf("update session id = %q, want %q", update.SessionID, newResp.SessionID)
			}
			kinds = append(kinds, update.Update.SessionUpdate)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for updates, got %v", kinds)
		}
	}
	if kinds[0] != schema.UpdateUserMessage || kinds[1] != schema.UpdateAgentMessage {
		t.Fatalf("update kinds = %v, want user then agent message", kinds)
	}

	snapshot, err := stack.Services().Sessions().Load(ctx, session.Ref{SessionID: newResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 {
		t.Fatalf("stored events = %d, want 2", len(snapshot.Events))
	}
	if got := session.EventText(snapshot.Events[1]); got != "pong" {
		t.Fatalf("stored assistant text = %q, want pong", got)
	}

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServeStdioPublishesAvailableCommandsFromAppServices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stack, err := local.New(local.Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
		},
		Provider: &testProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, Config{
			Services: stack.Services(),
			Implementation: schema.Implementation{
				Name:    "caelis",
				Version: "test",
			},
		}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	updates := make(chan availableCommandsNotification, 2)
	go func() {
		_ = conn.Serve(ctx, nil, func(_ context.Context, msg jsonrpc.Message) {
			if msg.Method != schema.MethodSessionUpdate {
				return
			}
			var notification availableCommandsNotification
			if err := json.Unmarshal(msg.Params, &notification); err == nil && notification.Update.SessionUpdate == schema.UpdateAvailableCmds {
				updates <- notification
			}
		})
	}()

	var newResp schema.NewSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionNew, schema.NewSessionRequest{CWD: "/tmp/project"}, &newResp); err != nil {
		t.Fatalf("session/new call error = %v", err)
	}
	select {
	case update := <-updates:
		if update.SessionID != newResp.SessionID {
			t.Fatalf("available command update session id = %q, want %q", update.SessionID, newResp.SessionID)
		}
		if command := requireAvailableCommand(t, update.Update.AvailableCommands, "connect"); command.Input == nil || command.Input.Hint == "" {
			t.Fatalf("connect command = %#v, want input hint", command)
		}
		if command := requireAvailableCommand(t, update.Update.AvailableCommands, "compact"); command.Input != nil {
			t.Fatalf("compact command = %#v, want no input hint", command)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for available_commands_update")
	}

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestACPCommandSurfaceUpdatesUseSharedViewModels(t *testing.T) {
	updates := acpSurfaceUpdatesFromCommand(appviewmodel.CommandExecutionView{
		Command: "/task list",
		TaskPanel: &appviewmodel.TaskPanelView{
			Supported: true,
			Summary:   appviewmodel.TaskPanelSummary{Total: 1, Running: 1},
		},
		ControllerPanel: &appviewmodel.ControllerPanelView{
			Active: true,
			Summary: appviewmodel.ControllerPanelSummary{
				Agent: "reviewer",
			},
		},
	})
	if len(updates) != 2 {
		t.Fatalf("updates = %#v, want task and controller surface updates", updates)
	}
	if updates[0].SessionUpdate != schema.UpdateSurface || updates[0].Kind != "task_panel" || updates[0].Surface != "acp" {
		t.Fatalf("task surface update = %#v", updates[0])
	}
	if updates[1].Kind != "controller_panel" {
		t.Fatalf("controller surface update = %#v", updates[1])
	}
	if updates[0].Meta["source"] != "app-services" || updates[0].Meta["command"] != "/task list" {
		t.Fatalf("surface meta = %#v, want app-service command source", updates[0].Meta)
	}
}

func TestServeStdioExecutesSlashCommandsThroughAppServices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stack, err := local.New(local.Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
		},
		Provider: &testProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("normal response")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, Config{
			Services: stack.Services(),
			Implementation: schema.Implementation{
				Name:    "caelis",
				Version: "test",
			},
		}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	agentMessages := make(chan string, 8)
	go func() {
		_ = conn.Serve(ctx, nil, func(_ context.Context, msg jsonrpc.Message) {
			if msg.Method != schema.MethodSessionUpdate {
				return
			}
			var notification contentNotification
			if err := json.Unmarshal(msg.Params, &notification); err != nil {
				return
			}
			if notification.Update.SessionUpdate == schema.UpdateAgentMessage && notification.Update.Content.Text != "" {
				agentMessages <- notification.Update.Content.Text
			}
		})
	}()

	var newResp schema.NewSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionNew, schema.NewSessionRequest{CWD: "/tmp/project"}, &newResp); err != nil {
		t.Fatalf("session/new call error = %v", err)
	}

	var promptResp schema.PromptResponse
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "/status"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt(/status) call error = %v", err)
	}
	if promptResp.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("status stop reason = %q, want %q", promptResp.StopReason, schema.StopReasonEndTurn)
	}
	statusText := waitForAgentMessage(t, ctx, agentMessages, "status:")
	if !strings.Contains(statusText, "model: not configured") {
		t.Fatalf("status output = %q, want model status", statusText)
	}
	snapshot, err := stack.Services().Sessions().Load(ctx, session.Ref{SessionID: newResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 0 {
		t.Fatalf("events after /status = %#v, want no model turn events", snapshot.Events)
	}

	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "remember durable state"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt(normal) call error = %v", err)
	}
	if promptResp.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("normal stop reason = %q, want %q", promptResp.StopReason, schema.StopReasonEndTurn)
	}
	waitForAgentMessage(t, ctx, agentMessages, "normal response")

	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "/compact"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt(/compact) call error = %v", err)
	}
	if promptResp.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("compact stop reason = %q, want %q", promptResp.StopReason, schema.StopReasonEndTurn)
	}
	waitForAgentMessage(t, ctx, agentMessages, "compaction completed")
	snapshot, err = stack.Services().Sessions().Load(ctx, session.Ref{SessionID: newResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotContainsEventType(snapshot, session.EventCompact) {
		t.Fatalf("events after /compact = %#v, want compact checkpoint event", snapshot.Events)
	}
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "/resume " + newResp.SessionID}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt(/resume) call error = %v", err)
	}
	if promptResp.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("resume stop reason = %q, want %q", promptResp.StopReason, schema.StopReasonEndTurn)
	}
	waitForAgentMessage(t, ctx, agentMessages, "resume session: "+newResp.SessionID)
	waitForAgentMessage(t, ctx, agentMessages, "normal response")

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServeStdioExposesTerminalLifecycleThroughAppServices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stack, err := local.New(local.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "tester",
			WorkspaceCWD: t.TempDir(),
		},
		Provider: &testProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
		BuiltinTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, Config{
			Services: stack.Services(),
			Implementation: schema.Implementation{
				Name:    "caelis",
				Version: "test",
			},
		}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	go func() {
		_ = conn.Serve(ctx, nil, nil)
	}()

	var initResp schema.InitializeResponse
	if err := conn.Call(ctx, schema.MethodInitialize, schema.InitializeRequest{
		ProtocolVersion:    schema.CurrentProtocolVersion,
		ClientCapabilities: map[string]any{},
	}, &initResp); err != nil {
		t.Fatalf("initialize call error = %v", err)
	}
	if _, ok := initResp.AgentCapabilities.SessionCapabilities["terminal"]; !ok {
		t.Fatalf("session capabilities = %#v, want terminal capability", initResp.AgentCapabilities.SessionCapabilities)
	}

	var newResp schema.NewSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionNew, schema.NewSessionRequest{CWD: stack.Runtime().WorkspaceCWD}, &newResp); err != nil {
		t.Fatalf("session/new call error = %v", err)
	}

	limit := 1024
	var created schema.CreateTerminalResponse
	if err := conn.Call(ctx, schema.MethodTerminalCreate, schema.CreateTerminalRequest{
		SessionID:       newResp.SessionID,
		Command:         "echo acp-terminal-ok",
		OutputByteLimit: &limit,
	}, &created); err != nil {
		t.Fatalf("terminal/create call error = %v", err)
	}
	if strings.TrimSpace(created.TerminalID) == "" {
		t.Fatal("terminal/create returned empty terminal id")
	}

	var waitResp schema.TerminalWaitForExitResponse
	if err := conn.Call(ctx, schema.MethodTerminalWaitForExit, schema.TerminalWaitForExitRequest{
		SessionID:  newResp.SessionID,
		TerminalID: created.TerminalID,
	}, &waitResp); err != nil {
		t.Fatalf("terminal/wait_for_exit call error = %v", err)
	}
	if waitResp.ExitCode == nil || *waitResp.ExitCode != 0 {
		t.Fatalf("wait response = %#v, want exit code 0", waitResp)
	}

	var outputResp schema.TerminalOutputResponse
	if err := conn.Call(ctx, schema.MethodTerminalOutput, schema.TerminalOutputRequest{
		SessionID:  newResp.SessionID,
		TerminalID: created.TerminalID,
	}, &outputResp); err != nil {
		t.Fatalf("terminal/output call error = %v", err)
	}
	if !strings.Contains(outputResp.Output, "acp-terminal-ok") || outputResp.Truncated {
		t.Fatalf("terminal output = %#v, want untruncated echo output", outputResp)
	}
	if outputResp.ExitStatus == nil || outputResp.ExitStatus.ExitCode == nil || *outputResp.ExitStatus.ExitCode != 0 {
		t.Fatalf("terminal exit status = %#v, want exit code 0", outputResp.ExitStatus)
	}
	if err := conn.Call(ctx, schema.MethodTerminalRelease, schema.TerminalReleaseRequest{
		SessionID:  newResp.SessionID,
		TerminalID: created.TerminalID,
	}, nil); err != nil {
		t.Fatalf("terminal/release call error = %v", err)
	}

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServeStdioExecutesModelAndApprovalSlashCommandsThroughAppServices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	alpha, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Alias:           "alpha",
		Provider:        "openai-compatible",
		Model:           "gpt-alpha",
		ReasoningMode:   "fixed",
		ReasoningLevels: []string{"low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Alias:           "beta",
		Provider:        "openai-compatible",
		Model:           "gpt-beta",
		ReasoningMode:   "fixed",
		ReasoningLevels: []string{"low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetDefaultModel(ctx, alpha.ID); err != nil {
		t.Fatal(err)
	}
	stack, err := local.New(local.Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Settings: manager,
		Provider: &testProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, Config{
			Services: stack.Services(),
			Implementation: schema.Implementation{
				Name:    "caelis",
				Version: "test",
			},
		}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	agentMessages := make(chan string, 8)
	go func() {
		_ = conn.Serve(ctx, nil, func(_ context.Context, msg jsonrpc.Message) {
			if msg.Method != schema.MethodSessionUpdate {
				return
			}
			var notification contentNotification
			if err := json.Unmarshal(msg.Params, &notification); err != nil {
				return
			}
			if notification.Update.SessionUpdate == schema.UpdateAgentMessage && notification.Update.Content.Text != "" {
				agentMessages <- notification.Update.Content.Text
			}
		})
	}()

	var newResp schema.NewSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionNew, schema.NewSessionRequest{CWD: "/tmp/project"}, &newResp); err != nil {
		t.Fatalf("session/new call error = %v", err)
	}
	var promptResp schema.PromptResponse
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "/connect openai-compatible gpt-gamma https://api.gamma.test/v1 30 env:GAMMA_KEY 131072 4096 low,high"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt(/connect) call error = %v", err)
	}
	if promptResp.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("connect stop reason = %q, want %q", promptResp.StopReason, schema.StopReasonEndTurn)
	}
	waitForAgentMessage(t, ctx, agentMessages, "connected: openai-compatible/gpt-gamma")
	gamma, err := manager.ResolveModel("openai-compatible/gpt-gamma")
	if err != nil {
		t.Fatal(err)
	}
	if gamma.TokenEnv != "GAMMA_KEY" || gamma.ContextWindowTokens != 131072 || gamma.MaxOutputTokens != 4096 {
		t.Fatalf("connected gamma = %#v, want parsed config", gamma)
	}
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "/model"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt(/model) call error = %v", err)
	}
	modelsText := waitForAgentMessage(t, ctx, agentMessages, "models:")
	if !strings.Contains(modelsText, "alpha") || !strings.Contains(modelsText, "beta") {
		t.Fatalf("models output = %q, want alpha and beta", modelsText)
	}

	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "/model use beta high"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt(/model use) call error = %v", err)
	}
	waitForAgentMessage(t, ctx, agentMessages, "model switched to: beta")
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "/approval manual"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt(/approval manual) call error = %v", err)
	}
	waitForAgentMessage(t, ctx, agentMessages, "approval mode: manual")

	snapshot, err := stack.Services().Sessions().Load(ctx, session.Ref{SessionID: newResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State[appservices.StateCurrentModelID] != beta.ID || snapshot.State[appservices.StateCurrentReasoningEffort] != "high" || snapshot.State[appservices.StateSessionMode] != coreruntime.SessionModeManual {
		t.Fatalf("session state = %#v, want beta/high/manual", snapshot.State)
	}

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServeStdioExecutesAgentSlashCommandsThroughAppServices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpsertACPAgent(ctx, plugin.ACPAgentDescriptor{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestACPServerExternalACPHelperProcess", "--"},
		Env:     map[string]string{"CAELIS_TEST_ACPSERVER_EXTERNAL_HELPER": "1"},
	}); err != nil {
		t.Fatal(err)
	}
	stack, err := local.New(local.Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Settings: manager,
		Provider: &testProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, Config{
			Services: stack.Services(),
			Implementation: schema.Implementation{
				Name:    "caelis",
				Version: "test",
			},
		}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	agentMessages := make(chan string, 8)
	go func() {
		_ = conn.Serve(ctx, nil, func(_ context.Context, msg jsonrpc.Message) {
			if msg.Method != schema.MethodSessionUpdate {
				return
			}
			var notification contentNotification
			if err := json.Unmarshal(msg.Params, &notification); err != nil {
				return
			}
			if notification.Update.SessionUpdate == schema.UpdateAgentMessage && notification.Update.Content.Text != "" {
				agentMessages <- notification.Update.Content.Text
			}
		})
	}()

	var newResp schema.NewSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionNew, schema.NewSessionRequest{CWD: "/tmp/project"}, &newResp); err != nil {
		t.Fatalf("session/new call error = %v", err)
	}
	var promptResp schema.PromptResponse
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "/agent list"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt(/agent list) call error = %v", err)
	}
	listText := waitForAgentMessage(t, ctx, agentMessages, "agents:")
	if !strings.Contains(listText, "helper") || !strings.Contains(listText, "controller: local") {
		t.Fatalf("agent list output = %q, want helper and local controller", listText)
	}
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "/helper inspect repo"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt(/helper) call error = %v", err)
	}
	waitForAgentMessage(t, ctx, agentMessages, "acpserver helper response")
	snapshot, err := stack.Services().Sessions().Load(ctx, session.Ref{SessionID: newResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotContainsAgentParticipant(snapshot, "helper") {
		t.Fatalf("events after /helper = %#v, want helper participant events", snapshot.Events)
	}
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "/agent use helper"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt(/agent use helper) call error = %v", err)
	}
	waitForAgentMessage(t, ctx, agentMessages, "agent controller: helper")
	snapshot, err = stack.Services().Sessions().Load(ctx, session.Ref{SessionID: newResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotContainsControllerHandoff(snapshot, "helper") {
		t.Fatalf("events after /agent use helper = %#v, want helper handoff", snapshot.Events)
	}
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "/agent use local"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt(/agent use local) call error = %v", err)
	}
	waitForAgentMessage(t, ctx, agentMessages, "agent controller: local")
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "/agent remove helper"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt(/agent remove helper) call error = %v", err)
	}
	waitForAgentMessage(t, ctx, agentMessages, "agent removed: helper")
	if agents := manager.ListACPAgents(); len(agents) != 0 {
		t.Fatalf("settings agents after remove = %#v, want none", agents)
	}

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestACPServerExternalACPHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_TEST_ACPSERVER_EXTERNAL_HELPER") != "1" {
		return
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case schema.MethodInitialize:
			return schema.InitializeResponse{
				ProtocolVersion: schema.CurrentProtocolVersion,
				AgentCapabilities: schema.AgentCapabilities{
					PromptCapabilities: schema.PromptCapabilities{Image: true},
				},
				AgentInfo: &schema.Implementation{Name: "helper", Version: "test"},
			}, nil
		case schema.MethodSessionNew:
			return schema.NewSessionResponse{SessionID: "remote-helper-session"}, nil
		case schema.MethodSessionPrompt:
			var req schema.PromptRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			_ = conn.Notify(schema.MethodSessionUpdate, schema.SessionNotification{
				SessionID: req.SessionID,
				Update: schema.ContentChunk{
					SessionUpdate: schema.UpdateAgentMessage,
					Content:       schema.TextContent{Type: "text", Text: "acpserver helper response"},
				},
			})
			return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestInitializeUsesAppServicePromptCapabilities(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
	}); err != nil {
		t.Fatal(err)
	}
	services, err := appservices.New(appservices.Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   &recordingServiceEngine{},
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Services: services})
	if err != nil {
		t.Fatal(err)
	}
	raw, rpcErr := server.initialize(ctx, schema.InitializeRequest{ProtocolVersion: schema.CurrentProtocolVersion})
	if rpcErr != nil {
		t.Fatalf("initialize rpc error = %#v", rpcErr)
	}
	resp, ok := raw.(schema.InitializeResponse)
	if !ok {
		t.Fatalf("initialize response = %#v, want schema.InitializeResponse", raw)
	}
	if resp.AgentCapabilities.PromptCapabilities.Image {
		t.Fatalf("prompt capabilities = %#v, want deepseek-only image support disabled", resp.AgentCapabilities.PromptCapabilities)
	}

	if _, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Provider: "openai",
		Model:    "gpt-4o",
	}); err != nil {
		t.Fatal(err)
	}
	raw, rpcErr = server.initialize(ctx, schema.InitializeRequest{ProtocolVersion: schema.CurrentProtocolVersion})
	if rpcErr != nil {
		t.Fatalf("initialize rpc error after image model = %#v", rpcErr)
	}
	resp = raw.(schema.InitializeResponse)
	if !resp.AgentCapabilities.PromptCapabilities.Image {
		t.Fatalf("prompt capabilities = %#v, want image support from configured gpt-4o", resp.AgentCapabilities.PromptCapabilities)
	}
}

func TestServeStdioListsLoadsAndResumesSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stack, err := local.New(local.Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
		},
		Provider: &testProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("unused")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := stack.Services().Engine()
	active, err := engine.StartSession(ctx, session.StartRequest{
		AppName:            "caelis",
		UserID:             "tester",
		PreferredSessionID: "sess-load",
		Workspace:          session.Workspace{Key: "project", CWD: "/tmp/project"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RecordEvents(ctx, active.Ref, []session.Event{
		{
			Type: session.EventUser,
			Message: &model.Message{
				Role:  model.RoleUser,
				Parts: []model.Part{model.NewTextPart("ping")},
			},
		},
		{
			Type: session.EventAssistant,
			Message: &model.Message{
				Role:  model.RoleAssistant,
				Parts: []model.Part{model.NewTextPart("pong")},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, Config{
			Engine:   engine,
			Services: stack.Services(),
			AppName:  "caelis",
			UserID:   "tester",
			Implementation: schema.Implementation{
				Name:    "caelis",
				Version: "test",
			},
		}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	updates := make(chan updateNotification, 4)
	go func() {
		_ = conn.Serve(ctx, nil, func(_ context.Context, msg jsonrpc.Message) {
			if msg.Method != schema.MethodSessionUpdate {
				return
			}
			var notification updateNotification
			if err := json.Unmarshal(msg.Params, &notification); err == nil {
				updates <- notification
			}
		})
	}()

	var initResp schema.InitializeResponse
	if err := conn.Call(ctx, schema.MethodInitialize, schema.InitializeRequest{
		ProtocolVersion:    schema.CurrentProtocolVersion,
		ClientCapabilities: map[string]any{},
	}, &initResp); err != nil {
		t.Fatalf("initialize call error = %v", err)
	}
	if !initResp.AgentCapabilities.LoadSession {
		t.Fatalf("load session capability = false, want true")
	}

	var listResp schema.SessionListResponse
	if err := conn.Call(ctx, schema.MethodSessionList, schema.SessionListRequest{CWD: "/tmp/project"}, &listResp); err != nil {
		t.Fatalf("session/list call error = %v", err)
	}
	if len(listResp.Sessions) != 1 || listResp.Sessions[0].SessionID != "sess-load" {
		t.Fatalf("session/list response = %#v, want sess-load", listResp)
	}
	if listResp.Sessions[0].CWD != "/tmp/project" || listResp.Sessions[0].Title != "ping" || listResp.Sessions[0].UpdatedAt == "" {
		t.Fatalf("session/list summary = %#v, want cwd/derived title/updatedAt", listResp.Sessions[0])
	}

	var loadResp schema.LoadSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionLoad, schema.LoadSessionRequest{SessionID: "sess-load", CWD: "/tmp/project"}, &loadResp); err != nil {
		t.Fatalf("session/load call error = %v", err)
	}
	var kinds []string
	for len(kinds) < 2 {
		select {
		case update := <-updates:
			if update.SessionID != "sess-load" {
				t.Fatalf("load update session id = %q, want sess-load", update.SessionID)
			}
			kinds = append(kinds, update.Update.SessionUpdate)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for load updates, got %v", kinds)
		}
	}
	if kinds[0] != schema.UpdateUserMessage || kinds[1] != schema.UpdateAgentMessage {
		t.Fatalf("load update kinds = %v, want user then agent", kinds)
	}

	var resumeResp schema.ResumeSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionResume, schema.ResumeSessionRequest{SessionID: "sess-load", CWD: "/tmp/project"}, &resumeResp); err != nil {
		t.Fatalf("session/resume call error = %v", err)
	}

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServeStdioExposesAndSetsModelOptions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	alpha, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Alias:                  "alpha",
		Provider:               "openai-compatible",
		Model:                  "gpt-alpha",
		BaseURL:                "https://api.alpha.test/v1",
		DefaultReasoningEffort: "low",
		ReasoningMode:          "fixed",
		ReasoningLevels:        []string{"low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Alias:                  "beta",
		Provider:               "openai-compatible",
		Model:                  "gpt-beta",
		BaseURL:                "https://api.beta.test/v1",
		DefaultReasoningEffort: "low",
		ReasoningMode:          "fixed",
		ReasoningLevels:        []string{"low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetDefaultModel(ctx, alpha.ID); err != nil {
		t.Fatal(err)
	}
	stack, err := local.New(local.Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Provider: &testProvider{message: model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.NewTextPart("unused")}}},
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, Config{
			Services: stack.Services(),
			Implementation: schema.Implementation{
				Name:    "caelis",
				Version: "test",
			},
		}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	go func() {
		_ = conn.Serve(ctx, nil, func(context.Context, jsonrpc.Message) {})
	}()

	var newResp schema.NewSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionNew, schema.NewSessionRequest{CWD: "/tmp/project"}, &newResp); err != nil {
		t.Fatalf("session/new call error = %v", err)
	}
	if newResp.Models == nil || newResp.Models.CurrentModelID != alpha.ID || len(newResp.Models.AvailableModels) != 2 {
		t.Fatalf("new session models = %#v, want alpha current with two models", newResp.Models)
	}
	if newResp.Modes == nil || newResp.Modes.CurrentModeID != coreruntime.SessionModeAutoReview || len(newResp.Modes.AvailableModes) != 2 {
		t.Fatalf("new session modes = %#v, want auto-review with two modes", newResp.Modes)
	}
	if len(newResp.ConfigOptions) != 15 {
		t.Fatalf("new session config options = %#v, want mode/model/reasoning plus expanded settings options", newResp.ConfigOptions)
	}
	if option := requireACPConfigOption(t, newResp.ConfigOptions, "skill_loading_mode"); option.CurrentValue != appsettings.SkillLoadingModeExplicit {
		t.Fatalf("skill loading option = %#v, want explicit default", option)
	}
	if option := requireACPConfigOption(t, newResp.ConfigOptions, "skill_max_expansion_chars"); option.CurrentValue != float64(64000) {
		t.Fatalf("skill budget option = %#v, want default budget", option)
	}
	if option := requireACPConfigOption(t, newResp.ConfigOptions, "prompt_agent_instructions"); option.CurrentValue != appsettings.PromptAgentInstructionsAll {
		t.Fatalf("prompt agent instructions option = %#v, want all", option)
	}
	if option := requireACPConfigOption(t, newResp.ConfigOptions, "prompt_plugin_prompts"); option.CurrentValue != appsettings.PromptPluginPromptsEnabled {
		t.Fatalf("prompt plugin prompts option = %#v, want enabled", option)
	}
	if option := requireACPConfigOption(t, newResp.ConfigOptions, "prompt_environment"); option.CurrentValue != appsettings.PromptEnvironmentEnabled {
		t.Fatalf("prompt environment option = %#v, want enabled", option)
	}
	if option := requireACPConfigOption(t, newResp.ConfigOptions, "auto_compaction"); option.CurrentValue != "enabled" {
		t.Fatalf("auto compaction option = %#v, want enabled default", option)
	}
	if option := requireACPConfigOption(t, newResp.ConfigOptions, "auto_compaction_watermark"); option.CurrentValue != float64(0) {
		t.Fatalf("auto compaction watermark option = %#v, want default zero", option)
	}
	if option := requireACPConfigOption(t, newResp.ConfigOptions, "compaction_max_source_chars"); option.CurrentValue != float64(0) {
		t.Fatalf("compaction max source option = %#v, want default zero", option)
	}
	if option := requireACPConfigOption(t, newResp.ConfigOptions, "compaction_task_index_limit"); option.CurrentValue != float64(appsettings.DefaultCompactionTaskIndexLimit) {
		t.Fatalf("compaction task retention option = %#v, want default task retention", option)
	}
	if option := requireACPConfigOption(t, newResp.ConfigOptions, "compaction_controller_index_limit"); option.CurrentValue != float64(appsettings.DefaultCompactionControllerIndexLimit) {
		t.Fatalf("compaction controller retention option = %#v, want default controller retention", option)
	}
	if option := requireACPConfigOption(t, newResp.ConfigOptions, "sandbox_backend"); option.CurrentValue != "auto" {
		t.Fatalf("sandbox backend option = %#v, want auto default", option)
	}
	if option := requireACPConfigOption(t, newResp.ConfigOptions, "sandbox_network"); option.CurrentValue != "inherit" {
		t.Fatalf("sandbox network option = %#v, want inherit default", option)
	}

	var setModeResp schema.SetSessionModeResponse
	if err := conn.Call(ctx, schema.MethodSessionSetMode, schema.SetSessionModeRequest{SessionID: newResp.SessionID, ModeID: coreruntime.SessionModeManual}, &setModeResp); err != nil {
		t.Fatalf("session/set_mode call error = %v", err)
	}
	mode, err := stack.Services().Modes().Current(ctx, session.Ref{SessionID: newResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if mode.ID != coreruntime.SessionModeManual {
		t.Fatalf("current mode = %#v, want manual", mode)
	}
	var setModelResp schema.SetSessionModelResponse
	if err := conn.Call(ctx, schema.MethodSessionSetModel, schema.SetSessionModelRequest{SessionID: newResp.SessionID, ModelID: beta.ID}, &setModelResp); err != nil {
		t.Fatalf("session/set_model call error = %v", err)
	}
	current, ok, err := stack.Services().Models().Current(ctx, session.Ref{SessionID: newResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || current.ID != beta.ID {
		t.Fatalf("current model = %#v, %v, want beta", current, ok)
	}

	var setConfigResp schema.SetSessionConfigOptionResponse
	if err := conn.Call(ctx, schema.MethodSessionSetConfig, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "reasoning_effort",
		Value:     "high",
	}, &setConfigResp); err != nil {
		t.Fatalf("session/set_config_option call error = %v", err)
	}
	if len(setConfigResp.ConfigOptions) != 15 {
		t.Fatalf("set config response = %#v, want fifteen config options", setConfigResp.ConfigOptions)
	}
	if option := requireACPConfigOption(t, setConfigResp.ConfigOptions, "reasoning_effort"); option.CurrentValue != "high" {
		t.Fatalf("reasoning option = %#v, want high", option)
	}
	snapshot, err := stack.Services().Sessions().Load(ctx, session.Ref{SessionID: newResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State[appservices.StateCurrentModelID] != beta.ID || snapshot.State[appservices.StateCurrentReasoningEffort] != "high" {
		t.Fatalf("session state = %#v, want beta/high", snapshot.State)
	}
	if err := conn.Call(ctx, schema.MethodSessionSetConfig, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "skill_loading_mode",
		Value:     appsettings.SkillLoadingModeMetadataOnly,
	}, &setConfigResp); err != nil {
		t.Fatalf("session/set_config_option(skill_loading_mode) call error = %v", err)
	}
	if option := requireACPConfigOption(t, setConfigResp.ConfigOptions, "skill_loading_mode"); option.CurrentValue != appsettings.SkillLoadingModeMetadataOnly {
		t.Fatalf("skill loading option = %#v, want metadata_only", option)
	}
	if err := conn.Call(ctx, schema.MethodSessionSetConfig, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "skill_max_expansion_chars",
		Value:     2048,
	}, &setConfigResp); err != nil {
		t.Fatalf("session/set_config_option(skill_max_expansion_chars) call error = %v", err)
	}
	if option := requireACPConfigOption(t, setConfigResp.ConfigOptions, "skill_max_expansion_chars"); option.CurrentValue != float64(2048) {
		t.Fatalf("skill budget option = %#v, want 2048", option)
	}
	if err := conn.Call(ctx, schema.MethodSessionSetConfig, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "prompt_agent_instructions",
		Value:     appsettings.PromptAgentInstructionsWorkspaceOnly,
	}, &setConfigResp); err != nil {
		t.Fatalf("session/set_config_option(prompt_agent_instructions) call error = %v", err)
	}
	if option := requireACPConfigOption(t, setConfigResp.ConfigOptions, "prompt_agent_instructions"); option.CurrentValue != appsettings.PromptAgentInstructionsWorkspaceOnly {
		t.Fatalf("prompt agent option = %#v, want workspace_only", option)
	}
	if err := conn.Call(ctx, schema.MethodSessionSetConfig, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "prompt_plugin_prompts",
		Value:     appsettings.PromptPluginPromptsDisabled,
	}, &setConfigResp); err != nil {
		t.Fatalf("session/set_config_option(prompt_plugin_prompts) call error = %v", err)
	}
	if option := requireACPConfigOption(t, setConfigResp.ConfigOptions, "prompt_plugin_prompts"); option.CurrentValue != appsettings.PromptPluginPromptsDisabled {
		t.Fatalf("prompt plugin option = %#v, want disabled", option)
	}
	if err := conn.Call(ctx, schema.MethodSessionSetConfig, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "prompt_environment",
		Value:     appsettings.PromptEnvironmentDisabled,
	}, &setConfigResp); err != nil {
		t.Fatalf("session/set_config_option(prompt_environment) call error = %v", err)
	}
	if option := requireACPConfigOption(t, setConfigResp.ConfigOptions, "prompt_environment"); option.CurrentValue != appsettings.PromptEnvironmentDisabled {
		t.Fatalf("prompt environment option = %#v, want disabled", option)
	}
	if err := conn.Call(ctx, schema.MethodSessionSetConfig, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "auto_compaction",
		Value:     "disabled",
	}, &setConfigResp); err != nil {
		t.Fatalf("session/set_config_option(auto_compaction) call error = %v", err)
	}
	if option := requireACPConfigOption(t, setConfigResp.ConfigOptions, "auto_compaction"); option.CurrentValue != "disabled" {
		t.Fatalf("auto compaction option = %#v, want disabled", option)
	}
	if err := conn.Call(ctx, schema.MethodSessionSetConfig, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "auto_compaction_watermark",
		Value:     0.72,
	}, &setConfigResp); err != nil {
		t.Fatalf("session/set_config_option(auto_compaction_watermark) call error = %v", err)
	}
	if option := requireACPConfigOption(t, setConfigResp.ConfigOptions, "auto_compaction_watermark"); option.CurrentValue != 0.72 {
		t.Fatalf("auto compaction watermark option = %#v, want 0.72", option)
	}
	if err := conn.Call(ctx, schema.MethodSessionSetConfig, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "compaction_max_source_chars",
		Value:     4096,
	}, &setConfigResp); err != nil {
		t.Fatalf("session/set_config_option(compaction_max_source_chars) call error = %v", err)
	}
	if option := requireACPConfigOption(t, setConfigResp.ConfigOptions, "compaction_max_source_chars"); option.CurrentValue != float64(4096) {
		t.Fatalf("compaction max source option = %#v, want 4096", option)
	}
	if err := conn.Call(ctx, schema.MethodSessionSetConfig, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "compaction_task_index_limit",
		Value:     17,
	}, &setConfigResp); err != nil {
		t.Fatalf("session/set_config_option(compaction_task_index_limit) call error = %v", err)
	}
	if option := requireACPConfigOption(t, setConfigResp.ConfigOptions, "compaction_task_index_limit"); option.CurrentValue != float64(17) {
		t.Fatalf("compaction task retention option = %#v, want 17", option)
	}
	if err := conn.Call(ctx, schema.MethodSessionSetConfig, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "compaction_controller_index_limit",
		Value:     6,
	}, &setConfigResp); err != nil {
		t.Fatalf("session/set_config_option(compaction_controller_index_limit) call error = %v", err)
	}
	if option := requireACPConfigOption(t, setConfigResp.ConfigOptions, "compaction_controller_index_limit"); option.CurrentValue != float64(6) {
		t.Fatalf("compaction controller retention option = %#v, want 6", option)
	}
	if err := conn.Call(ctx, schema.MethodSessionSetConfig, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "sandbox_backend",
		Value:     "auto",
	}, &setConfigResp); err != nil {
		t.Fatalf("session/set_config_option(sandbox_backend) call error = %v", err)
	}
	if option := requireACPConfigOption(t, setConfigResp.ConfigOptions, "sandbox_backend"); option.CurrentValue != "auto" {
		t.Fatalf("sandbox backend option = %#v, want auto", option)
	}
	if err := conn.Call(ctx, schema.MethodSessionSetConfig, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "sandbox_network",
		Value:     "disabled",
	}, &setConfigResp); err != nil {
		t.Fatalf("session/set_config_option(sandbox_network) call error = %v", err)
	}
	if option := requireACPConfigOption(t, setConfigResp.ConfigOptions, "sandbox_network"); option.CurrentValue != "disabled" {
		t.Fatalf("sandbox network option = %#v, want disabled", option)
	}
	doc, err := manager.Document(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Skills.LoadingMode != appsettings.SkillLoadingModeMetadataOnly || doc.Skills.MaxExpansionChars != 2048 ||
		doc.Prompt.AgentInstructions != appsettings.PromptAgentInstructionsWorkspaceOnly ||
		doc.Prompt.PluginPrompts != appsettings.PromptPluginPromptsDisabled ||
		doc.Prompt.Environment != appsettings.PromptEnvironmentDisabled ||
		doc.Compaction.Auto.Mode != "disabled" || doc.Compaction.Auto.WatermarkRatio != 0.72 || doc.Compaction.MaxSourceChars != 4096 ||
		doc.Compaction.Retention.TaskIndexLimit != 17 || doc.Compaction.Retention.ControllerIndexLimit != 6 ||
		doc.Runtime.Sandbox.Backend != "auto" || doc.Runtime.Sandbox.Network != "disabled" {
		t.Fatalf("settings document = %#v, want metadata_only/budget/compaction/sandbox settings", doc)
	}
	if runtime := stack.Services().Runtime(); runtime.Sandbox.Backend != "auto" {
		t.Fatalf("service runtime sandbox = %#v, want auto", runtime.Sandbox)
	}

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestPromptUsesSessionModelSelectionFromAppServices(t *testing.T) {
	ctx := context.Background()
	manager, err := appsettings.NewManager(ctx, nil, appsettings.Document{})
	if err != nil {
		t.Fatal(err)
	}
	alpha, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Alias:                  "alpha",
		Provider:               "openai-compatible",
		Model:                  "gpt-alpha",
		BaseURL:                "https://api.alpha.test/v1",
		DefaultReasoningEffort: "low",
		ReasoningMode:          "fixed",
		ReasoningLevels:        []string{"low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := manager.UpsertModel(ctx, appsettings.ModelConfig{
		Alias:                  "beta",
		Provider:               "openai-compatible",
		Model:                  "gpt-beta",
		BaseURL:                "https://api.beta.test/v1",
		DefaultReasoningEffort: "low",
		ReasoningMode:          "fixed",
		ReasoningLevels:        []string{"low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetDefaultModel(ctx, alpha.ID); err != nil {
		t.Fatal(err)
	}
	engine := &recordingServiceEngine{state: session.State{}}
	services, err := appservices.New(appservices.Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Engine:   engine,
		Settings: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Services: services})
	if err != nil {
		t.Fatal(err)
	}
	newResp, err := server.newSession(ctx, schema.NewSessionRequest{CWD: "/tmp/project"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.setSessionModel(ctx, schema.SetSessionModelRequest{SessionID: newResp.SessionID, ModelID: beta.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.setSessionConfigOption(ctx, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "reasoning_effort",
		Value:     "high",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.setSessionConfigOption(ctx, schema.SetSessionConfigOptionRequest{
		SessionID: newResp.SessionID,
		ConfigID:  "mode",
		Value:     coreruntime.SessionModeManual,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.prompt(ctx, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "ping"}),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if engine.turn.Model != beta.ID || engine.turn.Reasoning.Effort != "high" || engine.turn.Mode != coreruntime.SessionModeManual {
		t.Fatalf("turn request = %#v, want beta/high/manual from session state", engine.turn)
	}
}

func TestServeStdioClosesActiveSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	provider := &blockingProvider{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
	stack, err := local.New(local.Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Provider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, Config{
			Services: stack.Services(),
			Implementation: schema.Implementation{
				Name:    "caelis",
				Version: "test",
			},
		}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	go func() {
		_ = conn.Serve(ctx, nil, func(context.Context, jsonrpc.Message) {})
	}()

	var newResp schema.NewSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionNew, schema.NewSessionRequest{CWD: "/tmp/project"}, &newResp); err != nil {
		t.Fatalf("session/new call error = %v", err)
	}

	promptErr := make(chan error, 1)
	go func() {
		var promptResp schema.PromptResponse
		promptErr <- conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
			SessionID: newResp.SessionID,
			Prompt: []json.RawMessage{
				jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "wait"}),
			},
		}, &promptResp)
	}()

	select {
	case <-provider.started:
	case <-ctx.Done():
		t.Fatal("timed out waiting for provider to start")
	}

	var closeResp schema.CloseSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionClose, schema.CloseSessionRequest{SessionID: newResp.SessionID}, &closeResp); err != nil {
		t.Fatalf("session/close call error = %v", err)
	}
	select {
	case <-provider.cancelled:
	case <-ctx.Done():
		t.Fatal("timed out waiting for provider cancellation")
	}
	select {
	case err := <-promptErr:
		if err == nil {
			t.Fatal("session/prompt error = nil, want cancellation error")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for prompt cancellation")
	}
	if err := conn.Call(ctx, schema.MethodSessionClose, schema.CloseSessionRequest{SessionID: newResp.SessionID}, &closeResp); err != nil {
		t.Fatalf("second session/close call error = %v", err)
	}

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServeStdioBridgesPermissionResponseIntoTurnSubmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "call-1",
					Name:  "ECHO",
					Input: json.RawMessage(`{"text":"hello"}`),
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("done")},
		},
	}}
	stack, err := local.New(local.Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Provider: provider,
		ToolList: []tool.Tool{tool.NamedTool{
			Def: tool.Definition{Name: "ECHO"},
			Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
				return tool.Result{
					ID:      call.ID,
					Name:    call.Name,
					Content: []model.Part{model.NewTextPart("approved output")},
				}, nil
			},
		}},
		Approval: approval.AskAll(),
	})
	if err != nil {
		t.Fatal(err)
	}

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, Config{
			Engine:  stack.Services().Engine(),
			AppName: "caelis",
			UserID:  "tester",
			Implementation: schema.Implementation{
				Name:    "caelis",
				Version: "test",
			},
		}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	updates := make(chan updateNotification, 16)
	permissions := make(chan schema.RequestPermissionRequest, 2)
	go func() {
		_ = conn.Serve(ctx, func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
			if msg.Method != schema.MethodSessionReqPermission {
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
			var req schema.RequestPermissionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			permissions <- req
			return schema.RequestPermissionResponse{
				Outcome: schema.PermissionOutcome{
					Outcome:  schema.PermAllowOnce,
					OptionID: schema.PermAllowOnce,
				},
			}, nil
		}, func(_ context.Context, msg jsonrpc.Message) {
			if msg.Method != schema.MethodSessionUpdate {
				return
			}
			var notification updateNotification
			if err := json.Unmarshal(msg.Params, &notification); err == nil {
				updates <- notification
			}
		})
	}()

	var initResp schema.InitializeResponse
	if err := conn.Call(ctx, schema.MethodInitialize, schema.InitializeRequest{
		ProtocolVersion:    schema.CurrentProtocolVersion,
		ClientCapabilities: map[string]any{},
	}, &initResp); err != nil {
		t.Fatalf("initialize call error = %v", err)
	}
	var newResp schema.NewSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionNew, schema.NewSessionRequest{CWD: "/tmp/project"}, &newResp); err != nil {
		t.Fatalf("session/new call error = %v", err)
	}
	var promptResp schema.PromptResponse
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "run echo"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt call error = %v", err)
	}
	if promptResp.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want %q", promptResp.StopReason, schema.StopReasonEndTurn)
	}
	select {
	case req := <-permissions:
		if req.SessionID != newResp.SessionID || req.ToolCall.ToolCallID != "call-1" {
			t.Fatalf("permission request = %#v, want call-1 for session", req)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for permission request")
	}

	var kinds []string
	for len(kinds) < 4 {
		select {
		case update := <-updates:
			kinds = append(kinds, update.Update.SessionUpdate)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for updates, got %v", kinds)
		}
	}
	want := []string{
		schema.UpdateUserMessage,
		schema.UpdateToolCall,
		schema.UpdateToolCallInfo,
		schema.UpdateAgentMessage,
	}
	if len(kinds) < len(want) {
		t.Fatalf("update kinds = %v, want at least %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("update kinds = %v, want prefix %v", kinds, want)
		}
	}

	snapshot, err := stack.Services().Sessions().Load(ctx, session.Ref{SessionID: newResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 7 {
		t.Fatalf("stored events = %d, want approval-aware tool flow", len(snapshot.Events))
	}
	if snapshot.Events[3].Approval == nil || snapshot.Events[3].Approval.Status != session.ApprovalPending {
		t.Fatalf("stored pending approval = %#v", snapshot.Events[3])
	}
	if snapshot.Events[4].Approval == nil || snapshot.Events[4].Approval.Status != session.ApprovalApproved {
		t.Fatalf("stored approved approval = %#v", snapshot.Events[4])
	}

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

type updateNotification struct {
	SessionID string `json:"sessionId"`
	Update    struct {
		SessionUpdate string `json:"sessionUpdate"`
	} `json:"update"`
}

type availableCommandsNotification struct {
	SessionID string `json:"sessionId"`
	Update    struct {
		SessionUpdate     string                    `json:"sessionUpdate"`
		AvailableCommands []schema.AvailableCommand `json:"availableCommands"`
	} `json:"update"`
}

type contentNotification struct {
	SessionID string `json:"sessionId"`
	Update    struct {
		SessionUpdate string             `json:"sessionUpdate"`
		Content       schema.TextContent `json:"content"`
	} `json:"update"`
}

func waitForAgentMessage(t *testing.T, ctx context.Context, messages <-chan string, contains string) string {
	t.Helper()
	for {
		select {
		case message := <-messages:
			if strings.Contains(message, contains) {
				return message
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for agent message containing %q", contains)
		}
	}
}

func snapshotContainsEventType(snapshot session.Snapshot, eventType session.EventType) bool {
	for _, event := range snapshot.Events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func snapshotContainsControllerHandoff(snapshot session.Snapshot, agent string) bool {
	agent = strings.TrimSpace(agent)
	for _, event := range snapshot.Events {
		if event.Type != session.EventHandoff || event.Scope == nil {
			continue
		}
		controller := event.Scope.Controller
		if strings.EqualFold(strings.TrimSpace(controller.ID), agent) ||
			strings.EqualFold(strings.TrimSpace(controller.AgentName), agent) ||
			strings.EqualFold(strings.TrimSpace(controller.Label), agent) {
			return true
		}
	}
	return false
}

func snapshotContainsAgentParticipant(snapshot session.Snapshot, agent string) bool {
	agent = strings.TrimSpace(agent)
	for _, event := range snapshot.Events {
		if event.Scope == nil {
			continue
		}
		participant := event.Scope.Participant
		if strings.EqualFold(strings.TrimSpace(participant.ID), agent) ||
			strings.EqualFold(strings.TrimSpace(participant.AgentName), agent) ||
			strings.EqualFold(strings.TrimSpace(participant.Label), "@"+agent) {
			return true
		}
	}
	return false
}

func requireACPConfigOption(t *testing.T, options []schema.SessionConfigOption, id string) schema.SessionConfigOption {
	t.Helper()
	for _, option := range options {
		if option.ID == id {
			return option
		}
	}
	t.Fatalf("config option %q not found in %#v", id, options)
	return schema.SessionConfigOption{}
}

func requireAvailableCommand(t *testing.T, commands []schema.AvailableCommand, name string) schema.AvailableCommand {
	t.Helper()
	for _, command := range commands {
		if command.Name == name {
			return command
		}
	}
	t.Fatalf("available command %q not found in %#v", name, commands)
	return schema.AvailableCommand{}
}

type testProvider struct {
	message model.Message
}

func (p *testProvider) ID() string {
	return "test"
}

func (p *testProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "test", Provider: "test"}}, nil
}

func (p *testProvider) Stream(context.Context, model.Request) (model.Stream, error) {
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: schemaStopEvent(),
		Response: &model.Response{
			Status:  model.ResponseCompleted,
			Message: model.CloneMessage(p.message),
		},
	}}}, nil
}

func schemaStopEvent() model.StreamEventType {
	return model.StreamTurnDone
}

type scriptedProvider struct {
	responses []model.Message
}

func (p *scriptedProvider) ID() string {
	return "scripted"
}

func (p *scriptedProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "scripted", Provider: "scripted"}}, nil
}

func (p *scriptedProvider) Stream(context.Context, model.Request) (model.Stream, error) {
	response := model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("default")},
	}
	if len(p.responses) > 0 {
		response = model.CloneMessage(p.responses[0])
		p.responses = p.responses[1:]
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: model.StreamTurnDone,
		Response: &model.Response{
			Status:  model.ResponseCompleted,
			Message: response,
		},
	}}}, nil
}

type blockingProvider struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (p *blockingProvider) ID() string {
	return "blocking"
}

func (p *blockingProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "blocking", Provider: "blocking"}}, nil
}

func (p *blockingProvider) Stream(ctx context.Context, _ model.Request) (model.Stream, error) {
	close(p.started)
	<-ctx.Done()
	close(p.cancelled)
	return nil, ctx.Err()
}

type recordingServiceEngine struct {
	state session.State
	turn  coreruntime.TurnRequest
}

func (e *recordingServiceEngine) StartSession(_ context.Context, req session.StartRequest) (session.Session, error) {
	return session.Session{
		Ref: session.Ref{
			AppName:      req.AppName,
			UserID:       req.UserID,
			SessionID:    "sess-routing",
			WorkspaceKey: req.Workspace.Key,
		},
		Workspace: req.Workspace,
	}, nil
}

func (e *recordingServiceEngine) ListSessions(context.Context, session.ListQuery) (session.SessionPage, error) {
	return session.SessionPage{}, nil
}

func (e *recordingServiceEngine) LoadSession(_ context.Context, ref session.Ref) (session.Snapshot, error) {
	return session.Snapshot{
		Session: session.Session{Ref: ref},
		State:   cloneTestState(e.state),
	}, nil
}

func (e *recordingServiceEngine) RecordEvents(context.Context, session.Ref, []session.Event) (session.Cursor, error) {
	return "", nil
}

func (e *recordingServiceEngine) UpdateSessionState(_ context.Context, _ session.Ref, patch session.StatePatch) error {
	next, err := patch(cloneTestState(e.state))
	if err != nil {
		return err
	}
	e.state = cloneTestState(next)
	return nil
}

func (e *recordingServiceEngine) BeginTurn(_ context.Context, req coreruntime.TurnRequest) (coreruntime.Turn, error) {
	e.turn = req
	events := make(chan coreruntime.EventEnvelope)
	close(events)
	return emptyTurn{events: events}, nil
}

func (e *recordingServiceEngine) Interrupt(context.Context, session.Ref) error {
	return nil
}

func (e *recordingServiceEngine) Replay(context.Context, coreruntime.ReplayRequest) (<-chan coreruntime.EventEnvelope, error) {
	events := make(chan coreruntime.EventEnvelope)
	close(events)
	return events, nil
}

type emptyTurn struct {
	events <-chan coreruntime.EventEnvelope
}

func (t emptyTurn) ID() string {
	return "turn"
}

func (t emptyTurn) RunID() string {
	return "run"
}

func (t emptyTurn) SessionRef() session.Ref {
	return session.Ref{SessionID: "sess-routing"}
}

func (t emptyTurn) StartedAt() time.Time {
	return time.Time{}
}

func (t emptyTurn) Events() <-chan coreruntime.EventEnvelope {
	return t.events
}

func (t emptyTurn) Submit(context.Context, coreruntime.Submission) error {
	return nil
}

func (t emptyTurn) Cancel() coreruntime.CancelResult {
	return coreruntime.CancelResult{Status: coreruntime.CancelCancelled}
}

func (t emptyTurn) Close() error {
	return nil
}

func cloneTestState(in session.State) session.State {
	if len(in) == 0 {
		return nil
	}
	out := make(session.State, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
