package tuiapp

import (
	"testing"
	"time"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestRenderSchedulerMergesTerminalContent(t *testing.T) {
	t.Parallel()

	assertRenderSchedulerTerminalMerge(t, terminalMetaStreamEnvelope, []string{"Step 1/5\n", "Step 2/5\n"}, "Step 1/5\nStep 2/5\n")
}

func TestRenderSchedulerPreservesSplitTerminalNewlineFrame(t *testing.T) {
	t.Parallel()

	assertRenderSchedulerTerminalMerge(t, terminalMetaStreamEnvelope, []string{"Step 1/2", "\n", "Step 2/2\n"}, "Step 1/2\nStep 2/2\n")
}

func TestRenderSchedulerMergesGenericTerminalOverlap(t *testing.T) {
	t.Parallel()

	assertRenderSchedulerTerminalMerge(t, genericTerminalStreamEnvelope, []string{"line 1\nline 2\n", "line 2\nline 3\n"}, "line 1\nline 2\nline 3\n")
}

func TestRenderSchedulerMergesGenericCumulativeTerminalLine(t *testing.T) {
	t.Parallel()

	assertRenderSchedulerTerminalMerge(t, genericTerminalStreamEnvelope, []string{"abc", "abcd"}, "abcd")
}

func assertRenderSchedulerTerminalMerge(t *testing.T, envelope func(string, string) eventstream.Envelope, chunks []string, want string) {
	t.Helper()
	model := NewModel(Config{NoColor: true, NoAnimation: true, StreamTickInterval: time.Millisecond})
	for _, chunk := range chunks {
		updated, _, handled := model.dispatchRenderEvent(envelope("call-1", chunk))
		if !handled {
			t.Fatalf("terminal frame %q was not handled", chunk)
		}
		model = updated.(*Model)
	}
	if len(model.pendingRenderEvents.items) != 1 {
		t.Fatalf("pending render items = %#v, want one merged terminal update", model.pendingRenderEvents.items)
	}
	merged, ok := model.pendingRenderEvents.items[0].msg.(eventstream.Envelope)
	if !ok {
		t.Fatalf("pending message = %T, want eventstream.Envelope", model.pendingRenderEvents.items[0].msg)
	}
	update, ok := merged.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("env.Update = %T, want ToolCallUpdate", merged.Update)
	}
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
