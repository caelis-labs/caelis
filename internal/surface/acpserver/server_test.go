package acpserver

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
	"github.com/OnslaughtSnail/caelis/internal/app/local"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
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
		Title:              "Loaded session",
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
	if listResp.Sessions[0].CWD != "/tmp/project" || listResp.Sessions[0].Title != "Loaded session" || listResp.Sessions[0].UpdatedAt == "" {
		t.Fatalf("session/list summary = %#v, want cwd/title/updatedAt", listResp.Sessions[0])
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
	if len(newResp.ConfigOptions) != 2 {
		t.Fatalf("new session config options = %#v, want model and reasoning", newResp.ConfigOptions)
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
	if len(setConfigResp.ConfigOptions) != 2 || setConfigResp.ConfigOptions[1].CurrentValue != "high" {
		t.Fatalf("set config response = %#v, want high reasoning", setConfigResp.ConfigOptions)
	}
	snapshot, err := stack.Services().Sessions().Load(ctx, session.Ref{SessionID: newResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State[appservices.StateCurrentModelID] != beta.ID || snapshot.State[appservices.StateCurrentReasoningEffort] != "high" {
		t.Fatalf("session state = %#v, want beta/high", snapshot.State)
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
	if _, err := server.prompt(ctx, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "ping"}),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if engine.turn.Model != beta.ID || engine.turn.Reasoning.Effort != "high" {
		t.Fatalf("turn request = %#v, want beta/high from session state", engine.turn)
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
