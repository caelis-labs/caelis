package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestMainProjectedTaskWaitMatchesNativeSubagentSemantics(t *testing.T) {
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

	if hint := model.buildHintText(); !strings.Contains(hint, "Waiting on subagent") {
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

func TestMainProjectedFailedWaitRepairsSpawnWithReason(t *testing.T) {
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
		strings.Contains(plain, "↗") ||
		strings.Contains(strings.ToLower(plain), "failed") ||
		strings.Contains(plain, "• Ran wait") {
		t.Fatalf("Side ACP failed Spawn transcript mismatch:\n%s", plain)
	}
	if !renderedRowsHaveClickToken(rows, subagentOutputOverlayClickToken("spawn-1")) {
		t.Fatalf("Side ACP failed Spawn lost overlay click token:\n%s", plain)
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

func TestParticipantSpawnRendersStandardFinalResultWithoutSubagentUI(t *testing.T) {
	t.Parallel()

	const (
		turnID = "participant-turn-1"
		callID = "participant-spawn-1"
	)
	pending := schema.ToolStatusPending
	spawnTitle := "Spawn orbit: inspect"
	spawnKind := schema.ToolKindExecute
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "Spawn",
	})
	for _, result := range []struct {
		name   string
		status string
		text   string
		err    bool
	}{
		{name: "completed", status: schema.ToolStatusCompleted, text: "nested final response"},
		{name: "failed", status: schema.ToolStatusFailed, text: "nested child failed", err: true},
	} {
		result := result
		for _, mode := range []string{"live", "replay"} {
			mode := mode
			t.Run(result.name+"/"+mode, func(t *testing.T) {
				status := result.status
				envelopes := []eventstream.Envelope{
					{
						Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: turnID,
						Scope: eventstream.ScopeParticipant, ScopeID: turnID, ParticipantID: "reviewer", Actor: "@reviewer",
						Update: schema.ToolCall{
							SessionUpdate: schema.UpdateToolCall, ToolCallID: callID, Title: spawnTitle,
							Kind: spawnKind, Status: pending,
							RawInput: map[string]any{"agent": "orbit", "prompt": "inspect"},
							Meta:     meta,
						},
					},
					{
						Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: turnID,
						Scope: eventstream.ScopeParticipant, ScopeID: turnID, ParticipantID: "reviewer", Actor: "@reviewer", Final: true,
						Update: schema.ToolCallUpdate{
							SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: callID, Title: &spawnTitle,
							Kind: &spawnKind, Status: &status,
							Content: []schema.ToolCallContent{{
								Type: "content", Content: schema.TextContent{Type: "text", Text: result.text},
							}},
						},
					},
				}

				model := NewModel(Config{NoColor: true, NoAnimation: true})
				switch mode {
				case "live":
					model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(420, 0))
					for _, envelope := range envelopes {
						model = applyACPEnvelopeForTest(t, model, envelope)
					}
				case "replay":
					next, _ := model.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: projectResumeReplayEvents(envelopes)})
					model = next.(*Model)
				}

				block := model.findParticipantTurnBlock(turnID)
				if block == nil {
					t.Fatal("participant turn block missing")
				}
				physical := physicalTranscriptEventsForTest(block.Events)
				if len(physical) != 1 || physical[0].CallID != callID || !physical[0].Done ||
					physical[0].Err != result.err || physical[0].Output != result.text {
					t.Fatalf("participant Spawn events = %#v, want one standard tool result err=%v", physical, result.err)
				}
				plain := joinRenderedPlain(block.Render(BlockRenderContext{
					Width: 96, TermWidth: 96,
					Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
				}))
				if !strings.Contains(plain, "• Ran orbit: inspect") || !strings.Contains(plain, result.text) ||
					strings.Contains(plain, "↗") {
					t.Fatalf("participant Spawn did not render as a standard tool panel:\n%s", plain)
				}
				if len(model.subagentOutputViews) != 0 || model.subagentRosterCount() != 0 {
					t.Fatalf("participant Spawn created subagent UI: views=%#v roster=%d", model.subagentOutputViews, model.subagentRosterCount())
				}
				if model.openSubagentOutputOverlay(block.BlockID(), callID) {
					t.Fatal("participant Spawn opened a subagent output overlay")
				}
			})
		}
	}
}

func TestParticipantSpawnToolPanelExpandsFullFinalResponse(t *testing.T) {
	t.Parallel()

	const (
		turnID = "participant-turn-long"
		callID = "participant-spawn-long"
	)
	pending := schema.ToolStatusPending
	completed := schema.ToolStatusCompleted
	spawnTitle := "Spawn orbit: read-only strict review"
	spawnKind := schema.ToolKindExecute
	finalResponse := strings.Join([]string{
		"review summary",
		"finding one",
		"finding two",
		"critical middle finding",
		"finding four",
		"finding five",
		"verification complete",
	}, "\n")
	envelopes := []eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: turnID,
			Scope: eventstream.ScopeParticipant, ScopeID: turnID, ParticipantID: "reviewer", Actor: "@reviewer",
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall, ToolCallID: callID, Title: spawnTitle,
				Kind: spawnKind, Status: pending,
				RawInput: map[string]any{"agent": "orbit", "prompt": "read-only strict review"},
				Meta: metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
					metautil.RuntimeToolName: "Spawn",
				}),
			},
		},
		{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: turnID,
			Scope: eventstream.ScopeParticipant, ScopeID: turnID, ParticipantID: "reviewer", Actor: "@reviewer", Final: true,
			Update: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: callID, Status: &completed,
				Content: []schema.ToolCallContent{{
					Type: "content", Content: schema.TextContent{Type: "text", Text: finalResponse},
				}},
			},
		},
	}

	for _, mode := range []string{"live", "replay"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			model := NewModel(Config{NoColor: true, NoAnimation: true})
			switch mode {
			case "live":
				model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(430, 0))
				for _, envelope := range envelopes {
					model = applyACPEnvelopeForTest(t, model, envelope)
				}
			case "replay":
				next, _ := model.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: projectResumeReplayEvents(envelopes)})
				model = next.(*Model)
			}

			block := model.findParticipantTurnBlock(turnID)
			if block == nil {
				t.Fatal("participant turn block missing")
			}
			physical := physicalTranscriptEventsForTest(block.Events)
			if len(physical) != 1 || !isSpawnToolEvent(physical[0]) || physical[0].Output != finalResponse {
				t.Fatalf("participant Spawn events = %#v, want one Spawn result with the complete FinalResponse", physical)
			}
			if !block.toolPanelExpanded(callID) {
				t.Fatal("participant Spawn panel did not default to its summary")
			}
			summary := joinRenderedPlain(block.Render(BlockRenderContext{
				Width: 96, TermWidth: 96,
				Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
			}))
			if !strings.Contains(summary, "review summary") || !strings.Contains(summary, "... +3 lines") {
				t.Fatalf("expanded participant Spawn did not show the FinalResponse summary:\n%s", summary)
			}
			if strings.Contains(summary, "critical middle finding") {
				t.Fatalf("summarized participant Spawn unexpectedly showed hidden middle output:\n%s", summary)
			}
			if !model.tryToggleACPToolPanelToken(block.BlockID(), acpToolPanelClickToken(callID)) {
				t.Fatal("participant Spawn panel click could not reveal full FinalResponse")
			}
			full := joinRenderedPlain(block.Render(BlockRenderContext{
				Width: 96, TermWidth: 96,
				Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
			}))
			if !strings.Contains(full, "critical middle finding") || strings.Contains(full, "... +3 lines") {
				t.Fatalf("fully expanded participant Spawn did not show the complete FinalResponse:\n%s", full)
			}
			if strings.Contains(full, "↗") || len(model.subagentOutputViews) != 0 || model.subagentRosterCount() != 0 {
				t.Fatalf("participant Spawn expansion created subagent UI:\n%s", full)
			}
		})
	}
}

func TestStandardSparseToolUpdateUsesSharedCanonicalTerminalFallback(t *testing.T) {
	t.Parallel()

	completed := schema.ToolStatusCompleted
	start := func(scope eventstream.Scope, scopeID, participantID, actor string) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
			Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall, ToolCallID: "command-1",
				Title: "RUN_COMMAND printf ok", Kind: schema.ToolKindExecute,
				Status:   schema.ToolStatusInProgress,
				RawInput: map[string]any{"command": "printf ok"},
				Meta:     acpToolNameMeta("RUN_COMMAND"),
			},
		}
	}
	finish := func(scope eventstream.Scope, scopeID, participantID, actor string) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
			Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
			Update: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1", Status: &completed,
				RawOutput: map[string]any{"formatted_output": "SHARED_TOOL_OUTPUT\n", "exit_code": 0},
				Meta:      metautil.WithTerminalOutput(nil, "command-1", "SHARED_TOOL_OUTPUT\n"),
			},
		}
	}

	t.Run("participant transcript", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model = applyACPEnvelopeForTest(t, model, start(eventstream.ScopeParticipant, "participant-turn-1", "reviewer", "@reviewer"))
		model = applyACPEnvelopeForTest(t, model, finish(eventstream.ScopeParticipant, "participant-turn-1", "reviewer", "@reviewer"))
		block := model.findParticipantTurnBlock("participant-turn-1")
		if block == nil {
			t.Fatal("participant turn block missing")
		}
		plain := joinRenderedPlain(block.Render(BlockRenderContext{
			Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
		}))
		if !strings.Contains(plain, "SHARED_TOOL_OUTPUT") || strings.Contains(plain, "tool update") {
			t.Fatalf("participant sparse tool update rendered incorrectly:\n%s", plain)
		}
	})

	t.Run("subagent overlay", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model.width, model.height = 96, 28
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-1", Title: "SPAWN zenith: inspect",
				Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
				RawInput: map[string]any{"agent": "zenith", "prompt": "inspect"}, Meta: acpToolNameMeta("SPAWN"),
			},
		})
		childStart := start(eventstream.ScopeSubagent, "task-1", "codex", "@zenith")
		childStart.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "SPAWN"}
		model = applyACPEnvelopeForTest(t, model, childStart)
		childFinish := finish(eventstream.ScopeSubagent, "task-1", "codex", "@zenith")
		childFinish.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "SPAWN"}
		model = applyACPEnvelopeForTest(t, model, childFinish)
		block := requireMainACPTurnBlockForTest(t, model)
		if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-1") {
			t.Fatal("subagent output overlay did not open")
		}
		overlay := subagentOutputOverlayPlain(model)
		if !strings.Contains(overlay, "SHARED_TOOL_OUTPUT") || strings.Contains(overlay, "tool update") {
			t.Fatalf("subagent sparse tool update rendered incorrectly:\n%s", overlay)
		}
	})
}
