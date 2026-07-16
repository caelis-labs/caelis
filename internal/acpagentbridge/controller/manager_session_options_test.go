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
