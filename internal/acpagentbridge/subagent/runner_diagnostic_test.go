package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentenv"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestRunnerPromptFailureBeforeFirstUpdateDoesNotPersistRawDiagnostics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := NewRegistry([]AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestRunnerPromptFailureHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_SUBAGENT_HELPER": "prompt-failure",
		},
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	streams := &recordingStreams{}
	completions := make(chan delegation.Result, 1)
	_, _, err = runner.Spawn(ctx, tasksubagent.SpawnContext{
		TaskID:     "task-prompt-failure",
		CWD:        t.TempDir(),
		Streams:    streams,
		Completion: completionSinkFunc(func(result delegation.Result) { completions <- result }),
	}, delegation.Request{Agent: "helper", Prompt: "review"})
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	var got delegation.Result
	select {
	case got = <-completions:
	case <-ctx.Done():
		t.Fatalf("producer completion was not published: %v", ctx.Err())
	}
	if got.State != delegation.StateFailed || got.Running {
		t.Fatalf("Result = state %q running %v, want terminal failed", got.State, got.Running)
	}
	if got.Error != "subagent prompt failed" {
		t.Fatalf("Error = %q, want stable operation-level summary", got.Error)
	}
	if got.Result != "" {
		t.Fatalf("Result = %q, want no final assistant output", got.Result)
	}
	if got.OutputPreview != "subagent prompt failed" {
		t.Fatalf("OutputPreview = %q, want stable operation-level summary", got.OutputPreview)
	}
	for _, secret := range []string{
		"Bearer rpc-super-secret",
		"sk-stderr-super-secret",
		"/Users/private/workspace",
	} {
		for field, value := range map[string]string{
			"Error":         got.Error,
			"OutputPreview": got.OutputPreview,
			"Result":        got.Result,
		} {
			if strings.Contains(value, secret) {
				t.Fatalf("%s leaked %q: %q", field, secret, value)
			}
		}
	}
	if len(streams.frames) != 0 {
		t.Fatalf("stream frames = %#v, want failure before first child update", streams.frames)
	}
}

func TestRunnerInitializeFailureReportsStageWithoutChildStderr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := NewRegistry([]AgentConfig{{
		Name:    "self",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestRunnerPromptFailureHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_SUBAGENT_HELPER": "initialize-exit",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runner.Spawn(ctx, tasksubagent.SpawnContext{
		TaskID: "task-initialize-exit",
		CWD:    t.TempDir(),
	}, delegation.Request{Agent: "self", Prompt: "review"})
	if err == nil || !strings.Contains(err.Error(), `initialize spawned ACP child "self"`) {
		t.Fatalf("Spawn() error = %v, want explicit child initialize stage", err)
	}
	if strings.Contains(err.Error(), "startup-secret") {
		t.Fatalf("Spawn() error leaked child stderr: %v", err)
	}
}

func TestRunnerAppliesBuiltInSessionOptionsAfterSessionNewBeforePrompt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := NewRegistry([]AgentConfig{{
		Name:    "self",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestRunnerPromptFailureHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_SUBAGENT_HELPER": "session-options",
		},
		SessionOptions: controlagents.SessionOptions{
			ModelID: "model-selected",
			ConfigValues: map[string]string{
				"mode":             "manual",
				"reasoning_effort": "high",
			},
			ReasoningEffortConfigID: "reasoning_effort",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	preparedSession := ""
	runner, err := NewRunner(RunnerConfig{
		Registry: registry,
		SessionPreparer: func(_ context.Context, spawn tasksubagent.SpawnContext, sessionID string, config AgentConfig) (controlagents.SessionOptions, error) {
			if spawn.TaskID != "task-session-options" || config.Name != "self" {
				return controlagents.SessionOptions{}, fmt.Errorf("unexpected preparation scope: task=%q Agent=%q", spawn.TaskID, config.Name)
			}
			preparedSession = sessionID
			return config.SessionOptions, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	completion := make(chan delegation.Result, 1)
	_, initial, err := runner.Spawn(ctx, tasksubagent.SpawnContext{
		SessionRef: session.SessionRef{SessionID: "parent-session-options"},
		TaskID:     "task-session-options",
		CWD:        t.TempDir(),
		Completion: completionSinkFunc(func(result delegation.Result) { completion <- result }),
	}, delegation.Request{Agent: "self", Prompt: "review"})
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	if !initial.Running && initial.State != delegation.StateCompleted {
		t.Fatalf("Spawn() initial result = %#v, want running or completed", initial)
	}
	if preparedSession != "child-session-options" {
		t.Fatalf("prepared Session = %q, want child-session-options before configuration", preparedSession)
	}
	select {
	case got := <-completion:
		if got.Running || got.State != delegation.StateCompleted {
			t.Fatalf("completion = %#v, want completed after configured prompt", got)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for configured child: %v", ctx.Err())
	}
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatalf("Quiesce() error = %v", err)
	}
}

func TestRunnerPromptRecoversConfiguredAuthentication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := NewRegistry([]AgentConfig{{
		Name:    "protected-helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestRunnerPromptFailureHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_SUBAGENT_HELPER": "prompt-authentication",
		},
		Authentication: controlagents.Authentication{
			MethodID: "agent-login",
			Type:     controlagents.AuthenticationAgent,
		},
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	completions := make(chan delegation.Result, 1)
	_, _, err = runner.Spawn(ctx, tasksubagent.SpawnContext{
		TaskID:     "task-prompt-authentication",
		CWD:        t.TempDir(),
		Completion: completionSinkFunc(func(result delegation.Result) { completions <- result }),
	}, delegation.Request{Agent: "protected-helper", Prompt: "review"})
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	var got delegation.Result
	select {
	case got = <-completions:
	case <-ctx.Done():
		t.Fatalf("producer completion was not published: %v", ctx.Err())
	}
	if got.State != delegation.StateCompleted || got.Running {
		t.Fatalf("Result = state %q running %v, want completed", got.State, got.Running)
	}
	if err := runner.Quiesce(ctx); err != nil {
		t.Fatalf("Quiesce() error = %v", err)
	}
}

func TestRunnerLoadHistoryUsesSessionLoadAndPreservesMultipleTurns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := NewRegistry([]AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestRunnerPromptFailureHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_SUBAGENT_HELPER": "history-load",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := runner.LoadHistory(ctx, tasksubagent.HistoryRequest{
		Anchor: delegation.Anchor{TaskID: "task-history", SessionID: "child-history", AgentID: "agent-history"},
		Reconnect: tasksubagent.ReconnectRequest{
			Spawn: tasksubagent.SpawnContext{
				SessionRef: session.SessionRef{SessionID: "parent-history"},
				CWD:        os.TempDir(),
				TaskID:     "task-history",
				Handle:     "helper",
			},
			Target: delegation.AgentTarget("helper"),
		},
	})
	if err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}
	var (
		assistantTexts []string
		assistantTurns []string
		toolUpdate     *session.ProtocolUpdate
	)
	for _, event := range loaded.Events {
		if event == nil {
			continue
		}
		if session.EventTypeOf(event) == session.EventTypeAssistant && session.ProtocolSessionUpdateType(event) == string(session.ProtocolUpdateTypeAgentMessage) {
			assistantTexts = append(assistantTexts, session.EventText(event))
			assistantTurns = append(assistantTurns, event.Scope.TurnID)
		}
		if update := session.ProtocolUpdateOf(event); update != nil && update.SessionUpdate == string(session.ProtocolUpdateTypeToolUpdate) {
			toolUpdate = update
		}
	}
	if fmt.Sprint(assistantTexts) != "[first answer second answer]" || fmt.Sprint(assistantTurns) != "[task-history:1 task-history:2]" {
		t.Fatalf("assistant history = texts %v turns %v, want two session/load Turns", assistantTexts, assistantTurns)
	}
	if toolUpdate == nil || toolUpdate.RawOutput["formatted_output"] != "HISTORY_TOOL_OUTPUT\n" || len(session.ProtocolToolCallContentOf(toolUpdate)) != 0 {
		t.Fatalf("sparse standard tool update = %#v, want exact session/load update", toolUpdate)
	}
}

func TestRunnerLoadHistoryPassesMatchingCapabilityToBuiltInBridge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := NewRegistry([]AgentConfig{{
		Name:    "self",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestRunnerPromptFailureHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_SUBAGENT_HELPER":              "history-load-capability",
			acpagentenv.EnvManagedSessionHistoryToken: "",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.LoadHistory(ctx, tasksubagent.HistoryRequest{
		Anchor: delegation.Anchor{TaskID: "task-history", SessionID: "child-history", AgentID: "agent-history"},
		Reconnect: tasksubagent.ReconnectRequest{
			Spawn: tasksubagent.SpawnContext{
				SessionRef: session.SessionRef{SessionID: "parent-history"},
				CWD:        os.TempDir(),
				TaskID:     "task-history",
				Handle:     "self",
			},
			Target: delegation.AgentTarget("self"),
		},
	})
	if err != nil {
		t.Fatalf("LoadHistory(built-in capability) error = %v", err)
	}
}

func TestRunnerLoadHistoryReportsUnsupportedCapability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := NewRegistry([]AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestRunnerPromptFailureHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_SUBAGENT_HELPER": "history-unsupported",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.LoadHistory(ctx, tasksubagent.HistoryRequest{
		Anchor: delegation.Anchor{TaskID: "task-history", SessionID: "child-history", AgentID: "agent-history"},
		Reconnect: tasksubagent.ReconnectRequest{
			Spawn: tasksubagent.SpawnContext{
				SessionRef: session.SessionRef{SessionID: "parent-history"},
				CWD:        os.TempDir(),
				TaskID:     "task-history",
				Handle:     "helper",
			},
			Target: delegation.AgentTarget("helper"),
		},
	})
	if !errorcode.Is(err, errorcode.Unsupported) {
		t.Fatalf("LoadHistory() error = %v, want unsupported", err)
	}
}

func TestRunnerLoadHistoryRejectsUpdatesFromAnotherSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := NewRegistry([]AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestRunnerPromptFailureHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_SUBAGENT_HELPER": "history-load-mismatch",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := runner.LoadHistory(ctx, tasksubagent.HistoryRequest{
		Anchor: delegation.Anchor{TaskID: "task-history", SessionID: "child-history", AgentID: "agent-history"},
		Reconnect: tasksubagent.ReconnectRequest{
			Spawn: tasksubagent.SpawnContext{
				SessionRef: session.SessionRef{SessionID: "parent-history"},
				CWD:        os.TempDir(),
				TaskID:     "task-history",
				Handle:     "helper",
			},
			Target: delegation.AgentTarget("helper"),
		},
	})
	if err == nil || !strings.Contains(err.Error(), `belongs to Session "other-child"`) {
		t.Fatalf("LoadHistory() = %#v, %v; want mismatched provider Session rejected", loaded, err)
	}
	if len(loaded.Events) != 0 {
		t.Fatalf("mismatched Session history escaped rejection: %#v", loaded.Events)
	}
}

func TestLoadedAgentCommunicationPromptRestoresDisplayIdentity(t *testing.T) {
	t.Parallel()

	source := session.ActorRef{
		Kind: session.ActorKindParticipant, ID: "reviewer-1", Role: "delegated", Name: "reviewer",
	}
	got, body, ok := loadedAgentCommunicationPrompt(
		session.AgentCommunicationPromptHeader(source) + "\nreview this change",
	)
	if !ok || got != source || body != "review this change" {
		t.Fatalf("loaded Agent communication = (%#v, %q, %v), want exact display identity and body", got, body, ok)
	}
	if _, _, ok := loadedAgentCommunicationPrompt("ordinary user input"); ok {
		t.Fatal("ordinary input was interpreted as an Agent communication header")
	}
}

func TestRunnerPromptFailureHelperProcess(t *testing.T) {
	mode := os.Getenv("CAELIS_ACP_SUBAGENT_HELPER")
	switch mode {
	case "prompt-failure", "prompt-authentication", "message-reconnect", "history-load", "history-load-capability", "history-load-mismatch", "history-unsupported", "initialize-exit", "session-options":
	default:
		return
	}
	authenticated := false
	configuredModel := "model-default"
	configuredMode := "auto-review"
	configuredEffort := "low"
	configurationStep := 0
	configurationOptions := func() []client.SessionConfigOption {
		return []client.SessionConfigOption{
			{
				ID: "model", Name: "Model", Type: "select", Category: "model", CurrentValue: configuredModel,
				Options: []client.SessionConfigSelectOption{{Value: "model-default"}, {Value: "model-selected"}},
			},
			{
				ID: "mode", Name: "Approval Mode", Type: "select", Category: "mode", CurrentValue: configuredMode,
				Options: []client.SessionConfigSelectOption{{Value: "auto-review"}, {Value: "manual"}},
			},
			{
				ID: "reasoning_effort", Name: "Reasoning Effort", Type: "select", Category: "thought_level", CurrentValue: configuredEffort,
				Options: []client.SessionConfigSelectOption{{Value: "low"}, {Value: "high"}},
			},
		}
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case client.MethodInitialize:
			if mode == "initialize-exit" {
				fmt.Fprintln(os.Stderr, "OPENAI_API_KEY=startup-secret")
				os.Exit(23)
			}
			response := client.InitializeResponse{ProtocolVersion: 1}
			if mode == "history-load" || mode == "history-load-capability" || mode == "history-load-mismatch" {
				response.AgentCapabilities.LoadSession = true
			}
			if mode == "prompt-authentication" {
				response.AuthMethods = []json.RawMessage{
					json.RawMessage(`{"id":"agent-login","name":"Agent login"}`),
				}
			}
			return response, nil
		case client.MethodSessionNew:
			if mode == "session-options" {
				return client.NewSessionResponse{
					SessionID:     "child-session-options",
					ConfigOptions: configurationOptions(),
				}, nil
			}
			return client.NewSessionResponse{SessionID: "child-prompt-failure"}, nil
		case client.MethodSessionSetConfig:
			if mode != "session-options" {
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
			var req client.SetSessionConfigOptionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil || req.ValueId == nil || req.ValueId.SessionId != "child-session-options" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session config request"}
			}
			value := string(req.ValueId.Value)
			expected := [][2]string{
				{"model", "model-selected"},
				{"mode", "manual"},
				{"reasoning_effort", "high"},
			}
			if configurationStep >= len(expected) || string(req.ValueId.ConfigId) != expected[configurationStep][0] || value != expected[configurationStep][1] {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: fmt.Sprintf("configuration step %d = %s:%s", configurationStep, req.ValueId.ConfigId, value)}
			}
			switch req.ValueId.ConfigId {
			case "model":
				configuredModel = value
			case "mode":
				configuredMode = value
			case "reasoning_effort":
				configuredEffort = value
			}
			configurationStep++
			return client.SetSessionConfigOptionResponse{ConfigOptions: configurationOptions()}, nil
		case client.MethodSessionResume:
			var req client.ResumeSessionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			meta := diagnosticRawMeta(req.Meta)
			if req.SessionId != "child-reconnect" || req.Cwd != os.TempDir() ||
				metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeSession, metautil.RuntimeSessionParentID) != "parent-reconnect" ||
				metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeSession, metautil.RuntimeTaskID) != "task-reconnect" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/resume request"}
			}
			return client.ResumeSessionResponse{}, nil
		case client.MethodSessionLoad:
			if mode != "history-load" && mode != "history-load-capability" && mode != "history-load-mismatch" {
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
			var req client.LoadSessionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			meta := diagnosticRawMeta(req.Meta)
			if req.SessionId != "child-history" || req.Cwd != os.TempDir() ||
				metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeSession, metautil.RuntimeSessionKind) != metautil.RuntimeSessionKindSubagent ||
				metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeSession, metautil.RuntimeSessionParentID) != "parent-history" ||
				metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeSession, metautil.RuntimeTaskID) != "task-history" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/load request"}
			}
			if mode == "history-load-capability" {
				requestToken := metautil.String(meta, metautil.Root, metautil.Runtime, metautil.RuntimeSession, metautil.RuntimeSessionHistoryToken)
				if processToken := strings.TrimSpace(os.Getenv(acpagentenv.EnvManagedSessionHistoryToken)); len(processToken) != 64 || requestToken != processToken {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "managed history capability mismatch"}
				}
			}
			updates := []client.Update{
				client.ContentChunk{SessionUpdate: client.UpdateUserMessage, MessageID: "user-1", Content: jsonrpc.MustMarshalRaw(client.TextContent{Type: "text", Text: "first prompt"})},
				client.ContentChunk{SessionUpdate: client.UpdateAgentMessage, MessageID: "assistant-1", Content: jsonrpc.MustMarshalRaw(client.TextContent{Type: "text", Text: "first answer"})},
				client.ToolCall{SessionUpdate: client.UpdateToolCall, ToolCallID: "command-1", Title: "RUN_COMMAND printf", Kind: "execute", Status: "in_progress"},
				client.ToolCallUpdate{SessionUpdate: client.UpdateToolCallState, ToolCallID: "command-1", Status: stringPtr("completed"), RawOutput: map[string]any{"formatted_output": "HISTORY_TOOL_OUTPUT\n", "exit_code": 0}, Meta: metautil.WithTerminalOutput(nil, "command-1", "HISTORY_TOOL_OUTPUT\n")},
				client.ContentChunk{SessionUpdate: client.UpdateUserMessage, MessageID: "user-2", Content: jsonrpc.MustMarshalRaw(client.TextContent{Type: "text", Text: "second prompt"})},
				client.ContentChunk{SessionUpdate: client.UpdateAgentMessage, MessageID: "assistant-2", Content: jsonrpc.MustMarshalRaw(client.TextContent{Type: "text", Text: "second answer"})},
			}
			for _, update := range updates {
				sessionID := string(req.SessionId)
				if mode == "history-load-mismatch" {
					sessionID = "other-child"
				}
				if err := conn.Notify(client.MethodSessionUpdate, client.SessionNotification{
					SessionID: sessionID,
					Update:    jsonrpc.MustMarshalRaw(update),
				}); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
				}
			}
			return client.LoadSessionResponse{}, nil
		case client.MethodAuthenticate:
			var req client.AuthenticateRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil || req.MethodId != "agent-login" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected authenticate request"}
			}
			authenticated = true
			return client.AuthenticateResponse{}, nil
		case client.MethodSessionPrompt:
			if mode == "session-options" {
				if configurationStep != 3 {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: fmt.Sprintf("prompt before configuration completed at step %d", configurationStep)}
				}
				return client.PromptResponse{StopReason: string(acpsdk.StopReasonEndTurn)}, nil
			}
			if mode == "prompt-authentication" {
				if !authenticated {
					return nil, &jsonrpc.RPCError{Code: client.ErrorCodeAuthRequired, Message: "Authentication required"}
				}
				return client.PromptResponse{StopReason: string(acpsdk.StopReasonEndTurn)}, nil
			}
			fmt.Fprintln(os.Stderr, "OPENAI_API_KEY=sk-stderr-super-secret cwd=/Users/private/workspace")
			return nil, &jsonrpc.RPCError{
				Code:    -32000,
				Message: "prompt rejected Authorization: Bearer rpc-super-secret at /Users/private/workspace",
			}
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper Serve() error = %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

type completionSinkFunc func(delegation.Result)

func diagnosticRawMeta(raw map[string]json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	result := make(map[string]any, len(raw))
	for key, value := range raw {
		var decoded any
		if json.Unmarshal(value, &decoded) == nil {
			result[key] = decoded
		}
	}
	return result
}

func (sink completionSinkFunc) PublishSubagentCompletion(result delegation.Result) {
	sink(result)
}

func TestSubagentPromptFailureDetailIsStable(t *testing.T) {
	if got := subagentPromptFailureDetail(context.DeadlineExceeded); got != "subagent prompt timed out" {
		t.Fatalf("deadline detail = %q, want timeout summary", got)
	}
	if got := subagentPromptFailureDetail(errors.New("Authorization: Bearer secret")); got != "subagent prompt failed" {
		t.Fatalf("generic detail = %q, want non-sensitive failure summary", got)
	}
}
