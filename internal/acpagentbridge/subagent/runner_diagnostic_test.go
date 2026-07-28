package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
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
	anchor, _, err := runner.Spawn(ctx, tasksubagent.SpawnContext{
		TaskID:  "task-prompt-failure",
		CWD:     t.TempDir(),
		Streams: streams,
	}, delegation.Request{Agent: "helper", Prompt: "review"})
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	got, err := runner.Wait(ctx, anchor, 2_000)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
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

func TestRunnerPromptFailureHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_SUBAGENT_HELPER") != "prompt-failure" {
		return
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case client.MethodInitialize:
			return client.InitializeResponse{ProtocolVersion: 1}, nil
		case client.MethodSessionNew:
			return client.NewSessionResponse{SessionID: "child-prompt-failure"}, nil
		case client.MethodSessionPrompt:
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

func TestSubagentPromptFailureDetailIsStable(t *testing.T) {
	if got := subagentPromptFailureDetail(context.DeadlineExceeded); got != "subagent prompt timed out" {
		t.Fatalf("deadline detail = %q, want timeout summary", got)
	}
	if got := subagentPromptFailureDetail(errors.New("Authorization: Bearer secret")); got != "subagent prompt failed" {
		t.Fatalf("generic detail = %q, want non-sensitive failure summary", got)
	}
}
