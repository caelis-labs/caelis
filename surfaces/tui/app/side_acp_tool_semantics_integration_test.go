package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
)

func TestSideACPProjectedTaskWaitMatchesNativeTranscriptSemantics(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(400, 0))
	project := func(event *session.Event) {
		t.Helper()
		base := acpprojector.EnvelopeBaseFromSessionEvent(
			session.SessionRef{SessionID: "session-1"},
			event,
			acpprojector.SessionEventTransport{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"},
		)
		envelopes := acpprojector.ProjectSessionEventEnvelope(base, event)
		if len(envelopes) != 1 {
			t.Fatalf("ProjectSessionEventEnvelope(%s) = %#v, want one ACP update", event.Type, envelopes)
		}
		model = applyACPEnvelopeForTest(t, model, envelopes[0])
	}

	project(&session.Event{
		ID:        "event-spawn",
		Seq:       1,
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Tool: &session.EventTool{
			ID:     "spawn-1",
			Name:   "Spawn",
			Kind:   "execute",
			Title:  "Spawn orbit: inspect",
			Status: "pending",
			Input:  map[string]any{"agent": "orbit", "prompt": "inspect"},
		},
		Meta: metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
			metautil.RuntimeToolName: "Spawn",
		}),
	})
	project(&session.Event{
		ID:        "event-spawn-running",
		Seq:       2,
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID:     "spawn-1",
			Name:   "Spawn",
			Kind:   "execute",
			Title:  "Spawn orbit: inspect",
			Status: "running",
			Input:  map[string]any{"agent": "orbit", "prompt": "inspect"},
			Output: map[string]any{"handle": "orbit", "state": "running"},
		},
	})
	project(&session.Event{
		ID:        "event-wait",
		Seq:       3,
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Tool: &session.EventTool{
			ID:     "task-wait-1",
			Name:   "Task",
			Kind:   "execute",
			Title:  "Task wait",
			Status: "pending",
			Input:  map[string]any{"action": "wait", "handle": "orbit"},
		},
		Meta: metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
			metautil.RuntimeToolName: "Task",
		}),
	})

	if hint := model.buildHintText(); !strings.Contains(hint, "Wait subagent") {
		t.Fatalf("Side ACP hint = %q, want native Task wait activity", hint)
	}
	blocks := mainACPTurnBlocksForTest(model)
	if len(blocks) != 1 {
		t.Fatalf("main blocks = %#v, want one live Turn", blocks)
	}
	physical := physicalTranscriptEventsForTest(blocks[0].Events)
	if len(physical) != 1 || physical[0].CallID != "spawn-1" {
		t.Fatalf("Side ACP physical events = %#v, want only the Spawn owner", physical)
	}
	rows := blocks[0].Render(BlockRenderContext{
		Width: 96, TermWidth: 96,
		Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
	})
	plain := joinRenderedPlain(rows)
	if !strings.Contains(plain, "• Spawned orbit") ||
		strings.Contains(plain, "• Ran orbit") ||
		strings.Contains(plain, "• Ran wait") {
		t.Fatalf("Side ACP transcript did not match native tool semantics:\n%s", plain)
	}
}

func TestSideACPProjectedFailedWaitRepairsSpawnWithReason(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(410, 0))
	project := func(event *session.Event) {
		t.Helper()
		base := acpprojector.EnvelopeBaseFromSessionEvent(
			session.SessionRef{SessionID: "session-1"},
			event,
			acpprojector.SessionEventTransport{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"},
		)
		for _, envelope := range acpprojector.ProjectSessionEventEnvelope(base, event) {
			model = applyACPEnvelopeForTest(t, model, envelope)
		}
	}

	project(&session.Event{
		ID:        "event-spawn",
		Seq:       1,
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Tool: &session.EventTool{
			ID:     "spawn-1",
			Name:   "Spawn",
			Kind:   "execute",
			Title:  "Spawn breeze: inspect",
			Status: "pending",
			Input:  map[string]any{"agent": "breeze", "prompt": "inspect"},
		},
	})
	project(&session.Event{
		ID:        "event-spawn-running",
		Seq:       2,
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID:     "spawn-1",
			Name:   "Spawn",
			Kind:   "execute",
			Title:  "Spawn breeze: inspect",
			Status: "running",
			Input:  map[string]any{"agent": "breeze", "prompt": "inspect"},
			Output: map[string]any{"handle": "breeze", "state": "running"},
		},
	})
	project(&session.Event{
		ID:        "event-wait",
		Seq:       3,
		SessionID: "session-1",
		Type:      session.EventTypeToolCall,
		Tool: &session.EventTool{
			ID:     "task-wait-1",
			Name:   "Task",
			Kind:   "execute",
			Title:  "Task wait",
			Status: "pending",
			Input:  map[string]any{"action": "wait", "handle": "breeze"},
		},
	})
	project(&session.Event{
		ID:        "event-wait-result",
		Seq:       4,
		SessionID: "session-1",
		Type:      session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID:     "task-wait-1",
			Name:   "Task",
			Kind:   "execute",
			Title:  "Task wait",
			Status: "completed",
			Input:  map[string]any{"action": "wait", "handle": "breeze"},
			Output: map[string]any{
				"handle": "breeze", "target_kind": "subagent", "state": "failed",
				"parent_call": "spawn-1", "parent_tool": "Spawn",
				"error": "ACP child prompt failed",
			},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	physical := physicalTranscriptEventsForTest(block.Events)
	if len(physical) != 1 || physical[0].CallID != "spawn-1" ||
		!physical[0].Done || !physical[0].Err ||
		!strings.Contains(physical[0].Output, "ACP child prompt failed") {
		t.Fatalf("Side ACP failed Spawn = %#v, want one failed owner with its reason", physical)
	}
	rows := block.Render(BlockRenderContext{
		Width: 96, TermWidth: 96,
		Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
	})
	plain := joinRenderedPlain(rows)
	if !strings.Contains(plain, "• Spawned breeze") ||
		!strings.Contains(plain, "↗") ||
		strings.Contains(strings.ToLower(plain), "failed") ||
		strings.Contains(plain, "• Ran wait") {
		t.Fatalf("Side ACP failed Spawn transcript mismatch:\n%s", plain)
	}
	model.width = 96
	model.height = 28
	if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-1") {
		t.Fatal("failed Side ACP Spawn did not open its output overlay")
	}
	if overlay := subagentOutputOverlayPlain(model); strings.Contains(overlay, "ACP child prompt failed") {
		t.Fatalf("parent Task failure was injected into child stream order:\n%s", overlay)
	}
}
