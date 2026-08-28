package tuiapp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
	acpclient "github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
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
				if !strings.Contains(plain, "• Spawned orbit: inspect") || !strings.Contains(plain, result.text) ||
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

func TestCanonicalTerminalDeltaUsesSharedParticipantAndOverlaySemantics(t *testing.T) {
	t.Parallel()

	running := schema.ToolStatusInProgress
	completed := schema.ToolStatusCompleted
	narrative := func(scope eventstream.Scope, scopeID, participantID, actor, update, text string) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
			Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
			Update: schema.ContentChunk{
				SessionUpdate: update, MessageID: update + "-1",
				Content: schema.TextContent{Type: "text", Text: text},
			},
		}
	}
	start := func(scope eventstream.Scope, scopeID, participantID, actor string) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
			Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall, ToolCallID: "command-1",
				Title: "printf ok", Kind: schema.ToolKindExecute,
				Status:   schema.ToolStatusInProgress,
				RawInput: map[string]any{"command": "printf ok"},
				Content:  []schema.ToolCallContent{{Type: "terminal", TerminalID: "command-1"}},
				Meta:     metautil.WithTerminalInfo(nil, "command-1"),
			},
		}
	}
	delta := func(scope eventstream.Scope, scopeID, participantID, actor string) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
			Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
			Update: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1", Status: &running,
				Meta: metautil.WithTerminalOutput(nil, "command-1", "SHARED_TOOL_OUTPUT\n"),
			},
		}
	}
	finish := func(scope eventstream.Scope, scopeID, participantID, actor string) eventstream.Envelope {
		exitCode := 0
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
			Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
			Update: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1", Status: &completed,
				RawOutput: map[string]any{"formatted_output": "SHARED_TOOL_OUTPUT\n", "exit_code": 0},
				Meta:      metautil.WithTerminalExit(nil, "command-1", &exitCode, nil),
			},
		}
	}

	t.Run("participant transcript", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model = applyACPEnvelopeForTest(t, model, narrative(eventstream.ScopeParticipant, "participant-turn-1", "reviewer", "@reviewer", schema.UpdateAgentThought, "SHARED_REASONING"))
		model = applyACPEnvelopeForTest(t, model, start(eventstream.ScopeParticipant, "participant-turn-1", "reviewer", "@reviewer"))
		model = applyACPEnvelopeForTest(t, model, delta(eventstream.ScopeParticipant, "participant-turn-1", "reviewer", "@reviewer"))
		model = applyACPEnvelopeForTest(t, model, finish(eventstream.ScopeParticipant, "participant-turn-1", "reviewer", "@reviewer"))
		model = applyACPEnvelopeForTest(t, model, narrative(eventstream.ScopeParticipant, "participant-turn-1", "reviewer", "@reviewer", schema.UpdateAgentMessage, "SHARED_ASSISTANT"))
		block := model.findParticipantTurnBlock("participant-turn-1")
		if block == nil {
			t.Fatal("participant turn block missing")
		}
		plain := joinRenderedPlain(block.Render(BlockRenderContext{
			Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
		}))
		if strings.Count(plain, "SHARED_TOOL_OUTPUT") != 1 || !strings.Contains(plain, "SHARED_REASONING") ||
			!strings.Contains(plain, "SHARED_ASSISTANT") || strings.Contains(plain, "tool update") {
			t.Fatalf("participant sparse tool update rendered incorrectly:\n%s", plain)
		}
	})

	t.Run("subagent overlay", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model.width, model.height = 96, 28
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-1", Title: "Spawn zenith: inspect",
				Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
				RawInput: map[string]any{"agent": "zenith", "prompt": "inspect"}, Meta: acpToolNameMeta("Spawn"),
			},
		})
		childThought := narrative(eventstream.ScopeSubagent, "task-1", "codex", "@zenith", schema.UpdateAgentThought, "SHARED_REASONING")
		childThought.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
		model = applyACPEnvelopeForTest(t, model, childThought)
		childStart := start(eventstream.ScopeSubagent, "task-1", "codex", "@zenith")
		childStart.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
		model = applyACPEnvelopeForTest(t, model, childStart)
		childDelta := delta(eventstream.ScopeSubagent, "task-1", "codex", "@zenith")
		childDelta.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
		model = applyACPEnvelopeForTest(t, model, childDelta)
		childFinish := finish(eventstream.ScopeSubagent, "task-1", "codex", "@zenith")
		childFinish.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
		model = applyACPEnvelopeForTest(t, model, childFinish)
		childAnswer := narrative(eventstream.ScopeSubagent, "task-1", "codex", "@zenith", schema.UpdateAgentMessage, "SHARED_ASSISTANT")
		childAnswer.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
		model = applyACPEnvelopeForTest(t, model, childAnswer)
		block := requireMainACPTurnBlockForTest(t, model)
		if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-1") {
			t.Fatal("subagent output overlay did not open")
		}
		overlay := subagentOutputOverlayPlain(model)
		if strings.Count(overlay, "SHARED_TOOL_OUTPUT") != 1 || !strings.Contains(overlay, "SHARED_REASONING") ||
			!strings.Contains(overlay, "SHARED_ASSISTANT") || strings.Contains(overlay, "tool update") {
			t.Fatalf("subagent sparse tool update rendered incorrectly:\n%s", overlay)
		}
	})
}

func TestStandardACPWaitIsHiddenLikeTaskWaitAcrossParticipantAndOverlay(t *testing.T) {
	t.Parallel()

	completed := schema.ToolStatusCompleted
	narrative := func(scope eventstream.Scope, scopeID, participantID, actor string) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
			Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentThought, MessageID: "thought-1",
				Content: schema.TextContent{Type: "text", Text: "checking child agents"},
			},
		}
	}
	waitUpdates := func(scope eventstream.Scope, scopeID, participantID, actor string) []eventstream.Envelope {
		base := eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
			Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
		}
		start := base
		start.Update = schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "codex-wait-1",
			Title: "wait", Kind: schema.ToolKindOther, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"action": "wait", "target_kind": "subagent"},
		}
		finish := base
		finish.Update = schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "codex-wait-1", Status: &completed,
		}
		return []eventstream.Envelope{start, finish}
	}

	t.Run("participant transcript", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model = applyACPEnvelopeForTest(t, model, narrative(eventstream.ScopeParticipant, "participant-turn-1", "codex", "@reviewer"))
		for _, envelope := range waitUpdates(eventstream.ScopeParticipant, "participant-turn-1", "codex", "@reviewer") {
			model = applyACPEnvelopeForTest(t, model, envelope)
		}
		block := model.findParticipantTurnBlock("participant-turn-1")
		if block == nil {
			t.Fatal("participant block missing")
		}
		plain := joinRenderedPlain(block.Render(BlockRenderContext{
			Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
		}))
		if strings.Contains(strings.ToLower(plain), "wait") {
			t.Fatalf("participant transcript exposed collaboration wait row:\n%s", plain)
		}
	})

	t.Run("subagent overlay", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model.width, model.height = 96, 28
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-1", Title: "Spawn reviewer: inspect",
				Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
				RawInput: map[string]any{"agent": "reviewer", "prompt": "inspect"}, Meta: acpToolNameMeta("Spawn"),
			},
		})
		thought := narrative(eventstream.ScopeSubagent, "task-1", "codex", "@reviewer")
		thought.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
		model = applyACPEnvelopeForTest(t, model, thought)
		for _, envelope := range waitUpdates(eventstream.ScopeSubagent, "task-1", "codex", "@reviewer") {
			envelope.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
			model = applyACPEnvelopeForTest(t, model, envelope)
		}
		block := requireMainACPTurnBlockForTest(t, model)
		if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-1") {
			t.Fatal("subagent output overlay did not open")
		}
		overlay := subagentOutputOverlayPlain(model)
		if strings.Contains(strings.ToLower(overlay), "wait") {
			t.Fatalf("subagent overlay exposed collaboration wait row:\n%s", overlay)
		}
	})
}

func TestStandardACPToolPresentationSettlesAcrossParticipantAndOverlay(t *testing.T) {
	t.Parallel()

	toolUpdates := func(scope eventstream.Scope, scopeID, participantID, actor string) []eventstream.Envelope {
		completed := schema.ToolStatusCompleted
		inProgress := schema.ToolStatusInProgress
		failed := schema.ToolStatusFailed
		grokExecute := acpclient.NormalizeInboundUpdate(schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "execute-1",
			Title: "run_terminal_command", Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"command": "git status --short"},
			Meta: map[string]any{"x.ai/tool": map[string]any{
				"version": 1, "name": "run_terminal_command", "kind": "execute",
				"namespace": "grok_build", "label": "Run Command", "read_only": false,
			}},
		}).(schema.ToolCall)
		return []eventstream.Envelope{
			{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
				Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
				Update: schema.ToolCall{
					SessionUpdate: schema.UpdateToolCall, ToolCallID: "read-1",
					Title: "Read MEMORY.md", Kind: schema.ToolKindRead, Status: schema.ToolStatusInProgress,
					RawInput: map[string]any{"path": "MEMORY.md"},
				},
			},
			{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
				Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
				Update: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "read-1", Status: &completed,
				},
			},
			{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
				Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
				Update: schema.ToolCall{
					SessionUpdate: schema.UpdateToolCall, ToolCallID: "search-1",
					Title: "Search ToolCallStatus", Kind: schema.ToolKindSearch, Status: schema.ToolStatusCompleted,
					RawInput: map[string]any{"query": "ToolCallStatus"},
				},
			},
			{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
				Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
				Update: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "search-1", Status: &completed,
				},
			},
			{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
				Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
				Update: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "search-1", Status: &inProgress,
					RawOutput: map[string]any{"result": "stale running snapshot"},
				},
			},
			{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
				Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
				Update: schema.ToolCall{
					SessionUpdate: schema.UpdateToolCall, ToolCallID: "read-failed",
					Title: "Read missing.go", Kind: schema.ToolKindRead, Status: failed,
					RawInput: map[string]any{"path": "missing.go"},
					Content: []schema.ToolCallContent{{
						Type: "content", Content: schema.TextContent{Type: "text", Text: "missing file"},
					}},
				},
			},
			{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
				Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
				Update: grokExecute,
			},
			{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
				Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
				Update: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "execute-1", Status: &completed,
				},
			},
			{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
				Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
				Update: schema.ToolCall{
					SessionUpdate: schema.UpdateToolCall, ToolCallID: "subagent-1",
					Title: "Start subagent task_invocation_review", Kind: schema.ToolKindOther, Status: schema.ToolStatusInProgress,
					RawInput: map[string]any{"activityKind": "start", "agentPath": "task_invocation_review"},
				},
			},
			{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: scopeID,
				Scope: scope, ScopeID: scopeID, ParticipantID: participantID, Actor: actor,
				Update: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "subagent-1", Status: &completed,
				},
			},
		}
	}
	assertPresentation := func(t *testing.T, model *Model, block *ParticipantTurnBlock) {
		t.Helper()
		if block == nil {
			t.Fatal("participant transcript block missing")
		}
		tools := make([]SubagentEvent, 0, 4)
		for _, event := range block.Events {
			if event.Kind == SEToolCall {
				tools = append(tools, event)
			}
		}
		if len(tools) != 5 {
			t.Fatalf("tool events = %#v, want five standard ACP tools", tools)
		}
		failedReadFound := false
		executeFound := false
		for _, event := range tools {
			if event.Name != "" || !event.Done {
				t.Fatalf("tool event = %#v, want no forged exact name and settled lifecycle", event)
			}
			rows, _ := renderACPToolLifecycleRows(block.BlockID(), []SubagentEvent{event}, 0, 96, BlockRenderContext{
				Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
				AnimationsEnabled: true, SpinnerView: runningSpinnerFrames[len(runningSpinnerFrames)/2],
			}, acpTranscriptRenderOptions{ToolOutputPanels: true})
			if len(rows) == 0 || rows[0].acpHeaderMarkDim {
				t.Fatalf("settled tool rows = %#v, want a non-pulsing header", rows)
			}
			if event.CallID == "read-failed" {
				failedReadFound = event.Err && strings.Contains(event.Output, "missing file")
			}
			if event.CallID == "execute-1" {
				executeFound = event.ToolKind == schema.ToolKindExecute && event.Args == "git status --short"
			}
		}
		if !failedReadFound {
			t.Fatalf("failed Read lost its lifecycle error: %#v", tools)
		}
		if !executeFound {
			t.Fatalf("Grok execute lost its anonymous standard presentation: %#v", tools)
		}
		plain := joinRenderedPlain(block.Render(BlockRenderContext{
			Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
		}))
		if countExactTrimmedLine(plain, "• Explored") != 1 || !strings.Contains(plain, "Read MEMORY.md, missing.go failed") || !strings.Contains(plain, "Search ToolCallStatus") || !strings.Contains(plain, "Ran git status --short") || !strings.Contains(plain, "Start subagent task_invocation_review") {
			t.Fatalf("standard ACP presentation lost expected labels:\n%s", plain)
		}
		if strings.Contains(plain, "missing file") {
			t.Fatalf("failed exploration rendered a separate error detail:\n%s", plain)
		}
		if strings.Contains(plain, "• read") || strings.Contains(plain, "• search") || strings.Contains(plain, "Other Start") || strings.Contains(plain, "• Other") || strings.Contains(plain, "• Tool") {
			t.Fatalf("standard ACP presentation leaked kind-as-name compatibility rows:\n%s", plain)
		}
	}

	t.Run("participant transcript", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		for _, envelope := range toolUpdates(eventstream.ScopeParticipant, "participant-turn-1", "codex", "@reviewer") {
			model = applyACPEnvelopeForTest(t, model, envelope)
		}
		assertPresentation(t, model, model.findParticipantTurnBlock("participant-turn-1"))
	})

	t.Run("subagent overlay", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model.width, model.height = 96, 28
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-1", Title: "Spawn reviewer: inspect",
				Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
				RawInput: map[string]any{"agent": "reviewer", "prompt": "inspect"}, Meta: acpToolNameMeta("Spawn"),
			},
		})
		for _, envelope := range toolUpdates(eventstream.ScopeSubagent, "task-1", "codex", "@reviewer") {
			envelope.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
			model = applyACPEnvelopeForTest(t, model, envelope)
		}
		view := requireSubagentOutputViewForTest(t, model, "spawn-1")
		assertPresentation(t, model, view.block)
		mainBlock := requireMainACPTurnBlockForTest(t, model)
		if !model.openSubagentOutputOverlay(mainBlock.BlockID(), "spawn-1") {
			t.Fatal("subagent output overlay did not open")
		}
		overlay := subagentOutputOverlayPlain(model)
		if strings.Count(overlay, "• Explored") != 1 || !strings.Contains(overlay, "Read MEMORY.md, missing.go failed") || !strings.Contains(overlay, "Search ToolCallStatus") || !strings.Contains(overlay, "Ran git status --short") || !strings.Contains(overlay, "Start subagent task_invocation_review") || strings.Contains(overlay, "missing file") || strings.Contains(overlay, "Other Start") || strings.Contains(overlay, "• Tool") {
			t.Fatalf("overlay diverged from participant transcript semantics:\n%s", overlay)
		}
	})
}

func TestCapturedGrokBuildShellAndAnonymousFinalStreamRenderAcrossParticipantAndOverlay(t *testing.T) {
	t.Parallel()

	execute := schema.ToolKindExecute
	executeTitle := "Execute `printf 'SHELL_ACP_43\\n'`"
	completed := schema.ToolStatusCompleted
	updates := []schema.Update{
		acpclient.NormalizeInboundUpdate(schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "execute-1",
			Title: "run_terminal_command", Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"command": "printf 'SHELL_ACP_43\\n'"},
			Meta: map[string]any{"x.ai/tool": map[string]any{
				"version": 1, "name": "run_terminal_command", "kind": "execute",
				"namespace": "grok_build", "label": "Run Command", "read_only": false,
			}},
		}).(schema.ToolCall),
		acpclient.NormalizeInboundUpdate(schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "execute-1",
			Title: &executeTitle, Kind: &execute,
			RawInput: map[string]any{"variant": "Bash", "command": "printf 'SHELL_ACP_43\\n'"},
			Content: []schema.ToolCallContent{{
				Type: "content", Content: schema.TextContent{Type: "text", Text: "Execute the shell command"},
			}},
		}).(schema.ToolCallUpdate),
		acpclient.NormalizeInboundUpdate(schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "execute-1", Status: &completed,
			Content: []schema.ToolCallContent{{
				Type: "content", Content: schema.TextContent{Type: "text", Text: "SHELL_ACP_43\n"},
			}},
			RawOutput: map[string]any{
				"type": "Bash", "command": "printf 'SHELL_ACP_43\\n'",
				"output":            []any{float64(83), float64(72), float64(69), float64(76), float64(76), float64(95), float64(65), float64(67), float64(80), float64(95), float64(52), float64(51), float64(10)},
				"output_for_prompt": "exit: 0\nSHELL_ACP_43\n", "exit_code": float64(0),
			},
		}).(schema.ToolCallUpdate),
	}
	for _, chunk := range []string{"第一", "段", "完成", "。", "第二", "段", "完成", "。"} {
		updates = append(updates, schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: chunk},
		})
	}

	apply := func(t *testing.T, model *Model, scope eventstream.Scope, parent *eventstream.ParentToolRelation) *Model {
		t.Helper()
		for index, update := range updates {
			model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, EventID: fmt.Sprintf("captured-grok-%d", index),
				ProjectionID: fmt.Sprintf("captured-grok-%d:0", index),
				SessionID:    "session-1", TurnID: "grok-turn-1", Scope: scope, ScopeID: "grok-task-1",
				ParticipantID: "grok", Actor: "@grok", ParentTool: parent, Update: update,
			})
		}
		return model
	}
	assertTranscript := func(t *testing.T, model *Model, block *ParticipantTurnBlock) {
		t.Helper()
		if block == nil {
			t.Fatal("Grok transcript block missing")
		}
		var tools []SubagentEvent
		var answers []SubagentEvent
		for _, event := range block.Events {
			switch event.Kind {
			case SEToolCall:
				tools = append(tools, event)
			case SEAssistant:
				answers = append(answers, event)
			}
		}
		if len(tools) != 1 || tools[0].ToolKind != schema.ToolKindExecute || tools[0].Terminal || !isTerminalPanelToolEvent(tools[0]) || !tools[0].Done {
			t.Fatalf("Grok shell events = %#v, want terminal presentation without claiming terminal-byte ownership", tools)
		}
		if len(answers) != 1 || answers[0].Text != "第一段完成。第二段完成。" {
			t.Fatalf("Grok final response events = %#v, want one ordered anonymous assistant bucket", answers)
		}
		plain := joinRenderedPlain(block.Render(BlockRenderContext{
			Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
		}))
		if !strings.Contains(plain, "Ran printf") || !strings.Contains(plain, "SHELL_ACP_43") || strings.Contains(plain, "• Tool") {
			t.Fatalf("captured Grok shell degraded from standard execute presentation:\n%s", plain)
		}
		if strings.Count(plain, "第一段完成。第二段完成。") != 1 {
			t.Fatalf("captured Grok final response was split or reordered:\n%s", plain)
		}
	}

	t.Run("participant transcript", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model = apply(t, model, eventstream.ScopeParticipant, nil)
		assertTranscript(t, model, model.findParticipantTurnBlock("grok-turn-1"))
	})

	t.Run("subagent overlay", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model.width, model.height = 96, 28
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-1", Title: "Spawn grok: inspect shell",
				Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
				RawInput: map[string]any{"agent": "grok", "prompt": "inspect shell"}, Meta: acpToolNameMeta("Spawn"),
			},
		})
		parent := &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
		model = apply(t, model, eventstream.ScopeSubagent, parent)
		view := requireSubagentOutputViewForTest(t, model, "spawn-1")
		assertTranscript(t, model, view.block)
		mainBlock := requireMainACPTurnBlockForTest(t, model)
		if !model.openSubagentOutputOverlay(mainBlock.BlockID(), "spawn-1") {
			t.Fatal("subagent output overlay did not open")
		}
		overlay := subagentOutputOverlayPlain(model)
		if !strings.Contains(overlay, "Ran printf") || !strings.Contains(overlay, "SHELL_ACP_43") ||
			strings.Count(overlay, "第一段完成。第二段完成。") != 1 || strings.Contains(overlay, "• Tool") {
			t.Fatalf("subagent overlay diverged from captured Grok ACP semantics:\n%s", overlay)
		}
	})
}

func TestStandardExecuteContentPatchesReplaceWithoutClaimingTerminalBytes(t *testing.T) {
	t.Parallel()

	inProgress := schema.ToolStatusInProgress
	updates := []schema.Update{
		schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "execute-1",
			Title: "Execute code", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"command": "run phases"},
		},
		schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "execute-1", Status: &inProgress,
			Content: []schema.ToolCallContent{{
				Type: "content", Content: schema.TextContent{Type: "text", Text: "phase 1"},
			}},
		},
		schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "execute-1", Status: &inProgress,
			Content: []schema.ToolCallContent{{
				Type: "content", Content: schema.TextContent{Type: "text", Text: "phase 2"},
			}},
		},
	}
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	for index, update := range updates {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, EventID: fmt.Sprintf("execute-replace-%d", index),
			SessionID: "session-1", TurnID: "execute-turn-1", Scope: eventstream.ScopeParticipant,
			ScopeID: "participant-1", ParticipantID: "executor", Actor: "@executor", Update: update,
		})
	}
	block := model.findParticipantTurnBlock("execute-turn-1")
	if block == nil || len(block.Events) != 1 {
		t.Fatalf("execute transcript events = %#v, want one sparse-merged tool", block)
	}
	event := block.Events[0]
	if event.Kind != SEToolCall || event.Output != "phase 2" {
		t.Fatalf("execute output = %#v, want latest standard content collection", event)
	}
	if event.Terminal || !isTerminalPanelToolEvent(event) {
		t.Fatalf("execute terminal state = %#v, want terminal presentation without byte-stream ownership", event)
	}
}

func TestSideACPContentPresenceSurvivesSparseStatusAcrossParticipantAndSubagent(t *testing.T) {
	t.Parallel()

	inProgress := schema.ToolStatusInProgress
	completed := schema.ToolStatusCompleted
	updates := []schema.Update{
		schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "content-presence-1",
			Title: "Execute phases", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"command": "run phases"}, Meta: acpToolNameMeta("Shell"),
			Content: []schema.ToolCallContent{{
				Type: "content", Content: schema.TextContent{Type: "text", Text: "phase 1"},
			}},
		},
		schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "content-presence-1", Status: &inProgress,
		},
		schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "content-presence-1",
			Content: []schema.ToolCallContent{},
		},
		schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "content-presence-1", Status: &completed,
		},
		schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "content-presence-1", Status: &completed,
		},
	}
	run := func(t *testing.T, model *Model, scope eventstream.Scope, parent *eventstream.ParentToolRelation, current func(*Model) SubagentEvent) {
		t.Helper()
		for index, update := range updates {
			model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, EventID: fmt.Sprintf("content-presence-%d", index),
				SessionID: "session-1", TurnID: "content-presence-turn", Scope: scope,
				ScopeID: "content-presence-task", ParticipantID: "executor", Actor: "@executor",
				ParentTool: parent, Update: update,
			})
			event := current(model)
			switch index {
			case 0, 1:
				if event.Output != "phase 1" || !event.OutputCollection || event.Done {
					t.Fatalf("step %d event = %#v, want initial content collection preserved by sparse running status", index, event)
				}
			case 2:
				if event.Output != "" || !event.OutputCollection || event.Done {
					t.Fatalf("empty collection event = %#v, want explicit empty snapshot", event)
				}
			default:
				if event.Output != "" || !event.OutputCollection || !event.Done {
					t.Fatalf("final step %d event = %#v, want status-only final and duplicate final to retain empty snapshot", index, event)
				}
			}
		}
	}

	t.Run("participant", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		run(t, model, eventstream.ScopeParticipant, nil, func(model *Model) SubagentEvent {
			block := model.findParticipantTurnBlock("content-presence-turn")
			if block == nil || len(block.Events) != 1 {
				t.Fatalf("participant block = %#v, want one tool event", block)
			}
			return block.Events[0]
		})
	})

	t.Run("subagent", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-content-presence", Title: "Spawn executor",
				Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
				RawInput: map[string]any{"agent": "executor"}, Meta: acpToolNameMeta("Spawn"),
			},
		})
		parent := &eventstream.ParentToolRelation{ToolCallID: "spawn-content-presence", ToolName: "Spawn"}
		run(t, model, eventstream.ScopeSubagent, parent, func(model *Model) SubagentEvent {
			block := requireSubagentOutputViewForTest(t, model, "spawn-content-presence").block
			if len(block.Events) != 1 {
				t.Fatalf("subagent block = %#v, want one tool event", block.Events)
			}
			return block.Events[0]
		})
	})
}

func TestSideACPProjectedStandardExecuteContentReplacesEarlierTerminalBytesAcrossParticipantAndSubagent(t *testing.T) {
	t.Parallel()

	inProgress := schema.ToolStatusInProgress
	completed := schema.ToolStatusCompleted
	updates := []schema.Update{
		schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "execute-terminal-1",
			Title: "Execute phases", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"command": "run phases"},
		},
		schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "execute-terminal-1", Status: &inProgress,
			Meta: metautil.WithTerminalOutput(nil, "terminal-1", "terminal phase\n"),
		},
		schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "execute-terminal-1", Status: &inProgress,
			Content: []schema.ToolCallContent{{
				Type: "content", Content: schema.TextContent{Type: "text", Text: "collection phase"},
			}},
		},
		schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "execute-terminal-1", Status: &completed,
			Content: []schema.ToolCallContent{{
				Type: "content", Content: schema.TextContent{Type: "text", Text: "collection final"},
			}},
		},
	}
	apply := func(t *testing.T, model *Model, scope eventstream.Scope, parent *eventstream.ParentToolRelation) *Model {
		t.Helper()
		for index, update := range updates {
			model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, EventID: fmt.Sprintf("terminal-collection-%d", index),
				SessionID: "session-1", TurnID: "terminal-collection-turn", Scope: scope,
				ScopeID: "terminal-collection-task", ParticipantID: "executor", Actor: "@executor",
				ParentTool: parent, Update: update,
			})
		}
		return model
	}
	assertResult := func(t *testing.T, model *Model, block *ParticipantTurnBlock) {
		t.Helper()
		if block == nil || len(block.Events) != 1 {
			t.Fatalf("terminal/content events = %#v, want one merged tool", block)
		}
		event := block.Events[0]
		if !event.Done || !event.Terminal || event.OutputTerminal || !event.OutputCollection || event.Output != "collection final" {
			t.Fatalf("terminal/content event = %#v, want final collection to replace terminal bytes while retaining shell presentation", event)
		}
		plain := joinRenderedPlain(block.Render(BlockRenderContext{
			Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
		}))
		if !strings.Contains(plain, "collection final") || strings.Contains(plain, "terminal phase") {
			t.Fatalf("terminal/content presentation retained stale terminal bytes:\n%s", plain)
		}
	}

	t.Run("participant", func(t *testing.T) {
		model := apply(t, NewModel(Config{NoColor: true, NoAnimation: true}), eventstream.ScopeParticipant, nil)
		assertResult(t, model, model.findParticipantTurnBlock("terminal-collection-turn"))
	})

	t.Run("subagent", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-terminal-collection", Title: "Spawn executor",
				Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
				RawInput: map[string]any{"agent": "executor"}, Meta: acpToolNameMeta("Spawn"),
			},
		})
		parent := &eventstream.ParentToolRelation{ToolCallID: "spawn-terminal-collection", ToolName: "Spawn"}
		model = apply(t, model, eventstream.ScopeSubagent, parent)
		assertResult(t, model, requireSubagentOutputViewForTest(t, model, "spawn-terminal-collection").block)
	})
}

func TestMixedIdentityNarrativeRunsStayOrderedAcrossParticipantAndSubagent(t *testing.T) {
	t.Parallel()

	updates := []schema.Update{
		schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "匿名答复甲"},
		},
		schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentThought, MessageID: "reasoning-1",
			Content: schema.TextContent{Type: "text", Text: "具名推理中"},
		},
		schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "匿名答复乙"},
		},
	}
	apply := func(t *testing.T, model *Model, scope eventstream.Scope, parent *eventstream.ParentToolRelation) *Model {
		t.Helper()
		for index, update := range updates {
			model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, EventID: fmt.Sprintf("mixed-narrative-%d", index),
				SessionID: "session-1", TurnID: "mixed-turn-1", Scope: scope, ScopeID: "mixed-task-1",
				ParticipantID: "mixed", Actor: "@mixed", ParentTool: parent, Final: index == 1, Update: update,
			})
		}
		return model
	}
	assertOrder := func(t *testing.T, block *ParticipantTurnBlock) {
		t.Helper()
		if block == nil || len(block.Events) != 3 {
			t.Fatalf("mixed narrative events = %#v, want three ordered runs", block)
		}
		if block.Events[0].Kind != SEAssistant || block.Events[0].Text != "匿名答复甲" ||
			block.Events[1].Kind != SEReasoning || block.Events[1].Text != "具名推理中" ||
			block.Events[2].Kind != SEAssistant || block.Events[2].Text != "匿名答复乙" {
			t.Fatalf("mixed narrative events = %#v, want wire order preserved", block.Events)
		}
	}

	t.Run("participant transcript", func(t *testing.T) {
		model := apply(t, NewModel(Config{NoColor: true, NoAnimation: true}), eventstream.ScopeParticipant, nil)
		assertOrder(t, model.findParticipantTurnBlock("mixed-turn-1"))
	})

	t.Run("subagent transcript", func(t *testing.T) {
		model := NewModel(Config{NoColor: true, NoAnimation: true})
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-mixed", Title: "Spawn mixed",
				Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
				RawInput: map[string]any{"agent": "mixed"}, Meta: acpToolNameMeta("Spawn"),
			},
		})
		parent := &eventstream.ParentToolRelation{ToolCallID: "spawn-mixed", ToolName: "Spawn"}
		model = apply(t, model, eventstream.ScopeSubagent, parent)
		assertOrder(t, requireSubagentOutputViewForTest(t, model, "spawn-mixed").block)
	})
}
