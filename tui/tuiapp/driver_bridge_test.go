package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	tuiadapterruntime "github.com/OnslaughtSnail/caelis/gateway/adapter/tui/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
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

func TestProgramSenderCloseCancelsSubagentForwarder(t *testing.T) {
	ctx := context.Background()
	frames := make(chan tuiadapterruntime.SubagentStreamFrame)
	driver := &bridgeTestDriver{subagentStream: frames}
	sender := &ProgramSender{}
	var sent atomic.Int64
	sender.Send = func(tea.Msg) { sent.Add(1) }

	startDynamicSubagentOutputBridge(ctx, driver, sender, tuiadapterruntime.SubagentSnapshot{
		TaskID:  "task-1",
		Running: true,
		Mention: "@worker",
	})
	deadline := time.After(2 * time.Second)
	for driver.subagentStreamSubscribeCalls == 0 {
		select {
		case <-deadline:
			t.Fatal("SubscribeSubagentStream was not called")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	sender.Close()
	select {
	case frames <- tuiadapterruntime.SubagentStreamFrame{Text: "late", Running: true}:
		t.Fatal("subagent forwarder still received after sender.Close")
	case <-time.After(30 * time.Millisecond):
	}
	if got := sent.Load(); got != 0 {
		t.Fatalf("late sends after close = %d, want 0", got)
	}
}

func TestSubagentForwarderExitsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	frames := make(chan tuiadapterruntime.SubagentStreamFrame)
	driver := &bridgeTestDriver{subagentStream: frames}
	sender := &ProgramSender{}
	var sent atomic.Int64
	sender.Send = func(tea.Msg) { sent.Add(1) }

	startDynamicSubagentOutputBridge(ctx, driver, sender, tuiadapterruntime.SubagentSnapshot{
		TaskID:  "task-1",
		Running: true,
		Mention: "@worker",
	})
	deadline := time.After(2 * time.Second)
	for driver.subagentStreamSubscribeCalls == 0 {
		select {
		case <-deadline:
			t.Fatal("SubscribeSubagentStream was not called")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	cancel()
	if !sender.waitForwarders(time.Second) {
		t.Fatal("subagent forwarder did not exit after context cancellation")
	}
	select {
	case frames <- tuiadapterruntime.SubagentStreamFrame{Text: "late", Running: true}:
		t.Fatal("subagent forwarder still received after context cancellation")
	case <-time.After(30 * time.Millisecond):
	}
	if got := sent.Load(); got != 0 {
		t.Fatalf("late sends after cancel = %d, want 0", got)
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
		status:     tuiadapterruntime.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
		newSession: sdksession.Session{SessionRef: sdksession.SessionRef{SessionID: "new-session"}},
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
	for _, want := range []string{"/agent list | /agent add <builtin> | /agent use <agent|local> | /agent remove <agent>", "/connect", "/model use <alias> | /model del <alias>", "/compact", "/resume [session-id]"} {
		if !strings.Contains(log.Chunk, want) {
			t.Fatalf("slashHelp() chunk = %q, want substring %q", log.Chunk, want)
		}
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
	env, ok := sent[0].(appgateway.EventEnvelope)
	if !ok {
		t.Fatalf("sent msg = %#v, want EventEnvelope", sent[0])
	}
	if got := rawString(env.Event.ToolResult.RawOutput, "text"); got != "hello world" {
		t.Fatalf("merged text = %q, want hello world", got)
	}
	if got := env.Event.ToolResult.RawOutput["stdout_cursor"]; got != int64(2) {
		t.Fatalf("stdout_cursor = %#v, want int64(2)", got)
	}
}

func testTerminalFrame(text string, cursor int64) appgateway.EventEnvelope {
	return appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			HandleID:   "h1",
			RunID:      "r1",
			TurnID:     "t1",
			SessionRef: sdksession.SessionRef{SessionID: "s1"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "BASH",
				RawOutput: map[string]any{
					"running":       true,
					"text":          text,
					"task_id":       "task-1",
					"terminal_id":   "term-1",
					"stream":        "stdout",
					"stdout_cursor": cursor,
				},
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
		status:     tuiadapterruntime.StatusSnapshot{},
		newSession: sdksession.Session{SessionRef: sdksession.SessionRef{SessionID: "new-session"}},
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
		status:         tuiadapterruntime.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
		resumedSession: sdksession.Session{SessionRef: sdksession.SessionRef{SessionID: "resumed-session"}},
		replay: []appgateway.EventEnvelope{
			{
				Event: appgateway.Event{
					Kind: appgateway.EventKindUserMessage,
					Narrative: &appgateway.NarrativePayload{
						Role: appgateway.NarrativeRoleUser,
						Text: "history prompt",
					},
				},
			},
			{
				Event: appgateway.Event{
					Kind: appgateway.EventKindToolCall,
					ToolCall: &appgateway.ToolCallPayload{
						CallID:   "bash-1",
						ToolName: "BASH",
						Status:   appgateway.ToolStatusRunning,
					},
				},
			},
			{
				Event: appgateway.Event{
					Kind: appgateway.EventKindAssistantMessage,
					Narrative: &appgateway.NarrativePayload{
						Role:       appgateway.NarrativeRoleAssistant,
						Text:       "stream chunk",
						Final:      false,
						Visibility: string(sdksession.VisibilityUIOnly),
					},
				},
			},
			{
				Event: appgateway.Event{
					Kind: appgateway.EventKindAssistantMessage,
					Narrative: &appgateway.NarrativePayload{
						Role:  appgateway.NarrativeRoleAssistant,
						Text:  "history reply",
						Final: true,
						Scope: appgateway.EventScopeMain,
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
	for _, msg := range msgs {
		if log, ok := msg.(LogChunkMsg); ok && (strings.Contains(log.Chunk, "resumed session") || strings.Contains(log.Chunk, "replayed")) {
			t.Fatalf("slashResume() emitted noisy resume notice: %#v", log)
		}
		if env, ok := msg.(appgateway.EventEnvelope); ok {
			if env.Event.ToolCall != nil || env.Event.ToolResult != nil {
				t.Fatalf("slashResume() replayed tool process event: %#v", env)
			}
			if env.Event.Narrative != nil && env.Event.Narrative.Text == "stream chunk" {
				t.Fatalf("slashResume() replayed transient assistant chunk: %#v", env)
			}
			if env.Event.Narrative != nil && env.Event.Narrative.Text == "history prompt" {
				sawUserReplay = true
			}
			if env.Event.Narrative != nil && env.Event.Narrative.Text == "history reply" {
				sawAssistantReplay = true
			}
		}
	}
	if !sawUserReplay || !sawAssistantReplay {
		t.Fatalf("slashResume() messages = %#v, want user and final assistant replay", msgs)
	}
}

func TestExecuteLineViaDriverStreamsGatewayEventsDirectly(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan appgateway.EventEnvelope, 1),
	}
	turn.events <- appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Narrative: &appgateway.NarrativePayload{
				Role:  appgateway.NarrativeRoleAssistant,
				Text:  "direct gateway event",
				Final: true,
				Scope: appgateway.EventScopeMain,
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
	if _, ok := msgs[0].(appgateway.EventEnvelope); !ok {
		t.Fatalf("first msg = %#v, want appgateway.EventEnvelope", msgs[0])
	}
}

func TestExecuteLineViaDriverCoalescesUIOnlyReasoningBeforeToolEvent(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan appgateway.EventEnvelope, 4),
	}
	for _, text := range []string{"think ", "fast ", "now"} {
		turn.events <- appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindAssistantMessage,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Narrative: &appgateway.NarrativePayload{
					Role:          appgateway.NarrativeRoleAssistant,
					ReasoningText: text,
					Visibility:    "ui_only",
					UpdateType:    string(sdksession.ProtocolUpdateTypeAgentThought),
					Scope:         appgateway.EventScopeMain,
				},
			},
		}
	}
	turn.events <- appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "READ",
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeMain,
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
	reasoning, ok := msgs[0].(appgateway.EventEnvelope)
	if !ok || reasoning.Event.Narrative == nil {
		t.Fatalf("first msg = %#v, want coalesced reasoning EventEnvelope", msgs[0])
	}
	if got := reasoning.Event.Narrative.ReasoningText; got != "think fast now" {
		t.Fatalf("coalesced reasoning = %q, want %q", got, "think fast now")
	}
	tool, ok := msgs[1].(appgateway.EventEnvelope)
	if !ok || tool.Event.Kind != appgateway.EventKindToolCall {
		t.Fatalf("second msg = %#v, want tool event after reasoning flush", msgs[1])
	}
}

func TestExecuteLineViaDriverCoalescesUIOnlyReasoningPreservesLeadingSpaces(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan appgateway.EventEnvelope, 7),
	}
	for _, text := range []string{"Now", " let", " me", " verify", " the", " DDL", " matches"} {
		turn.events <- appgateway.EventEnvelope{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindAssistantMessage,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Narrative: &appgateway.NarrativePayload{
					Role:          appgateway.NarrativeRoleAssistant,
					ReasoningText: text,
					Visibility:    "ui_only",
					UpdateType:    string(sdksession.ProtocolUpdateTypeAgentThought),
					Scope:         appgateway.EventScopeMain,
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
	reasoning, ok := msgs[0].(appgateway.EventEnvelope)
	if !ok || reasoning.Event.Narrative == nil {
		t.Fatalf("first msg = %#v, want coalesced reasoning EventEnvelope", msgs[0])
	}
	if got := reasoning.Event.Narrative.ReasoningText; got != "Now let me verify the DDL matches" {
		t.Fatalf("coalesced reasoning = %q, want boundary spaces preserved", got)
	}
}

func TestExecuteLineViaDriverDoesNotCoalesceReasoningWithAnswerDelta(t *testing.T) {
	turn := &bridgeTestTurn{
		events: make(chan appgateway.EventEnvelope, 2),
	}
	turn.events <- appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Narrative: &appgateway.NarrativePayload{
				Role:          appgateway.NarrativeRoleAssistant,
				ReasoningText: "think",
				Visibility:    "ui_only",
				UpdateType:    string(sdksession.ProtocolUpdateTypeAgentThought),
				Scope:         appgateway.EventScopeMain,
			},
		},
	}
	turn.events <- appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Narrative: &appgateway.NarrativePayload{
				Role:       appgateway.NarrativeRoleAssistant,
				Text:       "answer",
				Visibility: "ui_only",
				UpdateType: string(sdksession.ProtocolUpdateTypeAgentMessage),
				Scope:      appgateway.EventScopeMain,
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
	first := msgs[0].(appgateway.EventEnvelope)
	second := msgs[1].(appgateway.EventEnvelope)
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
		events: make(chan appgateway.EventEnvelope, 1),
	}
	turn.events <- appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolResult,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "BASH",
				RawOutput: map[string]any{
					"task_id":       "task-1",
					"terminal_id":   "terminal-1",
					"running":       true,
					"state":         "running",
					"stdout_cursor": int64(4),
				},
				Status: appgateway.ToolStatusRunning,
			},
		},
	}
	close(turn.events)
	terminalEvents := make(chan appgateway.EventEnvelope, 1)
	terminalEvents <- appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind: appgateway.EventKindToolResult,
			ToolResult: &appgateway.ToolResultPayload{
				CallID:   "call-1",
				ToolName: "BASH",
				RawOutput: map[string]any{
					"stream": "stdout",
					"text":   "streamed\n",
				},
				Status: appgateway.ToolStatusRunning,
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
			env, ok := msg.(appgateway.EventEnvelope)
			if !ok || env.Event.ToolResult == nil {
				continue
			}
			if env.Event.ToolResult.RawOutput["text"] == "streamed\n" {
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
		status:         tuiadapterruntime.StatusSnapshot{Model: "gpt-4o", ModeLabel: "default"},
		resumedSession: sdksession.Session{SessionRef: sdksession.SessionRef{SessionID: "resumed-session"}},
		replay: []appgateway.EventEnvelope{{
			Event: appgateway.Event{
				Kind:       appgateway.EventKindAssistantMessage,
				SessionRef: sdksession.SessionRef{SessionID: "root-session"},
				Narrative: &appgateway.NarrativePayload{
					Role:  appgateway.NarrativeRoleAssistant,
					Text:  "history reply",
					Final: true,
					Scope: appgateway.EventScopeMain,
				},
			},
		}},
	}
	var msgs []tea.Msg
	slashResume(driver, func(msg tea.Msg) { msgs = append(msgs, msg) }, "resumed-session")
	var sawReplay bool
	for _, msg := range msgs {
		if env, ok := msg.(appgateway.EventEnvelope); ok && env.Event.Narrative != nil && env.Event.Narrative.Text == "history reply" {
			sawReplay = true
		}
	}
	if !sawReplay {
		t.Fatalf("slashResume() messages = %#v, want replayed gateway envelope", msgs)
	}
}

func TestSlashConnectCallsDriverAndUpdatesStatus(t *testing.T) {
	driver := &bridgeTestDriver{
		status:        tuiadapterruntime.StatusSnapshot{Model: "minimax/MiniMax-M1", ModeLabel: "default", Workspace: "/tmp/ws"},
		connectStatus: tuiadapterruntime.StatusSnapshot{Model: "minimax/MiniMax-M2", ModeLabel: "default", Workspace: "/tmp/ws"},
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
	if got := formatContextUsageStatus(12600, 88000); got != "12.6k/88k(14%)" {
		t.Fatalf("formatContextUsageStatus() = %q, want %q", got, "12.6k/88k(14%)")
	}
	if got := formatContextUsageStatus(0, 88000); got != "0/88k(0%)" {
		t.Fatalf("formatContextUsageStatus() zero = %q, want %q", got, "0/88k(0%)")
	}
}

func TestSlashAgentDispatchesPrimarySubcommands(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList: []tuiadapterruntime.AgentCandidate{{
			Name:        "copilot",
			Description: "ACP sidecar",
		}},
		agentStatus: tuiadapterruntime.AgentStatusSnapshot{
			SessionID:       "sess-1",
			ControllerKind:  "acp",
			ControllerLabel: "copilot",
			Participants: []tuiadapterruntime.AgentParticipantSnapshot{{
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
	if driver.listAgentCalls != 3 || driver.agentStatusCalls != 1 || driver.addAgentCalls != 1 || driver.removeAgentCalls != 1 || driver.handoffAgentCalls != 1 || driver.askAgentCalls != 0 {
		t.Fatalf("agent calls = list:%d status:%d add:%d remove:%d use:%d ask:%d", driver.listAgentCalls, driver.agentStatusCalls, driver.addAgentCalls, driver.removeAgentCalls, driver.handoffAgentCalls, driver.askAgentCalls)
	}
	if driver.lastAddedAgent != "copilot" || driver.lastRemovedAgent != "copilot" || driver.lastHandoffAgent != "copilot" || driver.lastAskedAgent != "" || driver.lastAskedPrompt != "" {
		t.Fatalf("agent targets = add:%q remove:%q handoff:%q ask:%q prompt:%q", driver.lastAddedAgent, driver.lastRemovedAgent, driver.lastHandoffAgent, driver.lastAskedAgent, driver.lastAskedPrompt)
	}
	if len(msgs) == 0 {
		t.Fatal("slashAgent() emitted no messages")
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

func TestDynamicAgentSlashAndHandleContinuation(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList: []tuiadapterruntime.AgentCandidate{{Name: "copilot"}},
		subagentSnapshot: tuiadapterruntime.SubagentSnapshot{
			Handle:        "jeff",
			Mention:       "@jeff",
			Agent:         "copilot",
			TaskID:        "task-1",
			State:         "completed",
			OutputPreview: "child ok",
		},
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

func TestDynamicAgentSlashWatchesRunningSubagentUntilOutput(t *testing.T) {
	stream := make(chan tuiadapterruntime.SubagentStreamFrame, 1)
	stream <- tuiadapterruntime.SubagentStreamFrame{
		TaskID:  "task-1",
		Stream:  "stdout",
		Text:    "我是 copilot 子代理",
		Running: false,
		Closed:  true,
	}
	close(stream)
	driver := &bridgeTestDriver{
		agentList: []tuiadapterruntime.AgentCandidate{{Name: "copilot"}},
		subagentSnapshot: tuiadapterruntime.SubagentSnapshot{
			Handle:  "mike",
			Mention: "@mike",
			Agent:   "copilot",
			TaskID:  "task-1",
			State:   "running",
			Running: true,
		},
		subagentStream: stream,
		waitSubagentSnapshot: tuiadapterruntime.SubagentSnapshot{
			Handle:        "mike",
			Mention:       "@mike",
			Agent:         "copilot",
			TaskID:        "task-1",
			State:         "completed",
			Running:       false,
			OutputPreview: "我是 copilot 子代理",
		},
	}
	msgs := make(chan tea.Msg, 16)
	result := dispatchSlashCommand(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs <- msg }}, "/copilot 介绍一下你自己")
	if result.Err != nil {
		t.Fatalf("dynamic slash error = %v", result.Err)
	}
	if !result.ContinueRunning {
		t.Fatalf("dynamic slash result = %#v, want running task to keep ticker active", result)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-msgs:
			switch typed := msg.(type) {
			case SubagentStartMsg:
				t.Fatalf("dynamic slash emitted SPAWN panel start message: %#v", typed)
			case TranscriptEventsMsg:
				if transcriptEventsContainText(typed.Events, "copilot 子代理") {
					if driver.subagentStreamSubscribeCalls == 0 {
						t.Fatal("SubscribeSubagentStream was not called for running dynamic slash child")
					}
					return
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for watched subagent output; stream subscribe calls=%d wait calls=%d", driver.subagentStreamSubscribeCalls, driver.waitSubagentCalls)
		}
	}
}

func TestDynamicAgentSlashDoesNotRenderRunningOutputPreviewAsAssistantText(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList: []tuiadapterruntime.AgentCandidate{{Name: "codex"}},
		subagentSnapshot: tuiadapterruntime.SubagentSnapshot{
			Handle:        "iris",
			Mention:       "@iris",
			Agent:         "codex",
			TaskID:        "task-1",
			State:         "running",
			Running:       true,
			OutputPreview: "Searching the Web",
		},
		waitSubagentSnapshot: tuiadapterruntime.SubagentSnapshot{
			Handle:  "iris",
			Mention: "@iris",
			Agent:   "codex",
			TaskID:  "task-1",
			State:   "completed",
			Running: false,
			Result:  "上海今天阴有小雨。",
		},
	}
	msgs := make(chan tea.Msg, 16)
	result := dispatchSlashCommand(driver, &ProgramSender{Send: func(msg tea.Msg) { msgs <- msg }}, "/codex 查询上海天气")
	if result.Err != nil {
		t.Fatalf("dynamic slash error = %v", result.Err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-msgs:
			if transcript, ok := msg.(TranscriptEventsMsg); ok {
				if transcriptEventsContainText(transcript.Events, "Searching the Web") {
					t.Fatalf("running output preview was rendered as assistant text: %#v", transcript)
				}
				if transcriptEventsContainText(transcript.Events, "上海今天阴有小雨") {
					return
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for final subagent output; wait calls=%d", driver.waitSubagentCalls)
		}
	}
}

func TestDynamicAgentSlashCompletedTurnKeepsDivider(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList: []tuiadapterruntime.AgentCandidate{{Name: "codex"}},
		subagentSnapshot: tuiadapterruntime.SubagentSnapshot{
			Handle:  "kate",
			Mention: "@kate",
			Agent:   "codex",
			TaskID:  "task-1",
			State:   "completed",
			Running: false,
			Result:  "上海今天阴有小雨。",
		},
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

func TestDynamicAgentSlashStreamCompletionKeepsDivider(t *testing.T) {
	stream := make(chan tuiadapterruntime.SubagentStreamFrame, 1)
	stream <- tuiadapterruntime.SubagentStreamFrame{
		TaskID:  "task-1",
		TurnID:  "task-1:1",
		Stream:  "stdout",
		Text:    "上海今天阴有小雨。",
		Running: false,
		Closed:  true,
	}
	close(stream)
	driver := &bridgeTestDriver{
		agentList: []tuiadapterruntime.AgentCandidate{{Name: "codex"}},
		subagentSnapshot: tuiadapterruntime.SubagentSnapshot{
			Handle:  "kate",
			Mention: "@kate",
			Agent:   "codex",
			TaskID:  "task-1",
			TurnID:  "task-1:1",
			State:   "running",
			Running: true,
		},
		subagentStream: stream,
	}
	msgs := make(chan tea.Msg, 8)
	sender := &ProgramSender{Send: func(msg tea.Msg) { msgs <- msg }}
	result := dispatchSlashCommand(driver, sender, "/codex 查询上海天气")
	if result.Err != nil {
		t.Fatalf("dynamic slash error = %v", result.Err)
	}
	if !result.ContinueRunning {
		t.Fatalf("dynamic slash result = %#v, want running task while stream bridge drains", result)
	}
	if !sender.waitForwarders(time.Second) {
		t.Fatal("subagent forwarder did not exit")
	}
	close(msgs)
	foundTaskResult := false
	for msg := range msgs {
		if task, ok := msg.(TaskResultMsg); ok {
			foundTaskResult = true
			if task.SuppressTurnDivider {
				t.Fatalf("stream completion task result = %#v, want divider kept", task)
			}
		}
	}
	if !foundTaskResult {
		t.Fatal("stream completion emitted no TaskResultMsg")
	}
}

func TestDynamicSubagentStreamPreservesWhitespaceFrames(t *testing.T) {
	stream := make(chan tuiadapterruntime.SubagentStreamFrame, 3)
	stream <- tuiadapterruntime.SubagentStreamFrame{TaskID: "task-1", Stream: "stdout", Text: "The", Running: true}
	stream <- tuiadapterruntime.SubagentStreamFrame{TaskID: "task-1", Stream: "stdout", Text: " ", Running: true}
	stream <- tuiadapterruntime.SubagentStreamFrame{TaskID: "task-1", Stream: "stdout", Text: "sandbox", Running: false, Closed: true}
	close(stream)

	driver := &bridgeTestDriver{subagentStream: stream}
	msgs := make(chan tea.Msg, 8)
	sender := &ProgramSender{Send: func(msg tea.Msg) { msgs <- msg }}

	startDynamicSubagentOutputBridge(context.Background(), driver, sender, tuiadapterruntime.SubagentSnapshot{
		TaskID:  "task-1",
		Running: true,
		Mention: "@worker",
	})
	if !sender.waitForwarders(time.Second) {
		t.Fatal("subagent forwarder did not exit")
	}
	close(msgs)

	var text strings.Builder
	for msg := range msgs {
		if transcript, ok := msg.(TranscriptEventsMsg); ok {
			for _, event := range transcript.Events {
				if event.Kind == TranscriptEventNarrative && event.Scope == ACPProjectionParticipant {
					text.WriteString(event.Text)
				}
			}
		}
	}
	if got := text.String(); got != "The sandbox" {
		t.Fatalf("streamed text = %q, want whitespace-only frame preserved", got)
	}
}

func TestDynamicAgentSlashPrefersStructuredSubagentEvents(t *testing.T) {
	env := appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Origin: &appgateway.EventOrigin{
				Scope:   appgateway.EventScopeSubagent,
				ScopeID: "child-1",
				Actor:   "copilot",
			},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "BASH",
				RawInput: map[string]any{"command": "go test ./tui/tuiapp/..."},
				Status:   appgateway.ToolStatusRunning,
				Scope:    appgateway.EventScopeSubagent,
			},
		},
	}
	stream := make(chan tuiadapterruntime.SubagentStreamFrame, 1)
	stream <- tuiadapterruntime.SubagentStreamFrame{
		TaskID:  "task-1",
		Stream:  "stdout",
		Text:    "working",
		Running: true,
		Event:   &env,
	}
	close(stream)
	driver := &bridgeTestDriver{
		agentList: []tuiadapterruntime.AgentCandidate{{Name: "copilot"}},
		subagentSnapshot: tuiadapterruntime.SubagentSnapshot{
			Handle:  "mike",
			Mention: "@mike",
			Agent:   "copilot",
			TaskID:  "task-1",
			State:   "running",
			Running: true,
		},
		subagentStream: stream,
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
			envMsg, ok := msg.(appgateway.EventEnvelope)
			if !ok {
				continue
			}
			if envMsg.Event.ToolCall == nil || envMsg.Event.ToolCall.ToolName != "BASH" {
				t.Fatalf("event envelope = %#v, want BASH tool call", envMsg)
			}
			if envMsg.Event.Origin == nil || envMsg.Event.Origin.Scope != appgateway.EventScopeParticipant {
				t.Fatalf("event origin = %#v, want dynamic side ACP participant scope", envMsg.Event.Origin)
			}
			if driver.subagentStreamSubscribeCalls == 0 {
				t.Fatal("SubscribeSubagentStream was not called")
			}
			return
		case <-deadline:
			t.Fatalf("timed out waiting for structured subagent event; stream subscribe calls=%d", driver.subagentStreamSubscribeCalls)
		}
	}
}

func TestDynamicSubagentStructuredEventUsesMentionAndPromptTurnScope(t *testing.T) {
	env := appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindToolCall,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
			Origin: &appgateway.EventOrigin{
				Scope:   appgateway.EventScopeSubagent,
				ScopeID: "codex-001",
				Actor:   "codex-001",
			},
			ToolCall: &appgateway.ToolCallPayload{
				CallID:   "call-1",
				ToolName: "Searching the Web",
				Actor:    "codex-001",
				RawInput: map[string]any{"query": "weather: Shanghai"},
				Status:   appgateway.ToolStatusRunning,
			},
		},
	}
	normalized := normalizeDynamicSubagentGatewayEvent(env, tuiadapterruntime.SubagentSnapshot{
		TaskID:  "task-1",
		TurnID:  "task-1:2",
		Mention: "@kate",
		Agent:   "codex",
	}, tuiadapterruntime.SubagentStreamFrame{
		TaskID: "task-1",
		TurnID: "task-1:2",
	})
	events := ProjectGatewayEventToTranscriptEvents(normalized.Event)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].Scope != ACPProjectionParticipant || events[0].ScopeID != "task-1:2" {
		t.Fatalf("event scope = %s/%q, want participant task-1:2", events[0].Scope, events[0].ScopeID)
	}
	if events[0].Actor != "@kate" {
		t.Fatalf("event actor = %q, want @kate", events[0].Actor)
	}
}

func TestDynamicAgentSlashFallsBackWhenStructuredEventIsNotRenderable(t *testing.T) {
	env := appgateway.EventEnvelope{
		Event: appgateway.Event{
			Kind:       appgateway.EventKindAssistantMessage,
			SessionRef: sdksession.SessionRef{SessionID: "root-session"},
		},
	}
	stream := make(chan tuiadapterruntime.SubagentStreamFrame, 1)
	stream <- tuiadapterruntime.SubagentStreamFrame{
		TaskID:  "task-1",
		Stream:  "stdout",
		Text:    "fallback side output",
		Running: false,
		Closed:  true,
		Event:   &env,
	}
	close(stream)
	driver := &bridgeTestDriver{
		agentList: []tuiadapterruntime.AgentCandidate{{Name: "copilot"}},
		subagentSnapshot: tuiadapterruntime.SubagentSnapshot{
			Handle:  "mike",
			Mention: "@mike",
			Agent:   "copilot",
			TaskID:  "task-1",
			State:   "running",
			Running: true,
		},
		subagentStream: stream,
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
			if _, ok := msg.(appgateway.EventEnvelope); ok {
				t.Fatalf("non-renderable structured event should not be emitted: %#v", msg)
			}
			transcript, ok := msg.(TranscriptEventsMsg)
			if ok && transcriptEventsContainText(transcript.Events, "fallback side output") {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for fallback side output; stream subscribe calls=%d", driver.subagentStreamSubscribeCalls)
		}
	}
}

func TestSlashConnectParsesEnvironmentVariableSecret(t *testing.T) {
	driver := &bridgeTestDriver{
		connectStatus: tuiadapterruntime.StatusSnapshot{Model: "openai/gpt-4o"},
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
		status:         tuiadapterruntime.StatusSnapshot{Model: "minimax/MiniMax-M1", ModeLabel: "default", Workspace: "/tmp/ws"},
		useModelStatus: tuiadapterruntime.StatusSnapshot{Model: "minimax/MiniMax-M2", ModeLabel: "default", Workspace: "/tmp/ws"},
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
		status:         tuiadapterruntime.StatusSnapshot{Model: "deepseek/deepseek-v4-pro", ModeLabel: "default", Workspace: "/tmp/ws"},
		useModelStatus: tuiadapterruntime.StatusSnapshot{Model: "deepseek/deepseek-v4-pro [high]", ModeLabel: "default", Workspace: "/tmp/ws"},
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
		status: tuiadapterruntime.StatusSnapshot{Model: "minimax/MiniMax-M1", ModeLabel: "default", Workspace: "/tmp/ws"},
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
		status: tuiadapterruntime.StatusSnapshot{Workspace: "/tmp/ws"},
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
		status: tuiadapterruntime.StatusSnapshot{
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
	for _, want := range []string{"/connect", "warning: API key is missing", "warning: commands may run on the host", "/tmp/.caelis"} {
		if !strings.Contains(log.Chunk, want) {
			t.Fatalf("slashStatus() chunk = %q, want substring %q", log.Chunk, want)
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

type bridgeTestDriver struct {
	status                       tuiadapterruntime.StatusSnapshot
	connectStatus                tuiadapterruntime.StatusSnapshot
	useModelStatus               tuiadapterruntime.StatusSnapshot
	newSession                   sdksession.Session
	resumedSession               sdksession.Session
	replay                       []appgateway.EventEnvelope
	connectCalls                 int
	useModelCalls                int
	deleteModelCalls             int
	listAgentCalls               int
	agentStatusCalls             int
	addAgentCalls                int
	removeAgentCalls             int
	handoffAgentCalls            int
	askAgentCalls                int
	compactCalls                 int
	lastConnect                  tuiadapterruntime.ConnectConfig
	lastModelAlias               string
	lastReasoningEffort          string
	lastDeletedAlias             string
	lastAddedAgent               string
	lastRemovedAgent             string
	lastHandoffAgent             string
	lastAskedAgent               string
	lastAskedPrompt              string
	lastStartedAgent             string
	lastStartedPrompt            string
	lastContinuedHandle          string
	lastContinuedPrompt          string
	subagentSnapshot             tuiadapterruntime.SubagentSnapshot
	subagentStream               <-chan tuiadapterruntime.SubagentStreamFrame
	subagentStreamSubscribeCalls int
	waitSubagentCalls            int
	lastWaitTaskID               string
	waitSubagentSnapshot         tuiadapterruntime.SubagentSnapshot
	agentList                    []tuiadapterruntime.AgentCandidate
	agentStatus                  tuiadapterruntime.AgentStatusSnapshot
}

type bridgeTestTurn struct {
	events chan appgateway.EventEnvelope
}

func (t *bridgeTestTurn) HandleID() string { return "handle-1" }
func (t *bridgeTestTurn) RunID() string    { return "run-1" }
func (t *bridgeTestTurn) TurnID() string   { return "turn-1" }
func (t *bridgeTestTurn) SessionRef() sdksession.SessionRef {
	return sdksession.SessionRef{SessionID: "root-session"}
}
func (t *bridgeTestTurn) Events() <-chan appgateway.EventEnvelope { return t.events }
func (t *bridgeTestTurn) Submit(context.Context, appgateway.SubmitRequest) error {
	return nil
}
func (t *bridgeTestTurn) Cancel() bool { return false }
func (t *bridgeTestTurn) Close() error { return nil }

type bridgeSubmitDriver struct {
	turn                   tuiadapterruntime.Turn
	terminalEvents         <-chan appgateway.EventEnvelope
	terminalSubscribeCalls int
	submitCalls            int
	lastSubmission         tuiadapterruntime.Submission
}

func (d *bridgeSubmitDriver) Status(context.Context) (tuiadapterruntime.StatusSnapshot, error) {
	return tuiadapterruntime.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) WorkspaceDir() string { return "" }
func (d *bridgeSubmitDriver) Submit(_ context.Context, sub tuiadapterruntime.Submission) (tuiadapterruntime.Turn, error) {
	d.submitCalls++
	d.lastSubmission = sub
	return d.turn, nil
}
func (d *bridgeSubmitDriver) SubscribeStream(context.Context, appgateway.EventEnvelope) (<-chan appgateway.EventEnvelope, bool) {
	d.terminalSubscribeCalls++
	if d.terminalEvents == nil {
		return nil, false
	}
	return d.terminalEvents, true
}
func (d *bridgeSubmitDriver) Interrupt(context.Context) error { return nil }
func (d *bridgeSubmitDriver) NewSession(context.Context) (sdksession.Session, error) {
	return sdksession.Session{}, nil
}
func (d *bridgeSubmitDriver) ResumeSession(context.Context, string) (sdksession.Session, error) {
	return sdksession.Session{}, nil
}
func (d *bridgeSubmitDriver) ListSessions(context.Context, int) ([]tuiadapterruntime.ResumeCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) ReplayEvents(context.Context) ([]appgateway.EventEnvelope, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) Compact(context.Context) error { return nil }
func (d *bridgeSubmitDriver) Connect(context.Context, tuiadapterruntime.ConnectConfig) (tuiadapterruntime.StatusSnapshot, error) {
	return tuiadapterruntime.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) UseModel(context.Context, string, ...string) (tuiadapterruntime.StatusSnapshot, error) {
	return tuiadapterruntime.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) DeleteModel(context.Context, string) error { return nil }
func (d *bridgeSubmitDriver) CycleSessionMode(context.Context) (tuiadapterruntime.StatusSnapshot, error) {
	return tuiadapterruntime.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) SetSandboxBackend(context.Context, string) (tuiadapterruntime.StatusSnapshot, error) {
	return tuiadapterruntime.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) SetSandboxMode(context.Context, string) (tuiadapterruntime.StatusSnapshot, error) {
	return tuiadapterruntime.StatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) ListAgents(context.Context, int) ([]tuiadapterruntime.AgentCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) AgentStatus(context.Context) (tuiadapterruntime.AgentStatusSnapshot, error) {
	return tuiadapterruntime.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) AddAgent(context.Context, string) (tuiadapterruntime.AgentStatusSnapshot, error) {
	return tuiadapterruntime.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) RemoveAgent(context.Context, string) (tuiadapterruntime.AgentStatusSnapshot, error) {
	return tuiadapterruntime.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) HandoffAgent(context.Context, string) (tuiadapterruntime.AgentStatusSnapshot, error) {
	return tuiadapterruntime.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) AskAgent(context.Context, string, string) (tuiadapterruntime.AgentStatusSnapshot, error) {
	return tuiadapterruntime.AgentStatusSnapshot{}, nil
}
func (d *bridgeSubmitDriver) StartAgentSubagent(context.Context, string, string) (tuiadapterruntime.SubagentSnapshot, error) {
	return tuiadapterruntime.SubagentSnapshot{}, nil
}
func (d *bridgeSubmitDriver) ContinueSubagent(context.Context, string, string) (tuiadapterruntime.SubagentSnapshot, error) {
	return tuiadapterruntime.SubagentSnapshot{}, nil
}
func (d *bridgeSubmitDriver) CompleteMention(context.Context, string, int) ([]tuiadapterruntime.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteFile(context.Context, string, int) ([]tuiadapterruntime.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteSkill(context.Context, string, int) ([]tuiadapterruntime.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteResume(context.Context, string, int) ([]tuiadapterruntime.ResumeCandidate, error) {
	return nil, nil
}
func (d *bridgeSubmitDriver) CompleteSlashArg(context.Context, string, string, int) ([]tuiadapterruntime.SlashArgCandidate, error) {
	return nil, nil
}

var _ tuiadapterruntime.Turn = (*bridgeTestTurn)(nil)
var _ tuiadapterruntime.Driver = (*bridgeSubmitDriver)(nil)

var _ = time.Time{}

func (d *bridgeTestDriver) Status(context.Context) (tuiadapterruntime.StatusSnapshot, error) {
	return d.status, nil
}
func (d *bridgeTestDriver) WorkspaceDir() string { return "" }
func (d *bridgeTestDriver) Submit(context.Context, tuiadapterruntime.Submission) (tuiadapterruntime.Turn, error) {
	return nil, nil
}
func (d *bridgeTestDriver) Interrupt(context.Context) error { return nil }
func (d *bridgeTestDriver) NewSession(context.Context) (sdksession.Session, error) {
	return d.newSession, nil
}
func (d *bridgeTestDriver) ResumeSession(context.Context, string) (sdksession.Session, error) {
	return d.resumedSession, nil
}
func (d *bridgeTestDriver) ListSessions(context.Context, int) ([]tuiadapterruntime.ResumeCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) ReplayEvents(context.Context) ([]appgateway.EventEnvelope, error) {
	return d.replay, nil
}
func (d *bridgeTestDriver) Compact(context.Context) error {
	d.compactCalls++
	return nil
}
func (d *bridgeTestDriver) Connect(_ context.Context, cfg tuiadapterruntime.ConnectConfig) (tuiadapterruntime.StatusSnapshot, error) {
	d.connectCalls++
	d.lastConnect = cfg
	if d.connectStatus.Model != "" || d.connectStatus.Workspace != "" || d.connectStatus.ModeLabel != "" {
		return d.connectStatus, nil
	}
	return d.status, nil
}
func (d *bridgeTestDriver) UseModel(_ context.Context, alias string, reasoningEffort ...string) (tuiadapterruntime.StatusSnapshot, error) {
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
func (d *bridgeTestDriver) CycleSessionMode(context.Context) (tuiadapterruntime.StatusSnapshot, error) {
	return d.status, nil
}
func (d *bridgeTestDriver) SetSandboxBackend(context.Context, string) (tuiadapterruntime.StatusSnapshot, error) {
	return d.status, nil
}
func (d *bridgeTestDriver) SetSandboxMode(context.Context, string) (tuiadapterruntime.StatusSnapshot, error) {
	return d.status, nil
}
func (d *bridgeTestDriver) ListAgents(context.Context, int) ([]tuiadapterruntime.AgentCandidate, error) {
	d.listAgentCalls++
	return d.agentList, nil
}
func (d *bridgeTestDriver) AgentStatus(context.Context) (tuiadapterruntime.AgentStatusSnapshot, error) {
	d.agentStatusCalls++
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) AddAgent(_ context.Context, target string) (tuiadapterruntime.AgentStatusSnapshot, error) {
	d.addAgentCalls++
	d.lastAddedAgent = target
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) RemoveAgent(_ context.Context, target string) (tuiadapterruntime.AgentStatusSnapshot, error) {
	d.removeAgentCalls++
	d.lastRemovedAgent = target
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) HandoffAgent(_ context.Context, target string) (tuiadapterruntime.AgentStatusSnapshot, error) {
	d.handoffAgentCalls++
	d.lastHandoffAgent = target
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) AskAgent(_ context.Context, target string, prompt string) (tuiadapterruntime.AgentStatusSnapshot, error) {
	d.askAgentCalls++
	d.lastAskedAgent = target
	d.lastAskedPrompt = prompt
	return d.agentStatus, nil
}
func (d *bridgeTestDriver) StartAgentSubagent(_ context.Context, agent string, prompt string) (tuiadapterruntime.SubagentSnapshot, error) {
	d.lastStartedAgent = agent
	d.lastStartedPrompt = prompt
	return d.subagentSnapshot, nil
}
func (d *bridgeTestDriver) ContinueSubagent(_ context.Context, handle string, prompt string) (tuiadapterruntime.SubagentSnapshot, error) {
	d.lastContinuedHandle = handle
	d.lastContinuedPrompt = prompt
	return d.subagentSnapshot, nil
}
func (d *bridgeTestDriver) SubscribeSubagentStream(_ context.Context, taskID string) (<-chan tuiadapterruntime.SubagentStreamFrame, bool) {
	d.subagentStreamSubscribeCalls++
	if d.subagentStream == nil {
		return nil, false
	}
	d.lastWaitTaskID = taskID
	return d.subagentStream, true
}
func (d *bridgeTestDriver) WaitSubagent(_ context.Context, taskID string) (tuiadapterruntime.SubagentSnapshot, error) {
	d.waitSubagentCalls++
	d.lastWaitTaskID = taskID
	if strings.TrimSpace(d.waitSubagentSnapshot.TaskID) == "" {
		return d.subagentSnapshot, nil
	}
	return d.waitSubagentSnapshot, nil
}
func (d *bridgeTestDriver) CompleteMention(context.Context, string, int) ([]tuiadapterruntime.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) CompleteFile(context.Context, string, int) ([]tuiadapterruntime.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) CompleteSkill(context.Context, string, int) ([]tuiadapterruntime.CompletionCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) CompleteResume(context.Context, string, int) ([]tuiadapterruntime.ResumeCandidate, error) {
	return nil, nil
}
func (d *bridgeTestDriver) CompleteSlashArg(context.Context, string, string, int) ([]tuiadapterruntime.SlashArgCandidate, error) {
	return nil, nil
}
