package tuiapp

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/ports/model"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
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

func TestEventStreamTerminalBatcherMergesGenericTerminalOverlap(t *testing.T) {
	t.Parallel()

	var sent []tea.Msg
	send := func(msg tea.Msg) {
		sent = append(sent, msg)
	}
	var batcher eventStreamTerminalBatcher
	for _, text := range []string{"line 1\nline 2\n", "line 2\nline 3\n"} {
		if !batcher.enqueue(genericTerminalStreamEnvelope("call-1", text), send) {
			t.Fatalf("generic terminal frame %q was not batched", text)
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
	if got, _ := acpTerminalOutput(update); got != "line 1\nline 2\nline 3\n" {
		t.Fatalf("acpTerminalOutput() = %q, want overlapped command stream merged", got)
	}
}

func TestEventStreamTerminalBatcherMergesGenericCumulativeSameLine(t *testing.T) {
	t.Parallel()

	var sent []tea.Msg
	send := func(msg tea.Msg) {
		sent = append(sent, msg)
	}
	var batcher eventStreamTerminalBatcher
	for _, text := range []string{"abc", "abcd"} {
		if !batcher.enqueue(genericTerminalStreamEnvelope("call-1", text), send) {
			t.Fatalf("generic terminal frame %q was not batched", text)
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
	if got, _ := acpTerminalOutput(update); got != "abcd" {
		t.Fatalf("acpTerminalOutput() = %q, want cumulative terminal frame", got)
	}
}

func TestEventStreamEnvelopeErrorReasonPrefersStructuredRedaction(t *testing.T) {
	t.Parallel()

	env := eventstream.Error(&model.RetryExhaustedError{
		MaxRetries: 5,
		Cause:      errors.New("model: http status 500 body=Internal Server Error"),
	})
	if !strings.Contains(env.Error, "Internal Server Error") {
		t.Fatalf("test setup error text = %q, want raw provider detail", env.Error)
	}
	reason := eventStreamEnvelopeErrorReason(env)
	if reason != "model request failed after 5 retries" {
		t.Fatalf("eventStreamEnvelopeErrorReason() = %q, want redacted retry error", reason)
	}
	if strings.Contains(reason, "Internal Server Error") || strings.Contains(reason, "http status 500") {
		t.Fatalf("failure reason leaked provider detail: %q", reason)
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

func genericTerminalStreamEnvelope(callID string, text string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		TurnID:    "turn-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    callID,
			Title:         stringPtr("shell output"),
			Kind:          stringPtr(schema.ToolKindExecute),
			Status:        stringPtr(schema.ToolStatusInProgress),
			Meta:          metautil.WithTerminalOutput(nil, "terminal-1", text),
		},
	}
}
