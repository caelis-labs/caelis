package tuiapp

import (
	"testing"

	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestObservedSpawnResultUsesHandleWhenCallIDIsReusedAcrossTurns(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	alpha := NewMainACPTurnBlock("turn-alpha")
	alpha.UpdateToolWithMeta("spawn-1", "SPAWN", "alpha", "", false, false, ToolUpdateMeta{TaskHandle: "alpha"})
	appendObservedSpawnOwner(model, alpha, "spawn-1", "alpha")
	beta := NewMainACPTurnBlock("turn-beta")
	beta.UpdateToolWithMeta("spawn-1", "SPAWN", "beta", "", false, false, ToolUpdateMeta{TaskHandle: "beta"})
	appendObservedSpawnOwner(model, beta, "spawn-1", "beta")

	model.applyObservedSpawnResults([]acpprojector.SpawnTaskResult{{
		ParentCallID: "spawn-1",
		Status:       schema.ToolStatusCompleted,
		RawOutput: map[string]any{
			"handle": "alpha", "state": "completed", "final_message": "alpha final",
		},
	}})

	if event := alpha.Events[0]; !event.Done || event.Output != "alpha final" {
		t.Fatalf("alpha owner = %#v, want exact observed completion", event)
	}
	if event := beta.Events[0]; event.Done || event.Output != "" {
		t.Fatalf("beta owner = %#v, want reused call ID owner left open", event)
	}
}

func TestObservedSpawnResultBatchClosesReusedCallIDByHandle(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	alpha := NewMainACPTurnBlock("turn-alpha")
	alpha.UpdateToolWithMeta("spawn-1", "SPAWN", "alpha", "", false, false, ToolUpdateMeta{TaskHandle: "alpha"})
	appendObservedSpawnOwner(model, alpha, "spawn-1", "alpha")
	beta := NewMainACPTurnBlock("turn-beta")
	beta.UpdateToolWithMeta("spawn-1", "SPAWN", "beta", "", false, false, ToolUpdateMeta{TaskHandle: "beta"})
	appendObservedSpawnOwner(model, beta, "spawn-1", "beta")

	model.applyObservedSpawnResults([]acpprojector.SpawnTaskResult{
		{
			ParentCallID: "spawn-1", Status: schema.ToolStatusCompleted,
			RawOutput: map[string]any{
				"handle": "alpha", "state": "completed", "final_message": "alpha final",
			},
		},
		{
			ParentCallID: "spawn-1", Status: schema.ToolStatusFailed,
			RawOutput: map[string]any{
				"handle": "beta", "state": "failed", "error": "beta failed",
			},
		},
	})

	if event := alpha.Events[0]; !event.Done || event.Output != "alpha final" {
		t.Fatalf("alpha owner = %#v, want exact completion", event)
	}
	if event := beta.Events[0]; !event.Done || !event.Err {
		t.Fatalf("beta owner = %#v, want exact failure", event)
	}
}

func TestObservedSpawnResultFailsClosedOnHandleMismatch(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	block := NewMainACPTurnBlock("turn-1")
	block.UpdateToolWithMeta("spawn-1", "SPAWN", "beta", "", false, false, ToolUpdateMeta{TaskHandle: "beta"})
	appendObservedSpawnOwner(model, block, "spawn-1", "beta")

	model.applyObservedSpawnResults([]acpprojector.SpawnTaskResult{{
		ParentCallID: "spawn-1",
		Status:       schema.ToolStatusCompleted,
		RawOutput: map[string]any{
			"handle": "alpha", "state": "completed", "final_message": "stale alpha",
		},
	}})

	if event := block.Events[0]; event.Done || event.Output != "" {
		t.Fatalf("owner = %#v, want mismatched observation ignored", event)
	}
}

func TestObservedSpawnResultNeverOverridesCanonicalOrFirstFallbackFinal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		initialOutput string
		complete      func(*Model, *MainACPTurnBlock)
	}{
		{
			name:          "canonical parent result wins",
			initialOutput: "canonical final",
			complete: func(_ *Model, block *MainACPTurnBlock) {
				block.UpdateToolWithMeta("spawn-1", "SPAWN", "", "canonical final", true, false, ToolUpdateMeta{
					TaskHandle: "alpha", OutputAuthoritative: true,
				})
			},
		},
		{
			name:          "first fallback wins",
			initialOutput: "first fallback",
			complete: func(model *Model, _ *MainACPTurnBlock) {
				model.applyObservedSpawnResults([]acpprojector.SpawnTaskResult{{
					ParentCallID: "spawn-1", Status: schema.ToolStatusCompleted,
					RawOutput: map[string]any{
						"handle": "alpha", "state": "completed", "final_message": "first fallback",
					},
				}})
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			model := NewModel(Config{NoColor: true, NoAnimation: true})
			block := NewMainACPTurnBlock("turn-1")
			block.UpdateToolWithMeta("spawn-1", "SPAWN", "alpha", "", false, false, ToolUpdateMeta{TaskHandle: "alpha"})
			appendObservedSpawnOwner(model, block, "spawn-1", "alpha")
			test.complete(model, block)

			model.applyObservedSpawnResults([]acpprojector.SpawnTaskResult{{
				ParentCallID: "spawn-1", Status: schema.ToolStatusFailed,
				RawOutput: map[string]any{
					"handle": "alpha", "state": "failed", "final_message": "stale replacement",
				},
			}})

			event := block.Events[0]
			if !event.Done || event.Err || event.Output != test.initialOutput {
				t.Fatalf("owner = %#v, want first authoritative completion preserved", event)
			}
		})
	}
}

func TestObservedSpawnResultWithoutHandleRequiresUniqueOpenOwner(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	first := NewMainACPTurnBlock("turn-1")
	first.UpdateToolWithMeta("spawn-1", "SPAWN", "first", "", false, false, ToolUpdateMeta{})
	appendObservedSpawnOwner(model, first, "spawn-1", "")
	second := NewMainACPTurnBlock("turn-2")
	second.UpdateToolWithMeta("spawn-1", "SPAWN", "second", "", false, false, ToolUpdateMeta{})
	appendObservedSpawnOwner(model, second, "spawn-1", "")

	model.applyObservedSpawnResults([]acpprojector.SpawnTaskResult{{
		ParentCallID: "spawn-1", Status: schema.ToolStatusCompleted,
		RawOutput: map[string]any{"state": "completed", "final_message": "ambiguous"},
	}})

	if first.Events[0].Done || second.Events[0].Done {
		t.Fatalf("owners = %#v / %#v, want ambiguous handle-free fallback ignored", first.Events[0], second.Events[0])
	}
}

func TestObservedSpawnResultWithHandleFailsClosedAcrossEmptyAndMismatchedOwners(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	unknown := NewMainACPTurnBlock("turn-unknown")
	unknown.UpdateToolWithMeta("spawn-1", "SPAWN", "unknown", "", false, false, ToolUpdateMeta{})
	appendObservedSpawnOwner(model, unknown, "spawn-1", "")
	beta := NewMainACPTurnBlock("turn-beta")
	beta.UpdateToolWithMeta("spawn-1", "SPAWN", "beta", "", false, false, ToolUpdateMeta{TaskHandle: "beta"})
	appendObservedSpawnOwner(model, beta, "spawn-1", "beta")

	model.applyObservedSpawnResults([]acpprojector.SpawnTaskResult{{
		ParentCallID: "spawn-1", Status: schema.ToolStatusCompleted,
		RawOutput: map[string]any{
			"handle": "alpha", "state": "completed", "final_message": "stale alpha",
		},
	}})

	if unknown.Events[0].Done || beta.Events[0].Done {
		t.Fatalf("owners = %#v / %#v, want reused call ID without exact handle match ignored", unknown.Events[0], beta.Events[0])
	}
}

func TestObservedSpawnResultIgnoresClosedReusedCallOwnerDuringFallback(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	closed := NewMainACPTurnBlock("turn-closed")
	closed.UpdateToolWithMeta("spawn-1", "SPAWN", "closed", "", false, false, ToolUpdateMeta{TaskHandle: "beta"})
	appendObservedSpawnOwner(model, closed, "spawn-1", "beta")
	closed.UpdateToolWithMeta("spawn-1", "SPAWN", "", "beta done", true, false, ToolUpdateMeta{TaskHandle: "beta"})
	open := NewMainACPTurnBlock("turn-open")
	open.UpdateToolWithMeta("spawn-1", "SPAWN", "open", "", false, false, ToolUpdateMeta{})
	appendObservedSpawnOwner(model, open, "spawn-1", "")

	model.applyObservedSpawnResults([]acpprojector.SpawnTaskResult{{
		ParentCallID: "spawn-1", Status: schema.ToolStatusCompleted,
		RawOutput: map[string]any{
			"handle": "alpha", "state": "completed", "final_message": "alpha done",
		},
	}})

	if event := open.Events[0]; !event.Done || event.Output != "alpha done" {
		t.Fatalf("open owner = %#v, want unique open compatible owner closed", event)
	}
}

func TestObservedSpawnResultWithoutHandleClosesUniqueOpenOwner(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	block := NewMainACPTurnBlock("turn-1")
	block.UpdateToolWithMeta("spawn-1", "SPAWN", "only owner", "", false, false, ToolUpdateMeta{})
	appendObservedSpawnOwner(model, block, "spawn-1", "")

	model.applyObservedSpawnResults([]acpprojector.SpawnTaskResult{{
		ParentCallID: "spawn-1", Status: schema.ToolStatusCompleted,
		RawOutput: map[string]any{"state": "completed", "final_message": "unique final"},
	}})

	if event := block.Events[0]; !event.Done || event.Output != "unique final" {
		t.Fatalf("owner = %#v, want unique handle-free fallback applied", event)
	}
}

func TestObservedSpawnResultClosesBlockAndActivityThroughSameOwner(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.liveTurn.Active = true
	block := NewMainACPTurnBlock("turn-1")
	block.UpdateToolWithMeta("spawn-1", "SPAWN", "inspect", "", false, false, ToolUpdateMeta{TaskHandle: "alpha"})
	appendObservedSpawnOwner(model, block, "spawn-1", "alpha")
	model.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:           TranscriptEventTool,
		Scope:          ACPProjectionMain,
		TurnID:         "turn-1",
		ToolCallID:     "spawn-1",
		ToolName:       "SPAWN",
		ToolTaskHandle: "alpha",
	})

	model.applyObservedSpawnResults([]acpprojector.SpawnTaskResult{{
		ParentCallID: "spawn-1", Status: schema.ToolStatusCompleted,
		RawOutput: map[string]any{
			"handle": "alpha", "state": "completed", "final_message": "done",
		},
	}})

	if !block.Events[0].Done || block.Events[0].Output != "done" {
		t.Fatalf("owner block = %#v, want completed", block.Events[0])
	}
	if model.runningActivity.Phase != runningPhaseThinking {
		t.Fatalf("runningActivity = %#v, want the same owner activity completed", model.runningActivity)
	}
}

func appendObservedSpawnOwner(model *Model, block *MainACPTurnBlock, callID string, handle string) {
	model.doc.Append(block)
	model.observeToolPresentationOwner(block, TranscriptEvent{
		Kind:           TranscriptEventTool,
		Scope:          ACPProjectionMain,
		TurnID:         block.TurnKey,
		ToolCallID:     callID,
		ToolName:       "SPAWN",
		ToolTaskHandle: handle,
	})
}
