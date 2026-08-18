package tuiapp

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type concurrentSpawnTask struct {
	callID string
	handle string
	prompt string
}

// TestConcurrentSpawnDeliveryRetainsEveryHandle reproduces the three-call
// shape from Session s-r6qf6wk223ze6i52kdmwrdsfwg. In particular, reya's
// folded result preview is one rune shorter than its start preview. The final
// handle-bearing preview/full pair must still replace the start pair.
func TestConcurrentSpawnDeliveryRetainsEveryHandle(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(1_786_000_000, 0))
	spawned := faultShapeConcurrentSpawnTasks()

	for index, task := range spawned {
		model = applyACPEnvelopeForTest(t, model, concurrentSpawnStartEnvelope(index+1, task))
	}
	for index, task := range spawned {
		model = applyACPEnvelopeForTest(t, model, concurrentSpawnResultEnvelope(index+4, task))
	}

	requireSpawnCallIDs(t, model, spawned)
	rows := model.subagentRosterRows()
	if len(rows) != len(spawned) {
		t.Fatalf("subagent roster rows = %#v, want %d", rows, len(spawned))
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	for _, task := range spawned {
		if !strings.Contains(plain, "Spawned "+task.handle+"[breeze]") {
			t.Errorf("rendered Spawn row omitted handle %q:\n%s", task.handle, plain)
		}
		found := false
		for row, line := range model.viewportPlainLines {
			if !strings.Contains(line, "Spawned "+task.handle+"[breeze]") {
				continue
			}
			found = true
			if token := model.viewportClickTokens[row]; token != subagentOutputOverlayClickToken(task.callID) {
				t.Errorf("Spawn row %s click token = %q", task.handle, token)
			}
		}
		if !found {
			t.Errorf("ordinary Spawn row %s was not independently addressable", task.handle)
		}
	}
}

// TestConcurrentSpawnForwarderPreservesEveryToolEnvelope fixes the lower
// boundary of the regression: non-narrative tool envelopes are semantic
// barriers and the Control-to-Tea forwarder must not batch or drop them.
func TestConcurrentSpawnForwarderPreservesEveryToolEnvelope(t *testing.T) {
	t.Parallel()

	spawned := faultShapeConcurrentSpawnTasks()
	events := make(chan eventstream.Envelope, len(spawned)*2)
	for index, task := range spawned {
		events <- concurrentSpawnStartEnvelope(index+1, task)
	}
	for index, task := range spawned {
		events <- concurrentSpawnResultEnvelope(index+4, task)
	}
	close(events)

	var sent []tea.Msg
	result := forwardTurnEventStream(context.Background(), &eventstreamIntegrationTurn{events: events}, &ProgramSender{
		Send: func(message tea.Msg) { sent = append(sent, message) },
	})
	if !result.queued {
		t.Fatalf("forward result = %#v, want queued terminal", result)
	}
	counts := make(map[string]int, len(spawned))
	for _, message := range sent {
		envelope, ok := message.(eventstream.Envelope)
		if !ok || envelope.Kind != eventstream.KindSessionUpdate {
			continue
		}
		switch update := envelope.Update.(type) {
		case schema.ToolCall:
			counts[update.ToolCallID]++
		case schema.ToolCallUpdate:
			counts[update.ToolCallID]++
		}
	}
	for _, task := range spawned {
		if counts[task.callID] != 2 {
			t.Errorf("forwarded envelopes for %s = %d, want start and result; sent=%#v", task.handle, counts[task.callID], sent)
		}
	}
}

func faultShapeConcurrentSpawnTasks() []concurrentSpawnTask {
	return []concurrentSpawnTask{
		{
			callID: "call_249af8582bac4cc2b22b4f51",
			handle: "xavi",
			prompt: "List all files in the current workspace directory (not recursively). Just return the filenames and types. Keep it brief.",
		},
		{
			callID: "call_c573fdd4119743d2bea72423",
			handle: "reya",
			prompt: "Check the current OS, shell, and available system info by running `uname -a` and `whoami`. Return the results concisely.",
		},
		{
			callID: "call_bb4ee0a7fb89445e9c8fdb41",
			handle: "nora",
			prompt: "Calculate the first 20 numbers of the Fibonacci sequence and return them as a comma-separated list. No code needed, just the result.",
		},
	}
}

func concurrentSpawnStartEnvelope(sequence int, task concurrentSpawnTask) eventstream.Envelope {
	return concurrentSpawnEnvelope(sequence, schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall,
		ToolCallID:    task.callID,
		Title:         "Spawn breeze: " + task.prompt,
		Kind:          schema.ToolKindExecute,
		Status:        schema.ToolStatusInProgress,
		RawInput:      map[string]any{"agent": "breeze", "prompt": task.prompt},
		Meta:          acpToolNameMeta("Spawn"),
	})
}

func concurrentSpawnResultEnvelope(sequence int, task concurrentSpawnTask) eventstream.Envelope {
	completed := schema.ToolStatusCompleted
	meta := metautil.WithRuntimeSection(acpToolNameMeta("Spawn"), metautil.RuntimeTask, map[string]any{
		"agent": "breeze", "handle": task.handle, "prompt": task.prompt, "target_kind": "subagent",
	})
	return concurrentSpawnEnvelope(sequence, schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    task.callID,
		Title:         stringPtr("Spawn breeze: " + task.prompt),
		Kind:          stringPtr(schema.ToolKindExecute),
		Status:        &completed,
		RawInput:      map[string]any{"agent": "breeze", "prompt": task.prompt},
		RawOutput: map[string]any{
			"handle": task.handle, "state": "running", "target_kind": "subagent",
			"parent_call": task.callID, "parent_tool": "Spawn",
		},
		Meta: meta,
	})
}

func concurrentSpawnEnvelope(sequence int, update schema.Update) eventstream.Envelope {
	eventID := "event-spawn-" + string(rune('0'+sequence))
	return eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1",
		HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
		Scope: eventstream.ScopeMain, ScopeID: "session-1",
		EventID: eventID, ProjectionID: eventstream.FormatProjectionID(eventID, 0),
		Update: update,
	}
}

func requireSpawnCallIDs(t *testing.T, model *Model, spawned []concurrentSpawnTask) {
	t.Helper()
	block := requireMainACPTurnBlockForTest(t, model)
	got := make(map[string]SubagentEvent)
	for _, event := range physicalTranscriptEventsForTest(block.Events) {
		if event.Name == surfaceToolSpawn {
			got[event.CallID] = event
		}
	}
	for _, task := range spawned {
		if _, ok := got[task.callID]; !ok {
			t.Errorf("reducer omitted canonical Spawn call %q; events = %#v", task.callID, block.Events)
		}
	}
	if len(got) != len(spawned) {
		t.Fatalf("reducer Spawn owners = %d, want %d; events = %#v", len(got), len(spawned), block.Events)
	}
}
