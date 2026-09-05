package subagent

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
)

func TestInitialPromptAbortDrainsIngressBeforeJoiningClient(t *testing.T) {
	t.Parallel()

	runner := &Runner{clock: time.Now}
	producerCtx, cancelProducer := context.WithCancel(t.Context())
	defer cancelProducer()
	events := make(chan childInputTestEvent, 8)
	spawn := childInputSpawnContext(t, "task-aborted-dispatch", events)
	run := &childRun{
		anchor: delegation.Anchor{TaskID: spawn.TaskID, SessionID: "child", AgentID: "helper"},
		taskID: spawn.TaskID, spawn: spawn, output: spawn.Output, completion: spawn.Completion,
		ctx: producerCtx, cancel: cancelProducer, running: true, state: delegation.StateRunning,
		done: make(chan struct{}),
	}
	slot := newChildSlot(agent.ChildEndpointRef{}, run)
	slot.beginInitialActivity(spawn.ActivityID, run)
	writer := &blockedPromptWriter{started: make(chan struct{}), closed: make(chan struct{})}
	reader, peer := io.Pipe()
	defer peer.Close()
	defer reader.Close()
	defer writer.Close()
	updateEntered := make(chan struct{})
	remote, err := client.NewStreamClient(writer, reader, client.Config{
		OnUpdate: func(env client.UpdateEnvelope) {
			close(updateEntered)
			runner.handleUpdate(run, env)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	run.client = remote
	callerCtx, cancelDispatch := context.WithCancel(t.Context())
	defer cancelDispatch()
	dispatchDone := slot.beginPromptDispatch(cancelDispatch)
	finished := make(chan struct{})
	go func() {
		runner.dispatchInitialPrompt(callerCtx, producerCtx, slot, run, dispatchDone, "unconfirmed initial prompt")
		close(finished)
	}()
	select {
	case <-writer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt write did not start")
	}
	// The peer has concrete input evidence while the local write outcome is
	// still unknown. Its callback enters before cancellation and waits for the
	// same ingress lock as the initial prompt projection.
	if err := json.NewEncoder(peer).Encode(map[string]any{
		"jsonrpc": "2.0", "method": client.MethodSessionUpdate,
		"params": client.SessionNotification{
			SessionID: "child",
			Update: jsonrpc.MustMarshalRaw(client.ContentChunk{
				SessionUpdate: client.UpdateUserMessage,
				Content:       jsonrpc.MustMarshalRaw(client.TextContent{Type: "text", Text: "peer input evidence"}),
			}),
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-updateEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("peer callback did not enter")
	}
	cancelDispatch()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled dispatch blocked waiting for its own ingress callback")
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(t.Context(), time.Second)
	defer cancelCleanup()
	if err := remote.Close(cleanupCtx); err != nil {
		t.Fatalf("subsequent cleanup retained a false connection join failure: %v", err)
	}
	frames, terminal := waitSpawnInputProjectionFrames(t, cleanupCtx, events, spawn.ActivityID)
	if terminal.Result == nil || terminal.Result.State != delegation.StateUnknownOutcome {
		t.Fatalf("terminal = %#v, want unknown dispatch outcome", terminal)
	}
	var inputs []string
	for _, frame := range frames {
		if input := session.ProtocolAgentCommunicationOf(frame.Event); input != nil {
			inputs = append(inputs, input.Text)
		}
	}
	if len(inputs) != 1 || inputs[0] != "peer input evidence" {
		t.Fatalf("observed input = %#v, want only concrete peer evidence", inputs)
	}
}

type blockedPromptWriter struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func (w *blockedPromptWriter) Write([]byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	<-w.closed
	return 0, io.ErrClosedPipe
}

func (w *blockedPromptWriter) Close() error {
	w.closeOnce.Do(func() { close(w.closed) })
	return nil
}
