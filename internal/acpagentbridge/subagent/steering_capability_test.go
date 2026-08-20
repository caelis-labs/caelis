package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestSpawnedChildRetainsNegotiatedSteeringCapability(t *testing.T) {
	t.Parallel()

	for _, supported := range []bool{false, true} {
		supported := supported
		t.Run(fmt.Sprintf("supported=%v", supported), func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			runner := steeringChildTestRunner(t, fmt.Sprintf(`{"supported":%v}`, supported), "")
			completion := make(chan delegation.Result, 1)
			anchor, _, err := runner.Spawn(ctx, tasksubagent.SpawnContext{
				TaskID: "task-new-" + fmt.Sprint(supported),
				CWD:    t.TempDir(),
				Completion: completionSinkFunc(func(result delegation.Result) {
					completion <- result
				}),
			}, delegation.Request{Agent: "helper", Prompt: "review"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := runner.lookup(anchor)
			if err != nil {
				t.Fatal(err)
			}
			if run.supportsSteering != supported {
				t.Fatalf("child steering capability = %v, want %v", run.supportsSteering, supported)
			}
			select {
			case <-completion:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := runner.Quiesce(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSpawnRejectsMalformedSteeringBeforeSessionOrRegistration(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	marker := filepath.Join(t.TempDir(), "session-called")
	runner := steeringChildTestRunner(t, `{"supported":null}`, marker)
	_, _, err := runner.Spawn(ctx, tasksubagent.SpawnContext{
		TaskID: "task-malformed", CWD: t.TempDir(),
	}, delegation.Request{Agent: "helper", Prompt: "review"})
	if err == nil {
		t.Fatal("Spawn() error = nil, want malformed steering capability")
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("session/new ran before steering rejection: stat error = %v", statErr)
	}
	runner.mu.RLock()
	registered := len(runner.runs)
	runner.mu.RUnlock()
	if registered != 0 {
		t.Fatalf("malformed steering registered %d child runs", registered)
	}
}

func steeringChildTestRunner(t *testing.T, meta string, marker string) *Runner {
	t.Helper()
	registry, err := NewRegistry([]AgentConfig{{
		Name:    "helper",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestRunnerSteeringCapabilityHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_STEERING_HELPER":       "1",
			"CAELIS_ACP_STEERING_META":         meta,
			"CAELIS_ACP_SESSION_EFFECT_MARKER": marker,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestRunnerSteeringCapabilityHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_STEERING_HELPER") != "1" {
		return
	}
	marker := os.Getenv("CAELIS_ACP_SESSION_EFFECT_MARKER")
	markSessionEffect := func() {
		if marker != "" {
			_ = os.WriteFile(marker, []byte("called"), 0o600)
		}
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(_ context.Context, message jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch message.Method {
		case client.MethodInitialize:
			response := client.InitializeResponse{
				ProtocolVersion: 1,
				Meta: map[string]json.RawMessage{
					client.SessionSteeringMetaKey: json.RawMessage(os.Getenv("CAELIS_ACP_STEERING_META")),
				},
			}
			return response, nil
		case client.MethodSessionNew:
			markSessionEffect()
			return client.NewSessionResponse{SessionID: "child-new"}, nil
		case client.MethodSessionResume:
			markSessionEffect()
			return client.ResumeSessionResponse{}, nil
		case client.MethodSessionPrompt:
			return client.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
