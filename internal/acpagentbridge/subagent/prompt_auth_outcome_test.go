package subagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestPromptAuthenticationFailureKeepsAdmissionEvidence(t *testing.T) {
	for _, test := range []struct {
		name          string
		method        string
		failure       string
		prompts       int
		authenticates int
		state         delegation.State
	}{
		{"removed method", "removed-login", "", 1, 0, delegation.StateFailed},
		{"authenticate RPC failed", "agent-login", "authenticate", 1, 1, delegation.StateFailed},
		{"authenticate connection lost", "agent-login", "authenticate-disconnect", 1, 1, delegation.StateFailed},
		{"retry rejected", "agent-login", "retry-auth-required", 2, 1, delegation.StateFailed},
		{"retry internal error", "agent-login", "retry", 2, 1, delegation.StateUnknownOutcome},
		{"retry connection lost", "agent-login", "retry-disconnect", 2, 1, delegation.StateUnknownOutcome},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			trace := filepath.Join(t.TempDir(), "requests.log")
			config := AgentConfig{
				Name: "helper", Command: os.Args[0], Args: []string{"-test.run=^TestRunnerPromptFailureHelperProcess$", "--"},
				Env:            map[string]string{"CAELIS_ACP_SUBAGENT_HELPER": "prompt-authentication", "CAELIS_ACP_AUTH_FAILURE": test.failure, "CAELIS_ACP_AUTH_TRACE": trace},
				Authentication: controlagents.Authentication{MethodID: test.method, Type: controlagents.AuthenticationAgent},
			}
			registry, err := NewRegistry([]AgentConfig{config})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := NewRunner(RunnerConfig{Registry: registry})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := runner.Quiesce(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			completed := make(chan delegation.Result, 2)
			sink := completionSinkFunc(func(result delegation.Result) { completed <- result })
			wait := func() delegation.Result {
				select {
				case result := <-completed:
					return result
				case <-ctx.Done():
					t.Fatal(ctx.Err())
					return delegation.Result{}
				}
			}
			anchor, _, err := runner.Spawn(ctx, tasksubagent.SpawnContext{
				SessionRef: session.SessionRef{SessionID: "parent-auth"},
				ActivityID: "initial-auth", TaskID: "task-auth", CWD: t.TempDir(),
				Output: &recordingStreams{}, Completion: sink,
			}, delegation.Request{Agent: "helper", Prompt: "first"})
			if err != nil {
				t.Fatal(err)
			}
			result := wait()
			if result.State != test.state || result.Running {
				t.Fatalf("result = %#v, want %s", result, test.state)
			}
			if strings.Contains(result.Error, "private") || strings.Contains(result.Error, "removed-login") {
				t.Fatalf("public error leaked authentication details: %s", result.Error)
			}
			requests, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(requests), client.MethodSessionPrompt+"\n") != test.prompts || strings.Count(string(requests), client.MethodAuthenticate+"\n") != test.authenticates {
				t.Fatalf("wrong prompt/authenticate dispatch count: %s", requests)
			}

			// Repairing authentication may resume a rejected prompt's Session,
			// but must not unlock an indeterminate retry's execution.
			config.Authentication.MethodID = "agent-login"
			delete(config.Env, "CAELIS_ACP_AUTH_FAILURE")
			if err := registry.Replace([]AgentConfig{config}); err != nil {
				t.Fatal(err)
			}
			run, err := runner.lookup(anchor)
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.SubmitChildInput(ctx, agent.ChildInputRequest{
				Target: run.slot.target, Source: session.ParentCommunicationActor(), Input: "after repair",
				ActivityID: "after-repair", Output: &recordingStreams{}, Completion: sink,
			})
			if test.state == delegation.StateUnknownOutcome {
				if !errorcode.Is(err, errorcode.UnknownOutcome) {
					t.Fatalf("indeterminate retry was not isolated: %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("repaired authentication cannot resume: %#v", diagnosticErrorChain(err))
				}
				if result := wait(); result.State != delegation.StateCompleted {
					t.Fatalf("after repair = %#v", result)
				}
			}
		})
	}
}
