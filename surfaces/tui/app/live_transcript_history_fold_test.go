package tuiapp

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// liveTurnEnvelopes builds one live main Turn with a tool execution trace and
// an assistant answer, followed by its terminal lifecycle.
func liveTurnEnvelopes(sessionID, turnID, command, output, answer string, at time.Time) []eventstream.Envelope {
	callID := "call-" + turnID
	return []eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate, SessionID: sessionID, Scope: eventstream.ScopeMain, TurnID: turnID, OccurredAt: at, Final: true,
			Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateUserMessage, Content: eventstream.TextContent{Type: "text", Text: "prompt " + turnID}},
		},
		{
			Kind: eventstream.KindSessionUpdate, SessionID: sessionID, Scope: eventstream.ScopeMain, TurnID: turnID, OccurredAt: at.Add(time.Second),
			Update: eventstream.ToolCall{
				SessionUpdate: eventstream.UpdateToolCall, ToolCallID: callID, Title: command,
				Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusPending, RawInput: map[string]any{"command": command},
				Content: []eventstream.ToolCallContent{{Type: "terminal", TerminalID: callID}}, Meta: testMeta.WithTerminalInfo(acpToolNameMeta("RunCommand"), callID),
			},
		},
		{
			Kind: eventstream.KindSessionUpdate, SessionID: sessionID, Scope: eventstream.ScopeMain, TurnID: turnID, OccurredAt: at.Add(2 * time.Second), Final: true,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: callID, Title: stringPtr(command),
				Kind: stringPtr(eventstream.ToolKindExecute), Status: stringPtr(eventstream.ToolStatusCompleted), RawInput: map[string]any{"command": command},
				Content: []eventstream.ToolCallContent{{Type: "terminal", TerminalID: callID}}, Meta: testMeta.WithTerminalOutput(acpToolNameMeta("RunCommand"), callID, output),
			},
		},
		{
			Kind: eventstream.KindSessionUpdate, SessionID: sessionID, Scope: eventstream.ScopeMain, TurnID: turnID, OccurredAt: at.Add(3 * time.Second), Final: true,
			Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: answer}},
		},
		completedRegressionTurn(sessionID, turnID),
	}
}

func applyLiveEnvelopes(t *testing.T, model *Model, envelopes []eventstream.Envelope) *Model {
	t.Helper()
	for _, env := range envelopes {
		model = applyACPEnvelopeForTest(t, model, env)
	}
	model.drainPendingRenderEvents(time.Now())
	model.flushPendingViewportSync()
	return model
}

func mainACPTurnBlockForTest(t *testing.T, model *Model, turnID string) *MainACPTurnBlock {
	t.Helper()
	for _, block := range mainACPTurnBlocksForTest(model) {
		if block.TurnKey == turnID {
			return block
		}
	}
	t.Fatalf("main turn %q missing", turnID)
	return nil
}

func requireNoFoldedTurns(t *testing.T, model *Model, context string) {
	t.Helper()
	if compact := historicalTurnBlocks(model.doc.Blocks()); len(compact) != 0 {
		t.Fatalf("%s folded live turns: %#v", context, compact)
	}
}

// A second prompt in the same live interaction keeps the previous Turn's
// execution trace: folding is a history-restore policy, not a live-turn
// boundary effect.
func TestRegressionLiveSecondPromptKeepsFirstTurnTraceFrame(t *testing.T) {
	t.Parallel()

	const sessionID = "session-live-fold"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.Update(tea.WindowSizeMsg{Width: 120, Height: 32})

	started := time.Unix(100, 0)
	model = applyLiveEnvelopes(t, model, liveTurnEnvelopes(sessionID, "turn-1", "echo turn-1", "trace-turn-1\n", "answer turn-1", started))
	model = applyLiveEnvelopes(t, model, liveTurnEnvelopes(sessionID, "turn-2", "echo turn-2", "trace-turn-2\n", "answer turn-2", started.Add(time.Minute)))

	requireNoFoldedTurns(t, model, "second prompt")

	// Block-level source evidence: the first live Turn still carries its tool
	// event and renders its trace.
	first := mainACPTurnBlockForTest(t, model, "turn-1")
	if first.Historical {
		t.Fatal("live turn marked as restored history")
	}
	if !mainACPBlockHasToolOutput(first, "trace-turn-1") {
		t.Fatal("first live turn lost its tool source event")
	}
	if !strings.Contains(joinRenderedPlain(first.Render(model.blockRenderContext(96))), "trace-turn-1") {
		t.Fatal("first live turn no longer renders its execution trace")
	}

	// Golden frame: both live Turns keep prompt, trace and answer, in order.
	frame := model.View().Content
	assertFrameContainsInOrder(t, "live second prompt", frame, []string{
		"prompt turn-1", "trace-turn-1", "answer turn-1", "prompt turn-2", "trace-turn-2", "answer turn-2",
	})
	if !strings.Contains(strings.Join(model.viewportPlainLines, "\n"), "trace-turn-1") {
		t.Fatalf("folded transcript lost the first live turn trace:\n%s", strings.Join(model.viewportPlainLines, "\n"))
	}
	updates := renderFullscreenFramesForTest(t, model.width, model.height, frame)
	assertPhysicalFullscreenFrame(t, model.width, model.height, frame, updates)
}

// Every additional live prompt keeps earlier live Turns fully detailed.
func TestLiveThirdPromptKeepsFirstTurnTraceRendered(t *testing.T) {
	t.Parallel()

	const sessionID = "session-live-three"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.Update(tea.WindowSizeMsg{Width: 120, Height: 32})

	started := time.Unix(200, 0)
	var frames []string
	for index, turnID := range []string{"turn-1", "turn-2", "turn-3"} {
		at := started.Add(time.Duration(index) * time.Minute)
		model = applyLiveEnvelopes(t, model, liveTurnEnvelopes(sessionID, turnID, "echo "+turnID, "trace-"+turnID+"\n", "answer "+turnID, at))
		frames = append(frames, model.View().Content)
	}
	requireNoFoldedTurns(t, model, "third prompt")

	for _, turn := range []string{"turn-1", "turn-2", "turn-3"} {
		block := mainACPTurnBlockForTest(t, model, turn)
		if !mainACPBlockHasToolOutput(block, "trace-"+turn) {
			t.Fatalf("%s lost its tool output", turn)
		}
		if !strings.Contains(joinRenderedPlain(block.Render(model.blockRenderContext(96))), "trace-"+turn) {
			t.Fatalf("%s did not render its tool output", turn)
		}
	}
	frame := frames[len(frames)-1]
	assertFrameContainsInOrder(t, "live three prompts", frame, []string{"trace-turn-3", "answer turn-3"})
	updates := renderFullscreenFramesForTest(t, model.width, model.height, frames...)
	assertPhysicalFullscreenFrame(t, model.width, model.height, frame, updates)
}

// Sending the next prompt while the current Turn is still running splits the
// live timeline but must not fold the already-rendered trace.
func TestSteeringPromptKeepsRunningTurnTraceRendered(t *testing.T) {
	t.Parallel()

	const sessionID = "session-steer-fold"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.Update(tea.WindowSizeMsg{Width: 120, Height: 32})

	started := time.Unix(400, 0)
	model.beginLiveTurn(SubmissionModeDefault, true, started)
	model = applyLiveEnvelopes(t, model, liveTurnEnvelopes(sessionID, "turn-1", "echo turn-1", "trace-turn-1\n", "answer turn-1", started)[:4])

	model.handleUserMessageMsg(UserMessageMsg{Text: "steer prompt"})
	model = applyLiveEnvelopes(t, model, []eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate, SessionID: sessionID, Scope: eventstream.ScopeMain, TurnID: "turn-1", OccurredAt: started.Add(4 * time.Second), Final: true,
			Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "continued turn-1"}},
		},
		completedRegressionTurn(sessionID, "turn-1"),
	})
	model = applyLiveEnvelopes(t, model, liveTurnEnvelopes(sessionID, "turn-2", "echo turn-2", "trace-turn-2\n", "answer turn-2", started.Add(time.Minute)))

	requireNoFoldedTurns(t, model, "steering prompt")
	frame := model.View().Content
	assertFrameContainsInOrder(t, "steering prompt", frame, []string{
		"trace-turn-1", "answer turn-1", "continued turn-1", "trace-turn-2", "answer turn-2",
	})
}

// Restored history still folds beyond the newest two restored Turns, and a live
// prompt never consumes those detail slots.
func TestRestoredHistoryFoldsOlderTurnsAndKeepsLiveTurnDetailed(t *testing.T) {
	t.Parallel()

	const sessionID = "session-restore-fold"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.Update(tea.WindowSizeMsg{Width: 120, Height: 32})

	started := time.Unix(300, 0)
	var replay []TranscriptEvent
	for index, turnID := range []string{"turn-1", "turn-2", "turn-3", "turn-4"} {
		envelopes := liveTurnEnvelopes(sessionID, turnID, "echo "+turnID, "trace-"+turnID+"\n", "answer "+turnID, started.Add(time.Duration(index)*time.Minute))
		replay = append(replay, projectResumeReplayEvents(envelopes)...)
	}
	model.Update(TranscriptEventsMsg{Events: replay, ReconnectReplay: true})
	model.drainPendingRenderEvents(time.Now())
	model.flushPendingViewportSync()

	restored := mainACPTurnBlocksForTest(model)
	if len(restored) != 4 {
		t.Fatalf("restored turns = %d, want four", len(restored))
	}
	for _, block := range restored {
		if !block.Historical {
			t.Fatalf("restored turn %q lost its history provenance", block.TurnKey)
		}
	}
	compact := historicalTurnBlocks(model.doc.Blocks())
	for _, turnID := range []string{"turn-1", "turn-2"} {
		block := mainACPTurnBlockForTest(t, model, turnID)
		if !compact[block.BlockID()] {
			t.Fatalf("restored turn %q not folded: %#v", turnID, compact)
		}
		plain := joinRenderedPlain(renderHistoricalTurn(block, model.blockRenderContext(96)))
		if strings.Contains(plain, "trace-"+turnID) || !strings.Contains(plain, "answer "+turnID) {
			t.Fatalf("restored turn %q fold = %q, want answer without execution detail", turnID, plain)
		}
	}
	for _, turnID := range []string{"turn-3", "turn-4"} {
		if block := mainACPTurnBlockForTest(t, model, turnID); compact[block.BlockID()] {
			t.Fatalf("newest restored turn %q folded: %#v", turnID, compact)
		}
	}

	// The next prompt is live: restored detail must not shift, and the live turn
	// keeps its own trace.
	model = applyLiveEnvelopes(t, model, liveTurnEnvelopes(sessionID, "turn-5", "echo turn-5", "trace-turn-5\n", "answer turn-5", started.Add(10*time.Minute)))
	compact = historicalTurnBlocks(model.doc.Blocks())
	for _, turnID := range []string{"turn-1", "turn-2"} {
		if block := mainACPTurnBlockForTest(t, model, turnID); !compact[block.BlockID()] {
			t.Fatalf("restored turn %q stopped folding after a live prompt: %#v", turnID, compact)
		}
	}
	for _, turnID := range []string{"turn-3", "turn-4", "turn-5"} {
		if block := mainACPTurnBlockForTest(t, model, turnID); compact[block.BlockID()] {
			t.Fatalf("turn %q folded after a live prompt: %#v", turnID, compact)
		}
	}
	live := mainACPTurnBlockForTest(t, model, "turn-5")
	if live.Historical {
		t.Fatal("live turn marked as restored history")
	}
	if !mainACPBlockHasToolOutput(live, "trace-turn-5") {
		t.Fatal("live turn lost its tool source event")
	}
	if plain := strings.Join(model.viewportPlainLines, "\n"); !strings.Contains(plain, "trace-turn-5") {
		t.Fatalf("live turn trace missing from the rendered transcript:\n%s", plain)
	}
}

// With restored history present, sending the second prompt must not fold the
// turns the user was already reading.
func TestSecondPromptKeepsRestoredFirstTurnDetailed(t *testing.T) {
	t.Parallel()

	const sessionID = "session-restore-two"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.Update(tea.WindowSizeMsg{Width: 120, Height: 32})

	started := time.Unix(500, 0)
	var replay []TranscriptEvent
	for index, turnID := range []string{"turn-1", "turn-2"} {
		envelopes := liveTurnEnvelopes(sessionID, turnID, "echo "+turnID, "trace-"+turnID+"\n", "answer "+turnID, started.Add(time.Duration(index)*time.Minute))
		replay = append(replay, projectResumeReplayEvents(envelopes)...)
	}
	model.Update(TranscriptEventsMsg{Events: replay, ReconnectReplay: true})
	model.drainPendingRenderEvents(time.Now())
	model.flushPendingViewportSync()

	model = applyLiveEnvelopes(t, model, liveTurnEnvelopes(sessionID, "turn-3", "echo turn-3", "trace-turn-3\n", "answer turn-3", started.Add(5*time.Minute)))
	model = applyLiveEnvelopes(t, model, liveTurnEnvelopes(sessionID, "turn-4", "echo turn-4", "trace-turn-4\n", "answer turn-4", started.Add(6*time.Minute)))

	compact := historicalTurnBlocks(model.doc.Blocks())
	for _, turnID := range []string{"turn-1", "turn-2", "turn-3", "turn-4"} {
		block := mainACPTurnBlockForTest(t, model, turnID)
		if compact[block.BlockID()] {
			t.Fatalf("turn %q folded after the second prompt: %#v", turnID, compact)
		}
		if !mainACPBlockHasToolOutput(block, "trace-"+turnID) {
			t.Fatalf("turn %q lost its tool output", turnID)
		}
	}
}
