package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestStartACPClientAppliesAgentDefaultsBeforeFirstPrompt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	manager := &Manager{}
	acpClient, sessionID, state, err := manager.startACPClient(ctx, t.TempDir(), subagent.AgentConfig{
		Name:    "opus",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerSessionDefaultsHelperProcess", "--"},
		Env:     map[string]string{"CAELIS_ACP_HELPER": "session-defaults"},
		SessionOptions: controlagents.SessionOptions{
			ModelID:      "opus",
			ConfigValues: map[string]string{"effort": "max"},
		},
	}, "", nil, func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
		return client.RequestPermissionResponse{}, nil
	})
	if err != nil {
		t.Fatalf("startACPClient() error = %v", err)
	}
	defer acpClient.Close(context.Background())
	if sessionID != "session-defaults" {
		t.Fatalf("sessionID = %q", sessionID)
	}
	status := (&controllerRun{configOptions: state.configOptions}).controllerStatusLocked(session.SessionRef{})
	if status.Model != "opus" || status.ReasoningEffort != "max" {
		t.Fatalf("startup status = %#v", status)
	}
	if _, err := acpClient.Prompt(ctx, sessionID, "work", nil); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
}

func TestStartACPClientAuthenticatesConfiguredMethodBeforeNewSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	manager := &Manager{}
	acpClient, sessionID, _, err := manager.startACPClient(ctx, t.TempDir(), subagent.AgentConfig{
		Name:    "protected",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerAuthenticationHelperProcess", "--"},
		Env:     map[string]string{"CAELIS_ACP_HELPER": "authentication"},
		Authentication: controlagents.Authentication{
			MethodID: "agent-login",
			Type:     controlagents.AuthenticationAgent,
		},
	}, "", nil, func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
		return client.RequestPermissionResponse{}, nil
	})
	if err != nil {
		t.Fatalf("startACPClient() error = %v", err)
	}
	defer acpClient.Close(context.Background())
	if sessionID != "authenticated-session" {
		t.Fatalf("sessionID = %q, want authenticated-session", sessionID)
	}
}

func TestStartACPClientDirectsConfiguredTerminalAuthenticationThroughConnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	manager := &Manager{}
	_, _, _, err := manager.startACPClient(ctx, t.TempDir(), subagent.AgentConfig{
		Name:    "protected-terminal",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerAuthenticationHelperProcess", "--"},
		Env:     map[string]string{"CAELIS_ACP_HELPER": "configured-terminal-authentication"},
		Authentication: controlagents.Authentication{
			MethodID: "terminal-login",
			Type:     controlagents.AuthenticationTerminal,
		},
	}, "", nil, func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
		return client.RequestPermissionResponse{}, nil
	})
	if err == nil {
		t.Fatal("startACPClient() error = nil for terminal auth_required")
	}
	if !strings.Contains(err.Error(), "run /connect") {
		t.Fatalf("startACPClient() error = %q, want /connect guidance", err)
	}
	for _, unexpected := range []string{"advertised no supported methods", "no longer advertises"} {
		if strings.Contains(err.Error(), unexpected) {
			t.Fatalf("startACPClient() error = %q, contains stale descriptor failure %q", err, unexpected)
		}
	}
}

func TestStartACPClientFallsBackAfterPostAuthenticationResumeFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	acpClient, sessionID, _, err := (&Manager{}).startACPClient(ctx, t.TempDir(), subagent.AgentConfig{
		Name:    "expired-session",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerAuthenticationHelperProcess", "--"},
		Env:     map[string]string{"CAELIS_ACP_HELPER": "resume-authentication-expired"},
		Authentication: controlagents.Authentication{
			MethodID: "agent-login",
			Type:     controlagents.AuthenticationAgent,
		},
	}, "expired-remote-session", nil, func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
		return client.RequestPermissionResponse{}, nil
	})
	if err != nil {
		t.Fatalf("startACPClient() error = %v", err)
	}
	defer acpClient.Close(context.Background())
	if sessionID != "fresh-after-resume" {
		t.Fatalf("sessionID = %q, want fresh fallback session", sessionID)
	}
}

func TestControllerPromptRecoversConfiguredAuthentication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg, acpClient, sessionID, state := startPromptAuthenticationClient(t, ctx)
	defer acpClient.Close(context.Background())

	run := &controllerRun{
		agent:                 cfg.Name,
		cfg:                   cfg,
		client:                acpClient,
		remoteSessionID:       sessionID,
		authenticationMethods: controlagents.CloneAuthenticationMethods(state.authenticationMethods),
	}
	resp, err := run.promptParts(ctx, []json.RawMessage{json.RawMessage(`{"type":"text","text":"work"}`)})
	if err != nil {
		t.Fatalf("promptParts() error = %v", err)
	}
	if resp.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("promptParts() stop reason = %q", resp.StopReason)
	}
}

func TestParticipantPromptRecoversConfiguredAuthentication(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg, acpClient, sessionID, state := startPromptAuthenticationClient(t, ctx)
	defer acpClient.Close(context.Background())

	run := &participantRun{
		agent:                 cfg.Name,
		client:                acpClient,
		remoteSessionID:       sessionID,
		authentication:        cfg.Authentication,
		authenticationMethods: controlagents.CloneAuthenticationMethods(state.authenticationMethods),
	}
	resp, err := run.promptParts(ctx, []json.RawMessage{json.RawMessage(`{"type":"text","text":"work"}`)})
	if err != nil {
		t.Fatalf("promptParts() error = %v", err)
	}
	if resp.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("promptParts() stop reason = %q", resp.StopReason)
	}
}

func startPromptAuthenticationClient(
	t *testing.T,
	ctx context.Context,
) (subagent.AgentConfig, *client.Client, string, controllerClientState) {
	t.Helper()
	cfg := subagent.AgentConfig{
		Name:    "prompt-protected",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestManagerAuthenticationHelperProcess", "--"},
		Env:     map[string]string{"CAELIS_ACP_HELPER": "prompt-authentication"},
		Authentication: controlagents.Authentication{
			MethodID: "agent-login",
			Type:     controlagents.AuthenticationAgent,
		},
	}
	acpClient, sessionID, state, err := (&Manager{}).startACPClient(
		ctx,
		t.TempDir(),
		cfg,
		"",
		nil,
		func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return client.RequestPermissionResponse{}, nil
		},
	)
	if err != nil {
		t.Fatalf("startACPClient() error = %v", err)
	}
	return cfg, acpClient, sessionID, state
}

func TestManagerSessionDefaultsHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_HELPER") != "session-defaults" {
		return
	}
	modelConfig := func(model string, effort string) []client.SessionConfigOption {
		return []client.SessionConfigOption{
			{
				ID: "model", Name: "Model", Type: "select", Category: "model", CurrentValue: model,
				Options: []client.SessionConfigSelectOption{{Value: "sonnet", Name: "Sonnet"}, {Value: "opus", Name: "Opus"}},
			},
			{
				ID: "effort", Name: "Reasoning effort", Type: "select", Category: "reasoning", CurrentValue: effort,
				Options: []client.SessionConfigSelectOption{{Value: "high", Name: "High"}, {Value: "max", Name: "Max"}},
			},
		}
	}
	modelApplied := false
	effortApplied := false
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	_ = conn.Serve(context.Background(), func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case client.MethodInitialize:
			return client.InitializeResponse{ProtocolVersion: 1}, nil
		case client.MethodSessionNew:
			return client.NewSessionResponse{SessionID: "session-defaults", ConfigOptions: modelConfig("sonnet", "high")}, nil
		case client.MethodSessionSetConfig:
			var req client.SetSessionConfigOptionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			switch strings.TrimSpace(req.ConfigID) {
			case "model":
				if fmt.Sprint(req.Value) != "opus" {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected model"}
				}
				modelApplied = true
				return client.SetSessionConfigOptionResponse{ConfigOptions: modelConfig("opus", "high")}, nil
			case "effort":
				if !modelApplied || fmt.Sprint(req.Value) != "max" {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "effort applied before model"}
				}
				effortApplied = true
				return client.SetSessionConfigOptionResponse{ConfigOptions: modelConfig("opus", "max")}, nil
			default:
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unknown config option"}
			}
		case client.MethodSessionPrompt:
			if !modelApplied || !effortApplied {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "prompt arrived before defaults"}
			}
			return client.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	os.Exit(0)
}

func TestManagerAuthenticationHelperProcess(t *testing.T) {
	mode := os.Getenv("CAELIS_ACP_HELPER")
	switch mode {
	case "authentication", "configured-terminal-authentication", "prompt-authentication", "resume-authentication-expired":
	default:
		return
	}
	authenticated := false
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	_ = conn.Serve(context.Background(), func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case client.MethodInitialize:
			response := client.InitializeResponse{ProtocolVersion: 1}
			if mode == "resume-authentication-expired" {
				response.AgentCapabilities.SessionCapabilities = map[string]json.RawMessage{
					"resume": json.RawMessage(`{}`),
				}
			}
			if mode != "configured-terminal-authentication" {
				response.AuthMethods = []json.RawMessage{
					json.RawMessage(`{"id":"agent-login","name":"Agent login"}`),
				}
			}
			return response, nil
		case client.MethodAuthenticate:
			var req client.AuthenticateRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil || req.MethodID != "agent-login" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected authenticate request"}
			}
			authenticated = true
			return client.AuthenticateResponse{}, nil
		case client.MethodSessionNew:
			if mode == "configured-terminal-authentication" || mode == "authentication" && !authenticated {
				return nil, &jsonrpc.RPCError{Code: client.ErrorCodeAuthRequired, Message: "Authentication required"}
			}
			if mode == "resume-authentication-expired" {
				return client.NewSessionResponse{SessionID: "fresh-after-resume"}, nil
			}
			return client.NewSessionResponse{SessionID: "authenticated-session"}, nil
		case client.MethodSessionResume:
			if mode != "resume-authentication-expired" {
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
			if !authenticated {
				return nil, &jsonrpc.RPCError{Code: client.ErrorCodeAuthRequired, Message: "Authentication required"}
			}
			return nil, &jsonrpc.RPCError{Code: -32004, Message: "session no longer exists"}
		case client.MethodSessionPrompt:
			if mode != "prompt-authentication" {
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
			if !authenticated {
				return nil, &jsonrpc.RPCError{Code: client.ErrorCodeAuthRequired, Message: "Authentication required"}
			}
			return client.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	os.Exit(0)
}
