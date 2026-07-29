package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	completions := &recordingCompletionSink{results: make(chan delegation.Result, 2)}
	anchor, _, err := runner.Spawn(ctx, tasksubagent.SpawnContext{
		TaskID:     "task-prompt-failure",
		CWD:        t.TempDir(),
		Streams:    streams,
		Completion: completions,
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
	if got.Error != "subagent authentication required" {
		t.Fatalf("Error = %q, want stable operation-level summary", got.Error)
	}
	if got.Result != "" {
		t.Fatalf("Result = %q, want no final assistant output", got.Result)
	}
	if got.OutputPreview != "subagent authentication required" {
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
	select {
	case completed := <-completions.results:
		if completed.TaskID != "task-prompt-failure" || completed.State != delegation.StateFailed ||
			completed.Running || completed.Error != "subagent authentication required" {
			t.Fatalf("completion = %#v, want one safe terminal failure", completed)
		}
	case <-ctx.Done():
		t.Fatal("terminal completion was not published")
	}
	select {
	case duplicate := <-completions.results:
		t.Fatalf("duplicate terminal completion = %#v", duplicate)
	default:
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
	spawnCompletions := &recordingCompletionSink{results: make(chan delegation.Result, 2)}
	anchor, _, err := runner.Spawn(ctx, tasksubagent.SpawnContext{
		TaskID:     "task-prompt-authentication",
		CWD:        t.TempDir(),
		Completion: spawnCompletions,
	}, delegation.Request{Agent: "protected-helper", Prompt: "review"})
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	got, err := runner.Wait(ctx, anchor, 2_000)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if got.State != delegation.StateCompleted || got.Running {
		t.Fatalf("Result = state %q running %v, want completed", got.State, got.Running)
	}
	assertOneCompletion(t, ctx, spawnCompletions, delegation.StateCompleted)

	continueCompletions := &recordingCompletionSink{results: make(chan delegation.Result, 2)}
	continued, err := runner.Continue(ctx, anchor, delegation.ContinueRequest{
		Prompt: "follow up", YieldTimeMS: 2_000, Completion: continueCompletions,
	})
	if err != nil {
		t.Fatalf("Continue() error = %v", err)
	}
	if continued.State != delegation.StateCompleted || continued.Running {
		t.Fatalf("Continue result = state %q running %v, want completed", continued.State, continued.Running)
	}
	assertOneCompletion(t, ctx, continueCompletions, delegation.StateCompleted)

	waitOnly, err := runner.Continue(ctx, anchor, delegation.ContinueRequest{
		Prompt: "wait-only follow up", YieldTimeMS: 2_000,
	})
	if err != nil {
		t.Fatalf("Continue(wait-only) error = %v", err)
	}
	if waitOnly.State != delegation.StateCompleted || waitOnly.Running {
		t.Fatalf("Continue(wait-only) result = %#v, want completed", waitOnly)
	}
	select {
	case stale := <-continueCompletions.results:
		t.Fatalf("nil continuation reused prior turn completion sink: %#v", stale)
	default:
	}
}

func TestRunnerWaitDoesNotPassBlockedCompletionDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := NewRegistry([]AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestRunnerPromptFailureHelperProcess", "--"},
		Env:     map[string]string{"CAELIS_ACP_SUBAGENT_HELPER": "prompt-failure"},
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	completion := &blockingCompletionSink{
		started: make(chan struct{}),
		release: make(chan struct{}),
		results: make(chan delegation.Result, 1),
	}
	anchor, _, err := runner.Spawn(ctx, tasksubagent.SpawnContext{
		TaskID: "task-blocked-completion", CWD: t.TempDir(), Completion: completion,
	}, delegation.Request{Agent: "helper", Prompt: "review"})
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	select {
	case <-completion.started:
	case <-ctx.Done():
		t.Fatal("completion delivery did not start")
	}
	waited := make(chan delegation.Result, 1)
	go func() {
		result, _ := runner.Wait(ctx, anchor, 2_000)
		waited <- result
	}()
	select {
	case result := <-waited:
		t.Fatalf("Wait() passed blocked completion delivery: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}
	close(completion.release)
	select {
	case result := <-waited:
		if result.State != delegation.StateFailed || result.Running {
			t.Fatalf("Wait() = %#v, want terminal failure", result)
		}
	case <-ctx.Done():
		t.Fatal("Wait() did not finish after completion delivery")
	}
}

func TestRunnerCancelAcknowledgementIsNotTerminalProof(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	run := &childRun{
		anchor:  delegation.Anchor{TaskID: "task-cancel-ack", SessionID: "child-cancel-ack"},
		taskID:  "task-cancel-ack",
		state:   delegation.StateRunning,
		running: true,
		done:    make(chan struct{}),
		cancel:  func() { cancelled <- struct{}{} },
	}
	runner := &Runner{clock: time.Now, runs: map[string]*childRun{"task-cancel-ack": run}}

	if err := runner.Cancel(context.Background(), run.anchor); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("Cancel() did not cancel the child prompt context")
	}
	got, err := runner.Wait(context.Background(), run.anchor, 0)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !got.Running || got.State != delegation.StateRunning {
		t.Fatalf("Wait(after cancel ACK) = %#v, want running until drivePrompt proves terminal", got)
	}
	run.mu.RLock()
	requested, finishing := run.cancelRequested, run.finishing
	run.mu.RUnlock()
	if !requested || !finishing {
		t.Fatalf("cancel state = requested %v finishing %v, want pending terminal transition", requested, finishing)
	}
	select {
	case <-run.done:
		t.Fatal("Cancel ACK closed done before terminal completion callback")
	default:
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

func TestSubagentPromptFailureDetailIsStable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "timeout", err: context.DeadlineExceeded, want: "subagent prompt timed out"},
		{name: "auth", err: &jsonrpc.CallError{Code: client.ErrorCodeAuthRequired, Message: "Bearer secret"}, want: "subagent authentication required"},
		{name: "connection", err: fmt.Errorf("wrapped path /Users/private: %w", io.EOF), want: "subagent connection closed"},
		{name: "unsupported", err: &jsonrpc.CallError{Code: -32601, Message: "secret"}, want: "subagent prompt is unsupported"},
		{name: "invalid", err: &jsonrpc.CallError{Code: -32602, Message: "secret"}, want: "subagent prompt was invalid"},
		{name: "peer rejection", err: &jsonrpc.CallError{Code: -32001, Message: "Bearer secret", Data: "/Users/private"}, want: "subagent prompt was rejected"},
		{name: "generic", err: errors.New("Authorization: Bearer secret"), want: "subagent prompt failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := subagentPromptFailureDetail(test.err)
			if got != test.want {
				t.Fatalf("detail = %q, want %q", got, test.want)
			}
			for _, forbidden := range []string{"secret", "/Users/private", "Bearer"} {
				if strings.Contains(got, forbidden) {
					t.Fatalf("detail leaked %q: %q", forbidden, got)
				}
			}
		})
	}
}

type recordingCompletionSink struct {
	results chan delegation.Result
}

type blockingCompletionSink struct {
	started chan struct{}
	release chan struct{}
	results chan delegation.Result
}

func (s *blockingCompletionSink) PublishSubagentCompletion(result delegation.Result) {
	close(s.started)
	<-s.release
	s.results <- delegation.CloneResult(result)
}

func (s *recordingCompletionSink) PublishSubagentCompletion(result delegation.Result) {
	s.results <- delegation.CloneResult(result)
}

func assertOneCompletion(t *testing.T, ctx context.Context, sink *recordingCompletionSink, want delegation.State) {
	t.Helper()
	select {
	case completed := <-sink.results:
		if completed.State != want || completed.Running {
			t.Fatalf("completion = %#v, want terminal %q", completed, want)
		}
	case <-ctx.Done():
		t.Fatalf("terminal %q completion was not published", want)
	}
	select {
	case duplicate := <-sink.results:
		t.Fatalf("duplicate terminal completion = %#v", duplicate)
	default:
	}
}
