package subagent

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
)

func TestIdlePromptDispatchOrdersInputBeforePeerOutput(t *testing.T) {
	for _, abort := range []bool{false, true} {
		name := "success"
		if abort {
			name = "cancelled write"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			events := make(chan childInputTestEvent, 8)
			run := &childRun{
				anchor: delegation.Anchor{TaskID: "task", SessionID: "child", AgentID: "helper"},
				taskID: "task", state: delegation.StateCompleted, ctx: ctx,
			}
			target := agent.ChildEndpointRef{
				ParticipantID: "helper", SessionID: "parent", EndpointKey: "task", Role: "worker",
				Placement: placement.Placement{Kind: placement.KindAgent, Agent: "helper"},
			}
			slot := newChildSlot(target, run)
			runner := &Runner{clock: time.Now, slots: map[string]*childSlot{"task": slot}}
			writer := &delayedPromptWriter{request: make(chan json.RawMessage, 1), release: make(chan struct{}), closed: make(chan struct{})}
			reader, peer := io.Pipe()
			defer peer.Close()
			defer writer.Close()
			entered := make(chan struct{})
			remote, err := client.NewStreamClient(writer, reader, client.Config{OnUpdate: func(env client.UpdateEnvelope) {
				close(entered)
				runner.handleUpdate(run, env)
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer remote.Close(context.Background())
			run.client = remote
			dispatchCtx, cancelDispatch := context.WithCancel(ctx)
			defer cancelDispatch()
			finished := make(chan error, 1)
			go func() {
				_, err := submitChildInputTest(runner, events, dispatchCtx, agent.ChildInputRequest{
					Target: target, UserInput: true, Source: session.ActorRef{Kind: session.ActorKindUser, ID: "user"}, Input: "continue",
				})
				finished <- err
			}()
			var request struct{ ID json.RawMessage }
			select {
			case data := <-writer.request:
				if err := json.Unmarshal(data, &request); err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("prompt write did not start")
			}
			// The peer can reply before the local writer reports completion.
			if err := json.NewEncoder(peer).Encode(map[string]any{
				"jsonrpc": "2.0", "method": client.MethodSessionUpdate,
				"params": client.SessionNotification{SessionID: "child", Update: jsonrpc.MustMarshalRaw(client.ContentChunk{
					SessionUpdate: client.UpdateAgentMessage,
					Content:       jsonrpc.MustMarshalRaw(client.TextContent{Type: "text", Text: "early output"}),
				})},
			}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("peer update did not enter")
			}
			select {
			case event := <-events:
				t.Fatalf("output overtook the pending input: %#v", event)
			case <-time.After(50 * time.Millisecond):
			}
			if abort {
				cancelDispatch()
			} else {
				close(writer.release)
			}
			select {
			case err := <-finished:
				if (err != nil) != abort {
					t.Fatalf("dispatch error = %v, abort = %v", err, abort)
				}
			case <-ctx.Done():
				t.Fatal("dispatch did not drain its ingress callback")
			}
			if !abort {
				if err := json.NewEncoder(peer).Encode(map[string]any{
					"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"stopReason": "end_turn"},
				}); err != nil {
					t.Fatal(err)
				}
			}
			var texts []string
			for {
				select {
				case event := <-events:
					if event.Frame != nil && event.Frame.Event != nil {
						texts = append(texts, session.EventText(event.Frame.Event))
					}
					if event.Result != nil {
						if !abort && (len(texts) != 2 || texts[0] != "continue" || texts[1] != "early output") {
							t.Fatalf("transcript = %q, want input followed by output", texts)
						}
						if abort {
							for _, text := range texts {
								if text == "continue" {
									t.Fatal("unconfirmed input was published")
								}
							}
						}
						return
					}
				case <-ctx.Done():
					t.Fatal("activity did not settle")
				}
			}
		})
	}
}

type delayedPromptWriter struct {
	request chan json.RawMessage
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (w *delayedPromptWriter) Write(data []byte) (int, error) {
	w.request <- append(json.RawMessage(nil), data...)
	select {
	case <-w.release:
		return len(data), nil
	case <-w.closed:
		return 0, io.ErrClosedPipe
	}
}

func (w *delayedPromptWriter) Close() error {
	w.once.Do(func() { close(w.closed) })
	return nil
}
