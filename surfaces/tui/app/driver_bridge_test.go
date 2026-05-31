package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/kernel"
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
	for _, want := range []string{"/agent <action>", "actions: list, add <builtin>", "/connect", "/model <action>", "actions: use <alias>, del <alias>", "/compact", "/resume [session-id]", "Shortcuts", "Paste clipboard image"} {
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

func TestSlashTaskListUsesSharedCommandExecutor(t *testing.T) {
	driver := &bridgeTestDriver{
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "task", Output: "tasks:\n  task-1  running  SPAWN reviewer  source=history"},
	}
	var msgs []tea.Msg
	result := dispatchSlashCommand(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, "/task list")
	if !result.SuppressTurnDivider || driver.commandCalls != 1 || driver.lastCommandInput != "/task list" {
		t.Fatalf("task list result = %#v calls=%d input=%q, want shared command execution", result, driver.commandCalls, driver.lastCommandInput)
	}
	if !noticeMessagesContain(msgs, "task-1") || !noticeMessagesContain(msgs, "SPAWN reviewer") || !noticeMessagesContain(msgs, "source=history") {
		t.Fatalf("task list messages = %#v, want task summary", msgs)
	}
}

func TestSlashTaskControlsUseSharedCommandExecutor(t *testing.T) {
	driver := &bridgeTestDriver{
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "task", Output: "task task-1: running\n  title: Echo Task\n  stdout:\n    ready"},
	}
	var msgs []tea.Msg
	send := func(msg tea.Msg) { msgs = append(msgs, msg) }

	waited := dispatchSlashCommand(driver, &ProgramSender{Send: send}, "/task wait task-1 2s")
	if waited.Err != nil || driver.commandCalls != 1 || driver.lastCommandInput != "/task wait task-1 2s" {
		t.Fatalf("task wait result=%#v calls=%d input=%q, want shared task wait command", waited, driver.commandCalls, driver.lastCommandInput)
	}
	wrote := dispatchSlashCommand(driver, &ProgramSender{Send: send}, "/task write task-1 -- ping")
	if wrote.Err != nil || driver.commandCalls != 2 || driver.lastCommandInput != "/task write task-1 -- ping" {
		t.Fatalf("task write result=%#v calls=%d input=%q, want shared task write command", wrote, driver.commandCalls, driver.lastCommandInput)
	}
	cancelled := dispatchSlashCommand(driver, &ProgramSender{Send: send}, "/task cancel task-1")
	if cancelled.Err != nil || driver.commandCalls != 3 || driver.lastCommandInput != "/task cancel task-1" {
		t.Fatalf("task cancel result=%#v calls=%d input=%q, want shared task cancel command", cancelled, driver.commandCalls, driver.lastCommandInput)
	}
	if !noticeMessagesContain(msgs, "ready") || !noticeMessagesContain(msgs, "Echo Task") {
		t.Fatalf("task control messages = %#v, want task output", msgs)
	}
}

func TestSlashSettingsUsesSharedCommandExecutor(t *testing.T) {
	driver := &bridgeTestDriver{
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "settings", Output: "settings:\n  diagnostics:\n    [warning] model/configuration: no model is configured\n  actions:\n    model.connect - Connect model (enabled)"},
	}
	var msgs []tea.Msg
	send := func(msg tea.Msg) { msgs = append(msgs, msg) }

	result := dispatchSlashCommand(driver, &ProgramSender{Send: send}, "/settings")
	if result.Err != nil || !result.SuppressTurnDivider {
		t.Fatalf("settings result = %#v, want handled shared command", result)
	}
	if driver.commandCalls != 1 || driver.lastCommandInput != "/settings" {
		t.Fatalf("settings command calls=%d input=%q, want shared /settings command", driver.commandCalls, driver.lastCommandInput)
	}
	if !noticeMessagesContain(msgs, "settings:") || !noticeMessagesContain(msgs, "model.connect") {
		t.Fatalf("settings messages = %#v, want shared settings output", msgs)
	}

	result = dispatchSlashCommand(driver, &ProgramSender{Send: send}, "/settings run sandbox.prepare")
	if result.Err != nil || driver.commandCalls != 2 || driver.lastCommandInput != "/settings run sandbox.prepare" {
		t.Fatalf("settings action result=%#v calls=%d input=%q, want shared settings action", result, driver.commandCalls, driver.lastCommandInput)
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
					Kind:   kernel.EventKindPlanUpdate,
					TurnID: "turn-complete",
					Plan: &kernel.PlanPayload{Entries: []kernel.PlanEntryPayload{
						{Content: "Inspect stored session", Status: "completed"},
						{Content: "Continue migration", Status: "in_progress"},
					}},
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
	var sawPlanReplay bool
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
				if event.Kind == TranscriptEventPlan && len(event.PlanEntries) == 2 && event.PlanEntries[1].Content == "Continue migration" {
					sawPlanReplay = true
				}
			}
		}
	}
	if replayBatchCount != 1 {
		t.Fatalf("slashResume() replay batches = %d, want 1", replayBatchCount)
	}
	if !sawUserReplay || !sawAssistantReplay || !sawPlanReplay {
		t.Fatalf("slashResume() messages = %#v, want user, plan, and final assistant replay", msgs)
	}
}

func TestSlashResumePrefersAppSessionEventReplay(t *testing.T) {
	driver := &bridgeAppReplayDriver{
		bridgeTestDriver: bridgeTestDriver{
			status:         tuidriver.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
			resumedSession: session.Session{SessionRef: session.SessionRef{SessionID: "resumed-session"}},
			replay: []kernel.EventEnvelope{{
				Event: kernel.Event{
					Kind: kernel.EventKindUserMessage,
					Narrative: &kernel.NarrativePayload{
						Role: kernel.NarrativeRoleUser,
						Text: "legacy prompt",
					},
				},
			}},
		},
		appReplay: []appviewmodel.SessionEventEnvelope{
			appviewmodel.EventEnvelopeFromSession("app-user", coresession.Event{
				ID:        "app-user",
				SessionID: "resumed-session",
				Type:      coresession.EventUser,
				Message:   coreTextMessage(coremodel.RoleUser, "app prompt"),
			}),
			appviewmodel.EventEnvelopeFromSession("app-assistant", coresession.Event{
				ID:        "app-assistant",
				SessionID: "resumed-session",
				Type:      coresession.EventAssistant,
				Message:   coreTextMessage(coremodel.RoleAssistant, "app reply"),
			}),
		},
	}

	var msgs []tea.Msg
	slashResume(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "resumed-session")

	if driver.appReplayCalls != 1 {
		t.Fatalf("ReplaySessionEvents calls = %d, want 1", driver.appReplayCalls)
	}
	if driver.replayCalls != 0 {
		t.Fatalf("ReplayEvents calls = %d, want 0 when app replay succeeds", driver.replayCalls)
	}
	if !transcriptBatchContainsText(msgs, "app prompt") || !transcriptBatchContainsText(msgs, "app reply") {
		t.Fatalf("slashResume() messages = %#v, want app replay transcript", msgs)
	}
	if transcriptBatchContainsText(msgs, "legacy prompt") {
		t.Fatalf("slashResume() messages = %#v, should not use legacy replay fallback", msgs)
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

func TestExecuteLineViaDriverPrefersAppSessionEventStream(t *testing.T) {
	legacyEvents := make(chan kernel.EventEnvelope, 1)
	legacyEvents <- kernel.EventEnvelope{Event: kernel.Event{
		Kind: kernel.EventKindUserMessage,
		Narrative: &kernel.NarrativePayload{
			Role: kernel.NarrativeRoleUser,
			Text: "legacy stream",
		},
	}}
	close(legacyEvents)
	appEvents := make(chan appviewmodel.SessionEventEnvelope, 1)
	appEvents <- appviewmodel.EventEnvelopeFromSession("app-stream", coresession.Event{
		ID:        "app-stream",
		SessionID: "root-session",
		Type:      coresession.EventAssistant,
		Message:   coreTextMessage(coremodel.RoleAssistant, "app stream"),
	})
	close(appEvents)
	turn := &bridgeAppEventTurn{
		bridgeTestTurn: &bridgeTestTurn{events: legacyEvents},
		appEvents:      appEvents,
	}

	driver := &bridgeSubmitDriver{turn: turn}
	var msgs []tea.Msg
	result := executeLineViaDriver(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaDriver() err = %v", result.Err)
	}
	if turn.eventsCalls != 0 {
		t.Fatalf("legacy Events() calls = %d, want 0 when app event stream is available", turn.eventsCalls)
	}
	if len(msgs) != 1 {
		t.Fatalf("executeLineViaDriver() emitted %d msgs, want 1", len(msgs))
	}
	transcript, ok := msgs[0].(TranscriptEventsMsg)
	if !ok || !transcriptEventsContainText(transcript.Events, "app stream") {
		t.Fatalf("first msg = %#v, want app session event projected to transcript", msgs[0])
	}
	if transcriptEventsContainText(transcript.Events, "legacy stream") {
		t.Fatalf("executeLineViaDriver() used legacy event stream transcript")
	}
}

func TestExecuteLineViaDriverProjectsAppToolEventOnce(t *testing.T) {
	appEvents := make(chan appviewmodel.SessionEventEnvelope, 1)
	appEvents <- appviewmodel.EventEnvelopeFromSession("tool-result", coresession.Event{
		ID:        "tool-result",
		SessionID: "root-session",
		Type:      coresession.EventToolResult,
		Tool: &coresession.ToolEvent{
			ID:     "call-1",
			Name:   "RUN_COMMAND",
			Kind:   "execute",
			Status: coresession.ToolCompleted,
			Input:  map[string]any{"command": "printf ok"},
			Content: []coresession.ToolContent{{
				Type: "text",
				Text: "ok\n",
			}},
		},
	})
	close(appEvents)
	turn := &bridgeAppEventTurn{
		bridgeTestTurn: &bridgeTestTurn{},
		appEvents:      appEvents,
	}

	driver := &bridgeSubmitDriver{turn: turn}
	var msgs []tea.Msg
	result := executeLineViaDriver(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "hello"})
	if result.Err != nil {
		t.Fatalf("executeLineViaDriver() err = %v", result.Err)
	}
	if len(msgs) != 1 {
		t.Fatalf("executeLineViaDriver() emitted %d msgs, want one direct transcript projection", len(msgs))
	}
	transcript, ok := msgs[0].(TranscriptEventsMsg)
	if !ok || len(transcript.Events) != 1 {
		t.Fatalf("first msg = %#v, want one transcript event", msgs[0])
	}
	if got := transcript.Events[0]; got.ToolName != "RUN_COMMAND" || got.ToolOutput != "ok\n" {
		t.Fatalf("tool transcript = %#v, want direct core tool projection", got)
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
		status:      tuidriver.StatusSnapshot{Model: "minimax/MiniMax-M2", ModeLabel: "default", Workspace: "/tmp/ws"},
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "connect", Output: "connected: minimax/MiniMax-M2"},
	}
	var msgs []tea.Msg
	slashConnect(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "minimax MiniMax-M2 - 60 sk-test 204800 8192 low,medium")
	if driver.commandCalls != 1 || driver.lastCommandInput != "/connect minimax MiniMax-M2 - 60 sk-test 204800 8192 low,medium" {
		t.Fatalf("command calls=%d input=%q, want shared /connect command", driver.commandCalls, driver.lastCommandInput)
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
		commandCatalog: tuidriver.CommandCatalogView{Commands: []tuidriver.CommandView{{Name: "agent"}}},
		commandViews: map[string]tuidriver.CommandExecutionView{
			"/agent list":           {Handled: true, Command: "agent", Output: "agents:\n  copilot"},
			"/agent add copilot":    {Handled: true, Command: "agent", Output: "agent registered: copilot"},
			"/agent remove copilot": {Handled: true, Command: "agent", Output: "agent removed: copilot"},
			"/agent use copilot":    {Handled: true, Command: "agent", Output: "agent controller: copilot"},
		},
	}
	var msgs []tea.Msg
	slashAgent(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "list")
	slashAgent(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "add copilot")
	slashAgent(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "remove copilot")
	slashAgent(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "use copilot")
	wantInputs := []string{"/agent list", "/agent add copilot", "/agent remove copilot", "/agent use copilot"}
	if got := strings.Join(driver.commandInputs, "\n"); got != strings.Join(wantInputs, "\n") {
		t.Fatalf("agent command inputs = %#v, want %#v", driver.commandInputs, wantInputs)
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

func TestSlashCommandsPreferSharedCommandCatalog(t *testing.T) {
	driver := &bridgeTestDriver{
		commandCatalog: tuidriver.CommandCatalogView{Commands: []tuidriver.CommandView{
			{Name: "status"},
			{Name: "reviewer"},
		}},
		agentList: []tuidriver.AgentCandidate{{Name: "legacy-agent"}},
	}
	commands := appendAgentSlashCommandsWithContext(context.Background(), driver, []string{"help", "status"})
	if !stringSliceContains(commands, "reviewer") {
		t.Fatalf("commands = %#v, want command from shared catalog", commands)
	}
	if stringSliceContains(commands, "legacy-agent") {
		t.Fatalf("commands = %#v, should not fall back to TUI agent list when catalog is available", commands)
	}
	if driver.commandCatalogCalls != 1 {
		t.Fatalf("commandCatalogCalls = %d, want 1", driver.commandCatalogCalls)
	}
	if driver.listAgentCalls != 0 {
		t.Fatalf("listAgentCalls = %d, want 0 with shared command catalog", driver.listAgentCalls)
	}
}

func TestSlashModelDeleteDisabledForACPController(t *testing.T) {
	driver := &bridgeTestDriver{
		agentStatus: tuidriver.AgentStatusSnapshot{ControllerKind: "acp"},
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "model", Output: "usage: /model use <model> [effort]"},
	}
	var msgs []tea.Msg
	slashModelWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "del minimax/MiniMax-M1")
	if driver.commandCalls != 1 || driver.lastCommandInput != "/model del minimax/MiniMax-M1" {
		t.Fatalf("command calls=%d input=%q, want shared /model command", driver.commandCalls, driver.lastCommandInput)
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
	driver := &bridgeTestDriver{
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "agent", Output: "agent installed: claude"},
	}
	var msgs []tea.Msg
	result := slashAgentWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "install claude")
	if result.Err != nil {
		t.Fatalf("slashAgentWithContext(install) error = %v", result.Err)
	}
	if driver.commandCalls != 1 || driver.lastCommandInput != "/agent install claude" {
		t.Fatalf("agent install command calls=%d input=%q, want shared command", driver.commandCalls, driver.lastCommandInput)
	}
	if len(msgs) == 0 {
		t.Fatal("slashAgentWithContext(install) emitted no messages")
	}
}

func TestSlashAgentUpdatePassesInstallOptions(t *testing.T) {
	driver := &bridgeTestDriver{
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "agent", Output: "agent updated: claude"},
	}
	var msgs []tea.Msg
	result := slashAgentWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "update claude")
	if result.Err != nil {
		t.Fatalf("slashAgentWithContext(update) error = %v", result.Err)
	}
	if driver.commandCalls != 1 || driver.lastCommandInput != "/agent update claude" {
		t.Fatalf("agent update command calls=%d input=%q, want shared command", driver.commandCalls, driver.lastCommandInput)
	}
	if !noticeMessagesContain(msgs, "agent updated: claude") {
		t.Fatalf("slashAgentWithContext(update) messages = %#v, want update notice", msgs)
	}
}

func TestSlashAgentAddCustomPassesConfig(t *testing.T) {
	driver := &bridgeTestDriver{
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "agent", Output: "agent registered: helper"},
	}
	var msgs []tea.Msg
	result := slashAgentWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "add custom helper -- helper-acp --stdio --model test")
	if result.Err != nil {
		t.Fatalf("slashAgentWithContext(add custom) error = %v", result.Err)
	}
	if driver.commandCalls != 1 || driver.lastCommandInput != "/agent add custom helper -- helper-acp --stdio --model test" {
		t.Fatalf("agent add custom command calls=%d input=%q, want shared command", driver.commandCalls, driver.lastCommandInput)
	}
	if len(msgs) == 0 || !noticeMessagesContain(msgs, "agent registered: helper") {
		t.Fatalf("slashAgentWithContext(add custom) messages = %#v, want registration notice", msgs)
	}
}

func TestSlashAgentInstallFailureComesFromSharedCommand(t *testing.T) {
	driver := &bridgeTestDriver{
		commandErr: fmt.Errorf("app/services: install ACP agent %q: exit status 7\nnpm ERR install failed", "claude"),
	}
	var msgs []tea.Msg
	result := slashAgentWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "install claude")
	if result.Err == nil {
		t.Fatal("slashAgentWithContext(install failure) error = nil, want failure")
	}
	if driver.commandCalls != 1 || driver.lastCommandInput != "/agent install claude" {
		t.Fatalf("agent install command calls=%d input=%q, want shared command", driver.commandCalls, driver.lastCommandInput)
	}
	if len(msgs) != 0 {
		t.Fatalf("install failure messages = %#v, want no TUI-owned tool-call wrapper", msgs)
	}
}

func TestSlashArgQueryAgentInstallAndUpdate(t *testing.T) {
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
	command, query, ok = slashArgQueryAtEnd([]rune("/agent update c"))
	if !ok {
		t.Fatal("slashArgQueryAtEnd(/agent update c) ok = false")
	}
	if command != "agent update" || query != "c" {
		t.Fatalf("slashArgQueryAtEnd(/agent update c) = command %q query %q, want agent update / c", command, query)
	}
	command, query, ok = slashArgQueryAtEnd([]rune("/agent update "))
	if !ok {
		t.Fatal("slashArgQueryAtEnd(/agent update ) ok = false")
	}
	if command != "agent update" || query != "" {
		t.Fatalf("slashArgQueryAtEnd(/agent update ) = command %q query %q, want agent update / empty", command, query)
	}
}

func TestAgentInstallSlashArgFallbackIsExecutable(t *testing.T) {
	if !isExecutableSlashArgInput("/agent install claude") {
		t.Fatal("isExecutableSlashArgInput(/agent install claude) = false, want true")
	}
	if !isExecutableSlashArgInput("/agent update claude") {
		t.Fatal("isExecutableSlashArgInput(/agent update claude) = false, want true")
	}
}

func TestSlashAgentHelpAndRecovery(t *testing.T) {
	driver := &bridgeTestDriver{commandErr: fmt.Errorf("app/services: usage: /agent remove <agent>")}
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
		SessionUsageTotal:        appviewmodel.TokenUsage{InputTokens: 12600, CachedInputTokens: 9000, OutputTokens: 200, ReasoningTokens: 50, TotalTokens: 12800},
		SessionUsageMain:         appviewmodel.TokenUsage{InputTokens: 10000, CachedInputTokens: 7000, OutputTokens: 150, ReasoningTokens: 30, TotalTokens: 10150},
		SessionUsageSubagents:    appviewmodel.TokenUsage{InputTokens: 2000, CachedInputTokens: 1800, OutputTokens: 40, ReasoningTokens: 15, TotalTokens: 2040},
		SessionUsageAutoReview:   appviewmodel.TokenUsage{InputTokens: 600, CachedInputTokens: 200, OutputTokens: 10, ReasoningTokens: 5, TotalTokens: 610},
		SessionInputTokens:       12600,
		SessionCachedInputTokens: 9000,
		SessionOutputTokens:      200,
		SessionReasoningTokens:   50,
		SessionTotalTokens:       12800,
		PermissionGrantCount:     2,
		PermissionReadRootCount:  3,
		PermissionWriteRootCount: 1,
	})
	for _, forbidden := range []string{"Status", "Tokens", "Warnings", "status:", "provider:", "model:", "alias:", "Provider:", "Store:", "\n  Reason:", "Session"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("formatStatusSnapshot() = %q, should not contain log-style label %q", got, forbidden)
		}
	}
	for _, want := range []string{"  Model:", "  Mode:", "  Sandbox:", "  Workspace:", "  Scope", "-----", "total", "12,800", "main", "10,150", "sub-agent", "2,040", "auto-review", "610", "Grants:", "2 approved, read roots 3, write roots 1", "Warning:", "API key is missing"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatStatusSnapshot() = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "Token usage:") || strings.Contains(got, "main usage:") || strings.Contains(got, "warn:") {
		t.Fatalf("formatStatusSnapshot() = %q, should use table-style token usage", got)
	}
	grantsIdx := strings.Index(got, "Grants:")
	warningIdx := strings.Index(got, "Warning:")
	usageIdx := strings.Index(got, "  Scope")
	if grantsIdx < 0 || warningIdx < 0 || usageIdx < 0 || grantsIdx >= warningIdx || warningIdx >= usageIdx {
		t.Fatalf("formatStatusSnapshot() = %q, want grants and warnings before token usage table", got)
	}
	if tail := got[usageIdx:]; strings.Contains(tail, "Grants:") || strings.Contains(tail, "Warning:") {
		t.Fatalf("formatStatusSnapshot() = %q, token usage table should be the final section", got)
	}
	if !strings.Contains(got, "\n\n  Scope") {
		t.Fatalf("formatStatusSnapshot() = %q, want blank line before token usage table", got)
	}
}

func TestFormatStatusSnapshotOmitsSetupReasonDetails(t *testing.T) {
	got := formatStatusSnapshot(tuidriver.StatusSnapshot{
		Model:                       "mimo-v2.5-pro [high]",
		ModeLabel:                   "auto-review",
		SandboxResolvedBackend:      "windows",
		Route:                       "sandbox",
		Workspace:                   "D:\\xue\\code\\storage",
		SandboxWorkspaceSetupReason: "workspace ACL manifest is stale and will be repaired lazily",
		SandboxSetupMarkerReason:    "stale sandbox setup marker",
	})
	for _, forbidden := range []string{"Status", "Tokens", "\n  Reason:", "workspace ACL manifest", "stale sandbox setup marker", "Store:", "Provider:", "Session"} {
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
	got := formatStatusSnapshot(tuidriver.StatusSnapshot{
		Model:                  "mimo-v2.5-pro [high]",
		ModeLabel:              "auto-review",
		SandboxResolvedBackend: "windows",
		Route:                  "sandbox",
		Workspace:              "D:\\xue\\code\\cmpctl",
		SandboxSetup: sandbox.SetupStatus{Checks: []sandbox.SetupCheck{{
			Name:     "workspace",
			Scope:    sandbox.SetupWorkspace,
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
	got := formatSessionTokenUsageStatus(tuidriver.StatusSnapshot{
		SessionUsageTotal: appviewmodel.TokenUsage{InputTokens: 100, CachedInputTokens: 20, OutputTokens: 10, TotalTokens: 110},
		SessionUsageMain:  appviewmodel.TokenUsage{InputTokens: 100, CachedInputTokens: 20, OutputTokens: 10, TotalTokens: 110},
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
		commandView:  dynamicAgentCommandView("copilot", participantAssistantCoreEvent("task-1", "@jeff", "child ok")),
		subagentTurn: bridgeTurnWithEvents(participantAssistantEnvelope("task-1", "@jeff", "child ok")),
	}
	var msgs []tea.Msg
	result := dispatchSlashCommand(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, "/copilot inspect")
	if result.Err != nil {
		t.Fatalf("dynamic slash error = %v", result.Err)
	}
	if driver.commandCalls != 1 || driver.lastCommandInput != "/copilot inspect" {
		t.Fatalf("dynamic slash command calls=%d input=%q, want shared command", driver.commandCalls, driver.lastCommandInput)
	}
	result = executeLineViaDriver(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{Text: "@jeff continue"})
	if result.Err != nil {
		t.Fatalf("handle continuation error = %v", result.Err)
	}
	if driver.lastContinuedHandle != "jeff" || driver.lastContinuedPrompt != "continue" {
		t.Fatalf("continued handle=%q prompt=%q", driver.lastContinuedHandle, driver.lastContinuedPrompt)
	}
	result = executeLineViaDriver(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{
		Text:        "/copilot see this",
		Attachments: []Attachment{{Name: "shot.png", Offset: len([]rune("/copilot see "))}},
	})
	if result.Err != nil {
		t.Fatalf("dynamic slash with attachment error = %v", result.Err)
	}
	if len(driver.lastCommandAttachments) != 1 || driver.lastCommandAttachments[0].Name != "shot.png" || driver.lastCommandAttachments[0].Offset != len([]rune("see ")) {
		t.Fatalf("command attachments = %#v, want prompt-relative image attachment", driver.lastCommandAttachments)
	}
	result = executeLineViaDriver(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs = append(msgs, msg) }}, Submission{
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
		agentList:   []tuidriver.AgentCandidate{{Name: "copilot"}},
		commandView: dynamicAgentCommandView("copilot", participantAssistantCoreEvent("task-1", "@mike", "我是 copilot 子代理")),
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
		agentList:   []tuidriver.AgentCandidate{{Name: "codex"}},
		commandView: dynamicAgentCommandView("codex", participantAssistantCoreEvent("task-1", "@iris", "上海今天阴有小雨。")),
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
		agentList:   []tuidriver.AgentCandidate{{Name: "codex"}},
		commandView: dynamicAgentCommandView("codex", participantAssistantCoreEvent("task-1", "@kate", "上海今天阴有小雨。")),
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
		agentList:   []tuidriver.AgentCandidate{{Name: "codex"}},
		commandView: dynamicAgentCommandView("codex", participantAssistantCoreEvent("task-1:1", "@kate", "上海今天阴有小雨。")),
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
	driver := &bridgeTestDriver{
		agentList:   []tuidriver.AgentCandidate{{Name: "copilot"}},
		commandView: dynamicAgentCommandView("copilot", participantToolCallCoreEvent("child-1", "RUN_COMMAND", map[string]any{"command": "go test ./surfaces/tui/app/..."})),
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
		agentList:   []tuidriver.AgentCandidate{{Name: "copilot"}},
		commandView: dynamicAgentCommandView("copilot", participantAssistantCoreEvent("task-1", "@mike", "fallback side output")),
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

func TestSlashConnectPassesEnvironmentVariableSecretToSharedCommand(t *testing.T) {
	driver := &bridgeTestDriver{
		status:      tuidriver.StatusSnapshot{Model: "openai/gpt-4o"},
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "connect", Output: "connected: openai/gpt-4o"},
	}
	slashConnect(driver, func(tea.Msg) {}, "openai gpt-4o - 60 env:OPENAI_API_KEY")
	if got := driver.lastCommandInput; got != "/connect openai gpt-4o - 60 env:OPENAI_API_KEY" {
		t.Fatalf("lastCommandInput = %q, want env secret preserved for shared command", got)
	}
}

func TestSlashModelUseCallsDriverAndUpdatesStatus(t *testing.T) {
	driver := &bridgeTestDriver{
		status:      tuidriver.StatusSnapshot{Model: "minimax/MiniMax-M2", ModeLabel: "default", Workspace: "/tmp/ws"},
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "model", Output: "model switched to: minimax/MiniMax-M2"},
	}
	var msgs []tea.Msg
	slashModel(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "use minimax/MiniMax-M2")
	if driver.commandCalls != 1 || driver.lastCommandInput != "/model use minimax/MiniMax-M2" {
		t.Fatalf("command calls=%d input=%q, want shared /model use command", driver.commandCalls, driver.lastCommandInput)
	}
	if len(msgs) == 0 {
		t.Fatal("slashModel(use) emitted no messages")
	}
}

func TestSlashModelUsePassesReasoningLevel(t *testing.T) {
	driver := &bridgeTestDriver{
		status:      tuidriver.StatusSnapshot{Model: "deepseek/deepseek-v4-pro [high]", ModeLabel: "default", Workspace: "/tmp/ws"},
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "model", Output: "model switched to: deepseek/deepseek-v4-pro (reasoning: high)"},
	}
	slashModel(driver, func(tea.Msg) {}, "use deepseek/deepseek-v4-pro high")
	if driver.commandCalls != 1 || driver.lastCommandInput != "/model use deepseek/deepseek-v4-pro high" {
		t.Fatalf("command calls=%d input=%q, want shared /model use command with reasoning", driver.commandCalls, driver.lastCommandInput)
	}
}

func TestSlashModelDeleteCallsDriverAndRefreshesStatus(t *testing.T) {
	driver := &bridgeTestDriver{
		status:      tuidriver.StatusSnapshot{Model: "minimax/MiniMax-M1", ModeLabel: "default", Workspace: "/tmp/ws"},
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "model", Output: "model deleted: minimax/MiniMax-M1"},
	}
	var msgs []tea.Msg
	slashModel(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "del minimax/MiniMax-M1")
	if driver.commandCalls != 1 || driver.lastCommandInput != "/model del minimax/MiniMax-M1" {
		t.Fatalf("command calls=%d input=%q, want shared /model del command", driver.commandCalls, driver.lastCommandInput)
	}
	if len(msgs) == 0 {
		t.Fatal("slashModel(del) emitted no messages")
	}
}

func TestSlashModelDeleteClearsStatusWhenNoModelRemains(t *testing.T) {
	driver := &bridgeTestDriver{
		status:      tuidriver.StatusSnapshot{Workspace: "/tmp/ws"},
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "model", Output: "model deleted: codefree/glm-5.1"},
	}
	var msgs []tea.Msg
	slashModel(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "del codefree/glm-5.1")
	if driver.commandCalls != 1 || driver.lastCommandInput != "/model del codefree/glm-5.1" {
		t.Fatalf("command calls=%d input=%q, want shared /model del command", driver.commandCalls, driver.lastCommandInput)
	}
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
	for _, want := range []string{"  Model:", "/connect", "Warning:", "API key is missing", "Commands may run on the host", "Auto-Review remains enabled"} {
		if !strings.Contains(log.Chunk, want) {
			t.Fatalf("slashStatus() chunk = %q, want substring %q", log.Chunk, want)
		}
	}
	for _, forbidden := range []string{"Status", "Tokens", "Warnings", "warn:", "/tmp/.caelis", "Store:", "Provider:", "Session", "\n  Reason:"} {
		if strings.Contains(log.Chunk, forbidden) {
			t.Fatalf("slashStatus() chunk = %q, should omit %q", log.Chunk, forbidden)
		}
	}
}

func TestSlashDoctorShowsReadinessChecklist(t *testing.T) {
	driver := &bridgeTestDriver{
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "doctor", Output: "doctor:\n  warn provider key missing - run /connect\n  ok session store: /tmp/.caelis\n  ok session: sess-1\n  warn sandbox: host"},
	}
	var msgs []tea.Msg
	result := slashDoctorWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "")
	if result.Err != nil {
		t.Fatalf("slashDoctorWithContext() error = %v", result.Err)
	}
	if len(msgs) != 1 {
		t.Fatalf("slashDoctorWithContext() emitted %d messages, want 1", len(msgs))
	}
	if driver.commandCalls != 1 || driver.lastCommandInput != "/doctor" {
		t.Fatalf("doctor command calls=%d input=%q, want shared /doctor command", driver.commandCalls, driver.lastCommandInput)
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
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "doctor", Output: "sandbox repair complete\n\ndoctor:\n  ok sandbox: windows"},
	}
	var msgs []tea.Msg
	result := slashDoctorWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "fix")
	if result.Err != nil {
		t.Fatalf("slashDoctorWithContext(fix) error = %v", result.Err)
	}
	if driver.commandCalls != 1 || driver.lastCommandInput != "/doctor fix" {
		t.Fatalf("doctor fix command calls=%d input=%q, want shared /doctor fix command", driver.commandCalls, driver.lastCommandInput)
	}
	if !noticeMessagesContain(msgs, "sandbox repair complete") || !noticeMessagesContain(msgs, "ok sandbox: windows") {
		t.Fatalf("slashDoctorWithContext(fix) messages = %#v, want shared doctor output", msgs)
	}
}

func TestFriendlyCommandErrorMakesResumeActionable(t *testing.T) {
	err := friendlyCommandError("resume session", fmt.Errorf("gateway: session not found"))
	if !strings.Contains(err.Error(), "/resume") {
		t.Fatalf("friendlyCommandError() = %q, want /resume guidance", err)
	}
}

func TestSlashApprovalUsesSharedCommandExecutor(t *testing.T) {
	driver := &bridgeTestDriver{
		status:      tuidriver.StatusSnapshot{ModeLabel: "manual", SessionMode: "manual"},
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "approval", Output: "approval mode: manual"},
	}
	var msgs []tea.Msg
	result := slashApprovalWithContext(context.Background(), driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "manual")
	if result.Err != nil {
		t.Fatalf("slashApprovalWithContext() error = %v", result.Err)
	}
	if driver.commandCalls != 1 || driver.lastCommandInput != "/approval manual" {
		t.Fatalf("approval command calls=%d input=%q, want shared /approval command", driver.commandCalls, driver.lastCommandInput)
	}
	if !noticeMessagesContain(msgs, "approval mode: manual") {
		t.Fatalf("slashApprovalWithContext() messages = %#v, want shared approval output", msgs)
	}
}

func TestConfigFromDriverToggleModeUsesSharedCommandExecutor(t *testing.T) {
	driver := &bridgeTestDriver{
		status:      tuidriver.StatusSnapshot{ModeLabel: "manual", SessionMode: "manual"},
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "approval", Output: "approval mode: manual"},
	}
	cfg := ConfigFromDriver(driver, &ProgramSender{}, Config{})
	if cfg.ToggleMode == nil {
		t.Fatal("ToggleMode = nil, want shared command executor binding")
	}
	hint, err := cfg.ToggleMode()
	if err != nil {
		t.Fatalf("ToggleMode() error = %v", err)
	}
	if driver.commandCalls != 1 || driver.lastCommandInput != "/approval toggle" {
		t.Fatalf("toggle command calls=%d input=%q, want shared /approval toggle command", driver.commandCalls, driver.lastCommandInput)
	}
	if hint != "manual approval mode enabled" {
		t.Fatalf("ToggleMode() hint = %q, want manual approval hint", hint)
	}
}

func TestSlashCompactUsesSharedCommandExecutor(t *testing.T) {
	driver := &bridgeTestDriver{
		commandView: tuidriver.CommandExecutionView{Handled: true, Command: "compact", Output: "compaction completed"},
	}
	var msgs []tea.Msg
	result := slashCompact(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "")
	if result.Err != nil {
		t.Fatalf("slashCompact() error = %v", result.Err)
	}
	if driver.commandCalls != 1 || driver.lastCommandInput != "/compact" {
		t.Fatalf("compact command calls=%d input=%q, want shared /compact command", driver.commandCalls, driver.lastCommandInput)
	}
	if !noticeMessagesContain(msgs, "compaction completed") {
		t.Fatalf("slashCompact() messages = %#v, want shared compact output", msgs)
	}
}

func TestSlashCompactRejectsArguments(t *testing.T) {
	driver := &bridgeTestDriver{}
	var msgs []tea.Msg
	result := slashCompact(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "note")
	if result.Err != nil {
		t.Fatalf("slashCompact() error = %v", result.Err)
	}
	if driver.commandCalls != 0 {
		t.Fatalf("commandCalls = %d, want 0 for invalid /compact args", driver.commandCalls)
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

func transcriptBatchContainsText(messages []tea.Msg, text string) bool {
	for _, msg := range messages {
		batch, ok := msg.(TranscriptEventsMsg)
		if !ok {
			continue
		}
		if transcriptEventsContainText(batch.Events, text) {
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
	status                   tuidriver.StatusSnapshot
	newSession               session.Session
	resumedSession           session.Session
	replay                   []kernel.EventEnvelope
	listAgentCalls           int
	agentStatusCalls         int
	lastContinuedHandle      string
	lastContinuedPrompt      string
	lastContinuedAttachments []tuidriver.Attachment
	subagentTurn             tuidriver.Turn
	agentList                []tuidriver.AgentCandidate
	agentStatus              tuidriver.AgentStatusSnapshot
	slashArgCandidates       map[string][]tuidriver.SlashArgCandidate
	commandView              tuidriver.CommandExecutionView
	commandViews             map[string]tuidriver.CommandExecutionView
	commandErr               error
	commandCalls             int
	commandInputs            []string
	lastCommandInput         string
	lastCommandAttachments   []tuidriver.Attachment
	commandCatalog           tuidriver.CommandCatalogView
	commandCatalogErr        error
	commandCatalogCalls      int
}

type bridgeAppReplayDriver struct {
	bridgeTestDriver
	appReplay      []appviewmodel.SessionEventEnvelope
	appReplayErr   error
	appReplayCalls int
	replayCalls    int
}

type bridgeLightweightStatusDriver struct {
	bridgeTestDriver
	lightweightStatus      tuidriver.StatusSnapshot
	statusCalls            int
	lightweightStatusCalls int
}

type bridgeTestTurn struct {
	events      chan kernel.EventEnvelope
	submissions []coreruntime.Submission
}

type bridgeAppEventTurn struct {
	*bridgeTestTurn
	appEvents   <-chan appviewmodel.SessionEventEnvelope
	eventsCalls int
}

func (t *bridgeTestTurn) HandleID() string { return "handle-1" }
func (t *bridgeTestTurn) RunID() string    { return "run-1" }
func (t *bridgeTestTurn) TurnID() string   { return "turn-1" }
func (t *bridgeTestTurn) SessionRef() session.SessionRef {
	return session.SessionRef{SessionID: "root-session"}
}
func (t *bridgeTestTurn) Events() <-chan kernel.EventEnvelope { return t.events }
func (t *bridgeAppEventTurn) Events() <-chan kernel.EventEnvelope {
	t.eventsCalls++
	return t.bridgeTestTurn.Events()
}
func (t *bridgeAppEventTurn) SessionEvents() <-chan appviewmodel.SessionEventEnvelope {
	return t.appEvents
}
func (t *bridgeTestTurn) Submit(_ context.Context, submission coreruntime.Submission) error {
	t.submissions = append(t.submissions, submission)
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

func cloneTUIDriverAttachments(items []tuidriver.Attachment) []tuidriver.Attachment {
	if len(items) == 0 {
		return nil
	}
	return append([]tuidriver.Attachment(nil), items...)
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

func dynamicAgentCommandView(command string, events ...coresession.Event) tuidriver.CommandExecutionView {
	return tuidriver.CommandExecutionView{
		Handled: true,
		Command: command,
		Events:  events,
	}
}

func coreTextMessage(role coremodel.Role, text string) *coremodel.Message {
	message := coremodel.Message{Role: role, Parts: []coremodel.Part{coremodel.NewTextPart(text)}}
	return &message
}

func participantAssistantCoreEvent(scopeID string, actor string, text string) coresession.Event {
	return coresession.Event{
		ID:        "event-" + strings.TrimSpace(scopeID),
		SessionID: "root-session",
		Type:      coresession.EventAssistant,
		Actor: coresession.ActorRef{
			Kind: coresession.ActorParticipant,
			ID:   strings.TrimPrefix(strings.TrimSpace(actor), "@"),
			Name: strings.TrimSpace(actor),
		},
		Scope: participantCoreScope(scopeID, actor),
		Message: &coremodel.Message{
			Role:  coremodel.RoleAssistant,
			Parts: []coremodel.Part{coremodel.NewTextPart(text)},
		},
	}
}

func participantToolCallCoreEvent(scopeID string, name string, input map[string]any) coresession.Event {
	return coresession.Event{
		ID:        "tool-" + strings.TrimSpace(scopeID),
		SessionID: "root-session",
		Type:      coresession.EventToolCall,
		Actor: coresession.ActorRef{
			Kind: coresession.ActorTool,
			ID:   "call-1",
			Name: strings.TrimSpace(name),
		},
		Scope: participantCoreScope(scopeID, "@copilot"),
		Tool: &coresession.ToolEvent{
			ID:     "call-1",
			Name:   strings.TrimSpace(name),
			Status: coresession.ToolRunning,
			Input:  input,
		},
	}
}

func participantCoreScope(scopeID string, actor string) *coresession.EventScope {
	agent := strings.TrimPrefix(strings.TrimSpace(actor), "@")
	if agent == "" {
		agent = strings.TrimSpace(scopeID)
	}
	return &coresession.EventScope{
		Source: "slash_" + agent,
		Participant: coresession.ParticipantBinding{
			ID:        strings.TrimSpace(scopeID),
			Kind:      coresession.ParticipantACP,
			Role:      coresession.ParticipantSidecar,
			AgentName: agent,
			Label:     strings.TrimSpace(actor),
		},
	}
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
func (d *bridgeSubmitDriver) ExecuteCommand(context.Context, tuidriver.CommandExecutionOptions) (tuidriver.CommandExecutionView, error) {
	return tuidriver.CommandExecutionView{}, nil
}
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
func (d *bridgeSubmitDriver) ListAgents(context.Context, int) ([]tuidriver.AgentCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) AgentStatus(context.Context) (tuidriver.AgentStatusSnapshot, error) {
	return tuidriver.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) ContinueSubagent(context.Context, string, string, []tuidriver.Attachment) (tuidriver.Turn, error) {
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
func (d *bridgeTestDriver) ExecuteCommand(_ context.Context, opts tuidriver.CommandExecutionOptions) (tuidriver.CommandExecutionView, error) {
	d.commandCalls++
	d.lastCommandInput = strings.TrimSpace(opts.Input)
	d.commandInputs = append(d.commandInputs, d.lastCommandInput)
	d.lastCommandAttachments = cloneTUIDriverAttachments(opts.Attachments)
	if d.commandErr != nil {
		return tuidriver.CommandExecutionView{}, d.commandErr
	}
	if d.commandViews != nil {
		if view, ok := d.commandViews[d.lastCommandInput]; ok {
			return view, nil
		}
	}
	if strings.EqualFold(d.lastCommandInput, "/new") {
		active, err := d.NewSession(context.Background())
		if err != nil {
			return tuidriver.CommandExecutionView{}, err
		}
		ref := coresession.Ref{
			AppName:      active.AppName,
			UserID:       active.UserID,
			SessionID:    active.SessionID,
			WorkspaceKey: active.WorkspaceKey,
		}
		return tuidriver.CommandExecutionView{
			Handled:    true,
			Command:    "new",
			Output:     "new session: " + strings.TrimSpace(ref.SessionID),
			SessionRef: &ref,
		}, nil
	}
	return d.commandView, nil
}
func (d *bridgeTestDriver) CommandCatalog(context.Context) (tuidriver.CommandCatalogView, error) {
	d.commandCatalogCalls++
	if d.commandCatalogErr != nil {
		return tuidriver.CommandCatalogView{}, d.commandCatalogErr
	}
	return d.commandCatalog, nil
}
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
func (d *bridgeAppReplayDriver) ReplayEvents(ctx context.Context) ([]kernel.EventEnvelope, error) {
	d.replayCalls++
	return d.bridgeTestDriver.ReplayEvents(ctx)
}
func (d *bridgeAppReplayDriver) ReplaySessionEvents(context.Context) ([]appviewmodel.SessionEventEnvelope, error) {
	d.appReplayCalls++
	if d.appReplayErr != nil {
		return nil, d.appReplayErr
	}
	return d.appReplay, nil
}
func (d *bridgeTestDriver) ListAgents(context.Context, int) ([]tuidriver.AgentCandidate, error) {
	d.listAgentCalls++
	return d.agentList, nil
}
func (d *bridgeTestDriver) AgentStatus(context.Context) (tuidriver.AgentStatusSnapshot, error) {
	d.agentStatusCalls++
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) ContinueSubagent(_ context.Context, handle string, prompt string, attachments []tuidriver.Attachment) (tuidriver.Turn, error) {
	d.lastContinuedHandle = handle
	d.lastContinuedPrompt = prompt
	d.lastContinuedAttachments = cloneTUIDriverAttachments(attachments)
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
