package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
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
		status:     tuidriver.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
		newSession: session.Session{SessionRef: session.SessionRef{SessionID: "new-session"}},
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
	for _, want := range []string{"/agent list | /agent add <builtin> | /agent install <adapter> | /agent use <agent|local> | /agent remove <agent>", "/connect", "/model use <alias> | /model del <alias>", "/compact", "/resume [session-id]"} {
		if !strings.Contains(log.Chunk, want) {
			t.Fatalf("slashHelp() chunk = %q, want substring %q", log.Chunk, want)
		}
	}
}

func TestModeToggleHintUsesRemoteACPModeLabel(t *testing.T) {
	got := modeToggleHint(tuidriver.StatusSnapshot{SessionMode: "review", ModeLabel: "Review"})
	if got != "Review mode enabled" {
		t.Fatalf("modeToggleHint() = %q, want Review mode enabled", got)
	}
}

func TestConfigFromDriverRefreshStatusUsesLightweightStatus(t *testing.T) {
	driver := &bridgeLightweightStatusDriver{
		bridgeTestDriver: bridgeTestDriver{
			status: tuidriver.StatusSnapshot{Model: "full-model", ModeLabel: "full-mode"},
		},
		lightweightStatus: tuidriver.StatusSnapshot{
			Model:               "light-model",
			ModeLabel:           "light-mode",
			TotalTokens:         12,
			ContextWindowTokens: 100,
		},
	}
	cfg := ConfigFromDriver(driver, nil, Config{})
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
	var batcher gatewayTerminalBatcher

	if !batcher.enqueue(testTerminalFrame("hello ", 1), send) {
		t.Fatal("first running frame was not accepted for batching")
	}
	if !batcher.enqueue(testTerminalFrame("world", 2), send) {
		t.Fatal("second running frame was not accepted for batching")
	}
	if len(sent) != 0 {
		t.Fatalf("batcher sent before flush: got %d messages", len(sent))
	}

	batcher.flush(send)
	if len(sent) != 1 {
		t.Fatalf("flush sent %d messages, want 1", len(sent))
	}
	env, ok := sent[0].(kernel.EventEnvelope)
	if !ok {
		t.Fatalf("sent msg = %#v, want EventEnvelope", sent[0])
	}
	if got, _ := gatewayTerminalContent(env); got != "hello world" {
		t.Fatalf("merged text = %q, want hello world", got)
	}
	if len(env.Event.ToolResult.RawOutput) != 0 {
		t.Fatalf("raw output = %#v, want terminal content only", env.Event.ToolResult.RawOutput)
	}
}

func TestGatewayTerminalBatcherMergesCumulativeRunningFrames(t *testing.T) {
	var sent []tea.Msg
	send := func(msg tea.Msg) {
		sent = append(sent, msg)
	}
	var batcher gatewayTerminalBatcher

	if !batcher.enqueue(testTerminalFrameForTool("SPAWN", "Let me write the script.", 1), send) {
		t.Fatal("first running frame was not accepted for batching")
	}
	if !batcher.enqueue(testTerminalFrameForTool("SPAWN", "Let me write the script. Now let me run the script.", 2), send) {
		t.Fatal("second running frame was not accepted for batching")
	}

	batcher.flush(send)
	if len(sent) != 1 {
		t.Fatalf("flush sent %d messages, want 1", len(sent))
	}
	env, ok := sent[0].(kernel.EventEnvelope)
	if !ok {
		t.Fatalf("sent msg = %#v, want EventEnvelope", sent[0])
	}
	want := "Let me write the script. Now let me run the script."
	if got, _ := gatewayTerminalContent(env); got != want {
		t.Fatalf("merged text = %q, want %q", got, want)
	}
}

func TestGatewayTerminalBatcherPreservesCommandPrefixDeltas(t *testing.T) {
	var sent []tea.Msg
	send := func(msg tea.Msg) {
		sent = append(sent, msg)
	}
	var batcher gatewayTerminalBatcher

	if !batcher.enqueue(testTerminalFrame("abc", 1), send) {
		t.Fatal("first running frame was not accepted for batching")
	}
	if !batcher.enqueue(testTerminalFrame("abcdef", 2), send) {
		t.Fatal("second running frame was not accepted for batching")
	}

	batcher.flush(send)
	if len(sent) != 1 {
		t.Fatalf("flush sent %d messages, want 1", len(sent))
	}
	env, ok := sent[0].(kernel.EventEnvelope)
	if !ok {
		t.Fatalf("sent msg = %#v, want EventEnvelope", sent[0])
	}
	if got, _ := gatewayTerminalContent(env); got != "abcabcdef" {
		t.Fatalf("merged RUN_COMMAND text = %q, want both byte deltas preserved", got)
	}
}

func TestGatewayNarrativeBatcherSyncsProtocolUpdateContent(t *testing.T) {
	var sent []tea.Msg
	send := func(msg tea.Msg) {
		sent = append(sent, msg)
	}
	var batcher gatewayNarrativeBatcher

	if !batcher.enqueue(testNarrativeFrame("hello "), send) {
		t.Fatal("first narrative frame was not accepted for batching")
	}
	if !batcher.enqueue(testNarrativeFrame("world"), send) {
		t.Fatal("second narrative frame was not accepted for batching")
	}

	batcher.flush(send)
	if len(sent) != 1 {
		t.Fatalf("flush sent %d messages, want 1", len(sent))
	}
	env, ok := sent[0].(kernel.EventEnvelope)
	if !ok {
		t.Fatalf("sent msg = %#v, want EventEnvelope", sent[0])
	}
	if got := env.Event.Narrative.Text; got != "hello world" {
		t.Fatalf("narrative text = %q, want merged text", got)
	}
	if env.Event.Protocol == nil || env.Event.Protocol.Update == nil {
		t.Fatalf("protocol = %#v, want protocol update", env.Event.Protocol)
	}
	if got := protocolTextContent(env.Event.Protocol.Update.Content); got != "hello world" {
		t.Fatalf("protocol content = %q, want merged text", got)
	}
	events := ProjectGatewayEventToTranscriptEvents(env.Event)
	if len(events) != 1 || events[0].Text != "hello world" {
		t.Fatalf("projected events = %#v, want protocol-first merged text", events)
	}
}

func testTerminalFrame(text string, cursor int64) kernel.EventEnvelope {
	return testTerminalFrameForTool("RUN_COMMAND", text, cursor)
}

func testNarrativeFrame(text string) kernel.EventEnvelope {
	return kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin:     &kernel.EventOrigin{Scope: kernel.EventScopeMain, ScopeID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:       kernel.NarrativeRoleAssistant,
				Text:       text,
				Visibility: string(session.VisibilityUIOnly),
				UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
				Scope:      kernel.EventScopeMain,
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

func testTerminalFrameForTool(toolName string, text string, cursor int64) kernel.EventEnvelope {
	_ = cursor
	return kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			HandleID:   "h1",
			RunID:      "r1",
			TurnID:     "t1",
			SessionRef: session.SessionRef{SessionID: "s1"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "call-1",
				ToolName: toolName,
				Status:   kernel.ToolStatusRunning,
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
		status:     tuidriver.StatusSnapshot{},
		newSession: session.Session{SessionRef: session.SessionRef{SessionID: "new-session"}},
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
		status:         tuidriver.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
		resumedSession: session.Session{SessionRef: session.SessionRef{SessionID: "resumed-session"}},
		replay: []kernel.EventEnvelope{
			{
				Event: kernel.Event{
					Kind:   kernel.EventKindUserMessage,
					TurnID: "turn-complete",
					Narrative: &kernel.NarrativePayload{
						Role: kernel.NarrativeRoleUser,
						Text: "history prompt",
					},
				},
			},
			{
				Event: kernel.Event{
					Kind:   kernel.EventKindToolCall,
					TurnID: "turn-complete",
					ToolCall: &kernel.ToolCallPayload{
						CallID:   "command-1",
						ToolName: "RUN_COMMAND",
						Status:   kernel.ToolStatusRunning,
					},
				},
			},
			{
				Event: kernel.Event{
					Kind:   kernel.EventKindAssistantMessage,
					TurnID: "turn-complete",
					Narrative: &kernel.NarrativePayload{
						Role:       kernel.NarrativeRoleAssistant,
						Text:       "stream chunk",
						Final:      false,
						Visibility: string(session.VisibilityUIOnly),
					},
				},
			},
			{
				Event: kernel.Event{
					Kind:   kernel.EventKindAssistantMessage,
					TurnID: "turn-complete",
					Narrative: &kernel.NarrativePayload{
						Role:  kernel.NarrativeRoleAssistant,
						Text:  "history reply",
						Final: true,
						Scope: kernel.EventScopeMain,
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
		if _, ok := msg.(kernel.EventEnvelope); ok {
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
		status:         tuidriver.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
		resumedSession: session.Session{SessionRef: session.SessionRef{SessionID: "resumed-session"}},
		replay: []kernel.EventEnvelope{
			{
				Event: kernel.Event{
					Kind:   kernel.EventKindUserMessage,
					TurnID: "participant-turn-1",
					Origin: &kernel.EventOrigin{
						Source:  "acp_participant",
						Scope:   kernel.EventScopeParticipant,
						ScopeID: "participant-turn-1",
						Actor:   "@codex",
					},
					Narrative: &kernel.NarrativePayload{
						Role:  kernel.NarrativeRoleUser,
						Text:  "review this change",
						Scope: kernel.EventScopeParticipant,
					},
				},
			},
			{
				Event: kernel.Event{
					Kind:   kernel.EventKindToolCall,
					TurnID: "participant-turn-1",
					Origin: &kernel.EventOrigin{
						Source:  "acp_participant",
						Scope:   kernel.EventScopeParticipant,
						ScopeID: "participant-turn-1",
						Actor:   "@codex",
					},
					ToolCall: &kernel.ToolCallPayload{
						CallID:   "side-command",
						ToolName: "RUN_COMMAND",
						Status:   kernel.ToolStatusCompleted,
						Scope:    kernel.EventScopeParticipant,
					},
				},
			},
			{
				Event: kernel.Event{
					Kind:   kernel.EventKindAssistantMessage,
					TurnID: "participant-turn-1",
					Origin: &kernel.EventOrigin{
						Source:  "acp_participant",
						Scope:   kernel.EventScopeParticipant,
						ScopeID: "participant-turn-1",
						Actor:   "@codex",
					},
					Narrative: &kernel.NarrativePayload{
						Role:  kernel.NarrativeRoleAssistant,
						Actor: "@codex",
						Text:  "review final message",
						Final: true,
						Scope: kernel.EventScopeParticipant,
					},
				},
			},
			{
				Event: kernel.Event{
					Kind:   kernel.EventKindAssistantMessage,
					TurnID: "participant-turn-1",
					Origin: &kernel.EventOrigin{
						Source:  "side_subagent",
						Scope:   kernel.EventScopeSubagent,
						ScopeID: "participant-turn-1",
						Actor:   "@reviewer",
					},
					Narrative: &kernel.NarrativePayload{
						Role:  kernel.NarrativeRoleAssistant,
						Actor: "@reviewer",
						Text:  "scoped final message",
						Final: true,
						Scope: kernel.EventScopeSubagent,
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
		status:         tuidriver.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
		resumedSession: session.Session{SessionRef: session.SessionRef{SessionID: "resumed-session"}},
		replay: []kernel.EventEnvelope{
			{
				Event: kernel.Event{
					Kind:   kernel.EventKindUserMessage,
					TurnID: "turn-interrupted",
					Narrative: &kernel.NarrativePayload{
						Role: kernel.NarrativeRoleUser,
						Text: "history prompt",
					},
				},
			},
			{
				Event: kernel.Event{
					Kind:   kernel.EventKindToolCall,
					TurnID: "turn-interrupted",
					ToolCall: &kernel.ToolCallPayload{
						CallID:   "command-1",
						ToolName: "RUN_COMMAND",
						Status:   kernel.ToolStatusRunning,
					},
				},
			},
			{
				Event: kernel.Event{
					Kind:   kernel.EventKindAssistantMessage,
					TurnID: "turn-interrupted",
					Narrative: &kernel.NarrativePayload{
						Role:       kernel.NarrativeRoleAssistant,
						Text:       "partial answer",
						Final:      true,
						Visibility: string(session.VisibilityMirror),
						Scope:      kernel.EventScopeMain,
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
		events: make(chan kernel.EventEnvelope, 1),
	}
	turn.events <- kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:  kernel.NarrativeRoleAssistant,
				Text:  "direct gateway event",
				Final: true,
				Scope: kernel.EventScopeMain,
			},
		},
	}
	close(turn.events)

	driver := &bridgeSubmitDriver{turn: turn}
	var msgs []tea.Msg
	result := executeLineViaDriver(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaDriver() err = %v", result.Err)
	}
	if len(msgs) != 1 {
		t.Fatalf("executeLineViaDriver() emitted %d msgs, want 1", len(msgs))
	}
	if _, ok := msgs[0].(kernel.EventEnvelope); !ok {
		t.Fatalf("first msg = %#v, want kernel.EventEnvelope", msgs[0])
	}
}

func TestExecuteLineViaDriverCoalescesUIOnlyReasoningBeforeToolEvent(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan kernel.EventEnvelope, 4),
	}
	for _, text := range []string{"think ", "fast ", "now"} {
		turn.events <- kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative: &kernel.NarrativePayload{
					Role:          kernel.NarrativeRoleAssistant,
					ReasoningText: text,
					Visibility:    "ui_only",
					UpdateType:    string(session.ProtocolUpdateTypeAgentThought),
					Scope:         kernel.EventScopeMain,
				},
			},
		}
	}
	turn.events <- kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "READ",
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeMain,
			},
		},
	}
	close(turn.events)

	driver := &bridgeSubmitDriver{turn: turn}
	var msgs []tea.Msg
	result := executeLineViaDriver(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaDriver() err = %v", result.Err)
	}
	if got := len(msgs); got != 2 {
		t.Fatalf("executeLineViaDriver() emitted %d msgs, want 2: %#v", got, msgs)
	}
	reasoning, ok := msgs[0].(kernel.EventEnvelope)
	if !ok || reasoning.Event.Narrative == nil {
		t.Fatalf("first msg = %#v, want coalesced reasoning EventEnvelope", msgs[0])
	}
	if got := reasoning.Event.Narrative.ReasoningText; got != "think fast now" {
		t.Fatalf("coalesced reasoning = %q, want %q", got, "think fast now")
	}
	tool, ok := msgs[1].(kernel.EventEnvelope)
	if !ok || tool.Event.Kind != kernel.EventKindToolCall {
		t.Fatalf("second msg = %#v, want tool event after reasoning flush", msgs[1])
	}
}

func TestExecuteLineViaDriverCoalescesUIOnlyReasoningPreservesLeadingSpaces(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan kernel.EventEnvelope, 7),
	}
	for _, text := range []string{"Now", " let", " me", " verify", " the", " DDL", " matches"} {
		turn.events <- kernel.EventEnvelope{
			Event: kernel.Event{
				Kind:       kernel.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative: &kernel.NarrativePayload{
					Role:          kernel.NarrativeRoleAssistant,
					ReasoningText: text,
					Visibility:    "ui_only",
					UpdateType:    string(session.ProtocolUpdateTypeAgentThought),
					Scope:         kernel.EventScopeMain,
				},
			},
		}
	}
	close(turn.events)

	driver := &bridgeSubmitDriver{turn: turn}
	var msgs []tea.Msg
	result := executeLineViaDriver(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaDriver() err = %v", result.Err)
	}
	if got := len(msgs); got != 1 {
		t.Fatalf("executeLineViaDriver() emitted %d msgs, want 1: %#v", got, msgs)
	}
	reasoning, ok := msgs[0].(kernel.EventEnvelope)
	if !ok || reasoning.Event.Narrative == nil {
		t.Fatalf("first msg = %#v, want coalesced reasoning EventEnvelope", msgs[0])
	}
	if got := reasoning.Event.Narrative.ReasoningText; got != "Now let me verify the DDL matches" {
		t.Fatalf("coalesced reasoning = %q, want boundary spaces preserved", got)
	}
}

func TestExecuteLineViaDriverDoesNotCoalesceReasoningWithAnswerDelta(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan kernel.EventEnvelope, 2),
	}
	turn.events <- kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:          kernel.NarrativeRoleAssistant,
				ReasoningText: "think",
				Visibility:    "ui_only",
				UpdateType:    string(session.ProtocolUpdateTypeAgentThought),
				Scope:         kernel.EventScopeMain,
			},
		},
	}
	turn.events <- kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindAssistantMessage,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Narrative: &kernel.NarrativePayload{
				Role:       kernel.NarrativeRoleAssistant,
				Text:       "answer",
				Visibility: "ui_only",
				UpdateType: string(session.ProtocolUpdateTypeAgentMessage),
				Scope:      kernel.EventScopeMain,
			},
		},
	}
	close(turn.events)

	driver := &bridgeSubmitDriver{turn: turn}
	var msgs []tea.Msg
	result := executeLineViaDriver(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaDriver() err = %v", result.Err)
	}
	if got := len(msgs); got != 2 {
		t.Fatalf("executeLineViaDriver() emitted %d msgs, want 2: %#v", got, msgs)
	}
	first := msgs[0].(kernel.EventEnvelope)
	second := msgs[1].(kernel.EventEnvelope)
	if first.Event.Narrative == nil || first.Event.Narrative.ReasoningText != "think" {
		t.Fatalf("first narrative = %#v, want reasoning", first.Event.Narrative)
	}
	if second.Event.Narrative == nil || second.Event.Narrative.Text != "answer" {
		t.Fatalf("second narrative = %#v, want answer", second.Event.Narrative)
	}
}

func TestExecuteLineViaDriverTreatsUnknownSlashAsUserMessage(t *testing.T) {
	driver := &bridgeSubmitDriver{}
	text := "/rbac/inner/workflow/switch Query 参数"
	result := executeLineViaDriver(driver, &ProgramSender{Send: func(tea.Msg) {}}, Submission{Text: text})
	if result.Err != nil {
		t.Fatalf("executeLineViaDriver() err = %v", result.Err)
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
		events: make(chan kernel.EventEnvelope, 1),
	}
	turn.events <- kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolResult,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "RUN_COMMAND",
				Content:  testTerminalContentWithID("seed\n", "terminal-1"),
				Status:   kernel.ToolStatusRunning,
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
	terminalEvents := make(chan kernel.EventEnvelope, 1)
	terminalEvents <- kernel.EventEnvelope{
		Event: kernel.Event{
			Kind: kernel.EventKindToolResult,
			ToolResult: &kernel.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "RUN_COMMAND",
				Content:  testTerminalContentWithID("streamed\n", "terminal-1"),
				Status:   kernel.ToolStatusRunning,
			},
		},
	}
	close(terminalEvents)

	driver := &bridgeSubmitDriver{turn: turn, terminalEvents: terminalEvents}
	var msgs []tea.Msg
	result := executeLineViaDriver(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaDriver() err = %v", result.Err)
	}
	deadline := time.After(2 * time.Second)
	for {
		var sawStream bool
		for _, msg := range msgs {
			env, ok := msg.(kernel.EventEnvelope)
			if !ok || env.Event.ToolResult == nil {
				continue
			}
			if text, _ := gatewayTerminalContent(env); text == "streamed\n" {
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
		status:         tuidriver.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
		resumedSession: session.Session{SessionRef: session.SessionRef{SessionID: "resumed-session"}},
		replay: []kernel.EventEnvelope{{
			Event: kernel.Event{
				Kind:       kernel.EventKindAssistantMessage,
				SessionRef: session.SessionRef{SessionID: "root-session"},
				Narrative: &kernel.NarrativePayload{
					Role:  kernel.NarrativeRoleAssistant,
					Text:  "history reply",
					Final: true,
					Scope: kernel.EventScopeMain,
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
		status:        tuidriver.StatusSnapshot{Model: "minimax/MiniMax-M1", ModeLabel: "default", Workspace: "/tmp/ws"},
		connectStatus: tuidriver.StatusSnapshot{Model: "minimax/MiniMax-M2", ModeLabel: "default", Workspace: "/tmp/ws"},
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
	if got := formatContextUsageStatus(12600, 88000); got != "ctx 12.6k / 88k · 14%" {
		t.Fatalf("formatContextUsageStatus() = %q, want %q", got, "ctx 12.6k / 88k · 14%")
	}
	if got := formatContextUsageStatus(0, 88000); got != "ctx 0 / 88k · 0%" {
		t.Fatalf("formatContextUsageStatus() zero = %q, want %q", got, "ctx 0 / 88k · 0%")
	}
}

func TestSlashAgentDispatchesPrimarySubcommands(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList: []tuidriver.AgentCandidate{{
			Name:        "copilot",
			Description: "ACP sidecar",
		}},
		agentStatus: tuidriver.AgentStatusSnapshot{
			SessionID:       "sess-1",
			ControllerKind:  "acp",
			ControllerLabel: "copilot",
			Participants: []tuidriver.AgentParticipantSnapshot{{
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
		agentList: []tuidriver.AgentCandidate{{
			Name:        "copilot",
			Description: "local ACP agent",
		}},
		agentStatus: tuidriver.AgentStatusSnapshot{
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
		agentStatus: tuidriver.AgentStatusSnapshot{ControllerKind: "acp"},
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
		slashArgCandidates: map[string][]tuidriver.SlashArgCandidate{
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
		env, ok := msg.(kernel.EventEnvelope)
		if !ok {
			continue
		}
		switch {
		case env.Event.ToolCall != nil:
			call := env.Event.ToolCall
			if call.ToolName == "RUN_COMMAND" &&
				call.Status == kernel.ToolStatusRunning &&
				strings.Contains(fmt.Sprint(call.RawInput["command"]), "npm install --prefix") {
				sawCall = true
			}
		case env.Event.ToolResult != nil:
			toolResult := env.Event.ToolResult
			if toolResult.ToolName == "RUN_COMMAND" &&
				toolResult.Status == kernel.ToolStatusFailed &&
				toolResult.Error &&
				strings.Contains(fmt.Sprint(toolResult.RawOutput["stderr"]), "npm ERR install failed") {
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
	status := tuidriver.AgentStatusSnapshot{
		SessionID:       "session-1",
		ControllerKind:  "kernel",
		ControllerLabel: "local",
		HasActiveTurn:   true,
		Participants: []tuidriver.AgentParticipantSnapshot{
			{
				ID:        "side-001",
				Label:     "@codex",
				AgentName: "codex",
				Kind:      string(session.ParticipantKindACP),
				Role:      string(session.ParticipantRoleSidecar),
				SessionID: "side-session",
			},
		},
		DelegatedParticipants: []tuidriver.AgentParticipantSnapshot{
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
	got := formatStatusSnapshot(tuidriver.StatusSnapshot{
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
		SessionUsageTotal:        kernel.UsageSnapshot{PromptTokens: 12600, CachedInputTokens: 9000, CompletionTokens: 200, ReasoningTokens: 50, TotalTokens: 12800},
		SessionUsageMain:         kernel.UsageSnapshot{PromptTokens: 10000, CachedInputTokens: 7000, CompletionTokens: 150, ReasoningTokens: 30, TotalTokens: 10150},
		SessionUsageSubagents:    kernel.UsageSnapshot{PromptTokens: 2000, CachedInputTokens: 1800, CompletionTokens: 40, ReasoningTokens: 15, TotalTokens: 2040},
		SessionUsageAutoReview:   kernel.UsageSnapshot{PromptTokens: 600, CachedInputTokens: 200, CompletionTokens: 10, ReasoningTokens: 5, TotalTokens: 610},
		SessionInputTokens:       12600,
		SessionCachedInputTokens: 9000,
		SessionOutputTokens:      200,
		SessionReasoningTokens:   50,
		SessionTotalTokens:       12800,
		PermissionGrantCount:     2,
		PermissionReadRootCount:  3,
		PermissionWriteRootCount: 1,
	})
	for _, forbidden := range []string{"status:", "provider:", "model:", "alias:"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("formatStatusSnapshot() = %q, should not contain log-style label %q", got, forbidden)
		}
	}
	for _, want := range []string{"Session", "  Model", "  Mode", "  Token usage", "    Scope", "total", "12,800", "main", "10,150", "sub-agent", "2,040", "auto-review", "610", "Grants     2 approved, read roots 3, write roots 1", "warn: API key is missing", "/tmp/store"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatStatusSnapshot() = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "Token usage:") || strings.Contains(got, "main usage:") {
		t.Fatalf("formatStatusSnapshot() = %q, should use table-style token usage", got)
	}
}

func TestFormatSessionTokenUsageStatusOmitsEmptyBreakdownBuckets(t *testing.T) {
	got := formatSessionTokenUsageStatus(tuidriver.StatusSnapshot{
		SessionUsageTotal: kernel.UsageSnapshot{PromptTokens: 100, CachedInputTokens: 20, CompletionTokens: 10, TotalTokens: 110},
		SessionUsageMain:  kernel.UsageSnapshot{PromptTokens: 100, CachedInputTokens: 20, CompletionTokens: 10, TotalTokens: 110},
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
		agentList:    []tuidriver.AgentCandidate{{Name: "copilot"}},
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
	result = executeLineViaDriver(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "@jeff continue"})
	if result.Err != nil {
		t.Fatalf("handle continuation error = %v", result.Err)
	}
	if driver.lastContinuedHandle != "jeff" || driver.lastContinuedPrompt != "continue" {
		t.Fatalf("continued handle=%q prompt=%q", driver.lastContinuedHandle, driver.lastContinuedPrompt)
	}
	if len(msgs) == 0 {
		t.Fatal("dynamic slash emitted no messages")
	}
}

func TestDynamicAgentSlashStreamsParticipantTurnOutput(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList:    []tuidriver.AgentCandidate{{Name: "copilot"}},
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
		case TranscriptEventsMsg:
			if transcriptEventsContainText(typed.Events, "copilot 子代理") {
				return
			}
		case kernel.EventEnvelope:
			if transcriptEventsContainText(ProjectGatewayEventToTranscriptEvents(typed.Event), "copilot 子代理") {
				return
			}
		}
	}
	t.Fatal("dynamic slash emitted no participant output")
}

func TestDynamicAgentSlashDoesNotRenderRunningOutputPreviewAsAssistantText(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList:    []tuidriver.AgentCandidate{{Name: "codex"}},
		subagentTurn: bridgeTurnWithEvents(participantAssistantEnvelope("task-1", "@iris", "上海今天阴有小雨。")),
	}
	msgs := make(chan tea.Msg, 16)
	result := dispatchSlashCommand(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs <- msg }}, "/codex 查询上海天气")
	if result.Err != nil {
		t.Fatalf("dynamic slash error = %v", result.Err)
	}
	close(msgs)
	for msg := range msgs {
		switch transcript := msg.(type) {
		case TranscriptEventsMsg:
			if transcriptEventsContainText(transcript.Events, "Searching the Web") {
				t.Fatalf("running output preview was rendered as assistant text: %#v", transcript)
			}
			if transcriptEventsContainText(transcript.Events, "上海今天阴有小雨") {
				return
			}
		case kernel.EventEnvelope:
			events := ProjectGatewayEventToTranscriptEvents(transcript.Event)
			if transcriptEventsContainText(events, "Searching the Web") {
				t.Fatalf("running output preview was rendered as assistant text: %#v", transcript)
			}
			if transcriptEventsContainText(events, "上海今天阴有小雨") {
				return
			}
		}
	}
	t.Fatal("final participant output was not rendered")
}

func TestDynamicAgentSlashCompletedTurnKeepsDivider(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList:    []tuidriver.AgentCandidate{{Name: "codex"}},
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
		agentList:    []tuidriver.AgentCandidate{{Name: "codex"}},
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
		switch typed := msg.(type) {
		case TranscriptEventsMsg:
			if transcriptEventsContainText(typed.Events, "上海今天阴有小雨") {
				foundOutput = true
			}
		case kernel.EventEnvelope:
			if transcriptEventsContainText(ProjectGatewayEventToTranscriptEvents(typed.Event), "上海今天阴有小雨") {
				foundOutput = true
			}
		}
	}
	if !foundOutput {
		t.Fatal("participant turn completion emitted no transcript output")
	}
}

func TestDynamicAgentSlashPrefersStructuredParticipantEvents(t *testing.T) {
	env := kernel.EventEnvelope{
		Event: kernel.Event{
			Kind:       kernel.EventKindToolCall,
			SessionRef: session.SessionRef{SessionID: "root-session"},
			Origin: &kernel.EventOrigin{
				Scope:   kernel.EventScopeParticipant,
				ScopeID: "child-1",
				Actor:   "copilot",
			},
			ToolCall: &kernel.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "RUN_COMMAND",
				RawInput: map[string]any{"command": "go test ./surfaces/tui/app/..."},
				Status:   kernel.ToolStatusRunning,
				Scope:    kernel.EventScopeParticipant,
			},
		},
	}
	driver := &bridgeTestDriver{
		agentList:    []tuidriver.AgentCandidate{{Name: "copilot"}},
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
			if transcript, ok := msg.(TranscriptEventsMsg); ok && transcriptEventsContainText(transcript.Events, "working") {
				t.Fatalf("structured frame emitted fallback transcript text: %#v", transcript)
			}
			envMsg, ok := msg.(kernel.EventEnvelope)
			if !ok {
				continue
			}
			if envMsg.Event.ToolCall == nil || envMsg.Event.ToolCall.ToolName != "RUN_COMMAND" {
				t.Fatalf("event envelope = %#v, want RUN_COMMAND tool call", envMsg)
			}
			if envMsg.Event.Origin == nil || envMsg.Event.Origin.Scope != kernel.EventScopeParticipant {
				t.Fatalf("event origin = %#v, want dynamic side ACP participant scope", envMsg.Event.Origin)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for structured participant event")
		}
	}
}

func TestDynamicAgentSlashParticipantTurnEmitsGatewayNarrative(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList:    []tuidriver.AgentCandidate{{Name: "copilot"}},
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
			transcript, ok := msg.(TranscriptEventsMsg)
			if ok && transcriptEventsContainText(transcript.Events, "fallback side output") {
				return
			}
			env, ok := msg.(kernel.EventEnvelope)
			if ok && transcriptEventsContainText(ProjectGatewayEventToTranscriptEvents(env.Event), "fallback side output") {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for fallback side output")
		}
	}
}

func TestSlashConnectParsesEnvironmentVariableSecret(t *testing.T) {
	driver := &bridgeTestDriver{
		connectStatus: tuidriver.StatusSnapshot{Model: "openai/gpt-4o"},
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
		status:         tuidriver.StatusSnapshot{Model: "minimax/MiniMax-M1", ModeLabel: "default", Workspace: "/tmp/ws"},
		useModelStatus: tuidriver.StatusSnapshot{Model: "minimax/MiniMax-M2", ModeLabel: "default", Workspace: "/tmp/ws"},
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
		status:         tuidriver.StatusSnapshot{Model: "deepseek/deepseek-v4-pro", ModeLabel: "default", Workspace: "/tmp/ws"},
		useModelStatus: tuidriver.StatusSnapshot{Model: "deepseek/deepseek-v4-pro [high]", ModeLabel: "default", Workspace: "/tmp/ws"},
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
		status: tuidriver.StatusSnapshot{Model: "minimax/MiniMax-M1", ModeLabel: "default", Workspace: "/tmp/ws"},
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
		status: tuidriver.StatusSnapshot{Workspace: "/tmp/ws"},
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
		status: tuidriver.StatusSnapshot{
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
	for _, want := range []string{"/connect", "warn: API key is missing", "warn: Commands may run on the host", "Auto-Review remains enabled", "/tmp/.caelis"} {
		if !strings.Contains(log.Chunk, want) {
			t.Fatalf("slashStatus() chunk = %q, want substring %q", log.Chunk, want)
		}
	}
}

func TestSlashDoctorShowsReadinessChecklist(t *testing.T) {
	driver := &bridgeTestDriver{
		status: tuidriver.StatusSnapshot{
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
	result := slashDoctorWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) })
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
	status              tuidriver.StatusSnapshot
	connectStatus       tuidriver.StatusSnapshot
	useModelStatus      tuidriver.StatusSnapshot
	newSession          session.Session
	resumedSession      session.Session
	replay              []kernel.EventEnvelope
	connectCalls        int
	useModelCalls       int
	deleteModelCalls    int
	listAgentCalls      int
	agentStatusCalls    int
	addAgentCalls       int
	removeAgentCalls    int
	handoffAgentCalls   int
	prepareSandboxCalls int
	resetSandboxCalls   int
	compactCalls        int
	lastConnect         tuidriver.ConnectConfig
	lastModelAlias      string
	lastReasoningEffort string
	lastDeletedAlias    string
	lastAddedAgent      string
	lastAddOptions      tuidriver.AgentAddOptions
	lastRemovedAgent    string
	lastHandoffAgent    string
	lastStartedAgent    string
	lastStartedPrompt   string
	lastContinuedHandle string
	lastContinuedPrompt string
	subagentTurn        tuidriver.Turn
	agentList           []tuidriver.AgentCandidate
	agentStatus         tuidriver.AgentStatusSnapshot
	addAgentErr         error
	slashArgCandidates  map[string][]tuidriver.SlashArgCandidate
}

type bridgeLightweightStatusDriver struct {
	bridgeTestDriver
	lightweightStatus      tuidriver.StatusSnapshot
	statusCalls            int
	lightweightStatusCalls int
}

type bridgeTestTurn struct {
	events chan kernel.EventEnvelope
}

func (t *bridgeTestTurn) HandleID() string { return "handle-1" }
func (t *bridgeTestTurn) RunID() string    { return "run-1" }
func (t *bridgeTestTurn) TurnID() string   { return "turn-1" }
func (t *bridgeTestTurn) SessionRef() session.SessionRef {
	return session.SessionRef{SessionID: "root-session"}
}
func (t *bridgeTestTurn) Events() <-chan kernel.EventEnvelope { return t.events }
func (t *bridgeTestTurn) Submit(context.Context, kernel.SubmitRequest) error {
	return nil
}
func (t *bridgeTestTurn) Cancel() kernel.CancelResult {
	return kernel.CancelResult{Status: kernel.CancelStatusAlreadyCancelled}
}
func (t *bridgeTestTurn) Close() error { return nil }

func bridgeTurnWithEvents(envs ...kernel.EventEnvelope) tuidriver.Turn {
	events := make(chan kernel.EventEnvelope, len(envs))
	for _, env := range envs {
		events <- env
	}
	close(events)
	return &bridgeTestTurn{events: events}
}

func participantAssistantEnvelope(scopeID string, actor string, text string) kernel.EventEnvelope {
	return kernel.EventEnvelope{Event: kernel.Event{
		Kind:       kernel.EventKindAssistantMessage,
		SessionRef: session.SessionRef{SessionID: "root-session"},
		Origin: &kernel.EventOrigin{
			Scope:   kernel.EventScopeParticipant,
			ScopeID: scopeID,
			Actor:   actor,
		},
		Narrative: &kernel.NarrativePayload{
			Role:  kernel.NarrativeRoleAssistant,
			Actor: actor,
			Text:  text,
			Final: true,
			Scope: kernel.EventScopeParticipant,
		},
	}}
}

type bridgeSubmitDriver struct {
	turn                   tuidriver.Turn
	terminalEvents         <-chan kernel.EventEnvelope
	terminalSubscribeCalls int
	submitCalls            int
	lastSubmission         tuidriver.Submission
}

func (d *bridgeSubmitDriver) Status(context.Context) (tuidriver.StatusSnapshot, error) {
	return tuidriver.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) WorkspaceDir() string { return "" }
func (d *bridgeSubmitDriver) Submit(_ context.Context, sub tuidriver.Submission) (tuidriver.Turn, error) {
	d.submitCalls++
	d.lastSubmission = sub
	return d.turn, nil
}
func (d *bridgeSubmitDriver) SubscribeStream(context.Context, kernel.EventEnvelope) (<-chan kernel.EventEnvelope, bool) {
	d.terminalSubscribeCalls++
	if d.terminalEvents == nil {
		return nil, false
	}
	return d.terminalEvents, true
}
func (d *bridgeSubmitDriver) Interrupt(context.Context) error { return nil }
func (d *bridgeSubmitDriver) NewSession(context.Context) (session.Session, error) {
	return session.Session{}, nil
}
func (d *bridgeSubmitDriver) ResumeSession(context.Context, string) (session.Session, error) {
	return session.Session{}, nil
}
func (d *bridgeSubmitDriver) ListSessions(context.Context, int) ([]tuidriver.ResumeCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) ReplayEvents(context.Context) ([]kernel.EventEnvelope, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) Compact(context.Context) error { return nil }
func (d *bridgeSubmitDriver) Connect(context.Context, tuidriver.ConnectConfig) (tuidriver.StatusSnapshot, error) {
	return tuidriver.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) UseModel(context.Context, string, ...string) (tuidriver.StatusSnapshot, error) {
	return tuidriver.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) DeleteModel(context.Context, string) error { return nil }
func (d *bridgeSubmitDriver) CycleSessionMode(context.Context) (tuidriver.StatusSnapshot, error) {
	return tuidriver.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) SetSandboxBackend(context.Context, string) (tuidriver.StatusSnapshot, error) {
	return tuidriver.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) PrepareSandbox(context.Context) (tuidriver.StatusSnapshot, error) {
	return tuidriver.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) SetSessionMode(context.Context, string) (tuidriver.StatusSnapshot, error) {
	return tuidriver.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) ListAgents(context.Context, int) ([]tuidriver.AgentCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) AgentStatus(context.Context) (tuidriver.AgentStatusSnapshot, error) {
	return tuidriver.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) AddAgent(context.Context, string) (tuidriver.AgentStatusSnapshot, error) {
	return tuidriver.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) AddAgentWithOptions(context.Context, string, tuidriver.AgentAddOptions) (tuidriver.AgentStatusSnapshot, error) {
	return tuidriver.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) RemoveAgent(context.Context, string) (tuidriver.AgentStatusSnapshot, error) {
	return tuidriver.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) HandoffAgent(context.Context, string) (tuidriver.AgentStatusSnapshot, error) {
	return tuidriver.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) StartAgentSubagent(context.Context, string, string) (tuidriver.Turn, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) ContinueSubagent(context.Context, string, string) (tuidriver.Turn, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteMention(context.Context, string, int) ([]tuidriver.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteFile(context.Context, string, int) ([]tuidriver.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteSkill(context.Context, string, int) ([]tuidriver.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteResume(context.Context, string, int) ([]tuidriver.ResumeCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteSlashArg(context.Context, string, string, int) ([]tuidriver.SlashArgCandidate, error) {
	return nil, nil
}

var _ tuidriver.Turn = (*bridgeTestTurn)(nil)
var _ tuidriver.Driver = (*bridgeSubmitDriver)(nil)

var _ = time.Time{}

func (d *bridgeTestDriver) Status(context.Context) (tuidriver.StatusSnapshot, error) {
	return d.status, nil
}

func (d *bridgeLightweightStatusDriver) Status(ctx context.Context) (tuidriver.StatusSnapshot, error) {
	d.statusCalls++
	return d.bridgeTestDriver.Status(ctx)
}

func (d *bridgeLightweightStatusDriver) LightweightStatus(context.Context) (tuidriver.StatusSnapshot, error) {
	d.lightweightStatusCalls++
	return d.lightweightStatus, nil
}

func (d *bridgeTestDriver) WorkspaceDir() string { return "" }
func (d *bridgeTestDriver) Submit(context.Context, tuidriver.Submission) (tuidriver.Turn, error) {
	return nil, nil
}
func (d *bridgeTestDriver) Interrupt(context.Context) error { return nil }
func (d *bridgeTestDriver) NewSession(context.Context) (session.Session, error) {
	return d.newSession, nil
}
func (d *bridgeTestDriver) ResumeSession(context.Context, string) (session.Session, error) {
	return d.resumedSession, nil
}
func (d *bridgeTestDriver) ListSessions(context.Context, int) ([]tuidriver.ResumeCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) ReplayEvents(context.Context) ([]kernel.EventEnvelope, error) {
	return d.replay, nil
}
func (d *bridgeTestDriver) Compact(context.Context) error {
	d.compactCalls++
	return nil
}
func (d *bridgeTestDriver) Connect(_ context.Context, cfg tuidriver.ConnectConfig) (tuidriver.StatusSnapshot, error) {
	d.connectCalls++
	d.lastConnect = cfg
	if d.connectStatus.Model != "" || d.connectStatus.Workspace != "" || d.connectStatus.ModeLabel != "" {
		return d.connectStatus, nil
	}
	return d.status, nil
}
func (d *bridgeTestDriver) UseModel(_ context.Context, alias string, reasoningEffort ...string) (tuidriver.StatusSnapshot, error) {
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
func (d *bridgeTestDriver) CycleSessionMode(context.Context) (tuidriver.StatusSnapshot, error) {
	return d.status, nil
}
func (d *bridgeTestDriver) SetSandboxBackend(context.Context, string) (tuidriver.StatusSnapshot, error) {
	return d.status, nil
}
func (d *bridgeTestDriver) PrepareSandbox(ctx context.Context) (tuidriver.StatusSnapshot, error) {
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
func (d *bridgeTestDriver) ResetSandbox(ctx context.Context) (tuidriver.StatusSnapshot, error) {
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
func (d *bridgeTestDriver) SetSessionMode(context.Context, string) (tuidriver.StatusSnapshot, error) {
	return d.status, nil
}
func (d *bridgeTestDriver) ListAgents(context.Context, int) ([]tuidriver.AgentCandidate, error) {
	d.listAgentCalls++
	return d.agentList, nil
}
func (d *bridgeTestDriver) AgentStatus(context.Context) (tuidriver.AgentStatusSnapshot, error) {
	d.agentStatusCalls++
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) AddAgent(_ context.Context, target string) (tuidriver.AgentStatusSnapshot, error) {
	return d.AddAgentWithOptions(context.Background(), target, tuidriver.AgentAddOptions{})
}
func (d *bridgeTestDriver) AddAgentWithOptions(_ context.Context, target string, opts tuidriver.AgentAddOptions) (tuidriver.AgentStatusSnapshot, error) {
	d.addAgentCalls++
	d.lastAddedAgent = target
	d.lastAddOptions = opts
	if d.addAgentErr != nil {
		return tuidriver.AgentStatusSnapshot{}, d.addAgentErr
	}
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) RemoveAgent(_ context.Context, target string) (tuidriver.AgentStatusSnapshot, error) {
	d.removeAgentCalls++
	d.lastRemovedAgent = target
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) HandoffAgent(_ context.Context, target string) (tuidriver.AgentStatusSnapshot, error) {
	d.handoffAgentCalls++
	d.lastHandoffAgent = target
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) StartAgentSubagent(_ context.Context, agent string, prompt string) (tuidriver.Turn, error) {
	d.lastStartedAgent = agent
	d.lastStartedPrompt = prompt
	if d.subagentTurn != nil {
		return d.subagentTurn, nil
	}
	return bridgeTurnWithEvents(), nil
}
func (d *bridgeTestDriver) ContinueSubagent(_ context.Context, handle string, prompt string) (tuidriver.Turn, error) {
	d.lastContinuedHandle = handle
	d.lastContinuedPrompt = prompt
	if d.subagentTurn != nil {
		return d.subagentTurn, nil
	}
	return bridgeTurnWithEvents(), nil
}
func (d *bridgeTestDriver) CompleteMention(context.Context, string, int) ([]tuidriver.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) CompleteFile(context.Context, string, int) ([]tuidriver.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) CompleteSkill(context.Context, string, int) ([]tuidriver.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) CompleteResume(context.Context, string, int) ([]tuidriver.ResumeCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) CompleteSlashArg(_ context.Context, command string, _ string, _ int) ([]tuidriver.SlashArgCandidate, error) {
	if d.slashArgCandidates != nil {
		return d.slashArgCandidates[strings.TrimSpace(command)], nil
	}
	return nil, nil
}
