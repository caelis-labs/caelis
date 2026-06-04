package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	acpprojector "github.com/OnslaughtSnail/caelis/protocol/acp/projector"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestProgramSenderDropsAfterClose(t *testing.T) {
	sender := &ProgramSender{}
	var sent atomic.Int64
	sender.Send = func(tea.Msg) { sent.Add(1) }

	sender.Close()
	sender.SendMsg(LogChunkMsg{Chunk: "late\n"})

	if got := sent.Load(); got != 0 {
		t.Fatalf("sent after close = %d, want 0", got)
	}
	if got := sender.DroppedAfterClose(); got != 1 {
		t.Fatalf("DroppedAfterClose = %d, want 1", got)
	}
}

func requireACPEnvelope(t *testing.T, msg tea.Msg) eventstream.Envelope {
	t.Helper()
	env, ok := msg.(eventstream.Envelope)
	if !ok {
		t.Fatalf("msg = %#v, want eventstream.Envelope", msg)
	}
	return env
}

func requireACPText(t *testing.T, msg tea.Msg, updateType string) string {
	t.Helper()
	env := requireACPEnvelope(t, msg)
	if env.Kind != eventstream.KindSessionUpdate {
		t.Fatalf("kind = %q, want %q", env.Kind, eventstream.KindSessionUpdate)
	}
	chunk, ok := env.Update.(schema.ContentChunk)
	if !ok {
		t.Fatalf("update = %#v, want ContentChunk", env.Update)
	}
	if got := strings.TrimSpace(chunk.SessionUpdate); got != updateType {
		t.Fatalf("sessionUpdate = %q, want %q", got, updateType)
	}
	return protocolTextContent(chunk.Content)
}

func requireACPToolCall(t *testing.T, msg tea.Msg) schema.ToolCall {
	t.Helper()
	env := requireACPEnvelope(t, msg)
	if env.Kind != eventstream.KindSessionUpdate {
		t.Fatalf("kind = %q, want %q", env.Kind, eventstream.KindSessionUpdate)
	}
	call, ok := env.Update.(schema.ToolCall)
	if !ok {
		t.Fatalf("update = %#v, want ToolCall", env.Update)
	}
	return call
}

func transcriptEventsFromMsg(msg tea.Msg) []TranscriptEvent {
	switch typed := msg.(type) {
	case TranscriptEventsMsg:
		return typed.Events
	case eventstream.Envelope:
		return ProjectACPEventToTranscriptEvents(typed)
	case gateway.EventEnvelope:
		return ProjectGatewayEventToTranscriptEvents(typed.Event)
	default:
		return nil
	}
}

func acpUpdateTerminalText(update schema.Update) string {
	var content []schema.ToolCallContent
	switch typed := update.(type) {
	case schema.ToolCall:
		content = typed.Content
	case schema.ToolCallUpdate:
		content = typed.Content
	default:
		return ""
	}
	for _, item := range content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		if text := protocolTextContent(item.Content); text != "" {
			return text
		}
	}
	return ""
}

func projectKernelTestEvents(events []gateway.EventEnvelope) []eventstream.Envelope {
	out := make([]eventstream.Envelope, 0, len(events))
	for _, env := range events {
		out = append(out, acpprojector.ProjectGatewayEventEnvelope(env)...)
	}
	return out
}

func requireProjectedACPEvent(t *testing.T, env gateway.EventEnvelope) eventstream.Envelope {
	t.Helper()
	events := acpprojector.ProjectGatewayEventEnvelope(env)
	if len(events) == 0 {
		t.Fatalf("ProjectGatewayEventEnvelope(%#v) returned no events", env)
	}
	return events[0]
}

func TestProgramSenderCancelActiveRunsCancelsRunContext(t *testing.T) {
	sender := &ProgramSender{}
	ctx, finish := sender.beginRunContext(context.Background())
	defer finish()

	if err := ctx.Err(); err != nil {
		t.Fatalf("run context error before cancel = %v", err)
	}
	if !sender.CancelActiveRuns() {
		t.Fatal("CancelActiveRuns() = false, want true")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("run context was not canceled")
	}
	if sender.CancelActiveRuns() {
		t.Fatal("CancelActiveRuns() = true after clearing active runs, want false")
	}
}

func TestDiagnosticsReportsProgramSenderDropsAfterClose(t *testing.T) {
	sender := &ProgramSender{}
	sender.Close()
	sender.SendMsg(LogChunkMsg{Chunk: "late\n"})

	var observed Diagnostics
	m := NewModel(Config{
		ProgramSender: sender,
		OnDiagnostics: func(diag Diagnostics) {
			observed = diag
		},
	})
	m.observeRender(time.Millisecond, 1, "incremental")

	if got := observed.ProgramSendsAfterClose; got != 1 {
		t.Fatalf("ProgramSendsAfterClose = %d, want 1", got)
	}
}

func TestSlashNewClearsHistoryBeforeNotice(t *testing.T) {
	driver := &bridgeTestDriver{
		status:     control.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
		newSession: control.SessionSnapshot{SessionID: "new-session"},
	}
	var msgs []tea.Msg
	slashNew(driver, func(msg tea.Msg) { msgs = append(msgs, msg) })
	if len(msgs) < 2 {
		t.Fatalf("slashNew() emitted %d messages, want at least 2", len(msgs))
	}
	if _, ok := msgs[0].(ClearHistoryMsg); !ok {
		t.Fatalf("first msg = %#v, want ClearHistoryMsg", msgs[0])
	}
	if log, ok := msgs[1].(LogChunkMsg); !ok || !strings.Contains(log.Chunk, "new session") {
		t.Fatalf("second msg = %#v, want new session notice", msgs[1])
	}
}

func TestSlashHelpListsMinimalCoreCommands(t *testing.T) {
	var msgs []tea.Msg
	slashHelp(func(msg tea.Msg) { msgs = append(msgs, msg) })
	if len(msgs) != 1 {
		t.Fatalf("slashHelp() emitted %d messages, want 1", len(msgs))
	}
	log, ok := msgs[0].(LogChunkMsg)
	if !ok {
		t.Fatalf("slashHelp() msg = %#v, want LogChunkMsg", msgs[0])
	}
	for _, want := range []string{"/agent <action>", "actions: list, add <builtin>", "/connect", "/model <action>", "actions: use <alias>, del <alias>", "/compact", "/resume [session-id]", "Shortcuts", "Paste clipboard image"} {
		if !strings.Contains(log.Chunk, want) {
			t.Fatalf("slashHelp() chunk = %q, want substring %q", log.Chunk, want)
		}
	}
}

func TestModeToggleHintUsesRemoteACPModeLabel(t *testing.T) {
	got := modeToggleHint(control.StatusSnapshot{SessionMode: "review", ModeLabel: "Review"})
	if got != "Review mode enabled" {
		t.Fatalf("modeToggleHint() = %q, want Review mode enabled", got)
	}
}

func TestConfigFromControlServiceRefreshStatusUsesLightweightStatus(t *testing.T) {
	driver := &bridgeLightweightStatusDriver{
		bridgeTestDriver: bridgeTestDriver{
			status: control.StatusSnapshot{Model: "full-model", ModeLabel: "full-mode"},
		},
		lightweightStatus: control.StatusSnapshot{
			Model:               "light-model",
			ModeLabel:           "light-mode",
			TotalTokens:         12,
			ContextWindowTokens: 100,
		},
	}
	cfg := ConfigFromControlService(driver, nil, Config{})
	model, contextText := cfg.RefreshStatus()
	if model != "light-model" {
		t.Fatalf("RefreshStatus model = %q, want lightweight model", model)
	}
	if !strings.Contains(contextText, "12") {
		t.Fatalf("RefreshStatus context = %q, want lightweight token usage", contextText)
	}
	if driver.statusCalls != 0 {
		t.Fatalf("Status calls = %d, want 0", driver.statusCalls)
	}
	if driver.lightweightStatusCalls != 1 {
		t.Fatalf("LightweightStatus calls = %d, want 1", driver.lightweightStatusCalls)
	}
}

func TestGatewayTerminalBatcherMergesRunningFrames(t *testing.T) {
	var sent []tea.Msg
	send := func(msg tea.Msg) {
		sent = append(sent, msg)
	}
	var batcher eventStreamTerminalBatcher

	if !batcher.enqueue(requireProjectedACPEvent(t, testTerminalFrame("hello ", 1)), send) {
		t.Fatal("first running frame was not accepted for batching")
	}
	if !batcher.enqueue(requireProjectedACPEvent(t, testTerminalFrame("world", 2)), send) {
		t.Fatal("second running frame was not accepted for batching")
	}
	if len(sent) != 0 {
		t.Fatalf("batcher sent before flush: got %d messages", len(sent))
	}

	batcher.flush(send)
	if len(sent) != 1 {
		t.Fatalf("flush sent %d messages, want 1", len(sent))
	}
	env, ok := sent[0].(eventstream.Envelope)
	if !ok {
		t.Fatalf("sent msg = %#v, want EventEnvelope", sent[0])
	}
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %#v, want ToolCallUpdate", env.Update)
	}
	if got, _ := acpTerminalContent(update); got != "hello world" {
		t.Fatalf("merged text = %q, want hello world", got)
	}
	if rawOutput := acpRawMap(update.RawOutput); len(rawOutput) != 0 {
		t.Fatalf("raw output = %#v, want terminal content only", rawOutput)
	}
}

func TestGatewayTerminalBatcherMergesCumulativeRunningFrames(t *testing.T) {
	var sent []tea.Msg
	send := func(msg tea.Msg) {
		sent = append(sent, msg)
	}
	var batcher eventStreamTerminalBatcher

	if !batcher.enqueue(requireProjectedACPEvent(t, testTerminalFrameForTool("SPAWN", "Let me write the script.", 1)), send) {
		t.Fatal("first running frame was not accepted for batching")
	}
	if !batcher.enqueue(requireProjectedACPEvent(t, testTerminalFrameForTool("SPAWN", "Let me write the script. Now let me run the script.", 2)), send) {
		t.Fatal("second running frame was not accepted for batching")
	}

	batcher.flush(send)
	if len(sent) != 1 {
		t.Fatalf("flush sent %d messages, want 1", len(sent))
	}
	env, ok := sent[0].(eventstream.Envelope)
	if !ok {
		t.Fatalf("sent msg = %#v, want EventEnvelope", sent[0])
	}
	want := "Let me write the script. Now let me run the script."
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %#v, want ToolCallUpdate", env.Update)
	}
	if got, _ := acpTerminalContent(update); got != want {
		t.Fatalf("merged text = %q, want %q", got, want)
	}
}

func TestGatewayTerminalBatcherPreservesCommandPrefixDeltas(t *testing.T) {
	var sent []tea.Msg
	send := func(msg tea.Msg) {
		sent = append(sent, msg)
	}
	var batcher eventStreamTerminalBatcher

	if !batcher.enqueue(requireProjectedACPEvent(t, testTerminalFrame("abc", 1)), send) {
		t.Fatal("first running frame was not accepted for batching")
	}
	if !batcher.enqueue(requireProjectedACPEvent(t, testTerminalFrame("abcdef", 2)), send) {
		t.Fatal("second running frame was not accepted for batching")
	}

	batcher.flush(send)
	if len(sent) != 1 {
		t.Fatalf("flush sent %d messages, want 1", len(sent))
	}
	env, ok := sent[0].(eventstream.Envelope)
	if !ok {
		t.Fatalf("sent msg = %#v, want EventEnvelope", sent[0])
	}
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("update = %#v, want ToolCallUpdate", env.Update)
	}
	if got, _ := acpTerminalContent(update); got != "abcabcdef" {
		t.Fatalf("merged RUN_COMMAND text = %q, want both byte deltas preserved", got)
	}
}

func TestGatewayNarrativeBatcherSyncsProtocolUpdateContent(t *testing.T) {
	var sent []tea.Msg
	send := func(msg tea.Msg) {
		sent = append(sent, msg)
	}
	var batcher eventStreamNarrativeBatcher

	if !batcher.enqueue(requireProjectedACPEvent(t, testNarrativeFrame("hello ")), send) {
		t.Fatal("first narrative frame was not accepted for batching")
	}
	if !batcher.enqueue(requireProjectedACPEvent(t, testNarrativeFrame("world")), send) {
		t.Fatal("second narrative frame was not accepted for batching")
	}

	batcher.flush(send)
	if len(sent) != 1 {
		t.Fatalf("flush sent %d messages, want 1", len(sent))
	}
	env, ok := sent[0].(eventstream.Envelope)
	if !ok {
		t.Fatalf("sent msg = %#v, want EventEnvelope", sent[0])
	}
	update, ok := env.Update.(schema.ContentChunk)
	if !ok {
		t.Fatalf("update = %#v, want ContentChunk", env.Update)
	}
	if got := protocolTextContent(update.Content); got != "hello world" {
		t.Fatalf("narrative text = %q, want merged text", got)
	}
	events := ProjectACPEventToTranscriptEvents(env)
	if len(events) != 1 || events[0].Text != "hello world" {
		t.Fatalf("projected events = %#v, want protocol-first merged text", events)
	}
}

func testTerminalFrame(text string, cursor int64) gateway.EventEnvelope {
	return testTerminalFrameForTool("RUN_COMMAND", text, cursor)
}

func testNarrativeFrame(text string) gateway.EventEnvelope {
	return gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &gateway.EventOrigin{Scope: gateway.EventScopeMain, ScopeID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:       gateway.NarrativeRoleAssistant,
				Text:       text,
				Visibility: string(session.VisibilityUIOnly),
				UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
				Scope:      gateway.EventScopeMain,
			},
			Protocol: &session.EventProtocol{
				UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
					Content:       session.ProtocolTextContent(text),
				},
			},
		},
	}
}

func testTerminalFrameForTool(toolName string, text string, cursor int64) gateway.EventEnvelope {
	_ = cursor
	return gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			HandleID:   "h1",
			RunID:      "r1",
			TurnID:     "t1",
			SessionRef: session.SessionRef{SessionID: "s1"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "call-1",
				ToolName: toolName,
				Status:   gateway.ToolStatusRunning,
				Content:  testTerminalContentWithID(text, "term-1"),
			},
		},
	}
}

func TestDefaultCommandsStayInHelpText(t *testing.T) {
	helpText := defaultHelpText()
	for _, command := range DefaultCommands() {
		if !strings.Contains(helpText, "/"+command) {
			t.Fatalf("defaultHelpText() = %q, want command /%s", helpText, command)
		}
	}
}

func TestDefaultCommandsAreRecognizedByDispatch(t *testing.T) {
	driver := &bridgeTestDriver{
		status:     control.StatusSnapshot{},
		newSession: control.SessionSnapshot{SessionID: "new-session"},
	}
	for _, command := range DefaultCommands() {
		var msgs []tea.Msg
		result := dispatchSlashCommand(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, "/"+command)
		if command == "exit" || command == "quit" {
			if !result.ExitNow {
				t.Fatalf("/%s did not request exit", command)
			}
			continue
		}
		for _, msg := range msgs {
			log, ok := msg.(LogChunkMsg)
			if ok && strings.Contains(log.Chunk, "unknown command") {
				t.Fatalf("/%s was treated as unknown: %q", command, log.Chunk)
			}
		}
	}
}

func TestSlashResumeClearsHistoryBeforeReplay(t *testing.T) {
	driver := &bridgeTestDriver{
		status:         control.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
		resumedSession: control.SessionSnapshot{SessionID: "resumed-session"},
		replay: []gateway.EventEnvelope{
			{
				Event: gateway.Event{
					Kind:   gateway.EventKindUserMessage,
					TurnID: "turn-complete",
					Narrative: &gateway.NarrativePayload{
						Role: gateway.NarrativeRoleUser,
						Text: "history prompt",
					},
				},
			},
			{
				Event: gateway.Event{
					Kind:   gateway.EventKindToolCall,
					TurnID: "turn-complete",
					ToolCall: &gateway.ToolCallPayload{
						CallID:   "command-1",
						ToolName: "RUN_COMMAND",
						Status:   gateway.ToolStatusRunning,
					},
				},
			},
			{
				Event: gateway.Event{
					Kind:   gateway.EventKindAssistantMessage,
					TurnID: "turn-complete",
					Narrative: &gateway.NarrativePayload{
						Role:       gateway.NarrativeRoleAssistant,
						Text:       "stream chunk",
						Final:      false,
						Visibility: string(session.VisibilityUIOnly),
					},
				},
			},
			{
				Event: gateway.Event{
					Kind:   gateway.EventKindAssistantMessage,
					TurnID: "turn-complete",
					Narrative: &gateway.NarrativePayload{
						Role:  gateway.NarrativeRoleAssistant,
						Text:  "history reply",
						Final: true,
						Scope: gateway.EventScopeMain,
					},
				},
			},
		},
	}
	var msgs []tea.Msg
	slashResume(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "resumed-session")
	if len(msgs) < 2 {
		t.Fatalf("slashResume() emitted %d messages, want at least 2", len(msgs))
	}
	if _, ok := msgs[0].(ClearHistoryMsg); !ok {
		t.Fatalf("first msg = %#v, want ClearHistoryMsg", msgs[0])
	}
	var sawUserReplay bool
	var sawAssistantReplay bool
	var replayBatchCount int
	for _, msg := range msgs {
		if log, ok := msg.(LogChunkMsg); ok && (strings.Contains(log.Chunk, "resumed session") || strings.Contains(log.Chunk, "replayed")) {
			t.Fatalf("slashResume() emitted noisy resume notice: %#v", log)
		}
		if _, ok := msg.(gateway.EventEnvelope); ok {
			t.Fatalf("slashResume() must batch historical replay, got per-envelope msg: %#v", msg)
		}
		if batch, ok := msg.(TranscriptEventsMsg); ok {
			replayBatchCount++
			for _, event := range batch.Events {
				if event.Kind == TranscriptEventTool {
					t.Fatalf("slashResume() replayed tool process event: %#v", event)
				}
				if event.Text == "stream chunk" {
					t.Fatalf("slashResume() replayed transient assistant chunk: %#v", event)
				}
				if event.Text == "history prompt" {
					sawUserReplay = true
				}
				if event.Text == "history reply" {
					sawAssistantReplay = true
				}
			}
		}
	}
	if replayBatchCount != 1 {
		t.Fatalf("slashResume() replay batches = %d, want 1", replayBatchCount)
	}
	if !sawUserReplay || !sawAssistantReplay {
		t.Fatalf("slashResume() messages = %#v, want user and final assistant replay", msgs)
	}
}

func TestSlashResumeReplaysSideACPFinalDialogueWithoutProcessTrace(t *testing.T) {
	driver := &bridgeTestDriver{
		status:         control.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
		resumedSession: control.SessionSnapshot{SessionID: "resumed-session"},
		replay: []gateway.EventEnvelope{
			{
				Event: gateway.Event{
					Kind:   gateway.EventKindUserMessage,
					TurnID: "participant-turn-1",
					Origin: &gateway.EventOrigin{
						Source:  "acp_participant",
						Scope:   gateway.EventScopeParticipant,
						ScopeID: "participant-turn-1",
						Actor:   "@codex",
					},
					Narrative: &gateway.NarrativePayload{
						Role:  gateway.NarrativeRoleUser,
						Text:  "review this change",
						Scope: gateway.EventScopeParticipant,
					},
				},
			},
			{
				Event: gateway.Event{
					Kind:   gateway.EventKindToolCall,
					TurnID: "participant-turn-1",
					Origin: &gateway.EventOrigin{
						Source:  "acp_participant",
						Scope:   gateway.EventScopeParticipant,
						ScopeID: "participant-turn-1",
						Actor:   "@codex",
					},
					ToolCall: &gateway.ToolCallPayload{
						CallID:   "side-command",
						ToolName: "RUN_COMMAND",
						Status:   gateway.ToolStatusCompleted,
						Scope:    gateway.EventScopeParticipant,
					},
				},
			},
			{
				Event: gateway.Event{
					Kind:   gateway.EventKindAssistantMessage,
					TurnID: "participant-turn-1",
					Origin: &gateway.EventOrigin{
						Source:  "acp_participant",
						Scope:   gateway.EventScopeParticipant,
						ScopeID: "participant-turn-1",
						Actor:   "@codex",
					},
					Narrative: &gateway.NarrativePayload{
						Role:  gateway.NarrativeRoleAssistant,
						Actor: "@codex",
						Text:  "review final message",
						Final: true,
						Scope: gateway.EventScopeParticipant,
					},
				},
			},
			{
				Event: gateway.Event{
					Kind:   gateway.EventKindAssistantMessage,
					TurnID: "participant-turn-1",
					Origin: &gateway.EventOrigin{
						Source:  "side_subagent",
						Scope:   gateway.EventScopeSubagent,
						ScopeID: "participant-turn-1",
						Actor:   "@reviewer",
					},
					Narrative: &gateway.NarrativePayload{
						Role:  gateway.NarrativeRoleAssistant,
						Actor: "@reviewer",
						Text:  "scoped final message",
						Final: true,
						Scope: gateway.EventScopeSubagent,
					},
				},
			},
		},
	}
	var msgs []tea.Msg
	slashResume(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "resumed-session")
	var sawSidePrompt bool
	var sawSideFinal bool
	var sawScopedFinal bool
	for _, msg := range msgs {
		batch, ok := msg.(TranscriptEventsMsg)
		if !ok {
			continue
		}
		for _, event := range batch.Events {
			if event.Kind == TranscriptEventTool {
				t.Fatalf("slashResume() replayed side ACP process event: %#v", event)
			}
			if event.Scope == ACPProjectionMain && event.Text == "User to @codex: review this change" {
				sawSidePrompt = true
			}
			if event.Scope == ACPProjectionParticipant && event.Text == "review final message" {
				sawSideFinal = true
			}
			if event.Scope == ACPProjectionSubagent && event.Text == "scoped final message" {
				sawScopedFinal = true
			}
		}
	}
	if !sawSidePrompt || !sawSideFinal || !sawScopedFinal {
		t.Fatalf("slashResume() messages = %#v, want scoped prompt and final messages replayed", msgs)
	}
}

func TestSlashResumeReplaysProcessEventsForInterruptedTurn(t *testing.T) {
	driver := &bridgeTestDriver{
		status:         control.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
		resumedSession: control.SessionSnapshot{SessionID: "resumed-session"},
		replay: []gateway.EventEnvelope{
			{
				Event: gateway.Event{
					Kind:   gateway.EventKindUserMessage,
					TurnID: "turn-interrupted",
					Narrative: &gateway.NarrativePayload{
						Role: gateway.NarrativeRoleUser,
						Text: "history prompt",
					},
				},
			},
			{
				Event: gateway.Event{
					Kind:   gateway.EventKindToolCall,
					TurnID: "turn-interrupted",
					ToolCall: &gateway.ToolCallPayload{
						CallID:   "command-1",
						ToolName: "RUN_COMMAND",
						Status:   gateway.ToolStatusRunning,
					},
				},
			},
			{
				Event: gateway.Event{
					Kind:   gateway.EventKindAssistantMessage,
					TurnID: "turn-interrupted",
					Narrative: &gateway.NarrativePayload{
						Role:       gateway.NarrativeRoleAssistant,
						Text:       "partial answer",
						Final:      true,
						Visibility: string(session.VisibilityMirror),
						Scope:      gateway.EventScopeMain,
					},
				},
			},
		},
	}
	var msgs []tea.Msg
	slashResume(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "resumed-session")
	var sawMirrorReplay bool
	for _, msg := range msgs {
		if batch, ok := msg.(TranscriptEventsMsg); ok {
			for _, event := range batch.Events {
				if event.Kind == TranscriptEventTool {
					t.Fatalf("slashResume() replayed interrupted process event: %#v", event)
				}
				if event.Text == "partial answer" {
					sawMirrorReplay = true
				}
			}
		}
	}
	if !sawMirrorReplay {
		t.Fatalf("slashResume() messages = %#v, want interrupted assistant replay", msgs)
	}
}

func TestExecuteLineViaDriverStreamsGatewayEventsDirectly(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan gateway.EventEnvelope, 1),
	}
	turn.events <- gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:  gateway.NarrativeRoleAssistant,
				Text:  "direct gateway event",
				Final: true,
				Scope: gateway.EventScopeMain,
			},
		},
	}
	close(turn.events)

	driver := &bridgeSubmitDriver{turn: turn}
	var msgs []tea.Msg
	result := executeLineViaControlService(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaControlService() err = %v", result.Err)
	}
	if len(msgs) != 1 {
		t.Fatalf("executeLineViaControlService() emitted %d msgs, want 1", len(msgs))
	}
	if got := requireACPText(t, msgs[0], schema.UpdateAgentMessage); got != "direct gateway event" {
		t.Fatalf("first ACP text = %q, want direct gateway event", got)
	}
}

func TestExecuteLineViaDriverCoalescesUIOnlyReasoningBeforeToolEvent(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan gateway.EventEnvelope, 4),
	}
	for _, text := range []string{"think ", "fast ", "now"} {
		turn.events <- gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative: &gateway.NarrativePayload{
					Role:          gateway.NarrativeRoleAssistant,
					ReasoningText: text,
					Visibility:    "ui_only",
					UpdateType:    string(session.ProtocolUpdateTypeAgentThought),
					Scope:         gateway.EventScopeMain,
				},
			},
		}
	}
	turn.events <- gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "READ",
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeMain,
			},
		},
	}
	close(turn.events)

	driver := &bridgeSubmitDriver{turn: turn}
	var msgs []tea.Msg
	result := executeLineViaControlService(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaControlService() err = %v", result.Err)
	}
	if got := len(msgs); got != 2 {
		t.Fatalf("executeLineViaControlService() emitted %d msgs, want 2: %#v", got, msgs)
	}
	if got := requireACPText(t, msgs[0], schema.UpdateAgentThought); got != "think fast now" {
		t.Fatalf("coalesced reasoning = %q, want %q", got, "think fast now")
	}
	tool := requireACPEnvelope(t, msgs[1])
	if update, ok := tool.Update.(schema.ToolCall); !ok || update.ToolCallID != "call-1" {
		t.Fatalf("second update = %#v, want tool event after reasoning flush", tool.Update)
	}
}

func TestExecuteLineViaDriverCoalescesUIOnlyReasoningPreservesLeadingSpaces(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan gateway.EventEnvelope, 7),
	}
	for _, text := range []string{"Now", " let", " me", " verify", " the", " DDL", " matches"} {
		turn.events <- gateway.EventEnvelope{
			Event: gateway.Event{
				Kind:       gateway.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative: &gateway.NarrativePayload{
					Role:          gateway.NarrativeRoleAssistant,
					ReasoningText: text,
					Visibility:    "ui_only",
					UpdateType:    string(session.ProtocolUpdateTypeAgentThought),
					Scope:         gateway.EventScopeMain,
				},
			},
		}
	}
	close(turn.events)

	driver := &bridgeSubmitDriver{turn: turn}
	var msgs []tea.Msg
	result := executeLineViaControlService(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaControlService() err = %v", result.Err)
	}
	if got := len(msgs); got != 1 {
		t.Fatalf("executeLineViaControlService() emitted %d msgs, want 1: %#v", got, msgs)
	}
	if got := requireACPText(t, msgs[0], schema.UpdateAgentThought); got != "Now let me verify the DDL matches" {
		t.Fatalf("coalesced reasoning = %q, want boundary spaces preserved", got)
	}
}

func TestExecuteLineViaDriverDoesNotCoalesceReasoningWithAnswerDelta(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan gateway.EventEnvelope, 2),
	}
	turn.events <- gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:          gateway.NarrativeRoleAssistant,
				ReasoningText: "think",
				Visibility:    "ui_only",
				UpdateType:    string(session.ProtocolUpdateTypeAgentThought),
				Scope:         gateway.EventScopeMain,
			},
		},
	}
	turn.events <- gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &gateway.NarrativePayload{
				Role:       gateway.NarrativeRoleAssistant,
				Text:       "answer",
				Visibility: "ui_only",
				UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
				Scope:      gateway.EventScopeMain,
			},
		},
	}
	close(turn.events)

	driver := &bridgeSubmitDriver{turn: turn}
	var msgs []tea.Msg
	result := executeLineViaControlService(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaControlService() err = %v", result.Err)
	}
	if got := len(msgs); got != 2 {
		t.Fatalf("executeLineViaControlService() emitted %d msgs, want 2: %#v", got, msgs)
	}
	if got := requireACPText(t, msgs[0], schema.UpdateAgentThought); got != "think" {
		t.Fatalf("first ACP text = %q, want reasoning", got)
	}
	if got := requireACPText(t, msgs[1], schema.UpdateAgentMessage); got != "answer" {
		t.Fatalf("second ACP text = %q, want answer", got)
	}
}

func TestExecuteLineViaDriverTreatsUnknownSlashAsUserMessage(t *testing.T) {
	driver := &bridgeSubmitDriver{}
	text := "/rbac/inner/workflow/switch Query 参数"
	result := executeLineViaControlService(driver, &ProgramSender{Send: func(tea.Msg) {}}, Submission{Text: text})
	if result.Err != nil {
		t.Fatalf("executeLineViaControlService() err = %v", result.Err)
	}
	if driver.submitCalls != 1 {
		t.Fatalf("Submit() calls = %d, want 1", driver.submitCalls)
	}
	if driver.lastSubmission.Text != text {
		t.Fatalf("submitted text = %q, want %q", driver.lastSubmission.Text, text)
	}
}

func TestExecuteLineViaDriverForwardsTerminalStreamEvents(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan gateway.EventEnvelope, 1),
	}
	turn.events <- gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "RUN_COMMAND",
				Content:  testTerminalContentWithID("seed\n", "terminal-1"),
				Status:   gateway.ToolStatusRunning,
			},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"task": map[string]any{
							"task_id":       "task-1",
							"terminal_id":   "terminal-1",
							"running":       true,
							"state":         "running",
							"output_cursor": int64(5),
						},
					},
				},
			},
		},
	}
	close(turn.events)
	terminalEvents := make(chan gateway.EventEnvelope, 1)
	terminalEvents <- gateway.EventEnvelope{
		Event: gateway.Event{
			Kind: gateway.EventKindToolResult,
			ToolResult: &gateway.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "RUN_COMMAND",
				Content:  testTerminalContentWithID("streamed\n", "terminal-1"),
				Status:   gateway.ToolStatusRunning,
			},
		},
	}
	close(terminalEvents)

	driver := &bridgeSubmitDriver{turn: turn, terminalEvents: terminalEvents}
	var msgs []tea.Msg
	result := executeLineViaControlService(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaControlService() err = %v", result.Err)
	}
	deadline := time.After(2 * time.Second)
	for {
		var sawStream bool
		for _, msg := range msgs {
			env, ok := msg.(eventstream.Envelope)
			if !ok {
				continue
			}
			if text := acpUpdateTerminalText(env.Update); text == "streamed\n" {
				sawStream = true
			}
		}
		if sawStream {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("messages = %#v, want forwarded terminal stream event", msgs)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if driver.terminalSubscribeCalls != 1 {
		t.Fatalf("terminalSubscribeCalls = %d, want 1", driver.terminalSubscribeCalls)
	}
}

func TestSlashResumeReplaysGatewayEventsDirectly(t *testing.T) {
	driver := &bridgeTestDriver{
		status:         control.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
		resumedSession: control.SessionSnapshot{SessionID: "resumed-session"},
		replay: []gateway.EventEnvelope{{
			Event: gateway.Event{
				Kind:       gateway.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative: &gateway.NarrativePayload{
					Role:  gateway.NarrativeRoleAssistant,
					Text:  "history reply",
					Final: true,
					Scope: gateway.EventScopeMain,
				},
			},
		}},
	}
	var msgs []tea.Msg
	slashResume(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "resumed-session")
	var sawReplay bool
	for _, msg := range msgs {
		if batch, ok := msg.(TranscriptEventsMsg); ok {
			for _, event := range batch.Events {
				if event.Text == "history reply" {
					sawReplay = true
				}
			}
		}
	}
	if !sawReplay {
		t.Fatalf("slashResume() messages = %#v, want replayed gateway envelope", msgs)
	}
}

func TestSlashConnectCallsDriverAndUpdatesStatus(t *testing.T) {
	driver := &bridgeTestDriver{
		status:        control.StatusSnapshot{Model: "minimax/MiniMax-M1", ModeLabel: "default", Workspace: "/tmp/ws"},
		connectStatus: control.StatusSnapshot{Model: "minimax/MiniMax-M2", ModeLabel: "default", Workspace: "/tmp/ws"},
	}
	var msgs []tea.Msg
	slashConnect(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "minimax MiniMax-M2 - 60 sk-test 204800 8192 low,medium")
	if driver.connectCalls != 1 {
		t.Fatalf("connectCalls = %d, want 1", driver.connectCalls)
	}
	if got := driver.lastConnect.Provider; got != "minimax" {
		t.Fatalf("lastConnect.Provider = %q, want minimax", got)
	}
	if got := driver.lastConnect.Model; got != "MiniMax-M2" {
		t.Fatalf("lastConnect.Model = %q, want MiniMax-M2", got)
	}
	if got := driver.lastConnect.APIKey; got != "sk-test" {
		t.Fatalf("lastConnect.APIKey = %q, want sk-test", got)
	}
	if got := driver.lastConnect.ContextWindowTokens; got != 204800 {
		t.Fatalf("lastConnect.ContextWindowTokens = %d, want 204800", got)
	}
	if got := driver.lastConnect.MaxOutputTokens; got != 8192 {
		t.Fatalf("lastConnect.MaxOutputTokens = %d, want 8192", got)
	}
	if len(msgs) == 0 {
		t.Fatal("slashConnect() emitted no messages")
	}
}

func TestFormatContextUsageStatus(t *testing.T) {
	if got := formatContextUsageStatus(12600, 88000); got != "12.6k / 88k · 14%" {
		t.Fatalf("formatContextUsageStatus() = %q, want %q", got, "12.6k / 88k · 14%")
	}
	if got := formatContextUsageStatus(0, 88000); got != "0 / 88k · 0%" {
		t.Fatalf("formatContextUsageStatus() zero = %q, want %q", got, "0 / 88k · 0%")
	}
}

func TestSlashAgentDispatchesPrimarySubcommands(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList: []control.AgentCandidate{{
			Name:        "copilot",
			Description: "ACP sidecar",
		}},
		agentStatus: control.AgentStatusSnapshot{
			SessionID:       "sess-1",
			ControllerKind:  "acp",
			ControllerLabel: "copilot",
			Participants: []control.AgentParticipantSnapshot{{
				ID:    "participant-1",
				Label: "copilot",
				Role:  "sidecar",
			}},
		},
	}
	var msgs []tea.Msg
	slashAgent(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "list")
	slashAgent(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "add copilot")
	slashAgent(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "remove copilot")
	slashAgent(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "use copilot")
	if driver.listAgentCalls != 1 || driver.agentStatusCalls != 4 || driver.addAgentCalls != 1 || driver.removeAgentCalls != 1 || driver.handoffAgentCalls != 1 {
		t.Fatalf("agent calls = list:%d status:%d add:%d remove:%d use:%d", driver.listAgentCalls, driver.agentStatusCalls, driver.addAgentCalls, driver.removeAgentCalls, driver.handoffAgentCalls)
	}
	if driver.lastAddedAgent != "copilot" || driver.lastRemovedAgent != "copilot" || driver.lastHandoffAgent != "copilot" {
		t.Fatalf("agent targets = add:%q remove:%q handoff:%q", driver.lastAddedAgent, driver.lastRemovedAgent, driver.lastHandoffAgent)
	}
	if len(msgs) == 0 {
		t.Fatal("slashAgent() emitted no messages")
	}
}

func TestACPControllerSlashCommandsUseRemoteSurface(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList: []control.AgentCandidate{{
			Name:        "copilot",
			Description: "local ACP agent",
		}},
		agentStatus: control.AgentStatusSnapshot{
			ControllerKind:     "acp",
			ControllerCommands: []string{"search", "/draft"},
		},
	}
	commands := appendAgentSlashCommandsWithContext(context.Background(), driver, DefaultCommands())
	for _, want := range []string{"help", "agent", "status", "resume", "model", "search", "draft"} {
		if !stringSliceContains(commands, want) {
			t.Fatalf("ACP commands = %#v, missing %q", commands, want)
		}
	}
	for _, blocked := range []string{"connect", "sandbox", "compact", "new", "copilot"} {
		if stringSliceContains(commands, blocked) {
			t.Fatalf("ACP commands = %#v, should not include local command %q", commands, blocked)
		}
	}
	if !isDispatchableSlashCommandWithContext(context.Background(), driver, "/status") {
		t.Fatal("/status should remain locally dispatchable under ACP")
	}
	for _, remoteOrDisabled := range []string{"/search docs", "/draft note", "/connect"} {
		if isDispatchableSlashCommandWithContext(context.Background(), driver, remoteOrDisabled) {
			t.Fatalf("%s should be submitted to the ACP controller, not local dispatch", remoteOrDisabled)
		}
	}
}

func TestSlashModelDeleteDisabledForACPController(t *testing.T) {
	driver := &bridgeTestDriver{
		agentStatus: control.AgentStatusSnapshot{ControllerKind: "acp"},
	}
	var msgs []tea.Msg
	slashModelWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "del minimax/MiniMax-M1")
	if driver.deleteModelCalls != 0 {
		t.Fatalf("deleteModelCalls = %d, want 0 under ACP controller", driver.deleteModelCalls)
	}
	if len(msgs) == 0 {
		t.Fatal("slashModel(del) emitted no usage message")
	}
}

func stringSliceContains(items []string, want string) bool {
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

func TestSlashAgentInstallPassesOptions(t *testing.T) {
	driver := &bridgeTestDriver{}
	var msgs []tea.Msg
	result := slashAgentWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "install claude")
	if result.Err != nil {
		t.Fatalf("slashAgentWithContext(install) error = %v", result.Err)
	}
	if driver.lastAddedAgent != "claude" {
		t.Fatalf("lastAddedAgent = %q, want claude", driver.lastAddedAgent)
	}
	if !driver.lastAddOptions.Install {
		t.Fatal("AddAgentWithOptions Install = false, want true")
	}
	if len(msgs) == 0 {
		t.Fatal("slashAgentWithContext(install) emitted no messages")
	}
}

func TestSlashAgentAddCustomPassesConfig(t *testing.T) {
	driver := &bridgeTestDriver{}
	var msgs []tea.Msg
	result := slashAgentWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "add custom helper -- helper-acp --stdio --model test")
	if result.Err != nil {
		t.Fatalf("slashAgentWithContext(add custom) error = %v", result.Err)
	}
	if driver.lastAddedAgent != "helper" {
		t.Fatalf("lastAddedAgent = %q, want helper", driver.lastAddedAgent)
	}
	if driver.lastAddOptions.Custom == nil {
		t.Fatal("AddAgentWithOptions Custom = nil, want config")
	}
	cfg := driver.lastAddOptions.Custom
	if cfg.Name != "helper" || cfg.Command != "helper-acp" {
		t.Fatalf("custom config = %#v, want helper/helper-acp", cfg)
	}
	if got, want := strings.Join(cfg.Args, " "), "--stdio --model test"; got != want {
		t.Fatalf("custom args = %q, want %q", got, want)
	}
	if len(msgs) == 0 || !noticeMessagesContain(msgs, "custom agent registered: helper") {
		t.Fatalf("slashAgentWithContext(add custom) messages = %#v, want registration notice", msgs)
	}
}

func TestSlashAgentInstallFailureEmitsRunCommandToolResult(t *testing.T) {
	driver := &bridgeTestDriver{
		addAgentErr: fmt.Errorf("gatewayapp: install ACP agent %q: exit status 7\nnpm ERR install failed", "claude"),
		slashArgCandidates: map[string][]control.SlashArgCandidate{
			"agent install": {{
				Value:  "claude",
				Detail: "npm install --prefix /tmp/caelis/acp-agents/npm @agentclientprotocol/claude-agent-acp@latest",
			}},
		},
	}
	var msgs []tea.Msg
	result := slashAgentWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "install claude")
	if result.Err == nil {
		t.Fatal("slashAgentWithContext(install failure) error = nil, want failure")
	}
	var sawCall, sawResult bool
	for _, msg := range msgs {
		env, ok := msg.(eventstream.Envelope)
		if !ok {
			continue
		}
		switch update := env.Update.(type) {
		case schema.ToolCall:
			if update.Kind == "RUN_COMMAND" &&
				update.Status == schema.ToolStatusInProgress &&
				strings.Contains(fmt.Sprint(acpRawMap(update.RawInput)["command"]), "npm install --prefix") {
				sawCall = true
			}
		case schema.ToolCallUpdate:
			if stringFromPtr(update.Kind) == "RUN_COMMAND" &&
				stringFromPtr(update.Status) == schema.ToolStatusFailed &&
				strings.Contains(fmt.Sprint(acpRawMap(update.RawOutput)["stderr"]), "npm ERR install failed") {
				sawResult = true
			}
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("install failure messages sawCall=%v sawResult=%v msgs=%#v", sawCall, sawResult, msgs)
	}
}

func TestSlashArgQueryAgentInstall(t *testing.T) {
	command, query, ok := slashArgQueryAtEnd([]rune("/agent install c"))
	if !ok {
		t.Fatal("slashArgQueryAtEnd(/agent install c) ok = false")
	}
	if command != "agent install" || query != "c" {
		t.Fatalf("slashArgQueryAtEnd(/agent install c) = command %q query %q, want agent install / c", command, query)
	}
	command, query, ok = slashArgQueryAtEnd([]rune("/agent install "))
	if !ok {
		t.Fatal("slashArgQueryAtEnd(/agent install ) ok = false")
	}
	if command != "agent install" || query != "" {
		t.Fatalf("slashArgQueryAtEnd(/agent install ) = command %q query %q, want agent install / empty", command, query)
	}
}

func TestAgentInstallSlashArgFallbackIsExecutable(t *testing.T) {
	if !isExecutableSlashArgInput("/agent install claude") {
		t.Fatal("isExecutableSlashArgInput(/agent install claude) = false, want true")
	}
}

func TestSlashAgentHelpAndRecovery(t *testing.T) {
	driver := &bridgeTestDriver{}
	var msgs []tea.Msg
	slashAgent(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "")
	slashAgent(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "remove")
	if len(msgs) < 2 {
		t.Fatalf("slashAgent() emitted %d messages, want help and recovery", len(msgs))
	}
	joined := ""
	for _, msg := range msgs {
		if log, ok := msg.(LogChunkMsg); ok {
			joined += log.Chunk
		}
	}
	for _, want := range []string{"/agent commands:", "usage: /agent remove <agent>"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("slashAgent() output = %q, want substring %q", joined, want)
		}
	}
}

func TestFormatAgentStatusSnapshotShowsDelegatedParticipants(t *testing.T) {
	status := control.AgentStatusSnapshot{
		SessionID:       "session-1",
		ControllerKind:  "kernel",
		ControllerLabel: "local",
		HasActiveTurn:   true,
		Participants: []control.AgentParticipantSnapshot{
			{
				ID:        "side-001",
				Label:     "@codex",
				AgentName: "codex",
				Kind:      string(session.ParticipantKindACP),
				Role:      string(session.ParticipantRoleSidecar),
				SessionID: "side-session",
			},
		},
		DelegatedParticipants: []control.AgentParticipantSnapshot{
			{
				ID:        "self-001",
				Label:     "@jude",
				AgentName: "self",
				Kind:      string(session.ParticipantKindSubagent),
				Role:      string(session.ParticipantRoleDelegated),
				SessionID: "self-session",
			},
			{
				ID:        "codex-001",
				Label:     "@kate",
				AgentName: "codex",
				Kind:      string(session.ParticipantKindSubagent),
				Role:      string(session.ParticipantRoleDelegated),
				SessionID: "codex-session",
			},
		},
	}

	got := formatAgentStatusSnapshot(status)
	if !strings.Contains(got, "side-001") || !strings.Contains(got, "@codex(codex)") {
		t.Fatalf("formatAgentStatusSnapshot() = %q, want side agent participant", got)
	}
	if !strings.Contains(got, "self-001") || !strings.Contains(got, "@jude(self)") || !strings.Contains(got, "codex-001") || !strings.Contains(got, "@kate(codex)") {
		t.Fatalf("formatAgentStatusSnapshot() = %q, want delegated task summary", got)
	}
	if strings.Contains(got, "agent status:") || strings.Contains(got, "active turn:") {
		t.Fatalf("formatAgentStatusSnapshot() = %q, should use friendly labels", got)
	}
	if !strings.Contains(got, "Agent Controller") || !strings.Contains(got, "State") || !strings.Contains(got, "Side agents") || !strings.Contains(got, "Delegated tasks") {
		t.Fatalf("formatAgentStatusSnapshot() = %q, want themed-friendly summary labels", got)
	}
}

func TestFormatStatusSnapshotUsesFriendlyThemeableLines(t *testing.T) {
	got := formatStatusSnapshot(control.StatusSnapshot{
		SessionID:                "sess-1",
		Provider:                 "acp",
		ModelName:                "gpt-5.5",
		Model:                    "gpt-5.5 [high]",
		ModeLabel:                "Default",
		SandboxType:              "seatbelt",
		Route:                    "sandbox",
		Workspace:                "/tmp/ws",
		StoreDir:                 "/tmp/store",
		MissingAPIKey:            true,
		HostExecution:            true,
		FullAccessMode:           true,
		SessionUsageTotal:        control.UsageSnapshot{PromptTokens: 12600, CachedInputTokens: 9000, CompletionTokens: 200, ReasoningTokens: 50, TotalTokens: 12800},
		SessionUsageMain:         control.UsageSnapshot{PromptTokens: 10000, CachedInputTokens: 7000, CompletionTokens: 150, ReasoningTokens: 30, TotalTokens: 10150},
		SessionUsageSubagents:    control.UsageSnapshot{PromptTokens: 2000, CachedInputTokens: 1800, CompletionTokens: 40, ReasoningTokens: 15, TotalTokens: 2040},
		SessionUsageAutoReview:   control.UsageSnapshot{PromptTokens: 600, CachedInputTokens: 200, CompletionTokens: 10, ReasoningTokens: 5, TotalTokens: 610},
		SessionInputTokens:       12600,
		SessionCachedInputTokens: 9000,
		SessionOutputTokens:      200,
		SessionReasoningTokens:   50,
		SessionTotalTokens:       12800,
	})
	for _, forbidden := range []string{"Status", "Tokens", "Warnings", "status:", "provider:", "model:", "alias:", "Provider:", "Store:", "\n  Reason:"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("formatStatusSnapshot() = %q, should not contain log-style label %q", got, forbidden)
		}
	}
	for _, want := range []string{"  Model:", "  Mode:", "  Sandbox:", "  Workspace:", "  Session:", "sess-1", "  Scope", "-----", "total", "12,800", "main", "10,150", "sub-agent", "2,040", "auto-review", "610", "Warning:", "API key is missing"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatStatusSnapshot() = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "Token usage:") || strings.Contains(got, "main usage:") || strings.Contains(got, "warn:") {
		t.Fatalf("formatStatusSnapshot() = %q, should use table-style token usage", got)
	}
	warningIdx := strings.Index(got, "Warning:")
	usageIdx := strings.Index(got, "  Scope")
	if warningIdx < 0 || usageIdx < 0 || warningIdx >= usageIdx {
		t.Fatalf("formatStatusSnapshot() = %q, want warnings before token usage table", got)
	}
	if tail := got[usageIdx:]; strings.Contains(tail, "Warning:") {
		t.Fatalf("formatStatusSnapshot() = %q, token usage table should be the final section", got)
	}
	if !strings.Contains(got, "\n\n  Scope") {
		t.Fatalf("formatStatusSnapshot() = %q, want blank line before token usage table", got)
	}
}

func TestFormatStatusSnapshotOmitsSetupReasonDetails(t *testing.T) {
	got := formatStatusSnapshot(control.StatusSnapshot{
		Model:                       "mimo-v2.5-pro [high]",
		ModeLabel:                   "auto-review",
		SandboxResolvedBackend:      "windows",
		Route:                       "sandbox",
		Workspace:                   "D:\\xue\\code\\storage",
		SandboxWorkspaceSetupReason: "workspace ACL manifest is stale and will be repaired lazily",
		SandboxSetupMarkerReason:    "stale sandbox setup marker",
	})
	for _, forbidden := range []string{"Status", "Tokens", "\n  Reason:", "workspace ACL manifest", "stale sandbox setup marker", "Store:", "Provider:"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("formatStatusSnapshot() = %q, should omit %q", got, forbidden)
		}
	}
	for _, want := range []string{"  Model:", "mimo-v2.5-pro [high]", "windows sandbox", "D:\\xue\\code\\storage"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatStatusSnapshot() = %q, want %q", got, want)
		}
	}
}

func TestFormatStatusSnapshotShowsExplicitSandboxRepairFailure(t *testing.T) {
	got := formatStatusSnapshot(control.StatusSnapshot{
		Model:                  "mimo-v2.5-pro [high]",
		ModeLabel:              "auto-review",
		SandboxResolvedBackend: "windows",
		Route:                  "sandbox",
		Workspace:              "D:\\xue\\code\\cmpctl",
		SandboxSetup: control.SandboxSetupStatus{Checks: []control.SandboxSetupCheck{{
			Name:     "workspace",
			Scope:    "workspace",
			Required: true,
			Error:    "acl: write D:\\xue\\code\\cmpctl DACL: Access is denied.",
		}}},
		SandboxWorkspaceSetupRequired: true,
		SandboxSetupError:             "acl: write D:\\xue\\code\\cmpctl DACL: Access is denied.",
	})
	for _, want := range []string{"Setup:", "current workspace ACL repair failed", "Error:", "Access is denied", "Warning:", "/doctor fix"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatStatusSnapshot() = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "will be repaired lazily") {
		t.Fatalf("formatStatusSnapshot() = %q, should not suggest lazy repair after explicit failure", got)
	}
}

func TestFormatSessionTokenUsageStatusOmitsEmptyBreakdownBuckets(t *testing.T) {
	got := formatSessionTokenUsageStatus(control.StatusSnapshot{
		SessionUsageTotal: control.UsageSnapshot{PromptTokens: 100, CachedInputTokens: 20, CompletionTokens: 10, TotalTokens: 110},
		SessionUsageMain:  control.UsageSnapshot{PromptTokens: 100, CachedInputTokens: 20, CompletionTokens: 10, TotalTokens: 110},
	})
	for _, want := range []string{"Scope", "Total", "Cached", "total", "main", "110", "100", "20"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatSessionTokenUsageStatus() = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "main usage:") {
		t.Fatalf("formatSessionTokenUsageStatus() = %q, should use table-style token usage", got)
	}
	for _, forbidden := range []string{"sub-agent", "auto-review"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("formatSessionTokenUsageStatus() = %q, should omit %q", got, forbidden)
		}
	}
}

func TestDynamicAgentSlashAndHandleContinuation(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList:    []control.AgentCandidate{{Name: "copilot"}},
		subagentTurn: bridgeTurnWithEvents(participantAssistantEnvelope("task-1", "@jeff", "child ok")),
	}
	var msgs []tea.Msg
	result := dispatchSlashCommand(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, "/copilot inspect")
	if result.Err != nil {
		t.Fatalf("dynamic slash error = %v", result.Err)
	}
	if driver.lastStartedAgent != "copilot" || driver.lastStartedPrompt != "inspect" {
		t.Fatalf("started agent=%q prompt=%q", driver.lastStartedAgent, driver.lastStartedPrompt)
	}
	result = executeLineViaControlService(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "@jeff continue"})
	if result.Err != nil {
		t.Fatalf("handle continuation error = %v", result.Err)
	}
	if driver.lastContinuedHandle != "jeff" || driver.lastContinuedPrompt != "continue" {
		t.Fatalf("continued handle=%q prompt=%q", driver.lastContinuedHandle, driver.lastContinuedPrompt)
	}
	result = executeLineViaControlService(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{
		Text:        "/copilot see this",
		Attachments: []Attachment{{Name: "shot.png", Offset: len([]rune("/copilot see "))}},
	})
	if result.Err != nil {
		t.Fatalf("dynamic slash with attachment error = %v", result.Err)
	}
	if len(driver.lastStartedAttachments) != 1 || driver.lastStartedAttachments[0].Name != "shot.png" || driver.lastStartedAttachments[0].Offset != len([]rune("see ")) {
		t.Fatalf("started attachments = %#v, want prompt-relative image attachment", driver.lastStartedAttachments)
	}
	result = executeLineViaControlService(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{
		Text:        "@jeff continue",
		Attachments: []Attachment{{Name: "follow.png", Offset: len([]rune("@jeff continue"))}},
	})
	if result.Err != nil {
		t.Fatalf("handle continuation with attachment error = %v", result.Err)
	}
	if len(driver.lastContinuedAttachments) != 1 || driver.lastContinuedAttachments[0].Name != "follow.png" || driver.lastContinuedAttachments[0].Offset != len([]rune("continue")) {
		t.Fatalf("continued attachments = %#v, want prompt-relative image attachment", driver.lastContinuedAttachments)
	}
	if len(msgs) == 0 {
		t.Fatal("dynamic slash emitted no messages")
	}
}

func TestDynamicAgentSlashStreamsParticipantTurnOutput(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList:    []control.AgentCandidate{{Name: "copilot"}},
		subagentTurn: bridgeTurnWithEvents(participantAssistantEnvelope("task-1", "@mike", "我是 copilot 子代理")),
	}
	msgs := make(chan tea.Msg, 16)
	result := dispatchSlashCommand(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs <- msg }}, "/copilot 介绍一下你自己")
	if result.Err != nil {
		t.Fatalf("dynamic slash error = %v", result.Err)
	}
	if result.ContinueRunning {
		t.Fatalf("dynamic slash result = %#v, want participant turn to complete through gateway handle", result)
	}
	close(msgs)
	for msg := range msgs {
		switch typed := msg.(type) {
		case SubagentStartMsg:
			t.Fatalf("dynamic slash emitted SPAWN panel start message: %#v", typed)
		default:
			if transcriptEventsContainText(transcriptEventsFromMsg(msg), "copilot 子代理") {
				return
			}
		}
	}
	t.Fatal("dynamic slash emitted no participant output")
}

func TestDynamicAgentSlashDoesNotRenderRunningOutputPreviewAsAssistantText(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList:    []control.AgentCandidate{{Name: "codex"}},
		subagentTurn: bridgeTurnWithEvents(participantAssistantEnvelope("task-1", "@iris", "上海今天阴有小雨。")),
	}
	msgs := make(chan tea.Msg, 16)
	result := dispatchSlashCommand(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs <- msg }}, "/codex 查询上海天气")
	if result.Err != nil {
		t.Fatalf("dynamic slash error = %v", result.Err)
	}
	close(msgs)
	for msg := range msgs {
		events := transcriptEventsFromMsg(msg)
		if transcriptEventsContainText(events, "Searching the Web") {
			t.Fatalf("running output preview was rendered as assistant text: %#v", msg)
		}
		if transcriptEventsContainText(events, "上海今天阴有小雨") {
			return
		}
	}
	t.Fatal("final participant output was not rendered")
}

func TestDynamicAgentSlashCompletedTurnKeepsDivider(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList:    []control.AgentCandidate{{Name: "codex"}},
		subagentTurn: bridgeTurnWithEvents(participantAssistantEnvelope("task-1", "@kate", "上海今天阴有小雨。")),
	}
	msgs := make(chan tea.Msg, 8)
	result := dispatchSlashCommand(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs <- msg }}, "/codex 查询上海天气")
	if result.Err != nil {
		t.Fatalf("dynamic slash error = %v", result.Err)
	}
	if result.SuppressTurnDivider {
		t.Fatalf("dynamic slash result = %#v, want completed ACP agent turn to keep divider", result)
	}
	if result.ContinueRunning {
		t.Fatalf("dynamic slash result = %#v, want completed turn", result)
	}
	close(msgs)
	if len(msgs) == 0 {
		t.Fatal("completed dynamic slash emitted no final transcript output")
	}
}

func TestDynamicAgentSlashParticipantTurnCompletionKeepsDivider(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList:    []control.AgentCandidate{{Name: "codex"}},
		subagentTurn: bridgeTurnWithEvents(participantAssistantEnvelope("task-1:1", "@kate", "上海今天阴有小雨。")),
	}
	msgs := make(chan tea.Msg, 8)
	sender := &ProgramSender{Send: func(msg tea.Msg) { msgs <- msg }}
	result := dispatchSlashCommand(driver, sender, "/codex 查询上海天气")
	if result.Err != nil {
		t.Fatalf("dynamic slash error = %v", result.Err)
	}
	if result.ContinueRunning {
		t.Fatalf("dynamic slash result = %#v, want participant turn to finish through gateway handle", result)
	}
	if result.SuppressTurnDivider {
		t.Fatalf("dynamic slash result = %#v, want divider kept", result)
	}
	close(msgs)
	foundOutput := false
	for msg := range msgs {
		if transcriptEventsContainText(transcriptEventsFromMsg(msg), "上海今天阴有小雨") {
			foundOutput = true
		}
	}
	if !foundOutput {
		t.Fatal("participant turn completion emitted no transcript output")
	}
}

func TestDynamicAgentSlashPrefersStructuredParticipantEvents(t *testing.T) {
	env := gateway.EventEnvelope{
		Event: gateway.Event{
			Kind:       gateway.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin: &gateway.EventOrigin{
				Scope:   gateway.EventScopeParticipant,
				ScopeID: "child-1",
				Actor:   "copilot",
			},
			ToolCall: &gateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "RUN_COMMAND",
				RawInput: map[string]any{"command": "go test ./surfaces/tui/app/..."},
				Status:   gateway.ToolStatusRunning,
				Scope:    gateway.EventScopeParticipant,
			},
		},
	}
	driver := &bridgeTestDriver{
		agentList:    []control.AgentCandidate{{Name: "copilot"}},
		subagentTurn: bridgeTurnWithEvents(env),
	}
	msgs := make(chan tea.Msg, 16)
	result := dispatchSlashCommand(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs <- msg }}, "/copilot run tests")
	if result.Err != nil {
		t.Fatalf("dynamic slash error = %v", result.Err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-msgs:
			if transcriptEventsContainText(transcriptEventsFromMsg(msg), "working") {
				t.Fatalf("structured frame emitted fallback transcript text: %#v", msg)
			}
			envMsg, ok := msg.(eventstream.Envelope)
			if !ok {
				continue
			}
			call, ok := envMsg.Update.(schema.ToolCall)
			if !ok || call.Kind != "RUN_COMMAND" {
				t.Fatalf("event envelope = %#v, want RUN_COMMAND tool call", envMsg)
			}
			if envMsg.Scope != eventstream.ScopeParticipant {
				t.Fatalf("event scope = %#v, want dynamic side ACP participant scope", envMsg.Scope)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for structured participant event")
		}
	}
}

func TestDynamicAgentSlashParticipantTurnEmitsGatewayNarrative(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList:    []control.AgentCandidate{{Name: "copilot"}},
		subagentTurn: bridgeTurnWithEvents(participantAssistantEnvelope("task-1", "@mike", "fallback side output")),
	}
	msgs := make(chan tea.Msg, 16)
	result := dispatchSlashCommand(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs <- msg }}, "/copilot run tests")
	if result.Err != nil {
		t.Fatalf("dynamic slash error = %v", result.Err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-msgs:
			if transcriptEventsContainText(transcriptEventsFromMsg(msg), "fallback side output") {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for fallback side output")
		}
	}
}

func TestSlashConnectParsesEnvironmentVariableSecret(t *testing.T) {
	driver := &bridgeTestDriver{
		connectStatus: control.StatusSnapshot{Model: "openai/gpt-4o"},
	}
	slashConnect(driver, func(tea.Msg) {}, "openai gpt-4o - 60 env:OPENAI_API_KEY")
	if got := driver.lastConnect.TokenEnv; got != "OPENAI_API_KEY" {
		t.Fatalf("lastConnect.TokenEnv = %q, want OPENAI_API_KEY", got)
	}
	if got := driver.lastConnect.APIKey; got != "" {
		t.Fatalf("lastConnect.APIKey = %q, want empty when env:... is used", got)
	}
}

func TestSlashModelUseCallsDriverAndUpdatesStatus(t *testing.T) {
	driver := &bridgeTestDriver{
		status:         control.StatusSnapshot{Model: "minimax/MiniMax-M1", ModeLabel: "default", Workspace: "/tmp/ws"},
		useModelStatus: control.StatusSnapshot{Model: "minimax/MiniMax-M2", ModeLabel: "default", Workspace: "/tmp/ws"},
	}
	var msgs []tea.Msg
	slashModel(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "use minimax/MiniMax-M2")
	if driver.useModelCalls != 1 {
		t.Fatalf("useModelCalls = %d, want 1", driver.useModelCalls)
	}
	if got := driver.lastModelAlias; got != "minimax/MiniMax-M2" {
		t.Fatalf("lastModelAlias = %q, want minimax/MiniMax-M2", got)
	}
	if got := driver.lastReasoningEffort; got != "" {
		t.Fatalf("lastReasoningEffort = %q, want empty", got)
	}
	if len(msgs) == 0 {
		t.Fatal("slashModel(use) emitted no messages")
	}
}

func TestSlashModelUsePassesReasoningLevel(t *testing.T) {
	driver := &bridgeTestDriver{
		status:         control.StatusSnapshot{Model: "deepseek/deepseek-v4-pro", ModeLabel: "default", Workspace: "/tmp/ws"},
		useModelStatus: control.StatusSnapshot{Model: "deepseek/deepseek-v4-pro [high]", ModeLabel: "default", Workspace: "/tmp/ws"},
	}
	slashModel(driver, func(tea.Msg) {}, "use deepseek/deepseek-v4-pro high")
	if driver.useModelCalls != 1 {
		t.Fatalf("useModelCalls = %d, want 1", driver.useModelCalls)
	}
	if got := driver.lastModelAlias; got != "deepseek/deepseek-v4-pro" {
		t.Fatalf("lastModelAlias = %q, want deepseek/deepseek-v4-pro", got)
	}
	if got := driver.lastReasoningEffort; got != "high" {
		t.Fatalf("lastReasoningEffort = %q, want high", got)
	}
}

func TestSlashModelDeleteCallsDriverAndRefreshesStatus(t *testing.T) {
	driver := &bridgeTestDriver{
		status: control.StatusSnapshot{Model: "minimax/MiniMax-M1", ModeLabel: "default", Workspace: "/tmp/ws"},
	}
	var msgs []tea.Msg
	slashModel(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "del minimax/MiniMax-M1")
	if driver.deleteModelCalls != 1 {
		t.Fatalf("deleteModelCalls = %d, want 1", driver.deleteModelCalls)
	}
	if got := driver.lastDeletedAlias; got != "minimax/MiniMax-M1" {
		t.Fatalf("lastDeletedAlias = %q, want minimax/MiniMax-M1", got)
	}
	if len(msgs) == 0 {
		t.Fatal("slashModel(del) emitted no messages")
	}
}

func TestSlashModelDeleteClearsStatusWhenNoModelRemains(t *testing.T) {
	driver := &bridgeTestDriver{
		status: control.StatusSnapshot{Workspace: "/tmp/ws"},
	}
	var msgs []tea.Msg
	slashModel(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "del codefree/glm-5.1")
	for _, msg := range msgs {
		status, ok := msg.(SetStatusMsg)
		if !ok {
			continue
		}
		if status.Model != "not configured (/connect)" {
			t.Fatalf("status model = %q, want not configured placeholder", status.Model)
		}
		return
	}
	t.Fatalf("slashModel(del) messages = %#v, want SetStatusMsg", msgs)
}

func TestSlashStatusShowsGuidanceAndWarnings(t *testing.T) {
	driver := &bridgeTestDriver{
		status: control.StatusSnapshot{
			SessionID:               "sess-1",
			StoreDir:                "/tmp/.caelis",
			Workspace:               "/tmp/ws",
			SandboxRequestedBackend: "seatbelt",
			SandboxResolvedBackend:  "host",
			Route:                   "host",
			FallbackReason:          "seatbelt is unavailable",
			HostExecution:           true,
			FullAccessMode:          true,
			MissingAPIKey:           true,
		},
	}
	var msgs []tea.Msg
	slashStatus(driver, func(msg tea.Msg) { msgs = append(msgs, msg) })
	if len(msgs) != 1 {
		t.Fatalf("slashStatus() emitted %d messages, want 1", len(msgs))
	}
	log, ok := msgs[0].(LogChunkMsg)
	if !ok {
		t.Fatalf("slashStatus() msg = %#v, want LogChunkMsg", msgs[0])
	}
	for _, want := range []string{"  Model:", "  Session:", "sess-1", "/connect", "Warning:", "API key is missing", "Commands may run on the host", "Auto-Review remains enabled"} {
		if !strings.Contains(log.Chunk, want) {
			t.Fatalf("slashStatus() chunk = %q, want substring %q", log.Chunk, want)
		}
	}
	for _, forbidden := range []string{"Status", "Tokens", "Warnings", "warn:", "/tmp/.caelis", "Store:", "Provider:", "\n  Reason:"} {
		if strings.Contains(log.Chunk, forbidden) {
			t.Fatalf("slashStatus() chunk = %q, should omit %q", log.Chunk, forbidden)
		}
	}
}

func TestSlashDoctorShowsReadinessChecklist(t *testing.T) {
	driver := &bridgeTestDriver{
		status: control.StatusSnapshot{
			SessionID:               "sess-1",
			Provider:                "openai",
			ModelName:               "gpt-5.5",
			StoreDir:                "/tmp/.caelis",
			SandboxRequestedBackend: "seatbelt",
			SandboxResolvedBackend:  "host",
			Route:                   "host",
			HostExecution:           true,
			MissingAPIKey:           true,
		},
	}
	var msgs []tea.Msg
	result := slashDoctorWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "")
	if result.Err != nil {
		t.Fatalf("slashDoctorWithContext() error = %v", result.Err)
	}
	if len(msgs) != 1 {
		t.Fatalf("slashDoctorWithContext() emitted %d messages, want 1", len(msgs))
	}
	log, ok := msgs[0].(LogChunkMsg)
	if !ok {
		t.Fatalf("slashDoctorWithContext() msg = %#v, want LogChunkMsg", msgs[0])
	}
	for _, want := range []string{"doctor:", "warn provider key missing", "/connect", "ok session store: /tmp/.caelis", "ok session: sess-1", "warn sandbox: host"} {
		if !strings.Contains(log.Chunk, want) {
			t.Fatalf("slashDoctorWithContext() chunk = %q, want substring %q", log.Chunk, want)
		}
	}
}

func TestSlashDoctorFixRepairsSandbox(t *testing.T) {
	driver := &bridgeTestDriver{
		status: control.StatusSnapshot{
			SandboxRequestedBackend: "windows",
			SandboxResolvedBackend:  "windows",
			Route:                   "sandbox",
		},
	}
	var msgs []tea.Msg
	result := slashDoctorWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "fix")
	if result.Err != nil {
		t.Fatalf("slashDoctorWithContext(fix) error = %v", result.Err)
	}
	if driver.repairSandboxCalls != 1 {
		t.Fatalf("repairSandboxCalls = %d, want 1", driver.repairSandboxCalls)
	}
	if !noticeMessagesContain(msgs, "Windows sandbox repair started") || !noticeMessagesContain(msgs, "Windows sandbox repair complete") {
		t.Fatalf("slashDoctorWithContext(fix) messages = %#v, want start and complete notices", msgs)
	}
}

func TestFriendlyCommandErrorMakesResumeActionable(t *testing.T) {
	err := friendlyCommandError("resume session", fmt.Errorf("gateway: session not found"))
	if !strings.Contains(err.Error(), "/resume") {
		t.Fatalf("friendlyCommandError() = %q, want /resume guidance", err)
	}
}

func TestSlashCompactRejectsArguments(t *testing.T) {
	driver := &bridgeTestDriver{}
	var msgs []tea.Msg
	result := slashCompact(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "note")
	if result.Err != nil {
		t.Fatalf("slashCompact() error = %v", result.Err)
	}
	if driver.compactCalls != 0 {
		t.Fatalf("compactCalls = %d, want 0", driver.compactCalls)
	}
	if len(msgs) != 1 {
		t.Fatalf("slashCompact() emitted %d messages, want 1", len(msgs))
	}
	log, ok := msgs[0].(LogChunkMsg)
	if !ok || !strings.Contains(log.Chunk, "usage: /compact") {
		t.Fatalf("slashCompact() msg = %#v, want usage", msgs[0])
	}
}

func transcriptEventsContainText(events []TranscriptEvent, text string) bool {
	for _, event := range events {
		if event.Kind == TranscriptEventNarrative && strings.Contains(event.Text, text) {
			return true
		}
	}
	return false
}

func noticeMessagesContain(messages []tea.Msg, text string) bool {
	for _, msg := range messages {
		log, ok := msg.(LogChunkMsg)
		if ok && strings.Contains(log.Chunk, text) {
			return true
		}
	}
	return false
}

func sandboxProgressMessagesContain(messages []tea.Msg, text string) bool {
	for _, msg := range messages {
		progress, ok := msg.(SandboxProgressMsg)
		if ok && strings.Contains(progress.Message, text) {
			return true
		}
	}
	return false
}

type bridgeTestDriver struct {
	status                   control.StatusSnapshot
	connectStatus            control.StatusSnapshot
	useModelStatus           control.StatusSnapshot
	newSession               control.SessionSnapshot
	resumedSession           control.SessionSnapshot
	replay                   []gateway.EventEnvelope
	connectCalls             int
	useModelCalls            int
	deleteModelCalls         int
	listAgentCalls           int
	agentStatusCalls         int
	addAgentCalls            int
	removeAgentCalls         int
	handoffAgentCalls        int
	prepareSandboxCalls      int
	repairSandboxCalls       int
	resetSandboxCalls        int
	compactCalls             int
	lastConnect              control.ConnectConfig
	lastModelAlias           string
	lastReasoningEffort      string
	lastDeletedAlias         string
	lastAddedAgent           string
	lastAddOptions           control.AgentAddOptions
	lastRemovedAgent         string
	lastHandoffAgent         string
	lastStartedAgent         string
	lastStartedPrompt        string
	lastStartedAttachments   []control.Attachment
	lastContinuedHandle      string
	lastContinuedPrompt      string
	lastContinuedAttachments []control.Attachment
	subagentTurn             control.Turn
	agentList                []control.AgentCandidate
	agentStatus              control.AgentStatusSnapshot
	addAgentErr              error
	slashArgCandidates       map[string][]control.SlashArgCandidate
}

type bridgeLightweightStatusDriver struct {
	bridgeTestDriver
	lightweightStatus      control.StatusSnapshot
	statusCalls            int
	lightweightStatusCalls int
}

type bridgeTestTurn struct {
	events chan gateway.EventEnvelope
}

func (t *bridgeTestTurn) HandleID() string { return "handle-1" }
func (t *bridgeTestTurn) RunID() string    { return "run-1" }
func (t *bridgeTestTurn) TurnID() string   { return "turn-1" }
func (t *bridgeTestTurn) Events() <-chan eventstream.Envelope {
	out := make(chan eventstream.Envelope, 8)
	go func() {
		defer close(out)
		for env := range t.events {
			for _, projected := range acpprojector.ProjectGatewayEventEnvelope(env) {
				out <- projected
			}
		}
	}()
	return out
}
func (t *bridgeTestTurn) SubmitApproval(context.Context, control.ApprovalDecision) error {
	return nil
}
func (t *bridgeTestTurn) Cancel()      {}
func (t *bridgeTestTurn) Close() error { return nil }

func bridgeTurnWithEvents(envs ...gateway.EventEnvelope) control.Turn {
	events := make(chan gateway.EventEnvelope, len(envs))
	for _, env := range envs {
		events <- env
	}
	close(events)
	return &bridgeTestTurn{events: events}
}

func cloneTUIDriverAttachments(items []control.Attachment) []control.Attachment {
	if len(items) == 0 {
		return nil
	}
	return append([]control.Attachment(nil), items...)
}

func participantAssistantEnvelope(scopeID string, actor string, text string) gateway.EventEnvelope {
	return gateway.EventEnvelope{Event: gateway.Event{
		Kind:       gateway.EventKindAssistantMessage,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Origin: &gateway.EventOrigin{
			Scope:   gateway.EventScopeParticipant,
			ScopeID: scopeID,
			Actor:   actor,
		},
		Narrative: &gateway.NarrativePayload{
			Role:  gateway.NarrativeRoleAssistant,
			Actor: actor,
			Text:  text,
			Final: true,
			Scope: gateway.EventScopeParticipant,
		},
	}}
}

type bridgeSubmitDriver struct {
	turn                   control.Turn
	terminalEvents         <-chan gateway.EventEnvelope
	terminalSubscribeCalls int
	submitCalls            int
	lastSubmission         control.Submission
}

func (d *bridgeSubmitDriver) Status(context.Context) (control.StatusSnapshot, error) {
	return control.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) WorkspaceDir() string { return "" }
func (d *bridgeSubmitDriver) Submit(_ context.Context, sub control.Submission) (control.Turn, error) {
	d.submitCalls++
	d.lastSubmission = sub
	return d.turn, nil
}
func (d *bridgeSubmitDriver) SubscribeStream(context.Context, eventstream.Envelope) (<-chan eventstream.Envelope, bool) {
	d.terminalSubscribeCalls++
	if d.terminalEvents == nil {
		return nil, false
	}
	out := make(chan eventstream.Envelope, 8)
	go func() {
		defer close(out)
		for env := range d.terminalEvents {
			for _, projected := range acpprojector.ProjectGatewayEventEnvelope(env) {
				out <- projected
			}
		}
	}()
	return out, true
}
func (d *bridgeSubmitDriver) Interrupt(context.Context) error { return nil }
func (d *bridgeSubmitDriver) NewSession(context.Context) (control.SessionSnapshot, error) {
	return control.SessionSnapshot{}, nil
}
func (d *bridgeSubmitDriver) ResumeSession(context.Context, string) (control.SessionSnapshot, error) {
	return control.SessionSnapshot{}, nil
}
func (d *bridgeSubmitDriver) ListSessions(context.Context, int) ([]control.ResumeCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) ReplayEvents(context.Context) ([]eventstream.Envelope, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) Compact(context.Context) error { return nil }
func (d *bridgeSubmitDriver) Connect(context.Context, control.ConnectConfig) (control.StatusSnapshot, error) {
	return control.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) UseModel(context.Context, string, ...string) (control.StatusSnapshot, error) {
	return control.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) DeleteModel(context.Context, string) error { return nil }
func (d *bridgeSubmitDriver) CycleSessionMode(context.Context) (control.StatusSnapshot, error) {
	return control.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) SetSandboxBackend(context.Context, string) (control.StatusSnapshot, error) {
	return control.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) PrepareSandbox(context.Context) (control.StatusSnapshot, error) {
	return control.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) RepairSandbox(context.Context) (control.StatusSnapshot, error) {
	return control.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) SetSessionMode(context.Context, string) (control.StatusSnapshot, error) {
	return control.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) ListAgents(context.Context, int) ([]control.AgentCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) AgentStatus(context.Context) (control.AgentStatusSnapshot, error) {
	return control.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) AddAgent(context.Context, string) (control.AgentStatusSnapshot, error) {
	return control.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) AddAgentWithOptions(context.Context, string, control.AgentAddOptions) (control.AgentStatusSnapshot, error) {
	return control.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) RemoveAgent(context.Context, string) (control.AgentStatusSnapshot, error) {
	return control.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) HandoffAgent(context.Context, string) (control.AgentStatusSnapshot, error) {
	return control.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) StartAgentSubagent(context.Context, string, string, []control.Attachment) (control.Turn, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) ContinueSubagent(context.Context, string, string, []control.Attachment) (control.Turn, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteMention(context.Context, string, int) ([]control.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteFile(context.Context, string, int) ([]control.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteSkill(context.Context, string, int) ([]control.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteResume(context.Context, string, int) ([]control.ResumeCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteSlashArg(context.Context, string, string, int) ([]control.SlashArgCandidate, error) {
	return nil, nil
}

var _ control.Turn = (*bridgeTestTurn)(nil)
var _ control.Service = (*bridgeSubmitDriver)(nil)

var _ = time.Time{}

func (d *bridgeTestDriver) Status(context.Context) (control.StatusSnapshot, error) {
	return d.status, nil
}

func (d *bridgeLightweightStatusDriver) Status(ctx context.Context) (control.StatusSnapshot, error) {
	d.statusCalls++
	return d.bridgeTestDriver.Status(ctx)
}

func (d *bridgeLightweightStatusDriver) LightweightStatus(context.Context) (control.StatusSnapshot, error) {
	d.lightweightStatusCalls++
	return d.lightweightStatus, nil
}

func (d *bridgeTestDriver) WorkspaceDir() string { return "" }
func (d *bridgeTestDriver) Submit(context.Context, control.Submission) (control.Turn, error) {
	return nil, nil
}
func (d *bridgeTestDriver) Interrupt(context.Context) error { return nil }
func (d *bridgeTestDriver) NewSession(context.Context) (control.SessionSnapshot, error) {
	return d.newSession, nil
}
func (d *bridgeTestDriver) ResumeSession(context.Context, string) (control.SessionSnapshot, error) {
	return d.resumedSession, nil
}
func (d *bridgeTestDriver) ListSessions(context.Context, int) ([]control.ResumeCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) ReplayEvents(context.Context) ([]eventstream.Envelope, error) {
	return projectKernelTestEvents(d.replay), nil
}
func (d *bridgeTestDriver) Compact(context.Context) error {
	d.compactCalls++
	return nil
}
func (d *bridgeTestDriver) Connect(_ context.Context, cfg control.ConnectConfig) (control.StatusSnapshot, error) {
	d.connectCalls++
	d.lastConnect = cfg
	if d.connectStatus.Model != "" || d.connectStatus.Workspace != "" || d.connectStatus.ModeLabel != "" {
		return d.connectStatus, nil
	}
	return d.status, nil
}
func (d *bridgeTestDriver) UseModel(_ context.Context, alias string, reasoningEffort ...string) (control.StatusSnapshot, error) {
	d.useModelCalls++
	d.lastModelAlias = alias
	if len(reasoningEffort) > 0 {
		d.lastReasoningEffort = reasoningEffort[0]
	}
	if d.useModelStatus.Model != "" || d.useModelStatus.Workspace != "" || d.useModelStatus.ModeLabel != "" {
		return d.useModelStatus, nil
	}
	return d.status, nil
}
func (d *bridgeTestDriver) DeleteModel(_ context.Context, alias string) error {
	d.deleteModelCalls++
	d.lastDeletedAlias = alias
	return nil
}
func (d *bridgeTestDriver) CycleSessionMode(context.Context) (control.StatusSnapshot, error) {
	return d.status, nil
}
func (d *bridgeTestDriver) SetSandboxBackend(context.Context, string) (control.StatusSnapshot, error) {
	return d.status, nil
}
func (d *bridgeTestDriver) PrepareSandbox(ctx context.Context) (control.StatusSnapshot, error) {
	d.prepareSandboxCalls++
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Message: "preparing current workspace ACL policy",
		Step:    1,
		Total:   2,
	})
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Message: "debug-only setup detail",
		Debug:   true,
	})
	return d.status, nil
}
func (d *bridgeTestDriver) RepairSandbox(ctx context.Context) (control.StatusSnapshot, error) {
	d.repairSandboxCalls++
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Message: "repairing current workspace ACL policy",
		Step:    1,
		Total:   1,
	})
	return d.status, nil
}
func (d *bridgeTestDriver) ResetSandbox(ctx context.Context) (control.StatusSnapshot, error) {
	d.resetSandboxCalls++
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Message: "removing Windows sandbox ACL state",
		Step:    3,
		Total:   6,
	})
	sandbox.ReportPrepareProgress(ctx, sandbox.PrepareProgress{
		Message: "debug-only reset detail",
		Debug:   true,
	})
	return d.status, nil
}
func (d *bridgeTestDriver) SetSessionMode(context.Context, string) (control.StatusSnapshot, error) {
	return d.status, nil
}
func (d *bridgeTestDriver) ListAgents(context.Context, int) ([]control.AgentCandidate, error) {
	d.listAgentCalls++
	return d.agentList, nil
}
func (d *bridgeTestDriver) AgentStatus(context.Context) (control.AgentStatusSnapshot, error) {
	d.agentStatusCalls++
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) AddAgent(_ context.Context, target string) (control.AgentStatusSnapshot, error) {
	return d.AddAgentWithOptions(context.Background(), target, control.AgentAddOptions{})
}
func (d *bridgeTestDriver) AddAgentWithOptions(_ context.Context, target string, opts control.AgentAddOptions) (control.AgentStatusSnapshot, error) {
	d.addAgentCalls++
	d.lastAddedAgent = target
	d.lastAddOptions = opts
	if d.addAgentErr != nil {
		return control.AgentStatusSnapshot{}, d.addAgentErr
	}
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) RemoveAgent(_ context.Context, target string) (control.AgentStatusSnapshot, error) {
	d.removeAgentCalls++
	d.lastRemovedAgent = target
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) HandoffAgent(_ context.Context, target string) (control.AgentStatusSnapshot, error) {
	d.handoffAgentCalls++
	d.lastHandoffAgent = target
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) StartAgentSubagent(_ context.Context, agent string, prompt string, attachments []control.Attachment) (control.Turn, error) {
	d.lastStartedAgent = agent
	d.lastStartedPrompt = prompt
	d.lastStartedAttachments = cloneTUIDriverAttachments(attachments)
	if d.subagentTurn != nil {
		return d.subagentTurn, nil
	}
	return bridgeTurnWithEvents(), nil
}
func (d *bridgeTestDriver) ContinueSubagent(_ context.Context, handle string, prompt string, attachments []control.Attachment) (control.Turn, error) {
	d.lastContinuedHandle = handle
	d.lastContinuedPrompt = prompt
	d.lastContinuedAttachments = cloneTUIDriverAttachments(attachments)
	if d.subagentTurn != nil {
		return d.subagentTurn, nil
	}
	return bridgeTurnWithEvents(), nil
}
func (d *bridgeTestDriver) CompleteMention(context.Context, string, int) ([]control.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) CompleteFile(context.Context, string, int) ([]control.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) CompleteSkill(context.Context, string, int) ([]control.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) CompleteResume(context.Context, string, int) ([]control.ResumeCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) CompleteSlashArg(_ context.Context, command string, _ string, _ int) ([]control.SlashArgCandidate, error) {
	if d.slashArgCandidates != nil {
		return d.slashArgCandidates[strings.TrimSpace(command)], nil
	}
	return nil, nil
}
