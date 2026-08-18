package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestObservedSpawnResultUsesHandleWhenCallIDIsReusedAcrossTurns(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	alpha := NewMainACPTurnBlock("turn-alpha")
	alpha.UpdateToolWithMeta("spawn-1", "Spawn", "alpha", "", false, false, ToolUpdateMeta{TaskHandle: "alpha"})
	appendObservedSpawnOwner(model, alpha, "spawn-1", "alpha")
	beta := NewMainACPTurnBlock("turn-beta")
	beta.UpdateToolWithMeta("spawn-1", "Spawn", "beta", "", false, false, ToolUpdateMeta{TaskHandle: "beta"})
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

func TestObservedSpawnResultDoesNotInjectParentTaskResultIntoOpenOutputOverlay(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 100
	model.height = 28
	block := NewMainACPTurnBlock("turn-1")
	block.UpdateToolWithMeta(
		"spawn-1",
		"Spawn",
		"reviewer: inspect",
		"",
		false,
		false,
		ToolUpdateMeta{TaskHandle: "alpha"},
	)
	appendObservedSpawnOwner(model, block, "spawn-1", "alpha")
	view := model.ensureSubagentOutputView("spawn-1")
	view.block.AppendStreamEvent(
		SEAssistant,
		"Partial child stream.",
		newNarrativeSourceIdentity("message-1", "", ""),
	)
	if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-1") {
		t.Fatal("openSubagentOutputOverlay() = false")
	}
	initialStatus := view.block.Status

	model.applyObservedSpawnResults([]acpprojector.SpawnTaskResult{{
		ParentCallID: "spawn-1",
		Status:       schema.ToolStatusCompleted,
		RawOutput: map[string]any{
			"handle":        "alpha",
			"state":         "completed",
			"final_message": "## Durable child final\n\n- complete",
		},
	}})

	if view.block.Status != initialStatus {
		t.Fatalf("parent Task result changed child transcript status to %q", view.block.Status)
	}
	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 20)), "\n")
	if !strings.Contains(plain, "Partial child stream.") {
		t.Fatalf("open overlay lost child stream after parent owner repair:\n%s", plain)
	}
	if strings.Contains(plain, "Durable child final") {
		t.Fatalf("parent Task result was injected into child stream order:\n%s", plain)
	}
}

func TestObservedSpawnResultBatchClosesReusedCallIDByHandle(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	alpha := NewMainACPTurnBlock("turn-alpha")
	alpha.UpdateToolWithMeta("spawn-1", "Spawn", "alpha", "", false, false, ToolUpdateMeta{TaskHandle: "alpha"})
	appendObservedSpawnOwner(model, alpha, "spawn-1", "alpha")
	beta := NewMainACPTurnBlock("turn-beta")
	beta.UpdateToolWithMeta("spawn-1", "Spawn", "beta", "", false, false, ToolUpdateMeta{TaskHandle: "beta"})
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
	block.UpdateToolWithMeta("spawn-1", "Spawn", "beta", "", false, false, ToolUpdateMeta{TaskHandle: "beta"})
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
				block.UpdateToolWithMeta("spawn-1", "Spawn", "", "canonical final", true, false, ToolUpdateMeta{
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
			block.UpdateToolWithMeta("spawn-1", "Spawn", "alpha", "", false, false, ToolUpdateMeta{TaskHandle: "alpha"})
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
	first.UpdateToolWithMeta("spawn-1", "Spawn", "first", "", false, false, ToolUpdateMeta{})
	appendObservedSpawnOwner(model, first, "spawn-1", "")
	second := NewMainACPTurnBlock("turn-2")
	second.UpdateToolWithMeta("spawn-1", "Spawn", "second", "", false, false, ToolUpdateMeta{})
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
	unknown.UpdateToolWithMeta("spawn-1", "Spawn", "unknown", "", false, false, ToolUpdateMeta{})
	appendObservedSpawnOwner(model, unknown, "spawn-1", "")
	beta := NewMainACPTurnBlock("turn-beta")
	beta.UpdateToolWithMeta("spawn-1", "Spawn", "beta", "", false, false, ToolUpdateMeta{TaskHandle: "beta"})
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
	closed.UpdateToolWithMeta("spawn-1", "Spawn", "closed", "", false, false, ToolUpdateMeta{TaskHandle: "beta"})
	appendObservedSpawnOwner(model, closed, "spawn-1", "beta")
	closed.UpdateToolWithMeta("spawn-1", "Spawn", "", "beta done", true, false, ToolUpdateMeta{TaskHandle: "beta"})
	open := NewMainACPTurnBlock("turn-open")
	open.UpdateToolWithMeta("spawn-1", "Spawn", "open", "", false, false, ToolUpdateMeta{})
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
	block.UpdateToolWithMeta("spawn-1", "Spawn", "only owner", "", false, false, ToolUpdateMeta{})
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
	model.currentSessionID = "session-1"
	model.liveTurn.Active = true
	block := NewMainACPTurnBlock("turn-1")
	block.UpdateToolWithMeta("spawn-1", "Spawn", "inspect", "", false, false, ToolUpdateMeta{TaskHandle: "alpha"})
	appendObservedSpawnOwner(model, block, "spawn-1", "alpha")
	model.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:           TranscriptEventTool,
		Scope:          ACPProjectionMain,
		TurnID:         "turn-1",
		ToolCallID:     "spawn-1",
		ToolName:       "Spawn",
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
	if model.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("runningActivity = %#v, want the same owner activity completed", model.runningActivity)
	}
}

func TestObservedSpawnResultDoesNotSplitActiveAssistantMessage(t *testing.T) {
	t.Parallel()

	const (
		messageID = "message-waiting-for-subagents"
		answer    = "子代理 **self**（handle: `siena`）和 **breeze**（handle: `eira`）已并发启动，正在同时等待两者返回……"
	)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	block := NewMainACPTurnBlock("turn-1")
	block.UpdateToolWithMeta(
		"spawn-1",
		"Spawn",
		"calculate",
		"",
		false,
		false,
		ToolUpdateMeta{TaskHandle: "eira"},
	)
	appendObservedSpawnOwner(model, block, "spawn-1", "eira")

	block.AppendStreamEvent(
		SEAssistant,
		"子代理 **self**（handle: `siena`）和 **breeze**（handle: `eira`",
		newNarrativeSourceIdentity(messageID, "chunk-1", "projection-1"),
	)
	model.applyObservedSpawnResults([]acpprojector.SpawnTaskResult{{
		ParentCallID: "spawn-1",
		Status:       schema.ToolStatusCompleted,
		RawOutput: map[string]any{
			"handle": "eira", "state": "completed", "final_message": "8",
		},
	}})
	block.AppendStreamEvent(
		SEAssistant,
		"）已并发启动，正在同时等待两者返回……",
		newNarrativeSourceIdentity(messageID, "chunk-2", "projection-2"),
	)
	block.ReplaceFinalStreamEvent(
		SEAssistant,
		answer,
		newNarrativeSourceIdentity(messageID, "canonical-final", "projection-final"),
	)

	var assistantEvents []SubagentEvent
	for _, event := range block.Events {
		if event.Kind == SEAssistant {
			assistantEvents = append(assistantEvents, event)
		}
	}
	if len(assistantEvents) != 1 || assistantEvents[0].Text != answer || assistantEvents[0].ActiveBuffer == nil || assistantEvents[0].ActiveBuffer.HasTail() {
		t.Fatalf("assistant events = %#v, want one finalized message after the observed Spawn completion", assistantEvents)
	}
	if event := block.Events[0]; !event.Done || event.Output != "8" {
		t.Fatalf("Spawn owner = %#v, want observed completion applied in place", event)
	}

	rows := block.Render(BlockRenderContext{
		Width: 180, TermWidth: 180,
		Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
	})
	var assistantRows []string
	for _, row := range renderedPlainRows(rows) {
		if strings.HasPrefix(strings.TrimSpace(row), "·") {
			assistantRows = append(assistantRows, strings.TrimSpace(row))
		}
	}
	const rendered = "· 子代理 self（handle: siena）和 breeze（handle: eira）已并发启动，正在同时等待两者返回……"
	if len(assistantRows) != 1 || assistantRows[0] != rendered {
		t.Fatalf("assistant rows = %#v, want one visible assistant row", assistantRows)
	}
}

func TestTerminalTaskReadRepairsSpawnBlockAndActivity(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	model.liveTurn.Active = true
	block := NewMainACPTurnBlock("turn-1")
	block.UpdateToolWithMeta("spawn-1", "Spawn", "inspect", "", false, false, ToolUpdateMeta{TaskHandle: "alpha"})
	appendObservedSpawnOwner(model, block, "spawn-1", "alpha")
	model.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:           TranscriptEventTool,
		Scope:          ACPProjectionMain,
		TurnID:         "turn-1",
		ToolCallID:     "spawn-1",
		ToolName:       "Spawn",
		ToolTaskHandle: "alpha",
	})

	completed := schema.ToolStatusCompleted
	taskMeta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName:     "Task",
		metautil.RuntimeToolAction:   "read",
		metautil.RuntimeTargetKind:   "subagent",
		metautil.RuntimeTargetHandle: "alpha",
	})
	envelope := eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "Spawn",
		},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "task-read-1",
			Status:        &completed,
			RawInput:      map[string]any{"action": "read", "handle": "alpha"},
			RawOutput: map[string]any{
				"handle": "alpha", "target_kind": "subagent", "state": "completed",
				"parent_call": "spawn-1", "parent_tool": "Spawn",
				"final_message": "## Exact child result\n\n- done",
			},
			Meta: taskMeta,
		},
	}
	repairs := acpprojector.TaskOwnerRepairsFromEnvelope(envelope)
	if len(repairs.Spawns) != 1 || repairs.Spawns[0].ParentCallID != "spawn-1" {
		t.Fatalf("Task owner repairs = %#v, want terminal Spawn read repair", repairs)
	}
	model = applyACPEnvelopeForTest(t, model, envelope)

	if event := block.Events[0]; !event.Done || event.Err || event.Output != "## Exact child result\n\n- done" {
		t.Fatalf("Spawn owner = %#v, want exact terminal Task read result", event)
	}
	if model.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("runningActivity = %#v, want repaired Spawn activity closed", model.runningActivity)
	}
}

func appendObservedSpawnOwner(model *Model, block *MainACPTurnBlock, callID string, handle string) {
	model.doc.Append(block)
	model.observeToolPresentationOwner(block, TranscriptEvent{
		Kind:           TranscriptEventTool,
		Scope:          ACPProjectionMain,
		TurnID:         block.TurnKey,
		ToolCallID:     callID,
		ToolName:       "Spawn",
		ToolTaskHandle: handle,
	})
}
