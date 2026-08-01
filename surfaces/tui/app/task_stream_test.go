package tuiapp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	sdkstream "github.com/caelis-labs/caelis/agent-sdk/task/stream"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

func TestTUISubagentOutputSubscribesInBackgroundAndOverlayCloseDoesNotCancelTask(t *testing.T) {
	t.Parallel()

	subscription := newTUITestTaskSubscription()
	controlService := &tuiTestTaskStreamService{
		subscription: subscription,
		requests:     make(chan controltaskstream.SubscribeRequest, 1),
		list: controltaskstream.ListResult{Tasks: []controltaskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true,
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "SPAWN"},
		}}},
	}
	service := taskstream.New(controlService)
	messages := make(chan tea.Msg, 8)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	model := NewModel(Config{
		Context:       context.Background(),
		NoColor:       true,
		NoAnimation:   true,
		TaskStreams:   bindTaskStreamTestClient(t, service),
		ProgramSender: sender,
	})
	model.width = 100
	model.height = 28
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "SPAWN",
	})
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-1", Title: "SPAWN helper",
			Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "self", "prompt": "inspect"}, Meta: meta,
		},
	})
	running := schema.ToolStatusInProgress
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "zuri", "state": "running"}, Meta: meta,
		},
	})
	resolved := receiveTUITaskStreamMessage[taskStreamResolvedMsg](t, messages)
	if next, _ := model.Update(resolved); next != nil {
		model = next.(*Model)
	}

	select {
	case request := <-controlService.requests:
		if request.SessionID != "session-1" || request.TaskID != "task-1" {
			t.Fatalf("Subscribe request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("background subagent observer did not subscribe")
	}
	opened := receiveTUITaskStreamMessage[taskStreamOpenedMsg](t, messages)
	if next, _ := model.Update(opened); next != nil {
		model = next.(*Model)
	}

	subscription.records <- controltaskstream.Record{
		Cursor: "cursor-1", Generation: "generation-1", Sequence: 1,
		Task: controltaskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true, CurrentTurnID: "child-turn-1",
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "SPAWN"},
		},
		Frame: &sdkstream.Frame{
			Ref:     sdkstream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "child-turn-1"},
			Running: true, Cursor: sdkstream.Cursor{Events: 1},
			Event: &session.Event{
				ID: "child-event-1", Type: session.EventTypeAssistant,
				Scope: &session.EventScope{Participant: session.ParticipantRef{Kind: session.ParticipantKindSubagent}},
				Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), MessageID: "child-message-1",
					Content: session.ProtocolTextContent("isolated child output"),
				}},
			},
		},
	}
	batch := receiveTUITaskStreamMessage[taskStreamBatchMsg](t, messages)
	if next, _ := model.Update(batch); next != nil {
		model = next.(*Model)
	}
	view := model.subagentOutputViews["spawn-1"]
	if view == nil || view.block == nil {
		t.Fatal("background subagent observer did not retain a transcript view")
	}
	childPlain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 20)), "\n")
	if !strings.Contains(childPlain, "isolated child output") {
		t.Fatalf("background child transcript omitted live output:\n%s", childPlain)
	}
	block := requireMainACPTurnBlockForTest(t, model)
	mainPlain := strings.Join(renderedPlainRows(block.Render(model.blockRenderContext(96))), "\n")
	if strings.Contains(mainPlain, "isolated child output") {
		t.Fatalf("background child transcript leaked into the main Spawn row:\n%s", mainPlain)
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "task-wait-1", Title: "Task wait",
			Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"action": "wait", "handle": "zuri"}, Meta: acpToolNameMeta("TASK"),
		},
	})
	if got := subscription.closeCalls.Load(); got != 0 {
		t.Fatalf("running Task wait closed the Spawn output subscription %d time(s)", got)
	}

	subscription.records <- controltaskstream.Record{
		Cursor: "cursor-2", Generation: "generation-1", Sequence: 2,
		Task: controltaskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true, CurrentTurnID: "child-turn-1",
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "SPAWN"},
		},
		Frame: &sdkstream.Frame{
			Ref:     sdkstream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "child-turn-1"},
			Running: true, Cursor: sdkstream.Cursor{Events: 2},
			Event: &session.Event{
				ID: "child-event-2", Type: session.EventTypeAssistant,
				Scope: &session.EventScope{Participant: session.ParticipantRef{Kind: session.ParticipantKindSubagent}},
				Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), MessageID: "child-message-2",
					Content: session.ProtocolTextContent("continued child output after Task wait"),
				}},
			},
		},
	}
	batch = receiveTUITaskStreamMessage[taskStreamBatchMsg](t, messages)
	if next, _ := model.Update(batch); next != nil {
		model = next.(*Model)
	}
	if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-1") {
		t.Fatal("Spawn did not open its already-observed Task output overlay")
	}
	if overlay := model.renderSubagentOutputOverlay(); !strings.Contains(overlay, "isolated child output") ||
		!strings.Contains(overlay, "continued child output after Task wait") {
		t.Fatalf("opened overlay omitted background transcript:\n%s", overlay)
	}

	model.closeSubagentOutputOverlay()
	if got := subscription.closeCalls.Load(); got != 0 {
		t.Fatalf("subscription Close calls = %d, hiding the overlay must retain observation", got)
	}
	if controlService.cancelCalls.Load() != 0 {
		t.Fatalf("Task cancel calls = %d, closing panel must not cancel Task", controlService.cancelCalls.Load())
	}
	model.closeTaskStreamSubscriptions()
	if got := subscription.closeCalls.Load(); got != 1 {
		t.Fatalf("subscription Close calls after Session cleanup = %d, want one", got)
	}
}

func TestTUITaskControlToolsDoNotCloseBackgroundSubagentStream(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"read", "wait", "write", "cancel"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()

			subscription := newTUIProtocolTaskSubscription()
			service := &tuiRetryTaskStreamService{subscription: subscription}
			sender := &ProgramSender{Send: func(tea.Msg) {}}
			defer sender.Close()
			model := NewModel(Config{
				Context: context.Background(), NoColor: true, NoAnimation: true,
				TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender,
			})
			model.currentSessionID = "session-1"
			view := model.ensureSubagentOutputView("spawn-1")
			view.taskHandle = "akio"
			view.block.Status = "running"
			model.taskStreamHandlesByID["task-1"] = "akio"
			model.taskStreamIDsByCallID["spawn-1"] = "task-1"
			model.taskStreamCallIDsByID["task-1"] = "spawn-1"
			model.taskStreamWanted["task-1"] = true
			model.taskStreamTokens["task-1"] = 7
			model.taskStreamSubscriptions["task-1"] = subscription

			model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
				Update: schema.ToolCall{
					SessionUpdate: schema.UpdateToolCall, ToolCallID: "task-control-" + action,
					Title: "Task " + action, Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
					RawInput: map[string]any{"action": action, "handle": "akio"}, Meta: acpToolNameMeta("TASK"),
				},
			})

			if got := subscription.closeCalls.Load(); got != 0 {
				t.Fatalf("Task %s closed the Spawn output subscription %d time(s)", action, got)
			}
			if !model.taskStreamWanted["task-1"] || model.taskStreamSubscriptions["task-1"] != subscription {
				t.Fatalf(
					"Task %s changed Spawn output demand: wanted=%v subscription=%p",
					action,
					model.taskStreamWanted["task-1"],
					model.taskStreamSubscriptions["task-1"],
				)
			}
		})
	}
}

func TestTaskStreamPanelHandleRejectsTaskControlOwners(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"read", "wait", "write", "cancel"} {
		events := []SubagentEvent{{
			Kind: SEToolCall, CallID: "task-" + action, Name: "Task",
			TaskAction: action, TaskHandle: "command-7",
		}}
		if handle := taskStreamPanelHandle(events, "task-"+action); handle != "" {
			t.Fatalf("Task %s became a stream panel owner with handle %q", action, handle)
		}
	}

	events := []SubagentEvent{{
		Kind: SEToolCall, CallID: "command-owner", Name: "RunCommand", TaskHandle: "@COMMAND-7",
	}}
	if handle := taskStreamPanelHandle(events, "command-owner"); handle != "command-7" {
		t.Fatalf("RunCommand stream panel handle = %q, want command-7", handle)
	}
}

func TestTUISubagentOutputStreamClosesAfterTerminalOwnerRepair(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-1", Title: "SPAWN helper",
			Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "self", "prompt": "inspect"}, Meta: acpToolNameMeta("SPAWN"),
		},
	})
	running := schema.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "zuri", "state": "running"}, Meta: acpToolNameMeta("SPAWN"),
		},
	})

	subscription := newTUIProtocolTaskSubscription()
	service := &tuiRetryTaskStreamService{subscription: subscription}
	sender := &ProgramSender{Send: func(tea.Msg) {}}
	defer sender.Close()
	model.cfg.TaskStreams = bindTaskStreamTestClient(t, service)
	model.cfg.ProgramSender = sender
	model.taskStreamHandlesByID["task-1"] = "zuri"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamSubscriptions["task-1"] = subscription

	completed := schema.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "SPAWN"},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "task-wait-1", Status: &completed,
			RawInput: map[string]any{"action": "wait", "handle": "zuri"},
			RawOutput: map[string]any{
				"action": "wait", "handle": "zuri", "state": "completed", "target_kind": "subagent",
				"parent_call": "spawn-1", "parent_tool": "SPAWN", "final_message": "durable child final",
			},
			Meta: acpToolNameMeta("TASK"),
		},
	})

	view := requireSubagentOutputViewForTest(t, model, "spawn-1")
	if view.finalResponse != "durable child final" || view.block.Status != "completed" {
		t.Fatalf("terminal owner repair = response %q status %q", view.finalResponse, view.block.Status)
	}
	if model.taskStreamWanted["task-1"] || model.taskStreamSubscriptions["task-1"] != nil {
		t.Fatalf(
			"terminal overlay owner retained stream demand: wanted=%v subscription=%p",
			model.taskStreamWanted["task-1"],
			model.taskStreamSubscriptions["task-1"],
		)
	}
	if got := subscription.closeCalls.Load(); got != 1 {
		t.Fatalf("terminal overlay owner closed subscription %d time(s), want one", got)
	}
}

func TestTUIBackgroundSubagentObservationRetriesDirectoryAndSubscriptionFailures(t *testing.T) {
	t.Parallel()

	subscription := newTUIProtocolTaskSubscription()
	service := &tuiRetryTaskStreamService{subscription: subscription}
	messages := make(chan tea.Msg, 16)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	model := NewModel(Config{
		Context: context.Background(), NoColor: true, NoAnimation: true,
		TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender,
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "SPAWN",
	})
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-1", Title: "SPAWN helper",
			Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "self", "prompt": "inspect"}, Meta: meta,
		},
	})
	running := schema.ToolStatusInProgress
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "zuri", "state": "running"}, Meta: meta,
		},
	})
	missing := receiveTUITaskStreamMessage[taskStreamResolvedMsg](t, messages)
	if !errors.Is(missing.err, errTaskStreamNotDiscoverable) {
		t.Fatalf("first directory result error = %v, want retryable discovery miss", missing.err)
	}
	next, retryResolve := model.Update(missing)
	model = next.(*Model)
	if retryResolve == nil {
		t.Fatal("directory miss did not schedule a retry")
	}
	next, _ = model.Update(retryResolve())
	model = next.(*Model)

	resolved := receiveTUITaskStreamMessage[taskStreamResolvedMsg](t, messages)
	if resolved.err != nil || resolved.taskID != "task-1" {
		t.Fatalf("retried directory result = %#v", resolved)
	}
	next, _ = model.Update(resolved)
	model = next.(*Model)

	closed := receiveTUITaskStreamMessage[taskStreamClosedMsg](t, messages)
	if !errorcode.Is(closed.err, errorcode.Unavailable) {
		t.Fatalf("first Subscribe error = %v, want unavailable", closed.err)
	}
	next, retrySubscribe := model.Update(closed)
	model = next.(*Model)
	if retrySubscribe == nil {
		t.Fatal("recoverable Subscribe failure did not schedule a retry")
	}
	next, _ = model.Update(retrySubscribe())
	model = next.(*Model)

	opened := receiveTUITaskStreamMessage[taskStreamOpenedMsg](t, messages)
	next, _ = model.Update(opened)
	model = next.(*Model)
	if calls := service.subscribeCalls.Load(); calls != 2 {
		t.Fatalf("Subscribe calls = %d, want failure plus retry", calls)
	}
	model.closeTaskStreamSubscriptions()
}

func TestTUIBackgroundSubagentObservationKeepsResolvingByParentCall(t *testing.T) {
	t.Parallel()

	service := &tuiRetryTaskStreamService{
		subscription:     newTUIProtocolTaskSubscription(),
		directoryMisses:  6,
		descriptorHandle: "canonical-child",
	}
	messages := make(chan tea.Msg, 32)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	model := NewModel(Config{
		Context: context.Background(), NoColor: true, NoAnimation: true,
		TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender,
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "SPAWN",
	})
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-1", Title: "SPAWN helper",
			Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "self", "prompt": "inspect"}, Meta: meta,
		},
	})
	running := schema.ToolStatusInProgress
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "provisional-child", "state": "running"}, Meta: meta,
		},
	})

	for attempt := 1; attempt <= 6; attempt++ {
		missing := receiveTUITaskStreamMessage[taskStreamResolvedMsg](t, messages)
		if !errors.Is(missing.err, errTaskStreamNotDiscoverable) {
			t.Fatalf("directory attempt %d = %#v, want retryable miss", attempt, missing)
		}
		next, retry := model.Update(missing)
		model = next.(*Model)
		if retry == nil {
			t.Fatalf("directory attempt %d stopped retrying", attempt)
		}
		next, _ = model.handleTaskStreamResolveRetry(taskStreamResolveRetryMsg{
			sessionID: missing.sessionID,
			callID:    missing.callID,
			handle:    missing.handle,
			token:     missing.token,
		})
		model = next.(*Model)
	}

	resolved := receiveTUITaskStreamMessage[taskStreamResolvedMsg](t, messages)
	if resolved.err != nil || resolved.taskID != "task-1" || resolved.handle != "canonical-child" {
		t.Fatalf("resolved Task = %#v, want canonical descriptor matched by parent call", resolved)
	}
	next, _ := model.Update(resolved)
	model = next.(*Model)
	view := requireSubagentOutputViewForTest(t, model, "spawn-1")
	if view.taskHandle != "canonical-child" {
		t.Fatalf("subagent public handle = %q, want canonical directory handle", view.taskHandle)
	}
}

func TestTUIBackgroundSubagentObservationStopsDirectoryRetryAfterSpawnTerminal(t *testing.T) {
	t.Parallel()

	service := &tuiRetryTaskStreamService{
		subscription:    newTUIProtocolTaskSubscription(),
		directoryMisses: 100,
	}
	messages := make(chan tea.Msg, 8)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	model := NewModel(Config{
		Context: context.Background(), NoColor: true, NoAnimation: true,
		TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender,
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeToolName: "SPAWN",
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall, ToolCallID: "spawn-1", Title: "SPAWN helper",
			Kind: schema.ToolKindExecute, Status: schema.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "self", "prompt": "inspect"}, Meta: meta,
		},
	})
	running := schema.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "zuri", "state": "running"}, Meta: meta,
		},
	})
	missing := receiveTUITaskStreamMessage[taskStreamResolvedMsg](t, messages)
	next, retry := model.Update(missing)
	model = next.(*Model)
	if retry == nil {
		t.Fatal("running Spawn directory miss did not schedule a retry")
	}

	completed := schema.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &completed,
			RawOutput: map[string]any{"handle": "zuri", "state": "completed", "final_response": "done"}, Meta: meta,
		},
	})
	if token := model.taskStreamResolveTokens["spawn-1"]; token != 0 {
		t.Fatalf("terminal Spawn resolve token = %d, want invalidated", token)
	}
	next, _ = model.Update(retry())
	model = next.(*Model)
	if calls := service.listCalls.Load(); calls != 1 {
		t.Fatalf("Task directory List calls after terminal Spawn = %d, want one initial call", calls)
	}
	if token := model.taskStreamResolveTokens["spawn-1"]; token != 0 {
		t.Fatalf("terminal Spawn resolve token = %d, want invalidated", token)
	}
}

func TestTUIBackgroundSubagentObservationStopsSubscribeRetryAfterSpawnTerminal(t *testing.T) {
	t.Parallel()

	service := &tuiRetryTaskStreamService{
		subscription:      newTUIProtocolTaskSubscription(),
		subscribeFailures: 100,
	}
	messages := make(chan tea.Msg, 8)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	model := NewModel(Config{
		Context: context.Background(), NoColor: true, NoAnimation: true,
		TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender,
	})
	model.currentSessionID = "session-1"
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	model.taskStreamHandlesByID["task-1"] = "zuri"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7

	next, retry := model.handleTaskStreamClosed(taskStreamClosedMsg{
		sessionID: "session-1", taskID: "task-1", token: 7,
		err: errorcode.New(errorcode.Unavailable, "temporary"),
	})
	model = next.(*Model)
	if retry == nil {
		t.Fatal("running Spawn subscription failure did not schedule a retry")
	}

	completed := schema.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &completed,
			RawOutput: map[string]any{"handle": "zuri", "state": "completed", "final_response": "done"},
			Meta:      acpToolNameMeta("SPAWN"),
		},
	})
	if model.taskStreamWanted["task-1"] {
		t.Fatal("terminal Spawn retained Task stream demand")
	}
	next, _ = model.Update(retry())
	model = next.(*Model)
	if calls := service.subscribeCalls.Load(); calls != 0 {
		t.Fatalf("Task Subscribe calls after terminal Spawn = %d, want zero", calls)
	}
	if strings.Contains(model.hint, "live output is unavailable") {
		t.Fatalf("terminal Spawn surfaced a stale Task stream hint: %q", model.hint)
	}
}

func TestTaskStreamDemandStopsForEveryTerminalSubagentStatus(t *testing.T) {
	t.Parallel()

	for _, status := range []string{
		"completed",
		"succeeded",
		"success",
		"failed",
		"cancelled",
		"canceled",
		"interrupted",
		"terminated",
		eventstream.LifecycleStateUnknown,
	} {
		t.Run(status, func(t *testing.T) {
			model := NewModel(Config{NoColor: true, NoAnimation: true})
			view := model.ensureSubagentOutputView("spawn-1")
			view.taskHandle = "zuri"
			view.block.Status = status
			demand := model.taskStreamDemandForOwner("spawn-1", "zuri")
			if demand != taskStreamDemandFinishedSubagent || demand.wanted() {
				t.Fatalf("terminal status %q demand = %v wanted=%v", status, demand, demand.wanted())
			}
		})
	}
}

func TestTUIBackgroundSubagentObservationClosesRemappedActiveStreamAfterSpawnTerminal(t *testing.T) {
	t.Parallel()

	subscription := newTUIProtocolTaskSubscription()
	service := &tuiRetryTaskStreamService{subscription: subscription}
	sender := &ProgramSender{Send: func(tea.Msg) {}}
	defer sender.Close()
	model := NewModel(Config{
		Context: context.Background(), NoColor: true, NoAnimation: true,
		TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender,
	})
	model.currentSessionID = "session-1"
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "canonical-child"
	model.taskStreamHandlesByID["task-1"] = "canonical-child"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamSubscriptions["task-1"] = subscription

	completed := schema.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &completed,
			RawOutput: map[string]any{"handle": "provisional-child", "state": "completed", "final_response": "done"},
			Meta:      acpToolNameMeta("SPAWN"),
		},
	})
	if model.taskStreamWanted["task-1"] {
		t.Fatal("terminal Spawn retained active Task stream demand")
	}
	if model.taskStreamSubscriptions["task-1"] != nil {
		t.Fatal("terminal Spawn retained its active Task stream subscription")
	}
	if _, open := <-subscription.events; open {
		t.Fatal("terminal Spawn did not close the remapped Task stream")
	}
}

func TestTUIClearHistoryClosesBackgroundSubagentStreams(t *testing.T) {
	t.Parallel()

	subscription := newTUIProtocolTaskSubscription()
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-old"
	model.taskStreamWanted["task-old"] = true
	model.taskStreamTokens["task-old"] = 7
	model.taskStreamSubscriptions["task-old"] = subscription
	model.taskStreamCursors["task-old"] = "cursor-old"
	model.taskStreamHandlesByID["task-old"] = "child-old"
	model.taskStreamIDsByCallID["spawn-old"] = "task-old"
	model.taskStreamCallIDsByID["task-old"] = "spawn-old"
	model.ensureSubagentOutputView("spawn-old")

	next, _ := model.Update(ClearHistoryMsg{})
	model = next.(*Model)

	if _, open := <-subscription.events; open {
		t.Fatal("ClearHistory left the old Session Task stream open")
	}
	if len(model.taskStreamWanted) != 0 ||
		len(model.taskStreamTokens) != 0 ||
		len(model.taskStreamSubscriptions) != 0 ||
		len(model.taskStreamCursors) != 0 ||
		len(model.taskStreamHandlesByID) != 0 ||
		len(model.taskStreamIDsByCallID) != 0 ||
		len(model.taskStreamCallIDsByID) != 0 {
		t.Fatalf(
			"Task stream state survived ClearHistory: wanted=%v tokens=%v subscriptions=%v cursors=%v handles=%v calls=%v taskCalls=%v",
			model.taskStreamWanted,
			model.taskStreamTokens,
			model.taskStreamSubscriptions,
			model.taskStreamCursors,
			model.taskStreamHandlesByID,
			model.taskStreamIDsByCallID,
			model.taskStreamCallIDsByID,
		)
	}
	if len(model.subagentOutputViews) != 0 {
		t.Fatalf("subagent output views survived ClearHistory: %#v", model.subagentOutputViews)
	}
}

func TestTUITaskMailboxBoundsOneUpdateBatch(t *testing.T) {
	t.Parallel()

	events := make(chan eventstream.Envelope, taskStreamMailboxBatchSize+8)
	for i := 0; i < cap(events); i++ {
		events <- eventstream.Envelope{EventID: "event"}
	}
	batch, open := readTaskStreamMailbox(context.Background(), events)
	if !open || len(batch) != taskStreamMailboxBatchSize {
		t.Fatalf("mailbox batch = %d open=%v, want %d/open", len(batch), open, taskStreamMailboxBatchSize)
	}

	one := make(chan eventstream.Envelope, 1)
	one <- eventstream.Envelope{EventID: "one"}
	started := time.Now()
	batch, open = readTaskStreamMailbox(context.Background(), one)
	if !open || len(batch) != 1 || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("time-bounded mailbox batch = %d open=%v elapsed=%v", len(batch), open, time.Since(started))
	}
}

func TestTUISubagentOutputSurfacesPermanentSubscriptionFailure(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	view.block.Status = "running"
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamHandlesByID["task-1"] = "zuri"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"

	next, _ := model.handleTaskStreamClosed(taskStreamClosedMsg{
		sessionID: "session-1", taskID: "task-1", token: 7,
		err: errorcode.New(errorcode.PermissionDenied, "task stream access denied"),
	})
	model = next.(*Model)
	if !strings.Contains(model.hint, "Task zuri live output is unavailable") || !strings.Contains(model.hint, "access denied") {
		t.Fatalf("permanent Task stream failure hint = %q", model.hint)
	}
}

func TestTUITaskPanelSilentlyAdvancesTransientGapBoundary(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7

	next, _ := model.handleTaskStreamBatch(taskStreamBatchMsg{
		sessionID: "session-1",
		taskID:    "task-1",
		token:     7,
		events: []eventstream.Envelope{{
			Kind:      eventstream.KindNotice,
			SessionID: "session-1",
			Scope:     eventstream.ScopeSubagent,
			ScopeID:   "task-1",
			Cursor:    "boundary-cursor",
			Notice:    "transient Task output before this boundary is no longer available",
			Meta: map[string]any{
				"task_stream": map[string]any{"transient_gap": true},
			},
		}},
	})
	model = next.(*Model)
	if got := model.taskStreamCursors["task-1"]; got != "boundary-cursor" {
		t.Fatalf("Task cursor = %q, want accepted gap boundary", got)
	}
	if frame := model.View().Content; strings.Contains(frame, "transient Task output") {
		t.Fatalf("Task transient gap leaked into TUI frame:\n%s", frame)
	}
}

func receiveTUITaskStreamMessage[T any](t *testing.T, messages <-chan tea.Msg) T {
	t.Helper()
	select {
	case raw := <-messages:
		message, ok := raw.(T)
		if !ok {
			t.Fatalf("task stream message = %T, want %T", raw, *new(T))
		}
		return message
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %T", *new(T))
		return *new(T)
	}
}

type tuiTestTaskStreamService struct {
	subscription *tuiTestTaskSubscription
	requests     chan controltaskstream.SubscribeRequest
	list         controltaskstream.ListResult
	cancelCalls  atomic.Int32
}

type tuiRetryTaskStreamService struct {
	listCalls         atomic.Int32
	subscribeCalls    atomic.Int32
	subscription      *tuiProtocolTaskSubscription
	directoryMisses   int32
	descriptorHandle  string
	subscribeFailures int32
}

type tuiProtocolTaskSubscription struct {
	events     chan eventstream.Envelope
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func newTUIProtocolTaskSubscription() *tuiProtocolTaskSubscription {
	return &tuiProtocolTaskSubscription{events: make(chan eventstream.Envelope)}
}

func (s *tuiProtocolTaskSubscription) Events() <-chan eventstream.Envelope { return s.events }
func (*tuiProtocolTaskSubscription) Err() error                            { return nil }
func (*tuiProtocolTaskSubscription) LastCursor() string                    { return "" }
func (s *tuiProtocolTaskSubscription) Close() error {
	s.closeOnce.Do(func() {
		s.closeCalls.Add(1)
		close(s.events)
	})
	return nil
}

func (s *tuiRetryTaskStreamService) List(context.Context, taskstream.Principal, taskstream.ListRequest) (taskstream.ListResult, error) {
	misses := s.directoryMisses
	if misses <= 0 {
		misses = 1
	}
	if s.listCalls.Add(1) <= misses {
		return taskstream.ListResult{}, nil
	}
	handle := strings.TrimSpace(s.descriptorHandle)
	if handle == "" {
		handle = "zuri"
	}
	return taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
		SessionID: "session-1", TaskID: "task-1", Handle: handle, Kind: task.KindSubagent,
		State: task.StateRunning, Running: true,
		ParentTool: taskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "SPAWN"},
	}}}, nil
}

func (*tuiRetryTaskStreamService) Events(context.Context, taskstream.Principal, taskstream.ReadRequest) (taskstream.Batch, error) {
	return taskstream.Batch{}, nil
}

func (s *tuiRetryTaskStreamService) Subscribe(context.Context, taskstream.Principal, taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	failures := s.subscribeFailures
	if failures <= 0 {
		failures = 1
	}
	if s.subscribeCalls.Add(1) <= failures {
		return taskstream.SubscribeResult{}, errorcode.New(errorcode.Unavailable, "task stream temporarily unavailable")
	}
	return taskstream.SubscribeResult{Subscription: s.subscription, ResumeMode: taskstream.ResumeModeExact}, nil
}

func bindTaskStreamTestClient(t *testing.T, service taskstream.Service) taskstream.Client {
	t.Helper()
	client, err := taskstream.BindClient(service, taskstream.Principal{ID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func (s *tuiTestTaskStreamService) List(context.Context, controltaskstream.Principal, controltaskstream.ListRequest) (controltaskstream.ListResult, error) {
	return s.list, nil
}

func (s *tuiTestTaskStreamService) Events(context.Context, controltaskstream.Principal, controltaskstream.ReadRequest) (controltaskstream.Batch, error) {
	return controltaskstream.Batch{}, nil
}

func (s *tuiTestTaskStreamService) Subscribe(_ context.Context, _ controltaskstream.Principal, request controltaskstream.SubscribeRequest) (controltaskstream.SubscribeResult, error) {
	s.requests <- request
	return controltaskstream.SubscribeResult{Subscription: s.subscription, ResumeMode: controltaskstream.ResumeModeExact}, nil
}

type tuiTestTaskSubscription struct {
	records    chan controltaskstream.Record
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func newTUITestTaskSubscription() *tuiTestTaskSubscription {
	return &tuiTestTaskSubscription{records: make(chan controltaskstream.Record, 8)}
}

func (s *tuiTestTaskSubscription) Records() <-chan controltaskstream.Record { return s.records }
func (s *tuiTestTaskSubscription) Err() error                               { return nil }
func (s *tuiTestTaskSubscription) LastCursor() string                       { return "" }
func (s *tuiTestTaskSubscription) Close() error {
	s.closeOnce.Do(func() {
		s.closeCalls.Add(1)
		close(s.records)
	})
	return nil
}
