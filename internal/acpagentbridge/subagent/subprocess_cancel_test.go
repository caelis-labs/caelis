package subagent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestRunnerCancelNonCooperativeProcessConvergesWithoutClaimingRemoteCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	registry, err := NewRegistry([]AgentConfig{{Name: "helper", Command: os.Args[0],
		Args: []string{"-test.run=^TestNonCooperativeSubagentHelperProcess$", "--"},
		Env:  map[string]string{"CAELIS_NONCOOPERATIVE_CHILD": "1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 8*time.Second)
		defer stop()
		if err := runner.Quiesce(cleanup); err != nil {
			t.Errorf("Quiesce: %v", err)
		}
	})
	events := make(chan childInputTestEvent, 16)
	spawn := childInputSpawnContext(t, "noncooperative", events)
	anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "start"})
	if err != nil {
		t.Fatal(err)
	}
	consumeSpawnInitialInput(t, ctx, events, spawn.ActivityID, "start")
	select {
	case event := <-events:
		if event.Frame == nil || event.Frame.Event == nil || event.Frame.Event.Text != "producer-ready" {
			t.Fatalf("ready = %#v", event)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := runner.Cancel(ctx, anchor); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Result == nil || event.Result.TaskID != spawn.TaskID || event.Result.State != delegation.StateUnknownOutcome || event.Result.Running {
			t.Fatalf("completion = %#v", event)
		}
	case <-ctx.Done():
		t.Fatal("noncooperative producer did not converge")
	}
	final, err := runner.Wait(ctx, anchor, 1000)
	if err != nil || final.Running || final.State != delegation.StateUnknownOutcome || final.TaskID != spawn.TaskID {
		t.Fatalf("terminal Wait = %#v, %v", final, err)
	}
}

func TestNonCooperativeSubagentHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_NONCOOPERATIVE_CHILD") != "1" {
		return
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := decoder.Decode(&request); err != nil {
			if err != io.EOF {
				os.Exit(2)
			}
			break
		}
		var result any
		switch request.Method {
		case client.MethodInitialize:
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}
		case client.MethodSessionNew:
			result = map[string]any{"sessionId": "noncooperative-session"}
		case client.MethodSessionPrompt:
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": client.MethodSessionUpdate, "params": map[string]any{
				"sessionId": "noncooperative-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "producer-ready"}},
			}})
			continue // Never answer the prompt or cooperate with cancellation.
		default:
			continue
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			os.Exit(3)
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}
