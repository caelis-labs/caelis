package tuiapp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	sdkmodel "github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskstream "github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
)

func TestHandleACPEventEnvelopeAppliesToolTerminalSequence(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "echo ok",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusPending,
			RawInput:      map[string]any{"command": "echo ok"},
			Content:       []schema.ToolCallContent{{Type: "terminal", TerminalID: "call-1"}},
			Meta:          metautil.WithTerminalInfo(nil, "call-1"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("echo ok"),
			Kind:          stringPtr(schema.ToolKindExecute),
			Status:        stringPtr(schema.ToolStatusCompleted),
			RawInput:      map[string]any{"command": "echo ok"},
			Content:       []schema.ToolCallContent{{Type: "terminal", TerminalID: "call-1"}},
			Meta:          metautil.WithTerminalOutput(nil, "call-1", "ok\n"),
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one tool event", block.Events)
	}
	event := block.Events[0]
	if event.Kind != SEToolCall || event.CallID != "call-1" || !event.Terminal || !event.Done {
		t.Fatalf("tool event = %#v, want completed ACP terminal call", event)
	}
	if !strings.Contains(event.Output, "ok") {
		t.Fatalf("tool output = %q, want terminal output", event.Output)
	}
	model.syncViewportContent()
	if strings.Contains(strings.Join(model.viewportPlainLines, "\n"), "• Tool") {
		t.Fatalf("viewport rendered generic Tool header: %#v", model.viewportPlainLines)
	}
}

func TestHandleACPEventEnvelopeDisplaysTaskOwnedRunCommandOutputOnce(t *testing.T) {
	t.Parallel()

	const output = "异步任务执行完毕，耗时 6 秒\n"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(241, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "command-1",
			Title: "RunCommand sleep 6", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"command": "sleep 6"}, Meta: acpToolNameMeta("RunCommand"),
		},
	})
	ref := taskstream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "terminal-1"}
	frames := acpprojector.ProjectTaskStreamFrame(acpprojector.StreamRequest{
		SessionRef: session.SessionRef{SessionID: "session-1"}, CallID: "command-1",
		ToolName: "RunCommand", Ref: ref, Scope: eventstream.ScopeMain,
	}, taskstream.Frame{
		Ref: ref, Text: output, Cursor: taskstream.Cursor{Output: int64(len([]byte(output)))},
		Running: true, State: "running",
	})
	if len(frames) != 1 {
		t.Fatalf("task stream projection = %#v, want one terminal frame", frames)
	}
	model = applyACPEnvelopeForTest(t, model, frames[0])

	meta := metautil.WithRuntimeSection(acpToolNameMeta("RunCommand"), metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID: "task-1", "kind": "command",
		metautil.RuntimeOutputDelta: output, "running": false, "state": "completed",
	})
	event := &session.Event{
		ID: "command-final", Seq: 2, SessionID: "session-1", Type: session.EventTypeToolResult,
		Visibility: session.VisibilityCanonical, Meta: meta,
		Tool: &session.EventTool{
			ID: "command-1", Name: "RunCommand", Kind: schema.ToolKindExecute,
			Title: "RunCommand sleep 6", Status: "completed",
			Input: map[string]any{"command": "sleep 6"}, Output: map[string]any{"stdout": output, "exit_code": 0},
			Content: []session.EventToolContent{{Type: "terminal", TerminalID: "terminal-1", Text: output}},
		},
	}
	base := acpprojector.EnvelopeBaseFromSessionEvent(
		session.SessionRef{SessionID: "session-1"}, event, acpprojector.SessionEventTransport{TurnID: "turn-1"},
	)
	final := acpprojector.ProjectSessionEventLiveSupplementEnvelope(base, event, agent.PublishedTerminal)
	if len(final) != 1 {
		t.Fatalf("live final projection = %#v, want one state envelope", final)
	}
	model = applyACPEnvelopeForTest(t, model, final[0])

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 || block.Events[0].Output != output || !block.Events[0].Done {
		t.Fatalf("RunCommand events = %#v, want one completed panel with one exact output", block.Events)
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if got := strings.Count(plain, strings.TrimSpace(output)); got != 1 {
		t.Fatalf("rendered RunCommand output occurrences = %d, want 1:\n%s", got, plain)
	}
}

func TestHandleACPEventEnvelopeRendersTaskWriteInteractionAndKeepsUTF8RunCommandOutputOnce(t *testing.T) {
	t.Parallel()

	const output = "请输入你的名字: 小明\r\n你好, 小明! (输入来自 stdin/TTY)\r\n"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(242, 0))
	commandMeta := metautil.WithRuntimeSection(acpToolNameMeta("RunCommand"), metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID:         "task-4",
		metautil.RuntimeTaskTerminalID: "terminal-4",
		"handle":                       "command-4",
		"kind":                         "command",
		"running":                      true,
		"state":                        "running",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "command-call-4",
			Title: "RunCommand read name", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"command": `read -p "请输入你的名字: " name && echo "你好, $name! (输入来自 stdin/TTY)"`},
			Meta:     commandMeta,
		},
	})
	ref := taskstream.Ref{SessionID: "session-1", TaskID: "task-4", TerminalID: "terminal-4"}
	frames := acpprojector.ProjectTaskStreamFrame(acpprojector.StreamRequest{
		SessionRef: session.SessionRef{SessionID: "session-1"}, CallID: "command-call-4",
		ToolName: "RunCommand", TaskHandle: "command-4", Ref: ref, Scope: eventstream.ScopeMain,
	}, taskstream.Frame{
		Ref: ref, Text: output, Cursor: taskstream.Cursor{Output: int64(len([]byte(output)))},
		Running: true, State: "running",
	})
	if len(frames) != 1 {
		t.Fatalf("task stream projection = %#v, want one terminal frame", frames)
	}
	model = applyACPEnvelopeForTest(t, model, frames[0])

	taskInput := map[string]any{
		"action": "write", "handle": "command-4", "target_kind": "command", "input": "小明",
	}
	taskMeta := metautil.WithRuntimeSection(acpToolNameMeta("Task"), metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "Task", metautil.RuntimeToolAction: "write",
		metautil.RuntimeToolInput: "小明", metautil.RuntimeTargetKind: "command", metautil.RuntimeTargetHandle: "command-4",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "task-write-4",
			Title: "Task write command-4", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: taskInput, Meta: taskMeta,
		},
	})
	completed := schema.ToolStatusCompleted
	taskMeta = metautil.WithRuntimeSection(taskMeta, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID: "task-4", metautil.RuntimeOutputDelta: output,
		"handle": "command-4", "kind": "command", "running": false, "state": "completed",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "task-write-4", Status: &completed,
			RawInput: taskInput, RawOutput: map[string]any{"handle": "command-4", "state": "completed", "result": output},
			Meta: taskMeta,
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	var tools []SubagentEvent
	for _, event := range block.Events {
		if event.Kind == SEToolCall {
			tools = append(tools, event)
		}
	}
	if len(tools) != 2 || tools[0].CallID != "command-call-4" || tools[0].Output != output ||
		tools[1].CallID != "task-write-4" || tools[1].Output != "" || !tools[1].Done || tools[1].Err {
		t.Fatalf("events = %#v, want RunCommand output plus one compact Task write interaction", block.Events)
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "• Interacted with background shell: 小明") {
		t.Fatalf("Task write interaction is not visible:\n%s", plain)
	}
	if strings.Contains(plain, "Task Write") || strings.Contains(plain, "command-4") {
		t.Fatalf("Task write interaction leaked its tool name or internal handle:\n%s", plain)
	}
	if strings.ContainsRune(plain, '\uFFFD') || strings.Count(plain, "你好, 小明! (输入来自 stdin/TTY)") != 1 {
		t.Fatalf("UTF-8 RunCommand output was corrupted or duplicated:\n%s", plain)
	}
}

func TestHandleACPEventEnvelopeRendersTaskWriteFailureWithoutInternalHandle(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(243, 0))
	taskInput := map[string]any{
		"action": "write", "handle": "command-10", "target_kind": "command", "input": "test",
	}
	taskMeta := metautil.WithRuntimeSection(acpToolNameMeta("Task"), metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "Task", metautil.RuntimeToolAction: "write",
		metautil.RuntimeToolInput: "test", metautil.RuntimeTargetKind: "command", metautil.RuntimeTargetHandle: "command-10",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "task-write-10",
			Title: "Task write command-10", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: taskInput, Meta: taskMeta,
		},
	})
	failed := schema.ToolStatusFailed
	const reason = `agent-sdk/runtime: command task "command-10" does not accept stdin`
	const displayedReason = "agent-sdk/runtime: command task does not accept stdin"
	taskMeta = metautil.WithRuntimeSection(taskMeta, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeOutputDelta: "existing shell output\n", "handle": "command-10", "kind": "command",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "task-write-10", Status: &failed,
			RawInput: taskInput, RawOutput: map[string]any{
				"action": "write", "handle": "command-10", "target_kind": "command", "error": reason,
			},
			Meta: taskMeta,
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 || !block.Events[0].Done || !block.Events[0].Err || block.Events[0].Output != displayedReason {
		t.Fatalf("Task write failure = %#v, want one visible interaction with its rejection reason", block.Events)
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "• Interacted with background shell: test failed") || !strings.Contains(plain, displayedReason) {
		t.Fatalf("Task write failure did not render its semantic action and reason:\n%s", plain)
	}
	if strings.Contains(plain, "Task Write") || strings.Contains(plain, "command-10") {
		t.Fatalf("Task write failure leaked its tool name or internal handle:\n%s", plain)
	}
}

func TestHandleACPEventEnvelopeRendersTerminalTaggedCommandTaskWriteAsShellInteraction(t *testing.T) {
	t.Parallel()

	const input = "你好，确认一下工作目录和模型，一句话回答"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(244, 0))
	taskInput := map[string]any{
		"action": "write", "handle": "command-82", "target_kind": "command", "input": input,
	}
	taskMeta := metautil.WithRuntimeSection(acpToolNameMeta("Task"), metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "Task", metautil.RuntimeToolAction: "write",
		metautil.RuntimeToolInput: input, metautil.RuntimeTargetKind: "command", metautil.RuntimeTargetHandle: "command-82",
	})
	taskMeta = metautil.WithRuntimeSection(taskMeta, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID: "task-82", metautil.RuntimeTaskTerminalID: "terminal-82",
		"handle": "command-82", "kind": "command", "running": true, "state": "running",
	})
	taskMeta = metautil.WithTerminalInfo(taskMeta, "terminal-82")
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "task-write-82",
			Title: "Task write command-82", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: taskInput, Content: []schema.ToolCallContent{{Type: "terminal", TerminalID: "terminal-82"}},
			Meta: taskMeta,
		},
	})
	completed := schema.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "task-write-82", Status: &completed,
			RawInput: taskInput, RawOutput: map[string]any{"handle": "command-82", "state": "completed", "action": "write"},
			Meta: taskMeta,
		},
	})

	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "• Interacted with background shell: "+input) {
		t.Fatalf("terminal-tagged command Task write is not visible as a shell interaction:\n%s", plain)
	}
	if strings.Contains(plain, "Ran Task Write") || strings.Contains(plain, "Task Write") || strings.Contains(plain, "command-82") {
		t.Fatalf("terminal-tagged command Task write leaked the execute/Ran header or handle:\n%s", plain)
	}
}

func TestHandleACPEventEnvelopeKeepsStreamedRunCommandOutputOnEmptyFinalFrame(t *testing.T) {
	t.Parallel()

	const command = `for i in 1 2 3 4 5; do echo "步骤 $i: 正在处理..."; sleep 1; done; echo "=== 全部完成 ==="`
	const output = "步骤 1: 正在处理...\n步骤 2: 正在处理...\n步骤 3: 正在处理...\n步骤 4: 正在处理...\n步骤 5: 正在处理...\n=== 全部完成 ===\n"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(240, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "command-1",
			Title:         "RunCommand " + command,
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": command, "yield_time_ms": 250},
			Content:       []schema.ToolCallContent{{Type: "terminal", TerminalID: "command-1"}},
			Meta:          acpToolNameMeta("RunCommand"),
		},
	})

	running := schema.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "command-1",
			Status:        &running,
			Meta:          runningSnapshotTerminalMeta("RunCommand", "task-1", "command-1", output, "append"),
		},
	})

	completed := schema.ToolStatusCompleted
	exitCode := 0
	finalMeta := metautil.WithRuntimeSection(acpToolNameMeta("RunCommand"), metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID:         "task-1",
		metautil.RuntimeTaskTerminalID: "command-1",
		"output_cursor":                len([]byte(output)),
		"running":                      false,
		"state":                        "completed",
	})
	finalMeta = metautil.WithTerminalInfo(finalMeta, "command-1")
	finalMeta = metautil.WithTerminalExit(finalMeta, "command-1", &exitCode, nil)
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "command-1",
			Status:        &completed,
			Meta:          finalMeta,
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one RunCommand panel", block.Events)
	}
	if event := block.Events[0]; !event.Done || event.Err || event.Output != output || strings.Contains(event.Output, "(no output)") {
		t.Fatalf("RunCommand final event = %#v, want streamed output preserved", event)
	}
}

func TestResumeUsesDurableTaskWaitResultWhenCommandTransientOutputIsMissing(t *testing.T) {
	t.Parallel()

	const recovered = "步骤 1: 正在处理...\n=== 全部完成 ===\n"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(242, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "command-1",
			Title: "RunCommand long job", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"command": "long job", "yield_time_ms": 250},
			Content:  []schema.ToolCallContent{{Type: "terminal", TerminalID: "terminal-1"}},
			Meta:     acpToolNameMeta("RunCommand"),
		},
	})
	completed := schema.ToolStatusCompleted
	exitCode := 0
	commandFinalMeta := metautil.WithRuntimeSection(acpToolNameMeta("RunCommand"), metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID: "task-1", metautil.RuntimeTaskTerminalID: "terminal-1",
		"running": false, "state": "completed",
	})
	commandFinalMeta = metautil.WithTerminalInfo(commandFinalMeta, "terminal-1")
	commandFinalMeta = metautil.WithTerminalExit(commandFinalMeta, "terminal-1", &exitCode, nil)
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1", Status: &completed, Meta: commandFinalMeta,
		},
	})
	model = applyACPEnvelopeForTest(t, model, completedRegressionTurn("session-1", "turn-1"))

	model.commitUserDisplayLine("resume")
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(243, 0))
	taskInput := map[string]any{"action": "wait", "task_id": "task-1", "target_kind": "command"}
	taskMeta := metautil.WithRuntimeSection(acpToolNameMeta("Task"), metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "Task", metautil.RuntimeToolAction: "wait",
		metautil.RuntimeTargetID: "task-1", metautil.RuntimeTargetKind: "command",
	})
	taskMeta = metautil.WithRuntimeSection(taskMeta, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID: "task-1", metautil.RuntimeTaskTerminalID: "terminal-1",
		"running": false, "state": "completed",
	})
	taskMeta = metautil.WithTerminalInfo(taskMeta, "terminal-1")
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-2", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "task-wait-1",
			Title: "Task wait task-1", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: taskInput, Meta: taskMeta,
		},
	})
	if model.runningActivity.Phase != runningPhaseToolWait || model.runningActivity.Target != runningTargetShell {
		t.Fatalf("runningActivity = %#v, want Wait shell while Task observes command", model.runningActivity)
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-2", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "task-wait-1", Status: &completed,
			RawInput: taskInput, RawOutput: map[string]any{
				"action": "wait", "task_id": "task-1", "target_kind": "command", "state": "completed", "result": recovered,
			},
			Meta: taskMeta,
		},
	})

	blocks := mainACPTurnBlocksForTest(model)
	if len(blocks) != 1 || len(blocks[0].Events) != 1 {
		t.Fatalf("resume blocks = %#v, want only the original command panel", blocks)
	}
	if command := blocks[0].Events[0]; command.Output != recovered || !command.OutputSynthetic || strings.Contains(command.Output, "(no output)") {
		t.Fatalf("recovered command = %#v, want replaceable durable Task snapshot in empty owner panel", command)
	}
	if model.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("runningActivity = %#v, want model waiting after Task wait completes", model.runningActivity)
	}
}

func TestTaskReadUsesActivityHintAndFoldsObservationIntoCommandOwner(t *testing.T) {
	t.Parallel()

	const (
		first  = "步骤 1\n"
		second = "步骤 2\n"
		third  = "步骤 3\n"
	)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(244, 0))
	apply := func(update schema.Update) {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1",
			Scope: eventstream.ScopeMain, Update: update,
		})
	}
	applyStream := func(update schema.Update, sequence uint64) {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "terminal-1", Scope: eventstream.ScopeMain,
			ParentTool: &eventstream.ParentToolRelation{ToolCallID: "command-call", ToolName: "RunCommand"},
			Delivery:   &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
			Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
				Generation: "task-stream-1", Sequence: sequence,
			}},
			Update: update,
		})
	}
	apply(schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall, ToolCallID: "command-call",
		Title: "RunCommand long job", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
		RawInput: map[string]any{"command": "long job"},
		Content:  []schema.ToolCallContent{{Type: "terminal", TerminalID: "terminal-1"}},
		Meta:     acpToolNameMeta("RunCommand"),
	})
	running := schema.ToolStatusInProgress
	firstMeta := runningSnapshotTerminalMeta("RunCommand", "command-task", "terminal-1", first, "append")
	firstMeta = metautil.WithRuntimeSection(firstMeta, metautil.RuntimeTask, map[string]any{
		"handle": "command-3",
	})
	firstMeta = metautil.WithRuntimeSection(firstMeta, metautil.RuntimeStream, map[string]any{
		metautil.RuntimeStreamMode:   "append",
		metautil.RuntimeOutputCursor: int64(len([]byte(first))),
	})
	applyStream(schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-call",
		Status: &running, Meta: firstMeta,
	}, 1)
	if owner := model.runningHintTracker.ownersByHandle["command-3"]; owner.Key != "" {
		t.Fatalf("background stream owner = %#v, want no hint ownership", owner)
	}

	readInput := map[string]any{"action": "read", "handle": "command-3"}
	readMeta := metautil.WithRuntimeSection(acpToolNameMeta("Task"), metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "Task", metautil.RuntimeToolAction: "read",
		metautil.RuntimeTargetHandle: "command-3", metautil.RuntimeTargetKind: "command",
	})
	readMeta = metautil.WithRuntimeSection(readMeta, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID: "command-task", metautil.RuntimeTaskTerminalID: "terminal-1",
		metautil.RuntimeOutputStart:  int64(len([]byte(first))),
		metautil.RuntimeOutputCursor: int64(len([]byte(first + second))),
		metautil.RuntimeOutputDelta:  second,
		"running":                    true, "state": "running", "handle": "command-3",
	})
	apply(schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall, ToolCallID: "task-read",
		Title: "Task read command-3", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
		RawInput: readInput, Meta: readMeta,
	})
	if model.runningActivity.Phase != runningPhaseToolWait || model.runningActivity.Target != runningTargetShell {
		t.Fatalf("runningActivity = %#v, want Task read to preserve the running shell owner", model.runningActivity)
	}
	if hint := model.buildHintText(); !strings.Contains(hint, "Waiting on shell") || strings.Contains(hint, "command-3") {
		t.Fatalf("hint = %q, want shell activity without raw handle", hint)
	}
	completed := schema.ToolStatusCompleted
	apply(schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "task-read", Status: &completed,
		RawInput: readInput,
		RawOutput: map[string]any{
			"action": "read", "handle": "command-3", "target_kind": "command",
			"state": "running", "latest_output": "compact model-only preview\n",
		},
		Meta: readMeta,
	})

	block := requireMainACPTurnBlockForTest(t, model)
	physical := physicalTranscriptEventsForTest(block.Events)
	if len(physical) != 1 || physical[0].CallID != "command-call" {
		t.Fatalf("events = %#v, want only the RunCommand panel", block.Events)
	}
	if got := physical[0].Output; got != first+second {
		t.Fatalf("command output after Task read = %q, want %q", got, first+second)
	}

	duplicateMeta := runningSnapshotTerminalMeta("RunCommand", "command-task", "terminal-1", second, "append")
	duplicateMeta = metautil.WithRuntimeSection(duplicateMeta, metautil.RuntimeStream, map[string]any{
		metautil.RuntimeStreamMode:   "append",
		metautil.RuntimeOutputCursor: int64(len([]byte(first + second))),
	})
	applyStream(schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-call",
		Status: &running, Meta: duplicateMeta,
	}, 2)
	thirdMeta := runningSnapshotTerminalMeta("RunCommand", "command-task", "terminal-1", third, "append")
	thirdMeta = metautil.WithRuntimeSection(thirdMeta, metautil.RuntimeStream, map[string]any{
		metautil.RuntimeStreamMode:   "append",
		metautil.RuntimeOutputCursor: int64(len([]byte(first + second + third))),
	})
	applyStream(schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-call",
		Status: &running, Meta: thirdMeta,
	}, 3)
	block = requireMainACPTurnBlockForTest(t, model)
	physical = physicalTranscriptEventsForTest(block.Events)
	if got := physical[0].Output; got != first+second+third {
		t.Fatalf("command output after overlapping stream = %q, want %q", got, first+second+third)
	}

	terminalReadInput := map[string]any{"action": "read", "handle": "command-3"}
	terminalReadMeta := metautil.WithRuntimeSection(acpToolNameMeta("Task"), metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "Task", metautil.RuntimeToolAction: "read",
		metautil.RuntimeTargetHandle: "command-3", metautil.RuntimeTargetKind: "command",
	})
	apply(schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall, ToolCallID: "task-read-terminal",
		Title: "Task read command-3", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
		RawInput: terminalReadInput, Meta: terminalReadMeta,
	})
	if model.runningActivity.Phase != runningPhaseToolWait {
		t.Fatalf("runningActivity = %#v, want terminal Task read to preserve the running shell owner", model.runningActivity)
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "command-call",
			ToolName:   "RunCommand",
		},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "task-read-terminal",
			Status:        &completed,
			RawInput:      terminalReadInput,
			RawOutput: map[string]any{
				"action": "read", "handle": "command-3", "target_kind": "command",
				"parent_call": "command-call", "parent_tool": "RunCommand", "state": "completed",
			},
			Meta: terminalReadMeta,
		},
	})
	if _, active := model.runningHintTracker.active["tool:turn-1:command-call"]; active {
		t.Fatalf("active activities = %#v, want terminal Task read to close RunCommand owner", model.runningHintTracker.active)
	}
	if model.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("runningActivity = %#v, want model waiting after Task read and RunCommand both close", model.runningActivity)
	}
}

func TestHandleACPEventEnvelopeMergesGrokGlobUpdate(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "glob-1",
			Title:         "Glob",
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "glob-1",
			Title:         stringPtr("Glob `**/*.py`"),
			Kind:          stringPtr(schema.ToolKindSearch),
			RawInput:      map[string]any{"variant": "CursorGlob", "glob_pattern": "**/*.py"},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one Grok glob event", block.Events)
	}
	event := block.Events[0]
	if event.Name != schema.ToolKindSearch || event.ToolKind != schema.ToolKindSearch || event.Args != "`**/*.py`" {
		t.Fatalf("glob event = %#v, want merged search update with pattern args", event)
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "Search `**/*.py`") || strings.Contains(plain, "Glob Glob") {
		t.Fatalf("viewport rendered unexpected glob header:\n%s", plain)
	}
}

func TestHandleACPEventEnvelopeMergesGrokShellUpdate(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "shell-1",
			Title:         "Shell",
			RawInput: map[string]any{
				"command":     "pwd",
				"description": "Print current working directory",
			},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "shell-1",
			Title:         stringPtr("Execute `pwd`"),
			Kind:          stringPtr(schema.ToolKindExecute),
			RawInput: map[string]any{
				"variant":     "CursorShell",
				"command":     "pwd",
				"description": "Print current working directory",
			},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one Grok shell event", block.Events)
	}
	event := block.Events[0]
	if event.Name != schema.ToolKindExecute || event.Args != "pwd" || event.ToolKind != schema.ToolKindExecute {
		t.Fatalf("shell event = %#v, want merged execute kind with command args", event)
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "Ran pwd") || strings.Contains(plain, "Ran Shell") {
		t.Fatalf("viewport rendered unexpected shell header:\n%s", plain)
	}
}

func TestHandleACPEventEnvelopeMergesPartialToolUpdateFromCaelisOutput(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "RunCommand pwd",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": "pwd"},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        stringPtr(schema.ToolStatusCompleted),
			RawOutput:     map[string]any{"stdout": "/tmp/work\n", "exit_code": 0},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one merged tool event", block.Events)
	}
	event := block.Events[0]
	if event.Name != schema.ToolKindExecute || event.ToolKind != schema.ToolKindExecute || event.Args != "pwd" || !event.Done {
		t.Fatalf("tool event = %#v, want partial update to preserve existing identity and complete", event)
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "Ran pwd") || strings.Contains(plain, "Ran Tool") {
		t.Fatalf("viewport rendered unexpected partial update header:\n%s", plain)
	}
}

func TestHandleACPEventEnvelopeDoesNotDuplicateRunningSnapshotAfterTerminalStream(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	kind := schema.ToolKindExecute
	status := schema.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "RunCommand echo ok",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": "echo ok"},
			Meta:          runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "", ""),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("RunCommand echo ok"),
			Kind:          &kind,
			Meta:          runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "Step 1/5\nStep 2/5\n", "append"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("RunCommand echo ok"),
			Kind:          &kind,
			Status:        &status,
			RawInput:      map[string]any{"command": "echo ok"},
			RawOutput:     map[string]any{"latest_output": "Step 1/5\nStep 2/5\n", "state": "running"},
			Meta:          runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "Step 1/5\nStep 2/5\n", ""),
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one RunCommand event", block.Events)
	}
	if got, want := block.Events[0].Output, "Step 1/5\nStep 2/5\n"; got != want {
		t.Fatalf("tool output = %q, want live stream output once %q", got, want)
	}
}

func TestHandleACPEventEnvelopePreservesSplitTerminalNewlineFrame(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "RunCommand for i in 1 2",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": "for i in 1 2; do echo Step $i/2; done"},
			Meta:          runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "", ""),
		},
	})
	for _, text := range []string{"Step 1/2", "\n", "Step 2/2\n"} {
		model = applyACPEnvelopeForTest(t, model, terminalMetaStreamEnvelope("call-1", text))
	}

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one RunCommand event", block.Events)
	}
	if got, want := block.Events[0].Output, "Step 1/2\nStep 2/2\n"; got != want {
		t.Fatalf("tool output = %q, want %q", got, want)
	}
}

func TestHandleACPEventEnvelopePreservesStandardTerminalPatchNewlines(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "RunCommand for i in 1 2",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"command": "for i in 1 2; do echo Step $i/2; done"},
			Meta:          runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "", ""),
		},
	})
	for _, text := range []string{"[1/2] 14:57:57\n", "[2/2] 14:57:58\n"} {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			Update: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Meta:          metautil.WithTerminalOutput(acpToolNameMeta("RunCommand"), "call-1", text),
			},
		})
	}

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one RunCommand event", block.Events)
	}
	const want = "[1/2] 14:57:57\n[2/2] 14:57:58\n"
	if got := block.Events[0].Output; got != want {
		t.Fatalf("tool output = %q, want %q", got, want)
	}
}

func TestHandleACPEventEnvelopeAppliesParticipantSequence(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindParticipant,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		ParticipantID: "agent-1",
		Actor:         "@lina",
		Meta:          map[string]any{"agent": "codex", "handle": "lina"},
		Participant:   &eventstream.Participant{State: "attached"},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		ParticipantID: "agent-1",
		Actor:         "@lina",
		Meta:          map[string]any{"agent": "codex", "handle": "lina"},
		Final:         true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "participant answer"},
		},
	})

	block := model.findParticipantTurnBlock("participant-turn-1")
	if block == nil {
		t.Fatal("participant block missing")
	}
	if block.Actor != "/codex(lina)" || block.Status != "completed" {
		t.Fatalf("participant block = %#v, want /codex(lina) completed", block)
	}
	if len(block.Events) != 1 || block.Events[0].Kind != SEAssistant || block.Events[0].Text != "participant answer" {
		t.Fatalf("participant events = %#v, want assistant answer", block.Events)
	}
}

func TestHandleACPEventEnvelopeShowsParticipantFailureBeforeTerminal(t *testing.T) {
	t.Parallel()

	const (
		turnID       = "participant-turn-1"
		displayError = "model request hit provider backpressure after 5 retries"
	)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(100, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		HandleID:      "handle-1",
		RunID:         "run-1",
		TurnID:        turnID,
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       turnID,
		ParticipantID: "side-reviewer",
		Actor:         "user",
		Meta: map[string]any{
			"agent":           "review",
			"handle":          "reviewer",
			"display_address": "/review",
		},
		Final: true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: "inspect the change"},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindError,
		SessionID:     "session-1",
		HandleID:      "handle-1",
		RunID:         "run-1",
		TurnID:        turnID,
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       turnID,
		ParticipantID: "side-reviewer",
		Err: &sdkmodel.RetryExhaustedError{
			MaxRetries:   5,
			Backpressure: true,
			Cause:        errors.New("model: http status 529 body=provider-secret"),
		},
	})

	block := model.findParticipantTurnBlock(turnID)
	if block == nil {
		t.Fatal("participant block missing")
	}
	if len(block.Events) != 1 ||
		block.Events[0].Kind != SENotice ||
		block.Events[0].Text != displayError {
		t.Fatalf("participant error events = %#v, want sanitized notice", block.Events)
	}
	if strings.Contains(block.Events[0].Text, "provider-secret") {
		t.Fatalf("participant error leaked provider detail: %q", block.Events[0].Text)
	}
	if participantTurnIsTerminal(block.Status) {
		t.Fatalf("participant status = %q, want error visible before terminal", block.Status)
	}
	if !model.turnRunning() {
		t.Fatal("main turn stopped before terminal lifecycle")
	}
	if len(model.subagentOutputViews) != 0 {
		t.Fatalf("participant error created Spawn output view: %#v", model.subagentOutputViews)
	}
	renderedBeforeTerminal := strings.Join(
		renderedPlainRows(block.Render(model.blockRenderContext(100))),
		"\n",
	)
	if got := strings.Count(renderedBeforeTerminal, displayError); got != 1 {
		t.Fatalf("rendered participant error count = %d, want one\n%s", got, renderedBeforeTerminal)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.TurnFailed(
		"handle-1",
		"run-1",
		turnID,
		displayError,
		time.Unix(101, 0),
	))
	if model.turnRunning() {
		t.Fatal("main turn still running after failed terminal")
	}
	if got := strings.ToLower(strings.TrimSpace(block.Status)); got != eventstream.LifecycleStateFailed {
		t.Fatalf("participant status = %q, want failed", block.Status)
	}
	if len(block.Events) != 1 {
		t.Fatalf("participant events after terminal = %#v, want failure reason exactly once", block.Events)
	}
	model.syncViewportContent()
	renderedAfterTerminal := strings.Join(model.viewportPlainLines, "\n")
	if got := strings.Count(renderedAfterTerminal, displayError); got != 1 {
		t.Fatalf("rendered terminal failure count = %d, want one\n%s", got, renderedAfterTerminal)
	}
}

func TestDirectedParticipantUserDisplayUsesAgentAndHumanHandle(t *testing.T) {
	t.Parallel()

	event := TranscriptEvent{
		Scope: ACPProjectionParticipant,
		Actor: "@lina",
		Meta:  map[string]any{"agent": "codex", "handle": "lina"},
		Text:  "inspect the workspace",
	}
	if got, want := directedParticipantUserDisplay(event), "/codex(lina) inspect the workspace"; got != want {
		t.Fatalf("directedParticipantUserDisplay() = %q, want %q", got, want)
	}
}

func TestDirectedParticipantUserDisplayPrefersSubmittedAddress(t *testing.T) {
	t.Parallel()

	event := TranscriptEvent{
		Scope: ACPProjectionParticipant,
		Actor: "@wen",
		Meta: map[string]any{
			"agent":           "grok",
			"handle":          "wen",
			"display_address": "/zenith(wen)",
		},
		Text: "introduce yourself",
	}
	if got, want := directedParticipantUserDisplay(event), "/zenith(wen) introduce yourself"; got != want {
		t.Fatalf("directedParticipantUserDisplay() = %q, want %q", got, want)
	}
}

func TestParticipantLiveAndFinalWithoutMessageIDRenderExactlyOnce(t *testing.T) {
	t.Parallel()

	const (
		turnID = "participant-turn-control-owned"
		answer = "opus owns this turn"
	)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       turnID,
		TurnID:        turnID,
		ParticipantID: "grok-1",
		Actor:         "user",
		Meta: map[string]any{
			"agent":           "grok",
			"handle":          "wen",
			"display_address": "/zenith(wen)",
		},
		Final: true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: "introduce yourself"},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       turnID,
		TurnID:        turnID,
		ParticipantID: "grok-1",
		Actor:         "@wen",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: answer},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       turnID,
		TurnID:        turnID,
		ParticipantID: "grok-1",
		Actor:         "@wen",
		EventID:       "event-final",
		ProjectionID:  "projection-final",
		Final:         true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: answer},
		},
	})

	block := model.findParticipantTurnBlock(turnID)
	if block == nil {
		t.Fatal("participant turn block missing")
	}
	if len(block.Events) != 1 || block.Events[0].Kind != SEAssistant || block.Events[0].Text != answer {
		t.Fatalf("participant events = %#v, want one final assistant answer", block.Events)
	}
	rendered := strings.Join(renderedPlainRows(block.Render(model.blockRenderContext(100))), "\n")
	if got := strings.Count(rendered, answer); got != 1 {
		t.Fatalf("rendered assistant answer count = %d, want one\n%s", got, rendered)
	}
}

func TestHandleACPEventEnvelopeDisplaysParticipantSkillContentReadAsSkill(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "side-reviewer",
		ParticipantID: "side-reviewer",
		Actor:         "@reviewer",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "skill-review-1",
			Title:         `Read <skill_content name="review">`,
			Kind:          schema.ToolKindRead,
			Status:        schema.ToolStatusInProgress,
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "side-reviewer",
		ParticipantID: "side-reviewer",
		Actor:         "@reviewer",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "skill-review-1",
			Title:         stringPtr(`Read <skill_content name="review">`),
			Kind:          stringPtr(schema.ToolKindRead),
			Status:        stringPtr(schema.ToolStatusCompleted),
		},
	})

	block := model.findParticipantTurnBlock("side-reviewer")
	if block == nil {
		t.Fatal("participant block missing")
	}
	if len(block.Events) != 1 || block.Events[0].Name != "Skill" || block.Events[0].Args != "review" || !block.Events[0].Done {
		t.Fatalf("participant events = %#v, want completed Skill review", block.Events)
	}
	rows := block.Render(BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme)})
	plain := joinRenderedPlain(rows)
	if !strings.Contains(plain, "Skill review") || strings.Contains(plain, "<skill_content") {
		t.Fatalf("rendered participant skill rows:\n%s", plain)
	}
}

func TestHandleACPEventEnvelopeAnchorsSubagentOutputToSpawnTool(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	spawnMeta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "Spawn",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-1",
			Title:         "Spawn reviewer: inspect",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusPending,
			RawInput:      map[string]any{"agent": "reviewer", "prompt": "inspect"},
			Meta:          spawnMeta,
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "task-1",
		Actor:     "reviewer",
		Final:     true,
		Meta: metautil.WithRuntimeSection(nil, metautil.RuntimeStream, map[string]any{
			metautil.RuntimeStreamParentCallID: "spawn-1",
			metautil.RuntimeStreamParentTool:   "Spawn",
		}),
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "subagent found the issue"},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want anchored spawn event", block.Events)
	}
	event := block.Events[0]
	if event.Kind != SEToolCall || event.CallID != "spawn-1" || event.Name != "Spawn" {
		t.Fatalf("spawn event = %#v, want Spawn tool call", event)
	}
	if strings.Contains(event.Output, "subagent found the issue") {
		t.Fatalf("spawn event = %#v, child output must stay in the detached view", event)
	}
	view := requireSubagentOutputViewForTest(t, model, "spawn-1")
	if got := subagentOutputNarrativeTextForTest(view, SEAssistant); got != "subagent found the issue" {
		t.Fatalf("detached child output = %q, want anchored narrative", got)
	}
}

func TestHandleACPEventEnvelopeStreamsDurableChildNarrativeBeforeCompletion(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(240, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-call-1",
			Title:         "Spawn explorer: inspect",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"agent": "explorer", "prompt": "inspect"},
			Meta:          acpToolNameMeta("Spawn"),
		},
	})

	chunks := []struct {
		eventID string
		text    string
		want    string
	}{
		{eventID: "child-mirror-1", text: "first ", want: "first "},
		{eventID: "child-mirror-2", text: "second", want: "first second"},
	}
	for index, chunk := range chunks {
		event := &session.Event{
			ID:         chunk.eventID,
			Seq:        uint64(index + 1),
			SessionID:  "session-1",
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityMirror,
			ChildOrigin: &session.EventChildOrigin{
				Scope:         session.EventChildScopeSubagent,
				ScopeID:       "task-1",
				TaskID:        "task-1",
				DelegationID:  "task-1",
				ParticipantID: "child-1",
				ACPSessionID:  "child-session-1",
				SourceEventID: chunk.eventID,
				ParentTool:    session.EventParentTool{CallID: "spawn-call-1", Name: "Spawn"},
			},
			Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				MessageID:     "message-1",
				Content:       session.ProtocolTextContent(chunk.text),
			}},
		}
		base := acpprojector.EnvelopeBaseFromSessionEvent(
			session.SessionRef{SessionID: "session-1"},
			event,
			acpprojector.SessionEventTransport{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"},
		)
		envelopes := acpprojector.ProjectSessionEventEnvelope(base, event)
		if len(envelopes) != 1 {
			t.Fatalf("child projection %d = %#v, want one envelope", index, envelopes)
		}
		model = applyACPEnvelopeForTest(t, model, envelopes[0])

		block := requireMainACPTurnBlockForTest(t, model)
		if len(block.Events) != 1 {
			t.Fatalf("after child chunk %d main events = %#v, want one Spawn panel", index, block.Events)
		}
		spawn := block.Events[0]
		if spawn.Done {
			t.Fatalf("after child chunk %d Spawn completed while child is still running: %#v", index, spawn)
		}
		if spawn.Output != "" {
			t.Fatalf("after child chunk %d Spawn leaked output %q", index, spawn.Output)
		}
		view := requireSubagentOutputViewForTest(t, model, "spawn-call-1")
		if got := subagentOutputNarrativeTextForTest(view, SEAssistant); got != chunk.want {
			t.Fatalf("after child chunk %d detached output = %q, want %q", index, got, chunk.want)
		}
	}

	if !model.turnRunning() {
		t.Fatal("child narrative ended the parent Turn before child completion")
	}
	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if strings.Contains(plain, "(wait subagent output)") || strings.Contains(plain, "first second") {
		t.Fatalf("main Spawn row leaked inline child output:\n%s", plain)
	}
	block := requireMainACPTurnBlockForTest(t, model)
	model.width = 96
	model.height = 28
	if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-call-1") {
		t.Fatal("running Spawn did not open its output overlay")
	}
	if overlay := subagentOutputOverlayPlain(model); !strings.Contains(overlay, "first second") {
		t.Fatalf("running output overlay did not preserve child chunks:\n%s", overlay)
	}

	completed := schema.ToolStatusCompleted
	finalMeta := metautil.WithRuntimeSection(acpToolNameMeta("Spawn"), metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID: "task-1",
		"running":              false,
		"state":                "completed",
		"result":               "first",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "spawn-call-1",
			Status:        &completed,
			Meta:          finalMeta,
		},
	})

	block = requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("after truncated final main events = %#v, want one Spawn panel", block.Events)
	}
	if spawn := block.Events[0]; !spawn.Done {
		t.Fatalf("after truncated final Spawn = %#v, want completed owner", spawn)
	}
	if got := subagentOutputNarrativeTextForTest(
		requireSubagentOutputViewForTest(t, model, "spawn-call-1"),
		SEAssistant,
	); got != "first second" {
		t.Fatalf("after truncated final detached output = %q, want complete live narrative preserved", got)
	}
}

func TestHandleACPEventEnvelopeChildFinalChunksDoNotCloseOrTruncateSpawn(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(245, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-call-1",
			Title:         "Spawn explorer: inspect",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"agent": "explorer", "prompt": "inspect"},
			Meta:          acpToolNameMeta("Spawn"),
		},
	})

	chunks := []struct {
		messageID string
		text      string
		want      string
	}{
		{messageID: "child-message-1", text: "当前", want: "当前"},
		{messageID: "child-message-1", text: "目录下共有 12 个文件", want: "当前目录下共有 12 个文件"},
		{messageID: "child-message-1", text: "。", want: "当前目录下共有 12 个文件。"},
	}
	for index, chunk := range chunks {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			TurnID:    "child-turn-1",
			Scope:     eventstream.ScopeSubagent,
			ScopeID:   "task-1",
			Actor:     "explorer",
			Final:     true,
			ParentTool: &eventstream.ParentToolRelation{
				ToolCallID: "spawn-call-1",
				ToolName:   "Spawn",
			},
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				MessageID:     chunk.messageID,
				Content:       schema.TextContent{Type: "text", Text: chunk.text},
			},
		})

		block := requireMainACPTurnBlockForTest(t, model)
		if len(block.Events) != 1 {
			t.Fatalf("after child chunk %d events = %#v, want one Spawn panel", index, block.Events)
		}
		spawn := block.Events[0]
		if spawn.Done || spawn.Output != "" {
			t.Fatalf("after child chunk %d Spawn = %#v, want compact running owner", index, spawn)
		}
		view := requireSubagentOutputViewForTest(t, model, "spawn-call-1")
		if got := subagentOutputNarrativeTextForTest(view, SEAssistant); got != chunk.want {
			t.Fatalf("after child chunk %d detached output = %q, want %q", index, got, chunk.want)
		}
	}

	completed := schema.ToolStatusCompleted
	finalMeta := metautil.WithRuntimeSection(acpToolNameMeta("Spawn"), metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID: "task-1",
		"running":              false,
		"state":                "completed",
		"result":               "。",
	})
	parentFinal := eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "spawn-call-1",
			Status:        &completed,
			Meta:          finalMeta,
		},
	}
	model = applyACPEnvelopeForTest(t, model, parentFinal)
	model = applyACPEnvelopeForTest(t, model, parentFinal)

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("after repeated parent final events = %#v, want one Spawn panel", block.Events)
	}
	if spawn := block.Events[0]; !spawn.Done {
		t.Fatalf("after repeated parent final Spawn = %#v, want completed owner", spawn)
	}
	if got := subagentOutputNarrativeTextForTest(
		requireSubagentOutputViewForTest(t, model, "spawn-call-1"),
		SEAssistant,
	); got != "当前目录下共有 12 个文件。" {
		t.Fatalf("after repeated parent final detached output = %q, want complete child narrative", got)
	}
}

func TestHandleACPEventEnvelopePreservesHiddenChildToolAsBlankMessageBoundary(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(247, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-call-1",
			Title: "Spawn explorer: inspect", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "explorer", "prompt": "inspect"}, Meta: acpToolNameMeta("Spawn"),
		},
	})
	childEnvelope := func(update schema.Update) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "child-turn-1",
			Scope: eventstream.ScopeSubagent, ScopeID: "task-1", Actor: "explorer",
			ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-call-1", ToolName: "Spawn"},
			Update:     update,
		}
	}
	model = applyACPEnvelopeForTest(t, model, childEnvelope(schema.ContentChunk{
		SessionUpdate: schema.UpdateAgentMessage,
		Content:       schema.TextContent{Type: "text", Text: "任务 3 完成。\n---"},
	}))
	model = applyACPEnvelopeForTest(t, model, childEnvelope(schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall, ToolCallID: "child-tool-1",
		Title: "Write", Kind: schema.ToolKindEdit, Status: schema.ToolStatusInProgress,
	}))
	model = applyACPEnvelopeForTest(t, model, childEnvelope(schema.ContentChunk{
		SessionUpdate: schema.UpdateAgentMessage,
		Content:       schema.TextContent{Type: "text", Text: "### 任务 4：创建文件"},
	}))

	view := requireSubagentOutputViewForTest(t, model, "spawn-call-1")
	physical := physicalTranscriptEventsForTest(view.block.Events)
	if len(physical) != 3 ||
		physical[0].Kind != SEAssistant || physical[0].Text != "任务 3 完成。\n---" ||
		physical[1].Kind != SEToolCall ||
		physical[2].Kind != SEAssistant || physical[2].Text != "### 任务 4：创建文件" {
		t.Fatalf("detached events = %#v, want a structured tool boundary between Markdown messages", physical)
	}
}

func TestHandleACPEventEnvelopeShowsChildToolActivityInRunningSpawn(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(248, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-call-1",
			Title: "Spawn explorer: inspect", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "explorer", "prompt": "inspect"}, Meta: acpToolNameMeta("Spawn"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "child-turn-1",
		Scope: eventstream.ScopeSubagent, ScopeID: "task-1", Actor: "explorer",
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-call-1", ToolName: "Spawn"},
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "child-tool-1",
			Title: "Read /workspace/config.go", Kind: schema.ToolKindRead, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"path": "/workspace/config.go"},
		},
	})

	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if strings.Contains(plain, "(wait subagent output)") {
		t.Fatalf("running Spawn still renders the empty-output placeholder:\n%s", plain)
	}
	block := requireMainACPTurnBlockForTest(t, model)
	model.width = 96
	model.height = 28
	if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-call-1") {
		t.Fatal("running Spawn did not open its output overlay")
	}
	overlay := subagentOutputOverlayPlain(model)
	if !strings.Contains(strings.ToLower(overlay), "read") || !strings.Contains(overlay, "config.go") {
		t.Fatalf("output overlay does not expose child tool activity:\n%s", overlay)
	}

	completed := schema.ToolStatusCompleted
	readKind := schema.ToolKindRead
	readTitle := "Read /workspace/config.go"
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "child-turn-1",
		Scope: eventstream.ScopeSubagent, ScopeID: "task-1", Actor: "explorer",
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-call-1", ToolName: "Spawn"},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "child-tool-1",
			Title: &readTitle, Kind: &readKind, Status: &completed,
			Content: []schema.ToolCallContent{{
				Type: "content", Content: schema.TextContent{Type: "text", Text: "loaded child settings"},
			}},
		},
	})
	overlay = subagentOutputOverlayPlain(model)
	if !strings.Contains(overlay, "loaded child settings") {
		t.Fatalf("generic child tool panel does not expose child tool output:\n%s", overlay)
	}
	if model.subagentOutputOverlay.geometry.totalRows <= 1 {
		t.Fatalf("unnamed external tool was incorrectly collapsed as an exact built-in:\n%s", overlay)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "child-turn-1",
		Scope: eventstream.ScopeSubagent, ScopeID: "task-1", Actor: "explorer",
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-call-1", ToolName: "Spawn"},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "found the configuration issue"},
		},
	})
	overlay = subagentOutputOverlayPlain(model)
	if !strings.Contains(overlay, "found the configuration issue") ||
		!strings.Contains(overlay, "loaded child settings") {
		t.Fatalf("output overlay did not retain the full child tool-to-narrative sequence:\n%s", overlay)
	}
	model.syncViewportContent()
	plain = strings.Join(model.viewportPlainLines, "\n")
	if strings.Contains(plain, "found the configuration issue") || strings.Contains(plain, "loaded child settings") {
		t.Fatalf("child transcript leaked back into the compact Spawn row:\n%s", plain)
	}
}

func TestHandleACPEventEnvelopeRoutesCrossTurnChildContinuationAfterSendMessage(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(250, 0))
	spawnMeta := metautil.WithRuntimeSection(acpToolNameMeta("Spawn"), metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "Spawn",
		metautil.RuntimeTargetID: "task-1",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-call-1",
			Title:         "Spawn explorer: inspect",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"agent": "explorer", "prompt": "inspect"},
			Meta:          spawnMeta,
		},
	})
	completed := schema.ToolStatusCompleted
	spawnFinalMeta := metautil.WithRuntimeSection(spawnMeta, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID: "task-1",
		"handle":               "task-1",
		"running":              false,
		"state":                "completed",
		"result":               "old child result",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "spawn-call-1",
			Status:        &completed,
			RawInput:      map[string]any{"agent": "explorer", "prompt": "inspect"},
			RawOutput:     map[string]any{"handle": "task-1", "state": "completed", "result": "old child result"},
			Meta:          spawnFinalMeta,
		},
	})
	model = applyACPEnvelopeForTest(t, model, completedRegressionTurn("session-1", "turn-1"))

	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(260, 0))
	messageInput := map[string]any{"to": "task-1", "message": "continue"}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "turn-2",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "message-1",
			Title:         "SendMessage continue",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      messageInput,
			Meta:          acpToolNameMeta("SendMessage"),
		},
	})

	chunks := []struct {
		text string
		want string
	}{
		{text: "当前", want: "当前"},
		{text: "目录下共有 12 个文件", want: "当前目录下共有 12 个文件"},
		{text: "。", want: "当前目录下共有 12 个文件。"},
	}
	for index, chunk := range chunks {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			TurnID:    "child-turn-1",
			Scope:     eventstream.ScopeSubagent,
			ScopeID:   "task-1",
			Actor:     "explorer",
			Final:     true,
			ParentTool: &eventstream.ParentToolRelation{
				ToolCallID: "spawn-call-1",
				ToolName:   "Spawn",
			},
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				MessageID:     "child-message-1",
				Content:       schema.TextContent{Type: "text", Text: chunk.text},
			},
		})

		blocks := mainACPTurnBlocksForTest(model)
		if len(blocks) != 2 {
			t.Fatalf("after child delta %d main blocks = %d, want old Spawn and current SendMessage blocks", index, len(blocks))
		}
		if len(blocks[0].Events) != 1 || blocks[0].Events[0].Output != "old child result" || !blocks[0].Events[0].Done {
			t.Fatalf("after child delta %d old Spawn block = %#v, want completed block unchanged", index, blocks[0].Events)
		}
		if len(blocks[1].Events) != 1 {
			t.Fatalf("after child delta %d current events = %#v, want one SendMessage row", index, blocks[1].Events)
		}
		if event := blocks[1].Events[0]; event.CallID != "message-1" || event.Done || event.Output != "" {
			t.Fatalf("after child delta %d SendMessage = %#v, want compact delivery row", index, event)
		}
		view := requireSubagentOutputViewForTest(t, model, "spawn-call-1")
		if got := subagentOutputNarrativeTextForTest(view, SEAssistant); got != chunk.want {
			t.Fatalf("after child delta %d detached output = %q, want %q", index, got, chunk.want)
		}
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "turn-2",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "message-1",
			Status:        &completed,
			RawInput:      messageInput,
			RawOutput:     map[string]any{"to": "task-1", "state": "accepted"},
			Meta:          acpToolNameMeta("SendMessage"),
		},
	})

	blocks := mainACPTurnBlocksForTest(model)
	if len(blocks) != 2 || len(blocks[0].Events) != 1 || len(blocks[1].Events) != 1 {
		t.Fatalf("final blocks = %#v, want one old Spawn and one current SendMessage row", blocks)
	}
	if event := blocks[1].Events[0]; !event.Done || event.Output != "" {
		t.Fatalf("final SendMessage = %#v, want completed delivery row without child output", event)
	}
	if got := subagentOutputNarrativeTextForTest(
		requireSubagentOutputViewForTest(t, model, "spawn-call-1"),
		SEAssistant,
	); got != "当前目录下共有 12 个文件。" {
		t.Fatalf("final detached output = %q, want child continuation without truncation", got)
	}
}

func TestHandleACPEventEnvelopeRendersSemanticSpawnEventsOnce(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-call-1",
			Title:         "Spawn explorer: inspect",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"agent": "explorer", "prompt": "inspect"},
			Meta:          acpToolNameMeta("Spawn"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "akio",
		Actor:     "explorer",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-call-1",
			ToolName:   "Spawn",
		},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "child-read-1",
			Title:         "Read ReadME.md",
			Kind:          schema.ToolKindRead,
			Status:        schema.ToolStatusCompleted,
			RawInput:      map[string]any{"path": "ReadME.md"},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "akio",
		Actor:     "explorer",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-call-1",
			ToolName:   "Spawn",
		},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update: schema.PlanUpdate{
			SessionUpdate: schema.UpdatePlan,
			Entries: []schema.PlanEntry{{
				Content: "inspect ReadME.md",
				Status:  "in_progress",
			}},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "akio",
		Actor:     "explorer",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-call-1",
			ToolName:   "Spawn",
		},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentThought,
			Content:       schema.TextContent{Type: "text", Text: "child private thought"},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "akio",
		Actor:     "explorer",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-call-1",
			ToolName:   "Spawn",
		},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "child semantic summary\n"},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want only parent Spawn event", block.Events)
	}
	if event := block.Events[0]; event.CallID != "spawn-call-1" || event.Output != "" {
		t.Fatalf("spawn event = %#v, want compact parent owner", event)
	}
	view := requireSubagentOutputViewForTest(t, model, "spawn-call-1")
	if got := subagentOutputNarrativeTextForTest(view, SEAssistant); got != "child semantic summary\n" {
		t.Fatalf("detached assistant output = %q, want semantic narrative once", got)
	}
	if got := subagentOutputNarrativeTextForTest(view, SEReasoning); got != "child private thought" {
		t.Fatalf("detached reasoning = %q, want complete child ACP reasoning", got)
	}
	physical := physicalTranscriptEventsForTest(view.block.Events)
	if len(physical) != 4 {
		t.Fatalf("detached child events = %#v, want tool, plan, reasoning, and assistant", physical)
	}
	if participant := model.findParticipantTurnBlock("akio"); participant != nil {
		t.Fatalf("subagent participant block = %#v, want anchored output kept in parent panel", participant)
	}
	for _, docBlock := range model.doc.Blocks() {
		if participant, ok := docBlock.(*ParticipantTurnBlock); ok {
			t.Fatalf("unexpected participant block = %#v", participant)
		}
	}
}

func TestHandleACPEventEnvelopeScopedChildTerminalKeepsOneSpawnPanelAndMainTurnAlive(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(230, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-call-1",
			Title:         "Spawn explorer: inspect",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"agent": "explorer", "prompt": "inspect"},
			Meta:          acpToolNameMeta("Spawn"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindLifecycle,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "task-1",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-call-1",
			ToolName:   "Spawn",
		},
		Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted},
	})
	if !model.turnRunning() {
		t.Fatal("scoped child terminal ended the parent live turn")
	}
	if participant := model.findParticipantTurnBlock("task-1"); participant != nil {
		t.Fatalf("scoped child terminal created duplicate participant block: %#v", participant)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Final:     true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "main answer"},
		},
	})
	terminal := eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(231, 0))
	terminal.SessionID = "session-1"
	terminal.ScopeID = "session-1"
	model = applyACPEnvelopeForTest(t, model, terminal)
	if model.turnRunning() {
		t.Fatal("main terminal did not end the parent live turn")
	}

	block := requireMainACPTurnBlockForTest(t, model)
	spawnPanels := 0
	for _, event := range block.Events {
		if event.Kind == SEToolCall && event.CallID == "spawn-call-1" {
			spawnPanels++
		}
	}
	if spawnPanels != 1 {
		t.Fatalf("spawn panels = %d, want one compact parent panel: %#v", spawnPanels, block.Events)
	}
}

func TestHandleACPEventEnvelopeApprovalSettlementKeepsMainTurnAlive(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(235, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:              eventstream.KindLifecycle,
		SessionID:         "session-1",
		HandleID:          "handle-1",
		RunID:             "run-1",
		TurnID:            "turn-1",
		Scope:             eventstream.ScopeMain,
		ApprovalRequestID: "approval-1",
		Lifecycle: &eventstream.Lifecycle{
			State:  eventstream.LifecycleStateCompleted,
			Reason: "resolved",
		},
	})
	if !model.turnRunning() {
		t.Fatal("approval settlement ended the main live turn")
	}

	terminal := eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(236, 0))
	terminal.SessionID = "session-1"
	terminal.ScopeID = "session-1"
	model = applyACPEnvelopeForTest(t, model, terminal)
	if model.turnRunning() {
		t.Fatal("Turn terminal did not end the main live turn")
	}
}

func TestHandleACPEventEnvelopeKeepsTaskWriteCompactWhenChildIsAnchoredToIt(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "task-call-1",
			Title:         "Task write akio",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"action": "write", "task_id": "akio"},
			Meta:          acpToolNameMeta("Task"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "akio",
		Actor:     "explorer",
		Final:     true,
		Meta:      parentToolStreamMeta("task-call-1", "Task"),
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "child write response"},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want only the Task write control row", block.Events)
	}
	if event := block.Events[0]; event.CallID != "task-call-1" || event.Output != "" || event.Terminal {
		t.Fatalf("task event = %#v, want compact Task write without child output or terminal ownership", event)
	}

	readKind := schema.ToolKindRead
	readTitle := "Read /workspace/config.go"
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "child-turn-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "akio",
		Actor:     "explorer",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "task-call-1",
			ToolName:   "Task",
		},
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "child-tool-1",
			Title:         readTitle,
			Kind:          readKind,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"path": "/workspace/config.go"},
		},
	})
	block = requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("after child tool main events = %#v, want Task write to stay a single control row", block.Events)
	}
	if event := block.Events[0]; event.CallID != "task-call-1" || event.Activity != "" || event.OutputNarrativeBoundary || event.Output != "" {
		t.Fatalf("after child tool Task write = %#v, want no child activity or narrative boundary", event)
	}
	if participant := model.findParticipantTurnBlock("akio"); participant != nil {
		t.Fatalf("subagent participant block = %#v, want retired Task-write child path not to open a side turn", participant)
	}
}

func TestHandleACPEventEnvelopeAppliesSpawnFinalRuntimeResultWithoutTerminalOutput(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	spawnMeta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "Spawn",
	})
	spawnMeta = metautil.WithRuntimeSection(spawnMeta, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID:         "task-1",
		metautil.RuntimeTaskTerminalID: "subagent-task-1",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-1",
			Title:         "Spawn reviewer: inspect",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusPending,
			RawInput:      map[string]any{"agent": "reviewer", "prompt": "inspect"},
			Meta:          spawnMeta,
		},
	})
	completed := schema.ToolStatusCompleted
	finalMeta := metautil.WithRuntimeSection(spawnMeta, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID:         "task-1",
		metautil.RuntimeTaskTerminalID: "subagent-task-1",
		"running":                      false,
		"state":                        "completed",
		"result":                       "Final child result",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "spawn-1",
			Status:        &completed,
			Meta:          finalMeta,
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 {
		t.Fatalf("main events = %#v, want one Spawn tool event", block.Events)
	}
	event := block.Events[0]
	if !event.Done || event.Output != "Final child result" {
		t.Fatalf("spawn event = %#v, want final result to replace streamed output", event)
	}
}

func TestHandleACPEventEnvelopeAppliesApprovalReview(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindApprovalReview,
		SessionID: "session-1",
		ApprovalReview: &eventstream.ApprovalReview{
			ToolCallID: "call-1",
			ToolName:   "RunCommand",
			RawInput:   map[string]any{"command": "go test ./..."},
			Status:     "in_progress",
		},
	})
	if model.runningActivity.Phase != runningPhaseReview {
		t.Fatalf("runningActivity = %#v, want approval review hint", model.runningActivity)
	}
	if hint := model.buildHintText(); strings.Contains(hint, "go test") {
		t.Fatalf("hint = %q, want activity label without generated command detail", hint)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindApprovalReview,
		SessionID: "session-1",
		ApprovalReview: &eventstream.ApprovalReview{
			ToolCallID:    "call-1",
			ToolName:      "RunCommand",
			RawInput:      map[string]any{"command": "go test ./..."},
			Status:        "approved",
			Risk:          "low",
			Authorization: "allow",
			Text:          "approved by policy",
		},
	})
	if model.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("runningActivity = %#v, want model waiting after terminal review", model.runningActivity)
	}
	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 || block.Events[0].Kind != SEApproval || block.Events[0].ApprovalStatus != "approved" {
		t.Fatalf("main events = %#v, want approved approval review", block.Events)
	}
	if block.Events[0].ApprovalText != "approved by policy" || block.Events[0].ApprovalRisk != "low" || block.Events[0].ApprovalAuth != "allow" {
		t.Fatalf("approval event = %#v, want review fields", block.Events[0])
	}
}

func TestForwardTurnEventStreamQueuesLiveACPEnvelopes(t *testing.T) {
	t.Parallel()

	terminal := eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(124, 0))
	terminal.SessionID = "session-1"
	terminal.ScopeID = "session-1"
	events := make(chan eventstream.Envelope, 2)
	events <- eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		TurnID:    "turn-1",
		Final:     true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "live answer"},
		},
	}
	events <- terminal
	close(events)

	var sent []tea.Msg
	result := forwardTurnEventStream(context.Background(), &eventstreamIntegrationTurn{events: events}, &ProgramSender{
		Send: func(msg tea.Msg) {
			sent = append(sent, msg)
		},
	})
	if !result.queued {
		t.Fatalf("forwardTurnEventStream() queued = false, want true")
	}
	if len(sent) != 2 {
		t.Fatalf("sent messages = %#v, want content + terminal lifecycle", sent)
	}
	first, ok := sent[0].(eventstream.Envelope)
	if !ok || first.Kind != eventstream.KindSessionUpdate {
		t.Fatalf("first message = %#v, want session/update envelope", sent[0])
	}
	last, ok := sent[1].(eventstream.Envelope)
	if !ok || !eventstream.IsTerminalLifecycle(last) || last.Lifecycle.State != eventstream.LifecycleStateCompleted {
		t.Fatalf("last message = %#v, want completed terminal lifecycle", sent[1])
	}

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	for _, msg := range sent {
		env, ok := msg.(eventstream.Envelope)
		if !ok {
			t.Fatalf("sent message = %T, want eventstream.Envelope", msg)
		}
		model = applyACPEnvelopeForTest(t, model, env)
	}
	block := requireMainACPTurnBlockForTest(t, model)
	if block.Status != "completed" {
		t.Fatalf("main ACP turn status = %q, want completed", block.Status)
	}
	if len(block.Events) != 1 || block.Events[0].Kind != SEAssistant || block.Events[0].Text != "live answer" {
		t.Fatalf("main ACP events = %#v, want live assistant answer", block.Events)
	}
	if model.turnRunning() {
		t.Fatal("model turn still running after terminal lifecycle")
	}
}

func TestParticipantFooterSuppressesOuterTurnDivider(t *testing.T) {
	t.Parallel()

	startedAt := time.Now()
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, true, startedAt)
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@ivy",
		OccurredAt:    startedAt.Add(time.Second),
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentThought,
			Content:       schema.TextContent{Type: "text", Text: "checking"},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@ivy",
		OccurredAt:    startedAt.Add(31*time.Second + 100*time.Millisecond),
		Final:         true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "done"},
		},
	})

	participant := model.findParticipantTurnBlock("participant-turn-1")
	if participant == nil || !participantTurnHasFooter(participant) {
		t.Fatalf("participant block = %#v, want completed block with duration footer", participant)
	}

	terminal := eventstream.TurnCompleted("handle-1", "run-1", "main-turn-1", startedAt.Add(34*time.Second+300*time.Millisecond))
	terminal.SessionID = "session-1"
	terminal.ScopeID = "session-1"
	model = applyACPEnvelopeForTest(t, model, terminal)

	var dividerCount, emptyMainCount int
	for _, docBlock := range model.doc.Blocks() {
		switch block := docBlock.(type) {
		case *DividerBlock:
			dividerCount++
		case *MainACPTurnBlock:
			if len(block.Events) == 0 {
				emptyMainCount++
			}
		}
	}
	if dividerCount != 0 {
		t.Fatalf("divider blocks = %d, want participant footer to be the only duration separator", dividerCount)
	}
	if emptyMainCount != 0 {
		t.Fatalf("empty main turn blocks = %d, want terminal-only lifecycle not to create one", emptyMainCount)
	}
}

func TestHandleACPEventEnvelopeMergesAttemptResetNoticeInMainTurn(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "partial"},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindLifecycle,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Lifecycle: &eventstream.Lifecycle{State: "attempt_reset"},
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"attempt_reset": map[string]any{
						"attempt":        1,
						"cause":          "model: http status 424 body=exceeds the context window",
						"max_retries":    5,
						"retry_delay_ms": 1000,
						"retrying":       true,
					},
				},
			},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindLifecycle,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Lifecycle: &eventstream.Lifecycle{State: "attempt_reset"},
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"attempt_reset": map[string]any{
						"attempt":        2,
						"cause":          "model: http status 500 body=Internal Server Error",
						"max_retries":    5,
						"retry_delay_ms": 5000,
						"retrying":       true,
					},
				},
			},
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 || block.Events[0].Kind != SENotice || block.Events[0].Text != "Retrying model request (2/5, retry in 5s)" {
		t.Fatalf("main ACP events = %#v, want merged retry notice", block.Events)
	}
	if block.Events[0].NoticeKind != transcript.NoticeKindModelRetry {
		t.Fatalf("first event notice kind = %q, want model retry", block.Events[0].NoticeKind)
	}
	if strings.Contains(block.Events[0].Text, "Internal Server Error") {
		t.Fatalf("retry notice leaked provider error: %q", block.Events[0].Text)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Final:     true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "final"},
		},
	})

	block = requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 || block.Events[0].Kind != SEAssistant || block.Events[0].Text != "final" {
		t.Fatalf("main ACP events = %#v, want retry notice cleared by later work", block.Events)
	}
	rows := block.Render(BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme)})
	if plain := joinRenderedPlain(rows); strings.Contains(plain, "Retrying model request") {
		t.Fatalf("stale retry notice remained after later work:\n%s", plain)
	}
}

func TestHandleACPEventEnvelopeSuppressesAdjacentDuplicateUserMessage(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	for range 2 {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			ScopeID:   "session-1",
			TurnID:    "turn-1",
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateUserMessage,
				Content:       schema.TextContent{Type: "text", Text: "继续"},
			},
		})
	}

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == "继续" {
			userBlocks++
		}
	}
	if userBlocks != 1 {
		t.Fatalf("userBlocks = %d, want one gateway user echo", userBlocks)
	}
}

func TestHandleACPEventEnvelopeSuppressesDuplicateUserMessageAfterMainTurnStarts(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine("继续")
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindLifecycle,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Lifecycle: &eventstream.Lifecycle{State: "running"},
	})
	if strings.TrimSpace(model.mainTimelineTailID) == "" {
		t.Fatal("main ACP turn did not start before user echo")
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: "继续"},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == "继续" {
			userBlocks++
		}
	}
	if userBlocks != 1 {
		t.Fatalf("userBlocks = %d, want started main ACP turn to absorb gateway user echo", userBlocks)
	}
	if strings.TrimSpace(model.mainTimelineTailID) == "" {
		t.Fatal("duplicate user echo closed the active main ACP turn")
	}
}

func TestHandleACPEventEnvelopeSuppressesDuplicateParticipantUserMessage(t *testing.T) {
	t.Parallel()

	const prompt = "搜一下上海明天的天气如何"
	const display = "/bela " + prompt
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine(display)
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Meta:          map[string]any{"mention": "@bela", "handle": "bela"},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == display {
			userBlocks++
		}
	}
	if userBlocks != 1 {
		t.Fatalf("userBlocks = %d, want participant user echo deduped", userBlocks)
	}
}

func TestHandleACPEventEnvelopeSuppressesLateParticipantUserEchoAfterTurnStarts(t *testing.T) {
	t.Parallel()

	const prompt = "搜一下上海明天的天气如何"
	const display = "/bela " + prompt
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine(display)
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "checking weather"},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Meta:          map[string]any{"mention": "@bela", "handle": "bela"},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == display {
			userBlocks++
		}
	}
	if userBlocks != 1 {
		t.Fatalf("userBlocks = %d, want late participant user echo deduped", userBlocks)
	}
	if got := strings.TrimSpace(model.activeParticipantTurnSessionID); got != "participant-turn-1" {
		t.Fatalf("activeParticipantTurnSessionID = %q, want participant-turn-1", got)
	}
}

func TestHandleACPEventEnvelopeSuppressesLateParticipantUserEchoAfterTurnCompletes(t *testing.T) {
	t.Parallel()

	const prompt = "搜一下上海明天的天气如何"
	const display = "/bela " + prompt
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine(display)
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Final:         true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "weather result"},
		},
	})
	if got := strings.TrimSpace(model.activeParticipantTurnSessionID); got != "" {
		t.Fatalf("activeParticipantTurnSessionID = %q, want cleared after completion", got)
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Meta:          map[string]any{"mention": "@bela", "handle": "bela"},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == display {
			userBlocks++
		}
	}
	if userBlocks != 1 {
		t.Fatalf("userBlocks = %d, want completed participant turn to absorb late user echo", userBlocks)
	}
}

func TestHandleACPEventEnvelopeRendersQueuedRepeatedParticipantUserMessage(t *testing.T) {
	t.Parallel()

	const prompt = "搜一下上海明天的天气如何"
	const display = "/bela " + prompt
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine(display)
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    display,
		displayLine: display,
		state:       pendingPromptDispatched,
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-1",
		TurnID:        "participant-turn-1",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Meta:          map[string]any{"mention": "@bela", "handle": "bela"},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == display {
			userBlocks++
		}
	}
	if userBlocks != 2 {
		t.Fatalf("userBlocks = %d, want queued repeated participant prompt rendered", userBlocks)
	}
	if len(model.pendingQueue) != 0 {
		t.Fatalf("pendingQueue = %#v, want queued participant prompt dequeued after echo", model.pendingQueue)
	}
}

func TestHandleACPEventEnvelopeRendersRepeatedParticipantUserMessageAcrossEmptyTurns(t *testing.T) {
	t.Parallel()

	const prompt = "搜一下上海明天的天气如何"
	const display = "/bela " + prompt
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine(display)
	for _, turnID := range []string{"participant-turn-1", "participant-turn-2"} {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind:          eventstream.KindParticipant,
			SessionID:     "session-1",
			Scope:         eventstream.ScopeParticipant,
			ScopeID:       turnID,
			TurnID:        turnID,
			ParticipantID: "grok-1",
			Actor:         "@bela",
			Participant:   &eventstream.Participant{State: "attached"},
		})
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     "session-1",
		Scope:         eventstream.ScopeParticipant,
		ScopeID:       "participant-turn-2",
		TurnID:        "participant-turn-2",
		ParticipantID: "grok-1",
		Actor:         "@bela",
		Meta:          map[string]any{"mention": "@bela", "handle": "bela"},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == display {
			userBlocks++
		}
	}
	if userBlocks != 2 {
		t.Fatalf("userBlocks = %d, want repeated prompt preserved across unrelated empty participant turns", userBlocks)
	}
}

func TestDequeuePendingUserMessageAnyIgnoresEmptyNeedles(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    "@bela hello",
		displayLine: "@bela hello",
		state:       pendingPromptDispatched,
	})
	if _, matched := model.pendingQueue.matchGatewayEcho("", "   "); matched {
		t.Fatal("pendingQueue.matchGatewayEcho(empty) = true, want false")
	}
	if len(model.pendingQueue) != 1 {
		t.Fatalf("pendingQueue = %#v, want preserved queue", model.pendingQueue)
	}
}

func TestDequeuePendingUserMessageAnyPreservesQueueOnMismatch(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.pendingQueue = append(model.pendingQueue,
		pendingPrompt{execLine: "first pending", displayLine: "first pending", state: pendingPromptDispatched},
		pendingPrompt{execLine: "second pending", displayLine: "second pending", state: pendingPromptDispatched},
	)
	if _, matched := model.pendingQueue.matchGatewayEcho("unrelated echo"); matched {
		t.Fatal("pendingQueue.matchGatewayEcho(unrelated) = true, want false")
	}
	if len(model.pendingQueue) != 2 {
		t.Fatalf("pendingQueue = %#v, want preserved queue on mismatch", model.pendingQueue)
	}
	if got := model.pendingQueue[0].displayText(); got != "first pending" {
		t.Fatalf("first pending = %q, want preserved queue head", got)
	}
}

func TestHandleACPEventEnvelopeRendersQueuedRepeatedUserMessage(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine("继续")
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    "继续",
		displayLine: "继续",
		state:       pendingPromptDispatched,
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindLifecycle,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Lifecycle: &eventstream.Lifecycle{State: "running"},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: "继续"},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if user, ok := block.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == "继续" {
			userBlocks++
		}
	}
	if userBlocks != 2 {
		t.Fatalf("userBlocks = %d, want queued repeated prompt rendered as a new user message", userBlocks)
	}
	if len(model.pendingQueue) != 0 {
		t.Fatalf("pendingQueue = %#v, want queued prompt dequeued after gateway echo", model.pendingQueue)
	}
}

func TestAcceptedActiveTurnPromptDisplaysBeforeFollowupMainOutput(t *testing.T) {
	t.Parallel()

	const prompt = "切换到国内源继续"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(130, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Final:     true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "before guidance"},
		},
	})
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    prompt,
		displayLine: prompt,
		state:       pendingPromptAwaitingActiveDisplay,
	})

	next, _ := model.handleTaskResultMsg(TaskResultMsg{ContinueRunning: true})
	model = next.(*Model)
	if got := countUserNarrativeBlocksForTest(model, prompt); got != 0 {
		t.Fatalf("user prompt blocks after ContinueRunning = %d, want 0 until insertion echo", got)
	}
	if got := model.pendingQueue.visibleCount(); got != 1 {
		t.Fatalf("visible pending prompts = %d, want 1 until insertion echo", got)
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Final:     true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "after guidance"},
		},
	})

	assertMainTurnDocumentOrder(t, model, "before guidance", prompt, "after guidance")
	if got := countUserNarrativeBlocksForTest(model, prompt); got != 1 {
		t.Fatalf("user prompt blocks = %d, want one local display despite gateway echo", got)
	}
	if len(model.pendingQueue) != 0 {
		t.Fatalf("pendingQueue = %#v, want active prompt dequeued by gateway echo", model.pendingQueue)
	}
}

func TestActiveTurnPromptEchoBeforeContinueRunningDisplaysBeforeFollowupMainOutput(t *testing.T) {
	t.Parallel()

	const prompt = "切换到国内源继续"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(130, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Final:     true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "before guidance"},
		},
	})
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    prompt,
		displayLine: prompt,
		state:       pendingPromptAwaitingActiveDisplay,
	})

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})
	next, _ := model.handleTaskResultMsg(TaskResultMsg{ContinueRunning: true})
	model = next.(*Model)
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Final:     true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "after guidance"},
		},
	})

	assertMainTurnDocumentOrder(t, model, "before guidance", prompt, "after guidance")
	if got := countUserNarrativeBlocksForTest(model, prompt); got != 1 {
		t.Fatalf("user prompt blocks = %d, want one gateway display", got)
	}
	if len(model.pendingQueue) != 0 {
		t.Fatalf("pendingQueue = %#v, want active prompt dequeued by gateway echo", model.pendingQueue)
	}
}

func TestActiveTurnPromptSurvivesTerminalBeforeContinueRunning(t *testing.T) {
	t.Parallel()

	const prompt = "继续补充约束"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(130, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "before guidance"},
		},
	})
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    prompt,
		displayLine: prompt,
		state:       pendingPromptAwaitingActiveDisplay,
	})

	next, _ := model.handleTaskResultMsg(TaskResultMsg{SuppressTurnDivider: true})
	model = next.(*Model)
	if len(model.pendingQueue) != 1 || !model.pendingQueue[0].awaitsAcceptedActiveDisplay() {
		t.Fatalf("pendingQueue after terminal = %#v, want active-turn correlation retained", model.pendingQueue)
	}

	next, _ = model.handleTaskResultMsg(TaskResultMsg{ContinueRunning: true})
	model = next.(*Model)
	if got := countUserNarrativeBlocksForTest(model, prompt); got != 0 {
		t.Fatalf("user prompt blocks after ContinueRunning = %d, want 0 until insertion echo", got)
	}
	if len(model.pendingQueue) != 1 || !model.pendingQueue[0].awaitsAcceptedActiveDisplay() {
		t.Fatalf("pendingQueue after ContinueRunning = %#v, want awaiting insertion echo", model.pendingQueue)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})
	if got := countUserNarrativeBlocksForTest(model, prompt); got != 1 {
		t.Fatalf("user prompt blocks after late echo = %d, want one insertion echo", got)
	}
	if len(model.pendingQueue) != 0 {
		t.Fatalf("pendingQueue after late echo = %#v, want correlation removed", model.pendingQueue)
	}
}

func TestAcceptedActiveTurnPendingPromptUsesFIFOSelection(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.pendingQueue = append(model.pendingQueue,
		pendingPrompt{execLine: "first guidance", displayLine: "first guidance", state: pendingPromptAwaitingActiveDisplay},
		pendingPrompt{execLine: "second guidance", displayLine: "second guidance", state: pendingPromptAwaitingActiveDisplay},
	)
	visible, ok := model.pendingQueue.nextVisible()
	if !ok || visible.displayText() != "first guidance" {
		t.Fatalf("next visible pending = %#v/%v, want first guidance", visible, ok)
	}

	if !model.renderNextAcceptedPendingPrompt() {
		t.Fatal("renderNextAcceptedPendingPrompt() = false, want first prompt rendered")
	}
	if got := countUserNarrativeBlocksForTest(model, "first guidance"); got != 1 {
		t.Fatalf("first guidance blocks = %d, want one", got)
	}
	visible, ok = model.pendingQueue.nextVisible()
	if !ok || visible.displayText() != "second guidance" {
		t.Fatalf("next visible pending after first render = %#v/%v, want second guidance", visible, ok)
	}

	if !model.renderNextAcceptedPendingPrompt() {
		t.Fatal("renderNextAcceptedPendingPrompt() second = false, want second prompt rendered")
	}
	if got := countUserNarrativeBlocksForTest(model, "second guidance"); got != 1 {
		t.Fatalf("second guidance blocks = %d, want one", got)
	}
	if got := model.pendingQueue.visibleCount(); got != 0 {
		t.Fatalf("visible pending prompts = %d, want none after both render", got)
	}
}

func TestActiveTurnPromptRendersAtLLMStepInsertionEcho(t *testing.T) {
	t.Parallel()

	const prompt = "提醒一下这个问题不是必现的"
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(130, 0))
	model.pendingQueue = append(model.pendingQueue, pendingPrompt{
		execLine:    prompt,
		displayLine: prompt,
		state:       pendingPromptAwaitingActiveDisplay,
	})

	next, _ := model.handleTaskResultMsg(TaskResultMsg{ContinueRunning: true})
	model = next.(*Model)
	model = applyACPEnvelopeForTest(t, model, mainThoughtEnvelope("inspect the renderer", false))
	model = applyACPEnvelopeForTest(t, model, mainThoughtEnvelope("inspect the renderer", true))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Final:     true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "I will search next."},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "Search renderer",
			Kind:          schema.ToolKindSearch,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"query": "renderer"},
		},
	})
	if got := countUserNarrativeBlocksForTest(model, prompt); got != 0 {
		t.Fatalf("user prompt blocks before insertion echo = %d, want 0 through the current LLM step", got)
	}
	blocks := mainACPTurnBlocksForTest(model)
	if len(blocks) != 1 {
		t.Fatalf("main turn blocks before insertion echo = %d, want 1 unsplit step", len(blocks))
	}

	completed := schema.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Status:        &completed,
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: prompt},
		},
	})
	if got := countUserNarrativeBlocksForTest(model, prompt); got != 1 {
		t.Fatalf("user prompt blocks after insertion echo = %d, want 1", got)
	}

	model = applyACPEnvelopeForTest(t, model, mainThoughtEnvelope("after the steer", false))
	assertMainTurnDocumentOrder(t, model, "I will search next.", prompt, "after the steer")
	blocks = mainACPTurnBlocksForTest(model)
	if len(blocks) != 2 {
		t.Fatalf("main turn blocks after insertion = %d, want current step then post-steer step", len(blocks))
	}
	if !mainACPBlockContainsText(blocks[0], "inspect the renderer") || !mainACPBlockContainsText(blocks[0], "I will search next.") {
		t.Fatalf("first step block = %#v, want reasoning and assistant before the user line", blocks[0].Events)
	}
	hasTool := false
	for _, event := range blocks[0].Events {
		if event.Kind == SEToolCall && event.CallID == "call-1" {
			hasTool = true
			break
		}
	}
	if !hasTool {
		t.Fatalf("first step block = %#v, want the step tool call before the user line", blocks[0].Events)
	}
}

func mainThoughtEnvelope(text string, final bool) eventstream.Envelope {
	return eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Final:     final,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentThought,
			Content:       schema.TextContent{Type: "text", Text: text},
		},
	}
}

func TestHandleACPEventEnvelopeSuppressesImageAttachmentUserEcho(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.commitUserDisplayLine("[image #1] hello")
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: "hello"},
		},
	})

	userBlocks := 0
	for _, block := range model.doc.Blocks() {
		if _, ok := block.(*UserNarrativeBlock); ok {
			userBlocks++
		}
	}
	if userBlocks != 1 {
		t.Fatalf("userBlocks = %d, want local image display line to absorb gateway echo", userBlocks)
	}
}

func TestHandleACPEventEnvelopeShowsContextCompactingHintUntilSuccessNotice(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentThought,
			Content:       schema.TextContent{Type: "text", Text: "Checking the remaining context."},
		},
	})
	if hint := model.buildHintText(); !strings.Contains(hint, "Thinking") {
		t.Fatalf("pre-compact hint = %q, want Thinking", hint)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindLifecycle,
		SessionID: "session-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Lifecycle: &eventstream.Lifecycle{State: session.LifecycleStatusContextCompacting},
	})
	if model.runningActivity.Phase != runningPhaseCompact {
		t.Fatalf("runningActivity = %#v, want context compacting", model.runningActivity)
	}
	if hint := model.buildHintText(); !strings.Contains(hint, "Compacting context") || strings.Contains(hint, "Thinking") {
		t.Fatalf("compacting hint = %q, want dedicated compact phase without Thinking", hint)
	}
	block := requireMainACPTurnBlockForTest(t, model)
	if block.Status == session.LifecycleStatusContextCompacting {
		t.Fatalf("main Turn status = %q, transient compact activity must remain hint-only", block.Status)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateCompact,
			Content:       schema.TextContent{Type: "text", Text: "CONTEXT CHECKPOINT\nObjective: continue"},
		},
		Final: true,
	})
	if model.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("runningActivity = %#v, want model wait after compact completes", model.runningActivity)
	}
	if hint := model.buildHintText(); strings.Contains(hint, "Compacting context") {
		t.Fatalf("post-compact hint = %q, want compact activity cleared", hint)
	}
	block = requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) == 0 || block.Events[len(block.Events)-1].Text != "• "+transcript.CompactNoticeLabel {
		t.Fatalf("main ACP events = %#v, want existing compacted notice", block.Events)
	}
}

func TestHandleACPEventEnvelopeKeepsCompactionForegroundAcrossBackgroundCommandStream(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "command-1",
			Title: "RunCommand long job", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"command": "long job", "yield_time_ms": 250}, Meta: acpToolNameMeta("RunCommand"),
		},
	})
	if model.runningActivity.Phase != runningPhaseToolWait || model.runningActivity.Target != runningTargetShell {
		t.Fatalf("tool start activity = %#v, want foreground shell invocation", model.runningActivity)
	}

	// A canonical yielded result completes the Agent's foreground invocation.
	// The command target may continue independently in the Task service.
	running := schema.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Final: true,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1", Status: &running,
			// Stream-shaped display metadata alone must not suppress a canonical
			// foreground result.
			Meta: runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "", "append"),
		},
	})
	if model.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("yielded command activity = %#v, want Agent model wait", model.runningActivity)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindLifecycle, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Lifecycle: &eventstream.Lifecycle{State: session.LifecycleStatusContextCompacting},
	})
	if model.runningActivity.Phase != runningPhaseCompact {
		t.Fatalf("compact activity = %#v, want strong compact boundary", model.runningActivity)
	}

	backgroundFrame := func(status *string, meta map[string]any, sequence uint64) eventstream.Envelope {
		return eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "runtime-terminal-1", Scope: eventstream.ScopeMain,
			ParentTool: &eventstream.ParentToolRelation{ToolCallID: "command-1", ToolName: "RunCommand"},
			Delivery:   &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
			Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
				Generation: "task-stream-1", Sequence: sequence,
			}},
			Update: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1", Status: status, Meta: meta,
			},
		}
	}

	// Typed Task-stream provenance remains authoritative even when the frame
	// carries none of the runtime stream display metadata.
	model = applyACPEnvelopeForTest(t, model, backgroundFrame(
		&running,
		runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "", ""),
		1,
	))
	if model.runningActivity.Phase != runningPhaseCompact || len(model.runningHintTracker.active) != 0 {
		t.Fatalf("activity after background output = %#v active=%#v, want compact without stream activity", model.runningActivity, model.runningHintTracker.active)
	}
	owners := model.runningHintTracker.ownersByCallID["command-1"]
	if len(owners) != 1 || owners[0].Key != "tool:turn-1:command-1" {
		t.Fatalf("command activity owners = %#v, want background stream excluded from hint ownership", owners)
	}

	model = applyACPEnvelopeForTest(t, model, backgroundFrame(
		&running,
		runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "tail\n", "append"),
		2,
	))
	if model.runningActivity.Phase != runningPhaseCompact {
		t.Fatalf("activity after background stream output = %#v, want compact", model.runningActivity)
	}

	completed := schema.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, backgroundFrame(
		&completed,
		runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "", "final"),
		3,
	))
	if model.runningActivity.Phase != runningPhaseCompact {
		t.Fatalf("activity after background stream final = %#v, want compact until typed compact completion", model.runningActivity)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateCompact,
			Content:       schema.TextContent{Type: "text", Text: "CONTEXT CHECKPOINT\nObjective: continue"},
		},
		Final: true,
	})
	if model.runningActivity.Phase != runningPhaseModelWait {
		t.Fatalf("post-compact activity = %#v, want model wait", model.runningActivity)
	}
	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) == 0 || !strings.Contains(block.Events[0].Output, "tail") {
		t.Fatalf("command transcript = %#v, want background output rendered without owning hint", block.Events)
	}
}

func TestHandleACPEventEnvelopeClearsContextCompactingHintOnFailureAndTurnCancel(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		terminal eventstream.Envelope
		want     runningActivityPhase
	}{
		{
			name: "failure notice",
			terminal: eventstream.Envelope{
				Kind:       eventstream.KindNotice,
				Scope:      eventstream.ScopeMain,
				Notice:     "localized compact failure",
				NoticeKind: eventstream.NoticeKindCompactFailed,
			},
			want: runningPhaseModelWait,
		},
		{
			name: "turn cancellation",
			terminal: eventstream.Envelope{
				Kind:      eventstream.KindLifecycle,
				Scope:     eventstream.ScopeMain,
				Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCancelled},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := NewModel(Config{NoColor: true, NoAnimation: true})
			model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
			model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
				Kind:      eventstream.KindLifecycle,
				SessionID: "session-1",
				TurnID:    "turn-1",
				Scope:     eventstream.ScopeMain,
				Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
				Lifecycle: &eventstream.Lifecycle{State: session.LifecycleStatusContextCompacting},
			})
			terminal := test.terminal
			terminal.SessionID = "session-1"
			terminal.TurnID = "turn-1"
			model = applyACPEnvelopeForTest(t, model, terminal)
			if model.runningActivity.Phase != test.want {
				t.Fatalf("runningActivity = %#v, want phase %q", model.runningActivity, test.want)
			}
			if hint := model.buildHintText(); strings.Contains(hint, "Compacting context") {
				t.Fatalf("terminal hint = %q, want compact activity cleared", hint)
			}
		})
	}
}

func TestHandleACPEventEnvelopeAnchorsCompactNoticeInMainTurn(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateCompact,
			Content:       schema.TextContent{Type: "text", Text: "CONTEXT CHECKPOINT\nObjective: continue"},
		},
		Final: true,
	})

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 || block.Events[0].Kind != SENotice || block.Events[0].Text != "• "+transcript.CompactNoticeLabel {
		t.Fatalf("main ACP events = %#v, want compact notice", block.Events)
	}
	if block.Events[0].NoticeKind != transcript.NoticeKindCompact {
		t.Fatalf("compact notice kind = %q, want compact", block.Events[0].NoticeKind)
	}
	rows := block.Render(BlockRenderContext{Width: 96, TermWidth: 96, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme)})
	if plain := joinRenderedPlain(rows); !strings.Contains(plain, "• "+transcript.CompactNoticeLabel) {
		t.Fatalf("rendered compact notice = %q", plain)
	}
	if len(rows) < 3 {
		t.Fatalf("rendered compact notice rows = %#v, want blank, notice, blank", rows)
	}
	noticeIndex := -1
	for i, row := range rows {
		if row.Plain == "• "+transcript.CompactNoticeLabel {
			noticeIndex = i
			break
		}
	}
	if noticeIndex <= 0 || noticeIndex >= len(rows)-1 {
		t.Fatalf("compact notice index = %d rows=%#v, want surrounding blank rows", noticeIndex, rows)
	}
	if strings.TrimSpace(rows[noticeIndex-1].Plain) != "" || strings.TrimSpace(rows[noticeIndex+1].Plain) != "" {
		t.Fatalf("compact notice rows = %#v, want surrounding blank rows", rows)
	}
}

func TestHandleACPEventEnvelopeCollapsesCanonicalAndTransientCompactSignals(t *testing.T) {
	t.Parallel()

	canonical := eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateCompact,
			Content:       schema.TextContent{Type: "text", Text: "CONTEXT CHECKPOINT\nObjective: continue"},
		},
		Final: true,
	}
	transient := eventstream.Envelope{
		Kind:       eventstream.KindNotice,
		SessionID:  "session-1",
		TurnID:     "turn-1",
		Scope:      eventstream.ScopeMain,
		Notice:     transcript.CompactNoticeLabel,
		NoticeKind: eventstream.NoticeKindCompact,
	}

	for _, test := range []struct {
		name   string
		inputs []eventstream.Envelope
	}{
		{name: "durable first", inputs: []eventstream.Envelope{canonical, transient}},
		{name: "transient first", inputs: []eventstream.Envelope{transient, canonical}},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := NewModel(Config{NoColor: true, NoAnimation: true})
			model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
			for _, input := range test.inputs {
				model = applyACPEnvelopeForTest(t, model, input)
			}
			block := requireMainACPTurnBlockForTest(t, model)
			if len(block.Events) != 1 || block.Events[0].NoticeKind != transcript.NoticeKindCompact {
				t.Fatalf("compact events = %#v, want one notice for paired projections", block.Events)
			}
		})
	}
}

func TestHandleACPEventEnvelopeKeepsRepeatedCompactionsVisibleOnceEach(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	for compactIndex := 0; compactIndex < 2; compactIndex++ {
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind:      eventstream.KindSessionUpdate,
			SessionID: "session-1",
			TurnID:    "turn-1",
			Scope:     eventstream.ScopeMain,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateCompact,
				Content:       schema.TextContent{Type: "text", Text: "CONTEXT CHECKPOINT"},
			},
			Final: true,
		})
		model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
			Kind:       eventstream.KindNotice,
			SessionID:  "session-1",
			TurnID:     "turn-1",
			Scope:      eventstream.ScopeMain,
			Notice:     transcript.CompactNoticeLabel,
			NoticeKind: eventstream.NoticeKindCompact,
		})
	}

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 2 {
		t.Fatalf("compact events = %#v, want two real compactions rendered once each", block.Events)
	}
	for _, event := range block.Events {
		if event.NoticeKind != transcript.NoticeKindCompact {
			t.Fatalf("compact event = %#v, want compact notice", event)
		}
	}
}

func TestRenderEventPolicyForACPEnvelopeRoutesStandardUpdates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  eventstream.Envelope
		lane renderEventLane
	}{
		{
			name: "tool",
			env: eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate,
				Update: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo,
					ToolCallID:    "call-1",
				},
			},
			lane: renderLaneToolStream,
		},
		{
			name: "participant",
			env: eventstream.Envelope{
				Kind:        eventstream.KindParticipant,
				Participant: &eventstream.Participant{State: "attached"},
			},
			lane: renderLaneParticipant,
		},
		{
			name: "approval",
			env: eventstream.Envelope{
				Kind: eventstream.KindRequestPermission,
				Permission: &schema.RequestPermissionRequest{
					ToolCall: schema.ToolCallUpdate{SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "call-1"},
				},
			},
			lane: renderLaneToolStream,
		},
		{
			name: "lifecycle",
			env: eventstream.Envelope{
				Kind:      eventstream.KindLifecycle,
				Lifecycle: &eventstream.Lifecycle{State: "completed"},
			},
			lane: renderLaneLifecycle,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			policy := renderEventPolicyForACPEnvelope(tt.env)
			if policy.lane != tt.lane {
				t.Fatalf("lane = %q, want %q", policy.lane, tt.lane)
			}
			if !policy.flushLogChunks {
				t.Fatalf("policy = %#v, want log flush for ACP render event", policy)
			}
		})
	}
}

func TestProjectResumeReplayEventsUsesACPEnvelopeTrace(t *testing.T) {
	t.Parallel()

	events := projectResumeReplayEvents([]eventstream.Envelope{{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         stringPtr("RunCommand echo ok"),
			Kind:          stringPtr(schema.ToolKindExecute),
			Status:        stringPtr(schema.ToolStatusCompleted),
			RawInput:      map[string]any{"command": "echo ok"},
			RawOutput:     map[string]any{"stdout": "ok\n"},
			Meta:          acpToolNameMeta("RunCommand"),
		},
	}, {
		Kind:      eventstream.KindLifecycle,
		SessionID: "session-1",
		Lifecycle: &eventstream.Lifecycle{
			State: "completed",
		},
	}})
	if len(events) != 2 {
		t.Fatalf("projectResumeReplayEvents() = %#v, want tool + lifecycle", events)
	}
	if events[0].Kind != TranscriptEventTool || events[0].ToolCallID != "call-1" || events[0].ToolName != "RunCommand" {
		t.Fatalf("tool replay event = %#v, want RunCommand ACP tool event", events[0])
	}
	if events[1].Kind != TranscriptEventLifecycle || events[1].State != "completed" {
		t.Fatalf("lifecycle replay event = %#v, want completed lifecycle", events[1])
	}
}

func TestResumeReplayDoesNotRestoreTransientContextCompactingHint(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	model.applyTranscriptRunningActivity(TranscriptEvent{
		Kind:  TranscriptEventLifecycle,
		Scope: ACPProjectionMain,
		State: session.LifecycleStatusContextCompacting,
	})
	if model.runningActivity.Phase != runningPhaseCompact {
		t.Fatalf("runningActivity = %#v, want compact before reconnect", model.runningActivity)
	}

	model.resetConversationView()
	replay := projectResumeReplayEvents([]eventstream.Envelope{{
		Kind:      eventstream.KindLifecycle,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Lifecycle: &eventstream.Lifecycle{State: session.LifecycleStatusContextCompacting},
	}})
	if len(replay) != 0 {
		t.Fatalf("projectResumeReplayEvents() = %#v, want transient compact activity filtered at replay boundary", replay)
	}
	for _, event := range replay {
		model = applyTranscriptEventForTest(t, model, event)
	}
	if model.runningActivity.Phase != "" || strings.Contains(model.buildHintText(), "Compacting context") {
		t.Fatalf("replayed compact activity restored stale hint: %#v %q", model.runningActivity, model.buildHintText())
	}
}

func TestResumeReplayMainTurnBlockIsolation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		replay  []eventstream.Envelope
		prompt  string
		live    eventstream.Envelope
		oldText string
		newText string
	}{
		{
			name: "lifecycle terminated replay",
			replay: []eventstream.Envelope{
				{
					Kind:      eventstream.KindSessionUpdate,
					SessionID: "session-1",
					ScopeID:   "session-1",
					TurnID:    "turn-1",
					Final:     true,
					Update: schema.ContentChunk{
						SessionUpdate: schema.UpdateAgentMessage,
						Content:       schema.TextContent{Type: "text", Text: "interrupted output"},
					},
				},
				{
					Kind:      eventstream.KindLifecycle,
					SessionID: "session-1",
					ScopeID:   "session-1",
					TurnID:    "turn-1",
					Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateInterrupted},
				},
			},
			prompt: "continue after resume",
			live: eventstream.Envelope{
				Kind:      eventstream.KindSessionUpdate,
				SessionID: "session-1",
				ScopeID:   "session-1",
				TurnID:    "turn-2",
				Final:     true,
				Update: schema.ContentChunk{
					SessionUpdate: schema.UpdateAgentMessage,
					Content:       schema.TextContent{Type: "text", Text: "new answer"},
				},
			},
			oldText: "interrupted output",
			newText: "new answer",
		},
		{
			name: "final message only replay",
			replay: []eventstream.Envelope{
				{
					Kind:      eventstream.KindSessionUpdate,
					SessionID: "session-1",
					Scope:     eventstream.ScopeMain,
					ScopeID:   "turn-5",
					TurnID:    "turn-5",
					Final:     true,
					Update: schema.ContentChunk{
						SessionUpdate: schema.UpdateAgentMessage,
						Content:       schema.TextContent{Type: "text", Text: "old async bash summary"},
					},
				},
			},
			prompt: "new replay check prompt",
			live: eventstream.Envelope{
				Kind:      eventstream.KindSessionUpdate,
				SessionID: "session-1",
				Scope:     eventstream.ScopeMain,
				ScopeID:   "turn-9",
				TurnID:    "turn-9",
				Final:     true,
				Update: schema.ContentChunk{
					SessionUpdate: schema.UpdateAgentMessage,
					Content:       schema.TextContent{Type: "text", Text: "new replay check answer"},
				},
			},
			oldText: "old async bash summary",
			newText: "new replay check answer",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := NewModel(Config{NoColor: true, NoAnimation: true})
			replayEvents := projectResumeReplayEvents(tc.replay)
			next, _ := model.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: replayEvents})
			model = next.(*Model)
			if id := strings.TrimSpace(model.mainTimelineTailID); id != "" {
				t.Fatalf("mainTimelineTailID after replay = %q, want empty", id)
			}

			model.commitUserDisplayLine(tc.prompt)
			model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(130, 0))
			model = applyACPEnvelopeForTest(t, model, tc.live)
			assertMainTurnDocumentOrder(t, model, tc.oldText, tc.prompt, tc.newText)
		})
	}
}

func TestMainACPEventWithoutTurnIDDoesNotReuseSessionBlock(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Final:     true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "old answer"},
		},
	})

	model.commitUserDisplayLine("continue")
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(130, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		Final:     true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "missing turn answer"},
		},
	})

	assertMainTurnDocumentOrder(t, model, "old answer", "continue", "missing turn answer")
}

func TestMainACPKeyedThenKeylessEventsStayInOneLiveTurnBlock(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(130, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "keyed chunk"},
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "keyless chunk"},
		},
	})

	blocks := mainACPTurnBlocksForTest(model)
	if len(blocks) != 1 {
		t.Fatalf("main ACP turn blocks = %d, want 1", len(blocks))
	}
	block := blocks[0]
	if got := strings.TrimSpace(block.TurnKey); got != "turn-1" {
		t.Fatalf("main ACP turn key = %q, want server turn id", got)
	}
	if !mainACPBlockContainsText(block, "keyed chunk") || !mainACPBlockContainsText(block, "keyless chunk") {
		t.Fatalf("main ACP events = %#v, want keyed and keyless chunks in one block", block.Events)
	}
}

func TestMainTimelineDoesNotRouteUnanchoredEventsByTurnID(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(130, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "before guidance"},
		},
	})
	model.commitUserDisplayLine("guide the running turn")
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		ScopeID:   "session-1",
		TurnID:    "turn-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "after guidance"},
		},
	})

	assertMainTurnDocumentOrder(t, model, "before guidance", "guide the running turn", "after guidance")
}

func TestMainTimelineStableToolAnchorUpdatesOriginalBlockAcrossUserBarrier(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyTranscriptEventForTest(t, model, TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "call-1",
		ToolName:   "RunCommand",
		ToolArgs:   "slow task",
		ToolOutput: "started\n",
	})
	model.commitUserDisplayLine("guide the running task")
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "after guidance"},
		},
	})
	model = applyTranscriptEventForTest(t, model, TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		ToolCallID: "call-1",
		ToolName:   "RunCommand",
		ToolArgs:   "slow task",
		ToolOutput: "finished\n",
		Final:      true,
	})

	blocks := mainACPTurnBlocksForTest(model)
	if len(blocks) != 2 {
		t.Fatalf("main ACP turn blocks = %d, want 2", len(blocks))
	}
	if !mainACPBlockHasToolOutput(blocks[0], "finished") {
		t.Fatalf("first block events = %#v, want anchored tool output updated before user barrier", blocks[0].Events)
	}
	if mainACPBlockHasToolCall(blocks[1], "call-1") {
		t.Fatalf("second block events = %#v, want no duplicate anchored tool event after user barrier", blocks[1].Events)
	}
	if !mainACPBlockContainsText(blocks[1], "after guidance") {
		t.Fatalf("second block events = %#v, want follow-up main output after user barrier", blocks[1].Events)
	}
}

func TestMainTimelineRepeatedToolCallIDAfterTerminalStartsNewBlock(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyTranscriptEventForTest(t, model, TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		TurnID:     "turn-1",
		ToolCallID: "RunCommand",
		ToolName:   "RunCommand",
		ToolArgs:   "first command",
		ToolOutput: "first output\n",
		Final:      true,
	})
	model = applyTranscriptEventForTest(t, model, TranscriptEvent{
		Kind:   TranscriptEventLifecycle,
		Scope:  ACPProjectionMain,
		TurnID: "turn-1",
		State:  eventstream.LifecycleStateCompleted,
	})

	model.commitUserDisplayLine("next prompt")
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(140, 0))
	model = applyTranscriptEventForTest(t, model, TranscriptEvent{
		Kind:       TranscriptEventTool,
		Scope:      ACPProjectionMain,
		TurnID:     "turn-2",
		ToolCallID: "RunCommand",
		ToolName:   "RunCommand",
		ToolArgs:   "second command",
		ToolOutput: "second output\n",
		Final:      true,
	})

	blocks := mainACPTurnBlocksForTest(model)
	if len(blocks) != 2 {
		t.Fatalf("main ACP turn blocks = %d, want 2", len(blocks))
	}
	if !mainACPBlockHasToolOutput(blocks[0], "first output") || mainACPBlockHasToolOutput(blocks[0], "second output") {
		t.Fatalf("first block events = %#v, want only first reused call output", blocks[0].Events)
	}
	if !mainACPBlockHasToolOutput(blocks[1], "second output") {
		t.Fatalf("second block events = %#v, want repeated call id routed to new turn block", blocks[1].Events)
	}
}

func TestMainTimelineRoutesCrossTurnTaskObserverStreamToOriginalCommandPanel(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(270, 0))
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "command-1",
			Title: "RunCommand long job", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"command": "long job", "yield_time_ms": 250},
			Content:  []schema.ToolCallContent{{Type: "terminal", TerminalID: "terminal-1"}},
			Meta:     acpToolNameMeta("RunCommand"),
		},
	})
	running := schema.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "command-1", ToolName: "RunCommand"},
		Delivery:   &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Generation: "task-stream-1", Sequence: 1,
		}},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1", Status: &running,
			Meta: runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "initial\n", "append"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, completedRegressionTurn("session-1", "turn-1"))

	model.commitUserDisplayLine("wait for the command")
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(271, 0))
	taskInput := map[string]any{"action": "wait", "task_id": "task-1", "target_kind": "terminal"}
	taskMeta := metautil.WithRuntimeSection(acpToolNameMeta("Task"), metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "Task", metautil.RuntimeToolAction: "wait",
		metautil.RuntimeTargetID: "task-1", metautil.RuntimeTargetKind: "terminal",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-2", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "task-wait-1",
			Title: "Task wait task-1", Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: taskInput, Meta: taskMeta,
		},
	})
	foreground := model.runningActivity
	if foreground.Key != "tool:turn-2:task-wait-1" || foreground.Target != runningTargetShell {
		t.Fatalf("foreground activity = %#v, want Agent's Task wait invocation", foreground)
	}

	// Control projects the physical command owner with the observing Task TurnID.
	// The typed stream provenance plus exact CallID+TaskID must still update the
	// original command panel instead of creating a duplicate in turn 2.
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-2", Scope: eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "command-1", ToolName: "RunCommand"},
		Delivery:   &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Position: &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Generation: "task-stream-1", Sequence: 2,
		}},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1", Status: &running,
			Meta: runningSnapshotTerminalMeta("RunCommand", "task-1", "terminal-1", "tail\n", "append"),
		},
	})

	blocks := mainACPTurnBlocksForTest(model)
	if len(blocks) != 1 {
		t.Fatalf("main blocks = %d, want only the original command block", len(blocks))
	}
	if len(blocks[0].Events) != 1 || blocks[0].Events[0].CallID != "command-1" || blocks[0].Events[0].Output != "initial\ntail\n" {
		t.Fatalf("original command block = %#v, want observer tail appended in place", blocks[0].Events)
	}
	if model.runningActivity != foreground {
		t.Fatalf("runningActivity = %#v, want background command stream to preserve foreground %#v", model.runningActivity, foreground)
	}
}

func TestMainNoticeWithoutTurnIDUsesTimelineBlock(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		running bool
	}{
		{name: "idle"},
		{name: "running", running: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := NewModel(Config{NoColor: true, NoAnimation: true})
			if tc.running {
				model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(130, 0))
			}
			model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
				Kind:      eventstream.KindNotice,
				SessionID: "session-1",
				ScopeID:   "session-1",
				Notice:    "retry notice",
			})

			blocks := mainACPTurnBlocksForTest(model)
			if len(blocks) != 1 {
				t.Fatalf("main ACP turn blocks = %d, want 1", len(blocks))
			}
			block := blocks[0]
			if len(block.Events) != 1 || block.Events[0].Kind != SENotice || block.Events[0].Text != "retry notice" {
				t.Fatalf("main ACP events = %#v, want notice", block.Events)
			}
			if got := strings.TrimSpace(block.TurnKey); got != "" {
				t.Fatalf("notice turn key = %q, want empty without a server turn id", got)
			}
			if got := strings.TrimSpace(model.mainTimelineTailID); got != strings.TrimSpace(block.BlockID()) {
				t.Fatalf("main timeline tail = %q, want notice block %q", got, block.BlockID())
			}
		})
	}
}

func assertMainTurnDocumentOrder(t *testing.T, model *Model, oldText string, prompt string, newText string) {
	t.Helper()
	var order []string
	for _, docBlock := range model.doc.Blocks() {
		switch block := docBlock.(type) {
		case *MainACPTurnBlock:
			switch {
			case mainACPBlockContainsText(block, oldText):
				order = append(order, "old")
				if mainACPBlockContainsText(block, newText) {
					t.Fatalf("old replay block contains new turn answer: %#v", block.Events)
				}
			case mainACPBlockContainsText(block, newText):
				order = append(order, "new")
			}
		case *UserNarrativeBlock:
			if strings.TrimSpace(block.Raw) == prompt {
				order = append(order, "user")
			}
		}
	}
	want := []string{"old", "user", "new"}
	if !slices.Equal(order, want) {
		t.Fatalf("document order = %#v, want %#v", order, want)
	}
}

func TestApprovalPayloadFromACPEventUsesStandardPermission(t *testing.T) {
	t.Parallel()

	req := approvalPayloadFromACPEvent(eventstream.Envelope{
		Kind:              eventstream.KindRequestPermission,
		ApprovalRequestID: "approval-child-1",
		Permission: &schema.RequestPermissionRequest{
			SessionID: "session-1",
			ToolCall: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Title:         stringPtr("RunCommand go test ./..."),
				Kind:          stringPtr(schema.ToolKindExecute),
				RawInput: map[string]any{
					"command":             "go test ./...",
					"approval_reason":     "needs execution",
					"justification":       "requested by user",
					"sandbox_permissions": "workspace-write",
				},
			},
			Options: []schema.PermissionOption{{
				OptionID: "allow_once",
				Name:     "Allow once",
				Kind:     "allow_once",
			}},
		},
		Meta: acpToolNameMeta("RunCommand"),
	})
	if req == nil {
		t.Fatal("approvalPayloadFromACPEvent() = nil")
	}
	if req.RequestID != "approval-child-1" || req.ToolCallID != "call-1" || req.ToolName != "RunCommand" || req.Reason != "needs execution" {
		t.Fatalf("approval payload = %#v, want ACP permission fields", req)
	}
	if len(req.Options) != 1 || req.Options[0].ID != "allow_once" {
		t.Fatalf("approval options = %#v, want allow_once", req.Options)
	}
}
