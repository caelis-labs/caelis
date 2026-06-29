package tuiapp

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestEventStreamTerminalBatcherMergesTerminalContent(t *testing.T) {
	t.Parallel()

	var sent []tea.Msg
	send := func(msg tea.Msg) {
		sent = append(sent, msg)
	}
	var batcher eventStreamTerminalBatcher
	if !batcher.enqueue(terminalMetaStreamEnvelope("call-1", "Step 1/5\n"), send) {
		t.Fatal("first terminal meta frame was not batched")
	}
	if !batcher.enqueue(terminalMetaStreamEnvelope("call-1", "Step 2/5\n"), send) {
		t.Fatal("second terminal meta frame was not batched")
	}
	batcher.flush(send)

	if len(sent) != 1 {
		t.Fatalf("sent messages = %#v, want one merged terminal update", sent)
	}
	env, ok := sent[0].(eventstream.Envelope)
	if !ok {
		t.Fatalf("sent[0] = %T, want eventstream.Envelope", sent[0])
	}
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("env.Update = %T, want ToolCallUpdate", env.Update)
	}
	const want = "Step 1/5\nStep 2/5\n"
	if got, _ := acpTerminalOutput(update); got != want {
		t.Fatalf("acpTerminalOutput() = %q, want %q", got, want)
	}
}

func TestEventStreamTerminalBatcherPreservesSplitNewlineFrame(t *testing.T) {
	t.Parallel()

	var sent []tea.Msg
	send := func(msg tea.Msg) {
		sent = append(sent, msg)
	}
	var batcher eventStreamTerminalBatcher
	for _, text := range []string{"Step 1/2", "\n", "Step 2/2\n"} {
		if !batcher.enqueue(terminalMetaStreamEnvelope("call-1", text), send) {
			t.Fatalf("terminal frame %q was not batched", text)
		}
	}
	batcher.flush(send)

	if len(sent) != 1 {
		t.Fatalf("sent messages = %#v, want one merged terminal update", sent)
	}
	env, ok := sent[0].(eventstream.Envelope)
	if !ok {
		t.Fatalf("sent[0] = %T, want eventstream.Envelope", sent[0])
	}
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("env.Update = %T, want ToolCallUpdate", env.Update)
	}
	const want = "Step 1/2\nStep 2/2\n"
	if got, _ := acpTerminalOutput(update); got != want {
		t.Fatalf("acpTerminalOutput() = %q, want %q", got, want)
	}
}

func terminalMetaStreamEnvelope(callID string, text string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		TurnID:    "turn-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    callID,
			Title:         stringPtr("RUN_COMMAND"),
			Kind:          stringPtr(schema.ToolKindExecute),
			Status:        stringPtr(schema.ToolStatusInProgress),
			Meta:          metautil.WithTerminalOutput(acpToolNameMeta("RUN_COMMAND"), "terminal-1", text),
		},
	}
}
