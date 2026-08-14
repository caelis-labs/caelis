package loader

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestSpawnReplayCanonicalParentResultWinsOverEarlierTaskWait(t *testing.T) {
	directFinal := &session.Event{
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{
			ID:     "spawn-alpha",
			Name:   "Spawn",
			Status: "completed",
			Output: map[string]any{
				"state": "completed", "target_kind": "subagent", "turn_id": "turn-1", "final_message": "canonical final",
			},
		},
	}
	replay := newSpawnReplayProjector([]*session.Event{directFinal})
	completed := acp.ToolStatusCompleted
	wait := eventstream.Envelope{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeMain,
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo,
			ToolCallID:    "wait-alpha",
			Status:        &completed,
			RawInput:      map[string]any{"action": "wait", "handle": "alpha"},
			RawOutput: map[string]any{
				"action": "wait",
				"tasks": []any{map[string]any{
					"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn", "turn_id": "turn-1",
					"state": "completed", "target_kind": "subagent", "final_message": "observer final",
				}},
			},
		},
	}
	if got := replay.observedParentCloses(wait, "session-1"); len(got) != 0 {
		t.Fatalf("observer closes = %#v, want none when a canonical parent result exists", got)
	}

	notification := replay.normalize(directFinal, acp.SessionNotification{
		SessionID: "session-1",
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo,
			ToolCallID:    "spawn-alpha",
			Status:        &completed,
			RawOutput:     directFinal.Tool.Output,
		},
	})
	update := notification.Update.(acp.ToolCallUpdate)
	if update.Meta != nil || len(update.Content) != 1 || update.Content[0].Type != "content" {
		t.Fatalf("canonical Spawn result = %#v, want standard content without terminal metadata", update)
	}
	text, ok := update.Content[0].Content.(acp.TextContent)
	if !ok || text.Text != "canonical final" {
		t.Fatalf("canonical Spawn content = %#v, want canonical final", update.Content)
	}

	wait.Update = acp.ToolCallUpdate{
		SessionUpdate: acp.UpdateToolCallInfo,
		ToolCallID:    "wait-alpha-2",
		Status:        &completed,
		RawInput:      map[string]any{"action": "wait", "handle": "alpha"},
		RawOutput: map[string]any{
			"action": "wait",
			"tasks": []any{map[string]any{
				"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn", "turn_id": "turn-2",
				"state": "completed", "target_kind": "subagent", "final_message": "second turn final",
			}},
		},
	}
	secondTurn := replay.observedParentCloses(wait, "session-1")
	if len(secondTurn) != 1 {
		t.Fatalf("second-Turn observer closes = %#v, want one result independent from authoritative turn-1", secondTurn)
	}
	secondUpdate := secondTurn[0].Update.(acp.ToolCallUpdate)
	if len(secondUpdate.Content) != 1 {
		t.Fatalf("second-Turn content = %#v, want one standard result", secondUpdate.Content)
	}
	secondText, ok := secondUpdate.Content[0].Content.(acp.TextContent)
	if !ok || secondText.Text != "second turn final" {
		t.Fatalf("second-Turn content = %#v, want second turn final", secondUpdate.Content)
	}
	if duplicate := replay.observedParentCloses(wait, "session-1"); len(duplicate) != 0 {
		t.Fatalf("repeated second-Turn observer closes = %#v, want suppressed", duplicate)
	}
}

func TestSpawnReplayLegacyAuthoritativeResultBindsOnlyCorrespondingKnownTurn(t *testing.T) {
	completed := acp.ToolStatusCompleted
	directFinal := &session.Event{
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{
			ID: "spawn-alpha", Name: "Spawn", Status: "completed",
			Output: map[string]any{
				"state": "completed", "target_kind": "subagent", "final_message": "legacy canonical final",
			},
		},
	}
	wait := func(turnID, text string) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeMain,
			Update: acp.ToolCallUpdate{
				SessionUpdate: acp.UpdateToolCallInfo, ToolCallID: "wait-alpha", Status: &completed,
				RawInput: map[string]any{"action": "wait", "handle": "alpha"},
				RawOutput: map[string]any{
					"action": "wait",
					"tasks": []any{map[string]any{
						"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn", "turn_id": turnID,
						"state": "completed", "target_kind": "subagent", "final_message": text,
					}},
				},
			},
		}
	}

	replay := newSpawnReplayProjector([]*session.Event{directFinal})
	firstTurn := wait("turn-1", "legacy canonical final")
	if got := replay.observedParentCloses(firstTurn, "session-1"); len(got) != 0 {
		t.Fatalf("known-Turn fallback for legacy authoritative result = %#v, want suppressed", got)
	}
	if duplicate := replay.observedParentCloses(firstTurn, "session-1"); len(duplicate) != 0 {
		t.Fatalf("repeated known-Turn fallback = %#v, want suppressed", duplicate)
	}

	notification := replay.normalize(directFinal, acp.SessionNotification{
		SessionID: "session-1",
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo, ToolCallID: "spawn-alpha", Status: &completed,
			RawOutput: directFinal.Tool.Output,
		},
	})
	update := notification.Update.(acp.ToolCallUpdate)
	if len(update.Content) != 1 {
		t.Fatalf("legacy authoritative result content = %#v, want one result", update.Content)
	}

	secondTurn := replay.observedParentCloses(wait("turn-2", "second turn final"), "session-1")
	if len(secondTurn) != 1 {
		t.Fatalf("later known-Turn fallback = %#v, want one independent result", secondTurn)
	}
	secondUpdate, ok := secondTurn[0].Update.(acp.ToolCallUpdate)
	if !ok || len(secondUpdate.Content) != 1 {
		t.Fatalf("later known-Turn update = %#v, want one content result", secondTurn[0])
	}
	text, ok := secondUpdate.Content[0].Content.(acp.TextContent)
	if !ok || text.Text != "second turn final" {
		t.Fatalf("later known-Turn content = %#v, want second turn final", secondUpdate.Content)
	}
}

func TestSpawnReplayUnmatchedLegacyAuthorityDoesNotHideLaterKnownTurn(t *testing.T) {
	completed := acp.ToolStatusCompleted
	directFinal := &session.Event{
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID: "spawn-alpha", Name: "Spawn", Status: "completed",
			Output: map[string]any{
				"state": "completed", "target_kind": "subagent", "final_message": "legacy first turn",
			},
		},
	}
	replay := newSpawnReplayProjector([]*session.Event{directFinal})
	replay.normalize(directFinal, acp.SessionNotification{
		SessionID: "session-1",
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo, ToolCallID: "spawn-alpha", Status: &completed,
			RawOutput: directFinal.Tool.Output,
		},
	})
	later := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeMain,
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo, ToolCallID: "wait-alpha", Status: &completed,
			RawInput: map[string]any{"action": "wait", "handle": "alpha"},
			RawOutput: map[string]any{
				"action": "wait",
				"tasks": []any{map[string]any{
					"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn", "turn_id": "turn-2",
					"state": "completed", "target_kind": "subagent", "final_message": "second turn final",
				}},
			},
		},
	}
	if got := replay.observedParentCloses(later, "session-1"); len(got) != 1 {
		t.Fatalf("later known-Turn fallback = %#v, want result when legacy authority had no matching fallback", got)
	}
}

func TestSpawnReplayLegacyUnknownFallbackConsumesOnlyOneLegacyAuthority(t *testing.T) {
	completed := acp.ToolStatusCompleted
	directFinal := &session.Event{
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID: "spawn-alpha", Name: "Spawn", Status: "completed",
			Output: map[string]any{
				"state": "completed", "target_kind": "subagent", "final_message": "legacy canonical final",
			},
		},
	}
	wait := func(turnID, text string) eventstream.Envelope {
		output := map[string]any{
			"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
			"state": "completed", "target_kind": "subagent", "final_message": text,
		}
		if turnID != "" {
			output["turn_id"] = turnID
		}
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeMain,
			Update: acp.ToolCallUpdate{
				SessionUpdate: acp.UpdateToolCallInfo, ToolCallID: "wait-alpha", Status: &completed,
				RawInput:  map[string]any{"action": "wait", "handle": "alpha"},
				RawOutput: map[string]any{"action": "wait", "tasks": []any{output}},
			},
		}
	}

	replay := newSpawnReplayProjector([]*session.Event{directFinal})
	if got := replay.observedParentCloses(wait("", "legacy observer final"), "session-1"); len(got) != 0 {
		t.Fatalf("legacy unknown-Turn fallback = %#v, want suppressed by the legacy authoritative result", got)
	}
	later := replay.observedParentCloses(wait("turn-2", "second turn final"), "session-1")
	if len(later) != 1 {
		t.Fatalf("later known-Turn fallback = %#v, want independent result after legacy unknown pairing", later)
	}
}

func TestSpawnReplayMissingTurnDoesNotSealLaterKnownTurn(t *testing.T) {
	completed := acp.ToolStatusCompleted
	wait := func(turnID, text string) eventstream.Envelope {
		taskOutput := map[string]any{
			"handle": "alpha", "parent_call": "spawn-alpha", "parent_tool": "Spawn",
			"state": "completed", "target_kind": "subagent", "final_message": text,
		}
		if turnID != "" {
			taskOutput["turn_id"] = turnID
		}
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeMain,
			Update: acp.ToolCallUpdate{
				SessionUpdate: acp.UpdateToolCallInfo, ToolCallID: "wait-alpha", Status: &completed,
				RawInput:  map[string]any{"action": "wait", "handle": "alpha"},
				RawOutput: map[string]any{"action": "wait", "tasks": []any{taskOutput}},
			},
		}
	}

	replay := newSpawnReplayProjector(nil)
	legacy := wait("", "legacy final")
	if got := replay.observedParentCloses(legacy, "session-1"); len(got) != 1 {
		t.Fatalf("legacy observer closes = %#v, want one result", got)
	}
	if duplicate := replay.observedParentCloses(legacy, "session-1"); len(duplicate) != 0 {
		t.Fatalf("repeated legacy observer closes = %#v, want suppressed", duplicate)
	}
	knownTurn := replay.observedParentCloses(wait("turn-2", "second turn final"), "session-1")
	if len(knownTurn) != 1 {
		t.Fatalf("known second-Turn observer closes = %#v, want independent result", knownTurn)
	}
}

func TestSpawnReplayDropsStaleSuccessWhenFailureHasNoReason(t *testing.T) {
	interrupted := "interrupted"
	update := withSpawnReplayResult(acp.ToolCallUpdate{
		SessionUpdate: acp.UpdateToolCallInfo,
		ToolCallID:    "spawn-alpha",
		Status:        &interrupted,
		RawOutput: map[string]any{
			"state": "cancelled", "turn_id": "turn-1", "final_message": "stale completed final",
		},
		Content: []acp.ToolCallContent{{
			Type: "content", Content: acp.TextContent{Type: "text", Text: "stale completed final"},
		}},
		Meta: metautil.WithTerminalOutput(nil, "spawn-alpha", "stale completed final"),
	}, map[string]any{
		"state": "cancelled", "turn_id": "turn-1", "final_message": "stale completed final",
	})
	if update.Status == nil || *update.Status != acp.ToolStatusFailed || len(update.Content) != 0 || update.Meta != nil {
		t.Fatalf("failed replay = %#v, want failed status without stale success output", update)
	}
}

func TestSpawnReplayNormalizesTerminalStatusToACPCompletedOrFailed(t *testing.T) {
	interrupted := "interrupted"
	event := &session.Event{
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID: "spawn-alpha", Name: "Spawn", Status: interrupted,
			Output: map[string]any{
				"state": "cancelled", "turn_id": "turn-1", "final_message": "stale completed final", "error": "cancelled by parent",
			},
		},
	}
	replay := newSpawnReplayProjector([]*session.Event{event})
	notification := replay.normalize(event, acp.SessionNotification{
		SessionID: "session-1",
		Update: acp.ToolCallUpdate{
			SessionUpdate: acp.UpdateToolCallInfo, ToolCallID: "spawn-alpha", Status: &interrupted,
			RawOutput: event.Tool.Output,
			Content: []acp.ToolCallContent{{
				Type: "content", Content: acp.TextContent{Type: "text", Text: "stale completed final"},
			}},
		},
	})
	update := notification.Update.(acp.ToolCallUpdate)
	if update.Status == nil || *update.Status != acp.ToolStatusFailed {
		t.Fatalf("replayed status = %#v, want ACP failed", update.Status)
	}
	if len(update.Content) != 1 {
		t.Fatalf("replayed failed content = %#v, want one standard result", update.Content)
	}
	text, ok := update.Content[0].Content.(acp.TextContent)
	if !ok || text.Text != "cancelled by parent" {
		t.Fatalf("replayed failed content = %#v, want cancellation reason", update.Content)
	}
}
