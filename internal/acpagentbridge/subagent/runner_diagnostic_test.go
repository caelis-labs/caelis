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

	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
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
}

func TestRunnerPromptFailureHelperProcess(t *testing.T) {
	mode := os.Getenv("CAELIS_ACP_SUBAGENT_HELPER")
	switch mode {
	case "prompt-failure", "prompt-authentication":
	default:
		return
	}
	authenticated := false
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case client.MethodInitialize:
			response := client.InitializeResponse{ProtocolVersion: 1}
			if mode == "prompt-authentication" {
				response.AuthMethods = []json.RawMessage{
					json.RawMessage(`{"id":"agent-login","name":"Agent login"}`),
				}
			}
			return response, nil
		case client.MethodSessionNew:
			return client.NewSessionResponse{SessionID: "child-prompt-failure"}, nil
		case client.MethodAuthenticate:
			var req client.AuthenticateRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil || req.MethodID != "agent-login" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected authenticate request"}
			}
			authenticated = true
			return client.AuthenticateResponse{}, nil
		case client.MethodSessionPrompt:
			if mode == "prompt-authentication" {
				if !authenticated {
					return nil, &jsonrpc.RPCError{Code: client.ErrorCodeAuthRequired, Message: "Authentication required"}
				}
				return client.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
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
