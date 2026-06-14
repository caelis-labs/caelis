package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestGatewayStreamingNarrativeKeepsReasoningAnswerBoundaries(t *testing.T) {
	model := newGatewayEventTestModel()

	send := func(payload *gateway.NarrativePayload) *Model {
		updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative:  payload,
			}}))

		model = updated.(*Model)
		return model
	}

	send(&gateway.NarrativePayload{
		Role:          gateway.NarrativeRoleAssistant,
		ReasoningText: "think-1 ",
		Final:         false,
		Scope:         gateway.EventScopeMain,
	})
	send(&gateway.NarrativePayload{
		Role:  gateway.NarrativeRoleAssistant,
		Text:  "answer-1 ",
		Final: false,
		Scope: gateway.EventScopeMain,
	})
	send(&gateway.NarrativePayload{
		Role:          gateway.NarrativeRoleAssistant,
		ReasoningText: "think-2 ",
		Final:         false,
		Scope:         gateway.EventScopeMain,
	})
	send(&gateway.NarrativePayload{
		Role:  gateway.NarrativeRoleAssistant,
		Text:  "answer-2",
		Final: false,
		Scope: gateway.EventScopeMain,
	})

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	if got := len(block.Events); got != 2 {
		t.Fatalf("len(block.Events) = %d, want 2 active narrative streams; got %#v", got, block.Events)
	}
	wantKinds := []SubagentEventKind{SEReasoning, SEAssistant}
	wantTexts := []string{"think-1 think-2 ", "answer-1 answer-2"}
	for i := range wantKinds {
		if block.Events[i].Kind != wantKinds[i] || block.Events[i].Text != wantTexts[i] {
			t.Fatalf("block.Events[%d] = %#v, want kind=%v text=%q", i, block.Events[i], wantKinds[i], wantTexts[i])
		}
	}
}

func TestGatewayParticipantStreamingChunksAppendInsteadOfReplace(t *testing.T) {
	model := newGatewayEventTestModel()

	send := func(text string) {
		updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Origin: &gateway.EventOrigin{
					Scope:         gateway.EventScopeParticipant,
					ScopeID:       "codex-001",
					Actor:         "codex-001",
					ParticipantID: "codex-001",
				},
				Narrative: &gateway.NarrativePayload{
					Role:  gateway.NarrativeRoleAssistant,
					Actor: "codex-001",
					Text:  text,
					Final: false,
					Scope: gateway.EventScopeParticipant,
				},
			}}))

		model = updated.(*Model)
	}

	send("上海今天")
	send("阴有小雨")
	send("。")

	block, ok := model.doc.Blocks()[0].(*ParticipantTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want ParticipantTurnBlock", model.doc.Blocks()[0])
	}
	if len(block.Events) != 1 || block.Events[0].Kind != SEAssistant {
		t.Fatalf("participant events = %#v, want one assistant stream", block.Events)
	}
	if got := block.Events[0].Text; got != "上海今天阴有小雨。" {
		t.Fatalf("participant assistant text = %q, want appended chunks", got)
	}
}

func TestGatewayParticipantFinalCumulativeMessagePreservesInterleavedTimeline(t *testing.T) {
	model := newGatewayEventTestModel()

	sendAssistant := func(text string, final bool) {
		updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Origin: &gateway.EventOrigin{
					Scope:         gateway.EventScopeParticipant,
					ScopeID:       "codex-turn-1",
					Actor:         "@codex",
					ParticipantID: "codex-001",
				},
				Narrative: &gateway.NarrativePayload{
					Role:  gateway.NarrativeRoleAssistant,
					Actor: "@codex",
					Text:  text,
					Final: final,
					Scope: gateway.EventScopeParticipant,
				},
			}}))

		model = updated.(*Model)
	}
	sendTool := func(kind gateway.EventKind, status gateway.ToolStatus) {
		event := gateway.Event{
			Kind:       kind,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin: &gateway.EventOrigin{
				Scope:         gateway.EventScopeParticipant,
				ScopeID:       "codex-turn-1",
				Actor:         "@codex",
				ParticipantID: "codex-001",
			},
		}
		if kind == gateway.EventKindToolCall {
			event.ToolCall = &gateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "READ",
				Status:   status,
				Scope:    gateway.EventScopeParticipant,
			}
		} else {
			event.ToolResult = &gateway.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "READ",
				Status:   status,
				Scope:    gateway.EventScopeParticipant,
			}
		}
		updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{Event: event}))
		model = updated.(*Model)
	}

	sendAssistant("I will inspect first.", false)
	sendTool(gateway.EventKindToolCall, gateway.ToolStatusRunning)
	sendTool(gateway.EventKindToolResult, gateway.ToolStatusCompleted)
	sendAssistant("The final answer is ready.", false)
	sendAssistant("I will inspect first.\n\nThe final answer is ready.", true)

	block, ok := model.doc.Blocks()[0].(*ParticipantTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want ParticipantTurnBlock", model.doc.Blocks()[0])
	}
	wantKinds := []SubagentEventKind{SEAssistant, SEToolCall, SEAssistant}
	if len(block.Events) != len(wantKinds) {
		t.Fatalf("participant events = %#v, want assistant/tool/assistant timeline", block.Events)
	}
	for i, kind := range wantKinds {
		if block.Events[i].Kind != kind {
			t.Fatalf("participant events[%d] = %#v, want kind %v", i, block.Events[i], kind)
		}
	}
	if block.Events[0].Text != "I will inspect first." {
		t.Fatalf("first assistant text = %q, want original first segment", block.Events[0].Text)
	}
	if !block.Events[1].Done {
		t.Fatalf("tool event = %#v, want completed tool preserved in place", block.Events[1])
	}
	if block.Events[2].Text != "The final answer is ready." {
		t.Fatalf("second assistant text = %q, want original second segment", block.Events[2].Text)
	}
}

func TestGatewayParticipantFinalMarkdownWhitespaceReplacesSingleLiveSegment(t *testing.T) {
	model := newGatewayEventTestModel()

	sendAssistant := func(text string, final bool) {
		updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Origin: &gateway.EventOrigin{
					Scope:         gateway.EventScopeParticipant,
					ScopeID:       "codex-turn-1",
					Actor:         "@codex",
					ParticipantID: "codex-001",
				},
				Narrative: &gateway.NarrativePayload{
					Role:  gateway.NarrativeRoleAssistant,
					Actor: "@codex",
					Text:  text,
					Final: final,
					Scope: gateway.EventScopeParticipant,
				},
			}}))

		model = updated.(*Model)
	}

	sendAssistant("- a - b", false)
	sendAssistant("- a\n- b", true)

	block, ok := model.doc.Blocks()[0].(*ParticipantTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want ParticipantTurnBlock", model.doc.Blocks()[0])
	}
	if len(block.Events) != 1 || block.Events[0].Kind != SEAssistant {
		t.Fatalf("participant events = %#v, want one assistant event", block.Events)
	}
	if block.Events[0].Text != "- a\n- b" {
		t.Fatalf("assistant final text = %q, want canonical Markdown line break", block.Events[0].Text)
	}
}

func TestGatewayParticipantPromptTurnsRenderAsSeparateBlocks(t *testing.T) {
	model := newGatewayEventTestModel()

	sendUser := func(text string) {
		updated, _ := model.Update(UserMessageMsg{Text: text})
		model = updated.(*Model)
	}
	sendParticipant := func(scopeID string, text string) {
		updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Origin: &gateway.EventOrigin{
					Scope:   gateway.EventScopeParticipant,
					ScopeID: scopeID,
					Actor:   "@kate",
				},
				Narrative: &gateway.NarrativePayload{
					Role:  gateway.NarrativeRoleAssistant,
					Actor: "codex-001",
					Text:  text,
					Final: false,
					Scope: gateway.EventScopeParticipant,
				},
			}}))

		model = updated.(*Model)
	}

	sendUser("/codex 查询一下上海今天的天气")
	sendParticipant("task-1:1", "first")
	sendUser("@kate 帮我清理一下/tmp目录")
	sendParticipant("task-1:2", "second")
	updated, _ := model.Update(TaskResultMsg{SuppressTurnDivider: true})
	model = updated.(*Model)

	blocks := model.doc.Blocks()
	var participantBlocks []*ParticipantTurnBlock
	var secondUserIndex = -1
	var secondTurnIndex = -1
	for i, block := range blocks {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.Contains(user.Raw, "@kate 帮我清理") {
			secondUserIndex = i
		}
		if transcript, ok := block.(*TranscriptBlock); ok && strings.Contains(transcript.Raw, "@kate 帮我清理") {
			secondUserIndex = i
		}
		if turn, ok := block.(*ParticipantTurnBlock); ok {
			participantBlocks = append(participantBlocks, turn)
			if turn.SessionID == "task-1:2" {
				secondTurnIndex = i
			}
		}
	}
	if len(participantBlocks) != 2 {
		t.Fatalf("participant blocks = %#v, want two prompt turns", participantBlocks)
	}
	firstTurn := participantBlocks[0]
	secondTurn := participantBlocks[1]
	if firstTurn.SessionID == secondTurn.SessionID {
		t.Fatalf("participant turn session ids both %q, want separate prompt scopes", firstTurn.SessionID)
	}
	if secondUserIndex < 0 || secondTurnIndex < 0 || secondTurnIndex <= secondUserIndex {
		t.Fatalf("second user index=%d second turn index=%d blocks=%#v", secondUserIndex, secondTurnIndex, blocks)
	}
	if firstTurn.Actor != "@kate" || secondTurn.Actor != "@kate" {
		t.Fatalf("actors = %q/%q, want @kate", firstTurn.Actor, secondTurn.Actor)
	}
	if got := secondTurn.Events[0].Text; got != "second" {
		t.Fatalf("second turn text = %q, want second", got)
	}
	if !participantTurnIsTerminal(secondTurn.Status) {
		t.Fatalf("second turn status = %q, want terminal after task result", secondTurn.Status)
	}
}

func TestGatewayParticipantUserMessageDoesNotDuplicateDisplayedPrompt(t *testing.T) {
	model := newGatewayEventTestModel()

	updated, _ := model.Update(UserMessageMsg{Text: "/claude 总结一下工作"})
	model = updated.(*Model)
	updated, _ = model.Update(gatewayEventMsg(gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindUserMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin: &gateway.EventOrigin{
				Scope:         gateway.EventScopeParticipant,
				ScopeID:       "participant-turn-1",
				ParticipantID: "participant-1",
				Actor:         "@jeff",
			},
			Narrative: &gateway.NarrativePayload{
				Role:  gateway.NarrativeRoleUser,
				Text:  "总结一下工作",
				Scope: gateway.EventScopeParticipant,
			},
		}}))

	model = updated.(*Model)

	var userLines []string
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok {
			userLines = append(userLines, "▌ "+user.Raw)
			continue
		}
		if transcript, ok := block.(*TranscriptBlock); ok && strings.HasPrefix(strings.TrimSpace(transcript.Raw), ">") {
			userLines = append(userLines, transcript.Raw)
		}
	}
	if len(userLines) != 1 || !strings.Contains(userLines[0], "/claude 总结一下工作") {
		t.Fatalf("user lines = %#v, want only displayed slash prompt", userLines)
	}
	if strings.Contains(strings.Join(userLines, "\n"), "▌ 总结一下工作") || strings.Contains(strings.Join(userLines, "\n"), "> 总结一下工作") {
		t.Fatalf("user lines = %#v, should not render participant prompt echo", userLines)
	}
}

func TestParticipantTurnCompletionDoesNotRenderTwoDurationDividers(t *testing.T) {
	model := NewModel(Config{NoColor: true})
	model.viewport.SetWidth(60)
	model.viewport.SetHeight(20)
	start := time.Now().Add(-2 * time.Minute)
	end := start.Add(45 * time.Second)
	block := NewParticipantTurnBlock("task-1:1", "@codex")
	block.StartedAt = start
	block.EndedAt = end
	block.Status = "completed"
	block.Events = append(block.Events, SubagentEvent{Kind: SEAssistant, Text: "side answer", Done: true})
	model.doc.Append(block)
	model.participantTurnIDs = map[string]string{block.SessionID: block.BlockID()}
	model.activeParticipantTurnSessionID = block.SessionID
	model.showTurnDivider = true
	model.runStartedAt = time.Now().Add(-75 * time.Second)

	updated, _ := model.Update(TaskResultMsg{})
	model = updated.(*Model)
	model.syncViewportContent()

	dividerCount := 0
	for _, line := range model.viewportPlainLines {
		if strings.Contains(line, "─") {
			dividerCount++
		}
	}
	if dividerCount != 1 {
		t.Fatalf("viewport lines = %#v, want one duration divider, got %d", model.viewportPlainLines, dividerCount)
	}
}

func TestEmptyTerminalParticipantTurnDoesNotRenderArrowOrZeroDurationFooter(t *testing.T) {
	model := NewModel(Config{NoColor: true})
	start := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	block := NewParticipantTurnBlock("participant-empty", "")
	block.StartedAt = start
	block.EndedAt = start
	block.Status = "completed"

	rows := block.Render(BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme})
	if len(rows) != 0 {
		t.Fatalf("rendered rows = %#v, want empty terminal participant turn hidden", renderedPlainRows(rows))
	}
}

func TestGatewayInterleavedStreamingFinalReplacesMatchingNarrativeOnly(t *testing.T) {
	model := newGatewayEventTestModel()

	send := func(payload *gateway.NarrativePayload) *Model {
		updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative:  payload,
			}}))

		model = updated.(*Model)
		return model
	}

	send(&gateway.NarrativePayload{
		Role:          gateway.NarrativeRoleAssistant,
		ReasoningText: "r1",
		Final:         false,
		Scope:         gateway.EventScopeMain,
	})
	send(&gateway.NarrativePayload{
		Role:  gateway.NarrativeRoleAssistant,
		Text:  "a1",
		Final: false,
		Scope: gateway.EventScopeMain,
	})
	send(&gateway.NarrativePayload{
		Role:          gateway.NarrativeRoleAssistant,
		ReasoningText: "r2-partial",
		Final:         false,
		Scope:         gateway.EventScopeMain,
	})
	send(&gateway.NarrativePayload{
		Role:  gateway.NarrativeRoleAssistant,
		Text:  "a2-partial",
		Final: false,
		Scope: gateway.EventScopeMain,
	})
	send(&gateway.NarrativePayload{
		Role:          gateway.NarrativeRoleAssistant,
		ReasoningText: "r2-final",
		Text:          "a2-final",
		Final:         true,
		Scope:         gateway.EventScopeMain,
	})

	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %#v, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	if got := len(block.Events); got != 2 {
		t.Fatalf("len(block.Events) = %d, want 2; got %#v", got, block.Events)
	}
	wantKinds := []SubagentEventKind{SEReasoning, SEAssistant}
	wantTexts := []string{"r2-final", "a2-final"}
	for i := range wantKinds {
		if block.Events[i].Kind != wantKinds[i] || block.Events[i].Text != wantTexts[i] {
			t.Fatalf("block.Events[%d] = %#v, want kind=%v text=%q", i, block.Events[i], wantKinds[i], wantTexts[i])
		}
	}
}

func TestGatewayAnchoredSubagentNarrativeRendersUnderSpawnTool(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "spawn-1",
				ToolName: "SPAWN",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "inspect"},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin: &gateway.EventOrigin{
				Scope:   gateway.EventScopeSubagent,
				ScopeID: "jack",
			},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"stream": map[string]any{
							"parent_call_id": "spawn-1",
							"parent_tool":    "SPAWN",
						},
					},
				},
			},
			Narrative: &gateway.NarrativePayload{
				Role: gateway.NarrativeRoleAssistant,
				Text: "child output",
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin: &gateway.EventOrigin{
				Scope:   gateway.EventScopeSubagent,
				ScopeID: "jack",
			},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"stream": map[string]any{
							"parent_call_id": "spawn-1",
							"parent_tool":    "SPAWN",
						},
					},
				},
			},
			Narrative: &gateway.NarrativePayload{
				Role:  gateway.NarrativeRoleAssistant,
				Text:  "final child answer",
				Final: true,
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "spawn-1",
				ToolName: "SPAWN",
				Status:   gateway.ToolStatusCompleted,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "inspect"},
				RawOutput: map[string]any{
					"state":   "completed",
					"task_id": "jack",
				},
			},
		}},
	} {
		updated, _ := model.Update(gatewayEventMsg(env))
		model = updated.(*Model)
	}
	if got := len(model.doc.Blocks()); got != 1 {
		t.Fatalf("doc blocks = %d, want only the main SPAWN turn: %#v", got, model.doc.Blocks())
	}
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %T, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	rows := block.Render(BlockRenderContext{Width: 120, TermWidth: 120, Theme: model.theme})
	joined := strings.Join(renderedPlainRows(rows), "\n")
	if !strings.Contains(joined, "• Spawned") || !strings.Contains(joined, "final child answer") {
		t.Fatalf("completed SPAWN tool missing final child answer:\n%s", joined)
	}
	if strings.Contains(joined, "child output") {
		t.Fatalf("completed SPAWN tool should show final answer only:\n%s", joined)
	}
}

func TestGatewayMirroredSubagentNarrativeDoesNotDuplicateSpawnPanelStream(t *testing.T) {
	model := newGatewayEventTestModel()
	callID := "spawn-dup"
	rawInput := map[string]any{"agent": "reviewer", "prompt": "review calculator"}
	send := func(env gateway.EventEnvelope) {
		updated, _ := model.Update(gatewayEventMsg(env))
		model = updated.(*Model)
	}
	send(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolCall,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		ToolCall: &gateway.ToolCallPayload{
			CallID:   callID,
			ToolName: "SPAWN",
			Status:   gateway.ToolStatusRunning,
			Scope:    gateway.EventScopeMain,
			RawInput: rawInput,
		},
	}})
	sendMirroredChild := func(text string) {
		send(gateway.EventEnvelope{Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin: &gateway.EventOrigin{
				Scope:   gateway.EventScopeSubagent,
				ScopeID: "reviewer",
				Actor:   "reviewer",
			},
			Meta: mirroredSpawnStreamMeta(callID),
			Narrative: &gateway.NarrativePayload{
				Role: gateway.NarrativeRoleAssistant,
				Text: text,
			},
		}})
	}
	sendParentToolUpdate := func(text string) {
		send(gateway.EventEnvelope{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:    callID,
				ToolName:  "SPAWN",
				Status:    gateway.ToolStatusRunning,
				Scope:     gateway.EventScopeMain,
				RawInput:  rawInput,
				RawOutput: map[string]any{"running": true, "state": "running", "task_id": "reviewer"},
				Content:   testTerminalContentWithID(text, "subagent-reviewer"),
			},
		}})
	}

	sendMirroredChild("### 10. ")
	sendParentToolUpdate("### 10. ")
	sendMirroredChild("`calculator.py` ")
	sendParentToolUpdate("`calculator.py` ")

	block := firstMainACPTurnBlock(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one SPAWN event", block.Events)
	}
	if got, want := block.Events[0].Output, "### 10. `calculator.py` "; got != want {
		t.Fatalf("SPAWN output = %q, want %q", got, want)
	}
	if strings.Count(block.Events[0].Output, "`calculator.py`") != 1 {
		t.Fatalf("SPAWN output duplicated child delta: %q", block.Events[0].Output)
	}
}

func TestGatewayMirroredSubagentFinalReplacesLiveSpawnPanelOnce(t *testing.T) {
	model := newGatewayEventTestModel()
	callID := "spawn-final-dup"
	rawInput := map[string]any{"agent": "reviewer", "prompt": "review calculator"}
	send := func(env gateway.EventEnvelope) {
		updated, _ := model.Update(gatewayEventMsg(env))
		model = updated.(*Model)
	}
	send(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolCall,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		ToolCall: &gateway.ToolCallPayload{
			CallID:   callID,
			ToolName: "SPAWN",
			Status:   gateway.ToolStatusRunning,
			Scope:    gateway.EventScopeMain,
			RawInput: rawInput,
		},
	}})
	send(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindAssistantMessage,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Origin: &gateway.EventOrigin{
			Scope:   gateway.EventScopeSubagent,
			ScopeID: "reviewer",
			Actor:   "reviewer",
		},
		Meta: mirroredSpawnStreamMeta(callID),
		Narrative: &gateway.NarrativePayload{
			Role: gateway.NarrativeRoleAssistant,
			Text: "live review line",
		},
	}})
	send(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolResult,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		ToolResult: &gateway.ToolResultPayload{
			CallID:    callID,
			ToolName:  "SPAWN",
			Status:    gateway.ToolStatusRunning,
			Scope:     gateway.EventScopeMain,
			RawInput:  rawInput,
			RawOutput: map[string]any{"running": true, "state": "running", "task_id": "reviewer"},
			Content:   testTerminalContentWithID("live review line", "subagent-reviewer"),
		},
	}})
	send(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolResult,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		ToolResult: &gateway.ToolResultPayload{
			CallID:    callID,
			ToolName:  "SPAWN",
			Status:    gateway.ToolStatusCompleted,
			Scope:     gateway.EventScopeMain,
			RawInput:  rawInput,
			RawOutput: map[string]any{"state": "completed", "task_id": "reviewer"},
			Content:   testToolContent("final child answer"),
		},
	}})

	block := firstMainACPTurnBlock(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one SPAWN event", block.Events)
	}
	if got := strings.Count(block.Events[0].Output, "final child answer"); got != 1 {
		t.Fatalf("SPAWN final output = %q, want final answer exactly once", block.Events[0].Output)
	}
	if strings.Contains(block.Events[0].Output, "live review linelive review line") {
		t.Fatalf("SPAWN final output retained duplicated live stream: %q", block.Events[0].Output)
	}
}

func TestGatewayAnchoredSubagentReasoningDoesNotAppendToSpawnTool(t *testing.T) {
	model := newGatewayEventTestModel()
	callID := "spawn-reasoning"
	send := func(env gateway.EventEnvelope) {
		updated, _ := model.Update(gatewayEventMsg(env))
		model = updated.(*Model)
	}
	send(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolCall,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		ToolCall: &gateway.ToolCallPayload{
			CallID:   callID,
			ToolName: "SPAWN",
			Status:   gateway.ToolStatusRunning,
			Scope:    gateway.EventScopeMain,
			RawInput: map[string]any{"agent": "reviewer", "prompt": "review"},
		},
	}})
	send(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindAssistantMessage,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Origin: &gateway.EventOrigin{
			Scope:   gateway.EventScopeSubagent,
			ScopeID: "reviewer",
			Actor:   "reviewer",
		},
		Meta: anchoredSpawnStreamMeta(callID),
		Narrative: &gateway.NarrativePayload{
			Role:          gateway.NarrativeRoleAssistant,
			ReasoningText: "private child thought",
		},
	}})

	block := firstMainACPTurnBlock(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one SPAWN event", block.Events)
	}
	if strings.Contains(block.Events[0].Output, "private child thought") {
		t.Fatalf("SPAWN output leaked child reasoning: %q", block.Events[0].Output)
	}
}

func firstMainACPTurnBlock(t *testing.T, model *Model) *MainACPTurnBlock {
	t.Helper()
	if model == nil || len(model.doc.Blocks()) == 0 {
		t.Fatalf("model doc has no blocks")
	}
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %T, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	return block
}

func anchoredSpawnStreamMeta(callID string) map[string]any {
	return map[string]any{
		"caelis": map[string]any{
			"runtime": map[string]any{
				"stream": map[string]any{
					"parent_call_id": callID,
					"parent_tool":    "SPAWN",
				},
			},
		},
	}
}

func mirroredSpawnStreamMeta(callID string) map[string]any {
	return map[string]any{
		"caelis": map[string]any{
			"runtime": map[string]any{
				"stream": map[string]any{
					"parent_call_id":          callID,
					"parent_tool":             "SPAWN",
					"mirrored_to_parent_tool": true,
				},
			},
		},
	}
}

func TestGatewaySpawnStatusOnlyFinalDoesNotCreateEmptySubagentPanel(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "spawn-status-only",
				ToolName: "SPAWN",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "inspect"},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Meta:       testRuntimeToolMeta(map[string]any{"target_id": "jack"}),
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "spawn-status-only",
				ToolName: "SPAWN",
				Status:   gateway.ToolStatusCompleted,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"agent": "self", "prompt": "inspect"},
				RawOutput: map[string]any{
					"state":   "completed",
					"task_id": "jack",
				},
			},
		}},
	} {
		updated, _ := model.Update(gatewayEventMsg(env))
		model = updated.(*Model)
	}
	for _, block := range model.doc.Blocks() {
		if participant, ok := block.(*ParticipantTurnBlock); ok {
			t.Fatalf("unexpected empty subagent participant turn: session=%q status=%q events=%#v", participant.SessionID, participant.Status, participant.Events)
		}
	}
}

func TestGatewayAnchoredSubagentApprovalRendersUnderSpawnToolUntilFinal(t *testing.T) {
	model := newGatewayEventTestModel()
	for _, env := range []gateway.EventEnvelope{
		{Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "spawn-approval",
				ToolName: "SPAWN",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
				RawInput: map[string]any{"agent": "claude", "prompt": "create hello_claude.txt"},
			},
		}},
		{Event: gateway.Event{
			Kind:       gateway.EventKindApprovalReview,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin: &gateway.EventOrigin{
				Scope:   gateway.EventScopeSubagent,
				ScopeID: "task-claude",
				Actor:   "claude",
			},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"stream": map[string]any{
							"parent_call_id": "spawn-approval",
							"parent_tool":    "SPAWN",
						},
					},
				},
			},
			ApprovalPayload: &gateway.ApprovalPayload{
				ToolCallID:     "perm-1",
				ToolName:       "custom_tool",
				RawInput:       map[string]any{"path": "hello_claude.txt"},
				ReviewStatus:   gateway.ApprovalReviewStatusApproved,
				ReviewText:     "Automatic approval review approved (risk: low, authorization: high): creating the requested file is narrow and authorized.",
				Risk:           "low",
				Authorization:  "high",
				DecisionSource: string(gateway.ApprovalModeAutoReview),
			},
		}},
	} {
		updated, _ := model.Update(gatewayEventMsg(env))
		model = updated.(*Model)
	}
	if got := len(model.doc.Blocks()); got != 1 {
		t.Fatalf("doc blocks = %d, want only the main SPAWN turn: %#v", got, model.doc.Blocks())
	}
	block, ok := model.doc.Blocks()[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("first block = %T, want MainACPTurnBlock", model.doc.Blocks()[0])
	}
	joined := strings.Join(renderedPlainRows(block.Render(BlockRenderContext{Width: 140, TermWidth: 140, Theme: model.theme})), "\n")
	for _, want := range []string{"Approval review approved custom_tool path: hello_claude.txt", "creating the requested file"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("SPAWN tool rows = %q, want approval tail %q", joined, want)
		}
	}

	updated, _ := model.Update(gatewayEventMsg(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindAssistantMessage,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Origin: &gateway.EventOrigin{
			Scope:   gateway.EventScopeSubagent,
			ScopeID: "task-claude",
			Actor:   "claude",
		},
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"stream": map[string]any{
						"parent_call_id": "spawn-approval",
						"parent_tool":    "SPAWN",
					},
				},
			},
		},
		Narrative: &gateway.NarrativePayload{
			Role:  gateway.NarrativeRoleAssistant,
			Text:  "created hello_claude.txt",
			Final: true,
		},
	}}))
	model = updated.(*Model)
	updated, _ = model.Update(gatewayEventMsg(gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindToolResult,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Meta:       testRuntimeToolMeta(map[string]any{"target_id": "task-claude"}),
		ToolResult: &gateway.ToolResultPayload{
			CallID:   "spawn-approval",
			ToolName: "SPAWN",
			Status:   gateway.ToolStatusCompleted,
			Scope:    gateway.EventScopeMain,
			RawInput: map[string]any{"agent": "claude", "prompt": "create hello_claude.txt"},
			RawOutput: map[string]any{
				"state":   "completed",
				"task_id": "task-claude",
			},
		},
	}}))

	model = updated.(*Model)
	joined = strings.Join(renderedPlainRows(block.Render(BlockRenderContext{Width: 140, TermWidth: 140, Theme: model.theme})), "\n")
	if !strings.Contains(joined, "created hello_claude.txt") {
		t.Fatalf("SPAWN tool rows = %q, want final SPAWN output", joined)
	}
	if strings.Contains(joined, "Approval review approved") || strings.Contains(joined, "creating the requested file") {
		t.Fatalf("SPAWN tool rows = %q, final SPAWN output should replace temporary approval tail", joined)
	}
}
