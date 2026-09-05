package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
)

func TestSpawnProjectsInitialInputBeforeOutputAndDeduplicatesEcho(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
	}{
		{name: "without provider echo", mode: "no-echo"},
		{name: "with provider echo", mode: "echo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			runner := spawnInputProjectionTestRunner(t, test.mode, nil)
			events := make(chan childInputTestEvent, 8)
			spawn := childInputSpawnContext(t, "task-spawn-input-"+test.mode, events)
			_, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "initial prompt"})
			if err != nil {
				t.Fatal(err)
			}

			frames, terminal := waitSpawnInputProjectionFrames(t, ctx, events, spawn.ActivityID)
			if terminal.Result == nil || terminal.Result.State != delegation.StateCompleted {
				t.Fatalf("terminal = %#v, want completed", terminal.Result)
			}
			if len(frames) != 2 || frames[0].Event == nil || frames[1].Event == nil {
				t.Fatalf("frames = %#v, want one initial input followed by one Agent output", frames)
			}
			communication := session.ProtocolAgentCommunicationOf(frames[0].Event)
			if communication == nil || communication.Text != "initial prompt" ||
				frames[0].Event.Actor != session.ParentCommunicationActor() {
				t.Fatalf("initial input = %#v, want typed parent user message", frames[0].Event)
			}
			if got := frames[1].Event.Text; got != "first output" {
				t.Fatalf("second frame = %#v, want first Agent output", frames[1].Event)
			}
			communicationCount := 0
			for _, frame := range frames {
				if session.ProtocolAgentCommunicationOf(frame.Event) != nil {
					communicationCount++
				}
			}
			if communicationCount != 1 {
				t.Fatalf("Agent communication count = %d in %#v, want one", communicationCount, frames)
			}
			if err := runner.Quiesce(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSpawnDoesNotProjectInitialInputWhenDispatchDoesNotStart(t *testing.T) {
	dispatchCtx, cancelDispatch := context.WithCancel(context.Background())
	observeCtx, cancelObserve := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelObserve()

	runner := spawnInputProjectionTestRunner(t, "no-echo", func(context.Context, tasksubagent.SpawnContext, string, AgentConfig) (controlagents.SessionOptions, error) {
		// Session setup is complete and zero options make the following apply a
		// no-op. Cancel here so the real initial PreparedPrompt dispatch observes
		// RequestSubmissionNotStarted without a timing race.
		cancelDispatch()
		return controlagents.SessionOptions{}, nil
	})
	events := make(chan childInputTestEvent, 4)
	spawn := childInputSpawnContext(t, "task-spawn-input-dispatch-failure", events)
	_, _, err := runner.Spawn(dispatchCtx, spawn, delegation.Request{Agent: "helper", Prompt: "must not appear"})
	if err != nil {
		t.Fatal(err)
	}

	frames, terminal := waitSpawnInputProjectionFrames(t, observeCtx, events, spawn.ActivityID)
	if terminal.Result == nil || terminal.Result.State == delegation.StateCompleted {
		t.Fatalf("terminal = %#v, want failed or interrupted dispatch", terminal.Result)
	}
	for _, frame := range frames {
		if session.ProtocolAgentCommunicationOf(frame.Event) != nil {
			t.Fatalf("unstarted prompt projected as child input: %#v", frame.Event)
		}
	}
	if err := runner.Quiesce(observeCtx); err != nil {
		t.Fatal(err)
	}
}

func waitSpawnInputProjectionFrames(
	t *testing.T,
	ctx context.Context,
	events <-chan childInputTestEvent,
	activityID string,
) ([]output.Event, childInputTestEvent) {
	t.Helper()
	var frames []output.Event
	for {
		select {
		case event := <-events:
			if event.ActivityID != activityID {
				continue
			}
			if event.Frame != nil {
				frames = append(frames, *event.Frame)
			}
			if event.Result != nil {
				return frames, event
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func spawnInputProjectionTestRunner(t *testing.T, mode string, preparer SessionPreparer) *Runner {
	t.Helper()
	registry, err := NewRegistry([]AgentConfig{{
		Name: "helper", Command: os.Args[0],
		Args: []string{"-test.run=TestSpawnInputProjectionHelperProcess", "--"},
		Env: map[string]string{
			"CAELIS_ACP_SPAWN_INPUT_HELPER": "1",
			"CAELIS_ACP_SPAWN_INPUT_MODE":   mode,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry, SessionPreparer: preparer})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestSpawnInputProjectionHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ACP_SPAWN_INPUT_HELPER") != "1" {
		return
	}
	mode := strings.TrimSpace(os.Getenv("CAELIS_ACP_SPAWN_INPUT_MODE"))
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(_ context.Context, message jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch message.Method {
		case client.MethodInitialize:
			return client.InitializeResponse{
				ProtocolVersion: 1,
				Meta: map[string]json.RawMessage{
					client.SessionSteeringMetaKey: json.RawMessage(`{"supported":true}`),
				},
			}, nil
		case client.MethodSessionNew:
			return client.NewSessionResponse{SessionID: "spawn-input-session"}, nil
		case client.MethodSessionPrompt:
			if mode == "echo" {
				if err := spawnInputProjectionNotify(conn, client.UpdateUserMessage, "initial prompt"); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
				}
			}
			if err := spawnInputProjectionNotify(conn, client.UpdateAgentMessage, "first output"); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
			}
			return client.PromptResponse{StopReason: string(acpsdk.StopReasonEndTurn)}, nil
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

func spawnInputProjectionNotify(conn *jsonrpc.Conn, updateType, text string) error {
	return conn.Notify(client.MethodSessionUpdate, client.SessionNotification{
		SessionID: "spawn-input-session",
		Update: jsonrpc.MustMarshalRaw(client.ContentChunk{
			SessionUpdate: updateType,
			MessageID:     "spawn-input-" + updateType,
			Content:       jsonrpc.MustMarshalRaw(client.TextContent{Type: "text", Text: text}),
		}),
	})
}
