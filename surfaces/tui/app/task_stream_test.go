package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

func TestTUIColdIdleWorkspaceUsesOneFollowingSubscription(t *testing.T) {
	service := &subagentRosterTestTaskStreamService{
		subscribeRequests: make(chan taskstream.SubscribeRequest, 4),
		eventRequests:     make(chan taskstream.ReadRequest, 4),
	}
	messages := make(chan tea.Msg, 8)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	model := NewModel(Config{NoColor: true, NoAnimation: true, TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender})
	model.currentSessionID = "session-old"
	model.width, model.height = 100, 30
	var descriptors []taskstream.TaskDescriptor
	for _, name := range []string{"one", "two"} {
		addSubagentRosterTestView(model, "spawn-"+name, name, name+"[helper]: old task", "completed", time.Unix(1, 0), time.Unix(2, 0))
		descriptors = append(descriptors, taskstream.TaskDescriptor{SessionID: "session-old", TaskID: "task-" + name, Handle: name, Kind: task.KindSubagent,
			State: task.StateCompleted, ActivityID: "activity-" + name, ParentTool: taskstream.ParentTool{ToolCallID: "spawn-" + name, ToolName: "StartThread"}})
	}
	service.list.Tasks = descriptors
	applySubagentDirectorySnapshotForTest(model, 1, descriptors)
	if len(service.subscribeRequests) != 0 || len(service.eventRequests) != 0 {
		t.Fatal("hidden workspaces started observation")
	}
	model.workspace.lastCallID = "spawn-two"
	if !model.openSubagentWorkspace() {
		t.Fatal("idle workspace did not open")
	}
	resolved := receiveTUITaskStreamMessage[taskStreamResolvedMsg](t, messages)
	model.Update(resolved)
	select {
	case request := <-service.subscribeRequests:
		if request.TaskID != "task-two" || !request.Follow {
			t.Fatalf("cold subscription = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("idle workspace did not subscribe")
	}
	select {
	case request := <-service.eventRequests:
		t.Fatalf("separate history read = %#v", request)
	default:
	}
	select {
	case request := <-service.subscribeRequests:
		t.Fatalf("extra subscription = %#v", request)
	default:
	}
	model.closeSubagentOutputOverlay()
}

func TestTUISubagentWorkspaceObservesOnlyWhileOpenAndResumesCursor(t *testing.T) {
	t.Parallel()

	firstSubscription := newTUITestTaskSubscription()
	controlService := &tuiTestTaskStreamService{
		subscription: firstSubscription,
		requests:     make(chan controltaskstream.SubscribeRequest, 2),
		list: controltaskstream.ListResult{Tasks: []controltaskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true,
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "StartThread"},
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
	meta := testMeta.WithRuntimeSection(nil, testMeta.RuntimeTool, map[string]any{
		testMeta.RuntimeToolName: "StartThread",
	})
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn-1", Title: "Spawn helper",
			Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "self", "prompt": "inspect"}, Meta: meta,
		},
	})
	running := eventstream.ToolStatusInProgress
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "zuri", "state": "running"}, Meta: meta,
		},
	})

	select {
	case message := <-messages:
		t.Fatalf("hidden subagent workspace started Task observation: %T", message)
	default:
	}
	block := requireMainACPTurnBlockForTest(t, model)
	if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-1") {
		t.Fatal("opening Spawn did not open the subagent workspace")
	}
	resolved := receiveTUITaskStreamMessage[taskStreamResolvedMsg](t, messages)
	if next, _ := model.Update(resolved); next != nil {
		model = next.(*Model)
	}

	select {
	case request := <-controlService.requests:
		if request.SessionID != "session-1" || request.TaskID != "task-1" || request.Cursor != "" || !request.Follow {
			t.Fatalf("Subscribe request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("open subagent workspace did not subscribe")
	}
	opened := receiveTUITaskStreamMessage[taskStreamOpenedMsg](t, messages)
	if next, _ := model.Update(opened); next != nil {
		model = next.(*Model)
	}

	firstSubscription.records <- controltaskstream.Record{
		Cursor: "cursor-1", Generation: "generation-1", Sequence: 1,
		Task: controltaskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true, CurrentTurnID: "child-turn-1",
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "StartThread"},
		},
		Frame: &controltaskstream.Frame{
			TerminalID: "child-turn-1",
			Running:    true,
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
	if view == nil || view.document == nil {
		t.Fatal("open subagent workspace did not retain a transcript Document")
	}
	childPlain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 20)), "\n")
	if !strings.Contains(childPlain, "isolated child output") {
		t.Fatalf("child workspace omitted first Turn output:\n%s", childPlain)
	}
	mainPlain := strings.Join(renderedPlainRows(block.Render(model.blockRenderContext(96))), "\n")
	if strings.Contains(mainPlain, "isolated child output") {
		t.Fatalf("background child transcript leaked into the main Spawn row:\n%s", mainPlain)
	}
	model.closeSubagentOutputOverlay()
	if got := firstSubscription.closeCalls.Load(); got != 1 {
		t.Fatalf("closing workspace closed subscription %d time(s), want one", got)
	}
	if controlService.cancelCalls.Load() != 0 {
		t.Fatalf("closing workspace canceled Task %d time(s)", controlService.cancelCalls.Load())
	}
	closed := receiveTUITaskStreamMessage[taskStreamClosedMsg](t, messages)
	if next, _ := model.Update(closed); next != nil {
		model = next.(*Model)
	}

	secondSubscription := newTUITestTaskSubscription()
	controlService.subscription = secondSubscription
	if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-1") {
		t.Fatal("reopening Spawn did not reopen the subagent workspace")
	}
	select {
	case request := <-controlService.requests:
		if request.TaskID != "task-1" || request.Cursor != "cursor-1" || !request.Follow {
			t.Fatalf("resumed Subscribe request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("reopened subagent workspace did not resume Task observation")
	}
	opened = receiveTUITaskStreamMessage[taskStreamOpenedMsg](t, messages)
	if next, _ := model.Update(opened); next != nil {
		model = next.(*Model)
	}

	secondSubscription.records <- controltaskstream.Record{
		Cursor: "cursor-2", Generation: "generation-1", Sequence: 2,
		Task: controltaskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true, CurrentTurnID: "child-turn-2",
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "StartThread"},
		},
		Frame: &controltaskstream.Frame{
			TerminalID: "child-turn-2",
			Running:    true,
			Event: &session.Event{
				ID: "child-event-2", Type: session.EventTypeAssistant,
				Scope: &session.EventScope{Participant: session.ParticipantRef{Kind: session.ParticipantKindSubagent}},
				Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), MessageID: "child-message-2",
					Content: session.ProtocolTextContent("observed second activity output"),
				}},
			},
		},
	}
	batch = receiveTUITaskStreamMessage[taskStreamBatchMsg](t, messages)
	if next, _ := model.Update(batch); next != nil {
		model = next.(*Model)
	}
	secondSubscription.records <- controltaskstream.Record{
		Cursor: "cursor-3", Generation: "generation-1", Sequence: 3,
		Task: controltaskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true, CurrentTurnID: "child-turn-2",
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "StartThread"},
		},
		Frame: &controltaskstream.Frame{
			TerminalID: "child-turn-2",
			Running:    true,
			Event: &session.Event{
				ID: "child-event-final", Type: session.EventTypeAssistant,
				Scope: &session.EventScope{Participant: session.ParticipantRef{Kind: session.ParticipantKindSubagent}},
				Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), MessageID: "child-message-final",
					Content: session.ProtocolTextContent("terminal final from child Task stream"),
				}},
			},
		},
	}
	batch = receiveTUITaskStreamMessage[taskStreamBatchMsg](t, messages)
	if next, _ := model.Update(batch); next != nil {
		model = next.(*Model)
	}
	secondSubscription.records <- controltaskstream.Record{
		Cursor: "cursor-4", Generation: "generation-1", Sequence: 4,
		Task: controltaskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
			State: task.StateCompleted, CurrentTurnID: "child-turn-2",
			ParentTool: controltaskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "StartThread"},
		},
		Frame: &controltaskstream.Frame{
			TerminalID: "child-turn-2",
			State:      string(task.StateCompleted),
			Closed:     true,
		},
	}
	batch = receiveTUITaskStreamMessage[taskStreamBatchMsg](t, messages)
	if next, _ := model.Update(batch); next != nil {
		model = next.(*Model)
	}
	if overlay := model.renderSubagentOutputOverlay(); !strings.Contains(overlay, "isolated child output") ||
		!strings.Contains(overlay, "observed second activity output") ||
		!strings.Contains(overlay, "terminal final from child Task stream") ||
		strings.Contains(overlay, "(no output)") {
		t.Fatalf("subagent workspace omitted multi-Turn transcript:\n%s", overlay)
	}
	if len(view.turnBlocks) != 2 {
		t.Fatalf("subagent workspace Turn blocks = %d, want two internal transcript groups", len(view.turnBlocks))
	}
	model.closeSubagentOutputOverlay()
	model.closeTaskStreamSubscriptions()
}

func TestTUITaskControlToolsDoNotAffectVisibleSubagentStream(t *testing.T) {
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
			model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1"}
			model.taskStreamHandlesByID["task-1"] = "akio"
			model.taskStreamIDsByCallID["spawn-1"] = "task-1"
			model.taskStreamCallIDsByID["task-1"] = "spawn-1"
			model.taskStreamWanted["task-1"] = true
			model.taskStreamTokens["task-1"] = 7
			model.taskStreamSubscriptions["task-1"] = subscription

			model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
				Update: eventstream.ToolCall{
					SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "task-control-" + action,
					Title: "Task " + action, Kind: eventstream.ToolKindOther, Status: eventstream.ToolStatusInProgress,
					RawInput: map[string]any{"action": action, "handle": "akio"}, Meta: acpToolNameMeta("Task"),
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

func TestTUIVisibleSubagentObservationRetriesDirectoryAndSubscriptionFailures(t *testing.T) {
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
	meta := testMeta.WithRuntimeSection(nil, testMeta.RuntimeTool, map[string]any{
		testMeta.RuntimeToolName: "StartThread",
	})
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn-1", Title: "Spawn helper",
			Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "self", "prompt": "inspect"}, Meta: meta,
		},
	})
	running := eventstream.ToolStatusInProgress
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "zuri", "state": "running"}, Meta: meta,
		},
	})
	block := requireMainACPTurnBlockForTest(t, model)
	if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-1") {
		t.Fatal("opening Spawn did not start subagent observation")
	}
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

func TestTUIVisibleSubagentObservationKeepsResolvingByParentCall(t *testing.T) {
	t.Parallel()

	service := &tuiRetryTaskStreamService{
		subscription:            newTUIProtocolTaskSubscription(),
		directoryMisses:         6,
		descriptorHandle:        "canonical-child",
		descriptorParticipantID: "participant-1",
	}
	messages := make(chan tea.Msg, 32)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	model := NewModel(Config{
		Context: context.Background(), NoColor: true, NoAnimation: true,
		TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender,
	})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	meta := testMeta.WithRuntimeSection(nil, testMeta.RuntimeTool, map[string]any{
		testMeta.RuntimeToolName: "StartThread",
	})
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn-1", Title: "Spawn helper",
			Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "self", "prompt": "inspect"}, Meta: meta,
		},
	})
	running := eventstream.ToolStatusInProgress
	_, _ = model.handleACPEventEnvelope(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "provisional-child", "state": "running"}, Meta: meta,
		},
	})
	block := requireMainACPTurnBlockForTest(t, model)
	if !model.openSubagentOutputOverlay(block.BlockID(), "spawn-1") {
		t.Fatal("opening Spawn did not start subagent observation")
	}

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
	if view.participantID != "participant-1" {
		t.Fatalf("subagent participant identity = %q, want directory identity", view.participantID)
	}
}

func TestTaskStreamDemandDependsOnWorkspaceVisibilityNotTaskStatus(t *testing.T) {
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
			if demand := model.taskStreamDemandForOwner("spawn-1", "zuri"); demand != taskStreamDemandNone {
				t.Fatalf("hidden workspace with status %q demand = %v, want none", status, demand)
			}
			model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1"}
			if demand := model.taskStreamDemandForOwner("spawn-1", "zuri"); demand != taskStreamDemandVisibleSubagent {
				t.Fatalf("visible workspace with status %q demand = %v, want Follow", status, demand)
			}
		})
	}
}

func TestTUIVisibleSubagentObservationSurvivesSpawnTerminalAndStopsOnClose(t *testing.T) {
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
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1"}
	model.taskStreamHandlesByID["task-1"] = "canonical-child"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamSubscriptions["task-1"] = subscription

	completed := eventstream.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &completed,
			RawOutput: map[string]any{"handle": "provisional-child", "state": "completed", "final_response": "done"},
			Meta:      acpToolNameMeta("StartThread"),
		},
	})
	if !model.taskStreamWanted["task-1"] {
		t.Fatal("terminal Spawn stopped visible Task stream demand")
	}
	if model.taskStreamSubscriptions["task-1"] != subscription {
		t.Fatal("terminal Spawn replaced the visible Task stream subscription")
	}
	model.closeSubagentOutputOverlay()
	if model.taskStreamWanted["task-1"] || model.taskStreamSubscriptions["task-1"] != nil {
		t.Fatal("closing the workspace retained Task stream observation")
	}
	if got := subscription.closeCalls.Load(); got != 1 {
		t.Fatalf("closing the workspace closed subscription %d time(s), want one", got)
	}
}

func TestTUILiveTaskStreamBatchesUseOneCoalescedOverlayFrame(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	model.width = 96
	model.height = 24
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1", followTail: true}
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamHandlesByID["task-1"] = "zuri"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"

	before := model.renderSubagentOutputOverlay()
	initialRenders := view.renderCache.renders
	if !strings.Contains(before, "Waiting for subagent output") {
		t.Fatalf("initial overlay omitted waiting state:\n%s", before)
	}

	for index, text := range []string{"first", " second"} {
		next, _ := model.handleTaskStreamBatch(taskStreamBatchMsg{
			sessionID: "session-1", taskID: "task-1", token: 7,
			events: []eventstream.Envelope{{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "task-1:1",
				Scope: eventstream.ScopeSubagent, ScopeID: "task-1", Cursor: fmt.Sprintf("cursor-%d", index+1),
				ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "StartThread"},
				Update: eventstream.ContentChunk{
					SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "message-1",
					Content: eventstream.TextContent{Type: "text", Text: text},
				},
			}},
		})
		model = next.(*Model)
	}

	if got := subagentOutputNarrativeTextForTest(view, SEAssistant); got != "first second" {
		t.Fatalf("semantic stream = %q, want exact accumulated deltas", got)
	}
	if !view.renderScheduled {
		t.Fatal("live batches did not schedule an overlay render")
	}
	if got := model.renderSubagentOutputOverlay(); strings.Join(strings.Split(got, "\n")[:model.subagentOutputOverlay.layout.contentRows+2], "\n") != strings.Join(strings.Split(before, "\n")[:model.subagentOutputOverlay.layout.contentRows+2], "\n") {
		t.Fatalf("live batches bypassed the coalescing window\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}
	if got := view.renderCache.renders; got != initialRenders {
		t.Fatalf("live batches rendered %d full frames before the tick, want %d", got, initialRenders)
	}

	next, _ := model.handleSubagentOutputRenderTick(subagentOutputRenderTickMsg{callID: "spawn-1"})
	model = next.(*Model)
	after := model.renderSubagentOutputOverlay()
	if !strings.Contains(after, "first second") {
		t.Fatalf("coalesced overlay omitted exact accumulated output:\n%s", after)
	}
	if got := view.renderCache.renders; got != initialRenders+1 {
		t.Fatalf("coalesced live batches rendered %d full frames, want %d", got, initialRenders+1)
	}
}

func TestTUIVisibleSubagentEmptyBatchWaitsForTaskLifecycleBeforeDetaching(t *testing.T) {
	t.Parallel()

	subscription := newTUIProtocolTaskSubscription()
	sender := &ProgramSender{Send: func(tea.Msg) {}}
	defer sender.Close()
	model := NewModel(Config{
		Context: context.Background(), NoColor: true, NoAnimation: true,
		TaskStreams: bindTaskStreamTestClient(t, &subagentRosterTestTaskStreamService{}), ProgramSender: sender,
	})
	model.currentSessionID = "session-1"
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1"}
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamSubscriptions["task-1"] = subscription
	model.taskStreamHandlesByID["task-1"] = "zuri"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"

	next, _ := model.handleTaskStreamBatch(taskStreamBatchMsg{
		sessionID: "session-1", taskID: "task-1", token: 7,
	})
	model = next.(*Model)
	view = requireSubagentOutputViewForTest(t, model, "spawn-1")
	if !view.historyResolved || subagentOutputViewHasTranscript(view) {
		t.Fatalf("empty batch resolved=%v transcript=%v, want resolved empty view", view.historyResolved, subagentOutputViewHasTranscript(view))
	}

	startedAt := time.Unix(100, 0)
	applySubagentDirectorySnapshotForTest(model, 1, []taskstream.TaskDescriptor{{
		SessionID: "session-1", TaskID: "task-1", Handle: "zuri", Kind: task.KindSubagent,
		State: task.StateCompleted, Running: false, CurrentTurnID: "task-1:2", UpdatedAt: startedAt,
		ParentTool: taskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "StartThread"},
	}})
	if model.taskStreamSubscriptions["task-1"] != subscription || !model.taskStreamWanted["task-1"] {
		t.Fatal("terminal directory metadata detached an empty visible child before its Task lifecycle frame")
	}
	if got := subscription.closeCalls.Load(); got != 0 {
		t.Fatalf("terminal directory metadata closed empty subscription %d time(s), want zero", got)
	}

	terminal := eventstream.Envelope{
		Kind: eventstream.KindLifecycle, SessionID: "session-1", TurnID: "task-1:2",
		Scope: eventstream.ScopeSubagent, ScopeID: "task-1", Cursor: "terminal-cursor", OccurredAt: startedAt.Add(time.Second),
		Delivery:   &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		ParentTool: &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "StartThread"},
		Lifecycle:  &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted}, Final: true,
	}
	next, _ = model.handleTaskStreamBatch(taskStreamBatchMsg{
		sessionID: "session-1", taskID: "task-1", token: 7, events: []eventstream.Envelope{terminal},
	})
	model = next.(*Model)
	view = requireSubagentOutputViewForTest(t, model, "spawn-1")
	if view.block.Status != eventstream.LifecycleStateCompleted {
		t.Fatalf("empty child status = %q, want completed lifecycle", view.block.Status)
	}
	if model.taskStreamSubscriptions["task-1"] != subscription || !model.taskStreamWanted["task-1"] {
		t.Fatal("terminal lifecycle detached the empty cross-activity observer")
	}
	if got := subscription.closeCalls.Load(); got != 0 {
		t.Fatalf("terminal lifecycle closed empty subscription %d time(s), want zero", got)
	}
}

func TestTUIClearHistoryClosesVisibleSubagentStreams(t *testing.T) {
	t.Parallel()

	subscription := newTUIProtocolTaskSubscription()
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-old"
	model.slashSkillLoaded = true
	model.slashSkillLoadPending = true
	model.slashSkillLoadSeq = 9
	model.slashSkillCatalog = []CompletionCandidate{{Value: "old-skill"}}
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
	if model.slashSkillLoaded || model.slashSkillLoadPending || model.slashSkillLoadSeq != 10 || len(model.slashSkillCatalog) != 0 {
		t.Fatalf(
			"slash skill cache survived ClearHistory: loaded=%v loading=%v request=%d catalog=%#v",
			model.slashSkillLoaded,
			model.slashSkillLoadPending,
			model.slashSkillLoadSeq,
			model.slashSkillCatalog,
		)
	}
}

func TestTUITaskMailboxAcceptsOneDeliveryPage(t *testing.T) {
	t.Parallel()

	events := make([]eventstream.Envelope, taskStreamMailboxBatchSize+8)
	for i := range events {
		events[i] = tuiExactEnvelope(eventstream.Envelope{EventID: "event"}, "cursor-1", uint64(i+1))
	}
	deliveries := make(chan taskstream.Delivery, 1)
	deliveries <- taskstream.Delivery{
		Kind: taskstream.DeliveryAppendPage, Source: taskstream.SourceExact,
		Events: events, NextCursor: "cursor-1",
	}
	started := time.Now()
	batch, cursor, _, replacement, open, err := (&taskStreamMailbox{}).read(context.Background(), deliveries)
	if err != nil || !open || replacement || cursor != "cursor-1" || len(batch) != len(events) || time.Since(started) > 100*time.Millisecond {
		t.Fatalf("mailbox page = %d cursor=%q replacement=%v open=%v err=%v elapsed=%v", len(batch), cursor, replacement, open, err, time.Since(started))
	}
}

func TestTUISubagentOutputSurfacesPermanentSubscriptionFailure(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	view.block.Status = "running"
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1"}
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

func TestTUISubagentOutputStopsMaskingRepeatedCleanFollowExit(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "zuri"
	view.block.Status = "running"
	model.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn-1"}
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamRetries["task-1"] = taskStreamCleanExitRetries
	model.taskStreamHandlesByID["task-1"] = "zuri"
	model.taskStreamIDsByCallID["spawn-1"] = "task-1"
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"

	next, _ := model.handleTaskStreamClosed(taskStreamClosedMsg{
		sessionID: "session-1", taskID: "task-1", token: 7,
	})
	model = next.(*Model)
	if !strings.Contains(model.hint, "following task stream ended unexpectedly") {
		t.Fatalf("clean Follow exit hint = %q, want invariant failure surfaced", model.hint)
	}
	if model.taskStreamTokens["task-1"] != 0 {
		t.Fatalf("Task stream token = %d, want retries stopped", model.taskStreamTokens["task-1"])
	}
}

func TestTUISubagentReplacementRebuildsCompleteMultiTurnHistory(t *testing.T) {
	t.Parallel()

	startedAt := time.Unix(300, 0)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.currentSessionID = "session-1"
	model.taskStreamWanted["task-1"] = true
	model.taskStreamTokens["task-1"] = 7
	model.taskStreamCallIDsByID["task-1"] = "spawn-1"
	view := model.ensureSubagentOutputView("spawn-1")
	view.block.AppendStreamEvent(SEReasoning, "stale partial prefix", narrativeSourceIdentity{})

	next, _ := model.handleTaskStreamBatch(taskStreamBatchMsg{
		sessionID:   "session-1",
		taskID:      "task-1",
		token:       7,
		replacement: true,
		events: []eventstream.Envelope{{
			Kind:       eventstream.KindSessionUpdate,
			SessionID:  "session-1",
			TurnID:     "task-1:1",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			Cursor:     "boundary-cursor",
			OccurredAt: startedAt,
			ParentTool: &eventstream.ParentToolRelation{
				ToolCallID: "spawn-1", ToolName: "StartThread",
			},
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentThought,
				MessageID:     "reasoning-turn-1",
				Content:       eventstream.TextContent{Type: "text", Text: "complete rebuilt first reasoning"},
			},
		}, {
			Kind:       eventstream.KindSessionUpdate,
			SessionID:  "session-1",
			TurnID:     "task-1:1",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			Cursor:     "boundary-cursor",
			OccurredAt: startedAt.Add(3 * time.Second),
			ParentTool: &eventstream.ParentToolRelation{
				ToolCallID: "spawn-1", ToolName: "StartThread",
			},
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				MessageID:     "final-turn-1",
				Content:       eventstream.TextContent{Type: "text", Text: "first exact Final Message"},
			},
		}, {
			Kind:       eventstream.KindLifecycle,
			SessionID:  "session-1",
			TurnID:     "task-1:1",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			Cursor:     "boundary-cursor",
			OccurredAt: startedAt.Add(4 * time.Second),
			ParentTool: &eventstream.ParentToolRelation{
				ToolCallID: "spawn-1", ToolName: "StartThread",
			},
			Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted},
		}, {
			Kind:       eventstream.KindSessionUpdate,
			SessionID:  "session-1",
			TurnID:     "task-1:2",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			Cursor:     "boundary-cursor",
			OccurredAt: startedAt.Add(10 * time.Second),
			ParentTool: &eventstream.ParentToolRelation{
				ToolCallID: "spawn-1", ToolName: "StartThread",
			},
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentThought,
				MessageID:     "reasoning-turn-2",
				Content:       eventstream.TextContent{Type: "text", Text: "complete rebuilt second reasoning"},
			},
		}, {
			Kind:       eventstream.KindSessionUpdate,
			SessionID:  "session-1",
			TurnID:     "task-1:2",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			Cursor:     "boundary-cursor",
			OccurredAt: startedAt.Add(17 * time.Second),
			ParentTool: &eventstream.ParentToolRelation{
				ToolCallID: "spawn-1", ToolName: "StartThread",
			},
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				MessageID:     "final-turn-2",
				Content:       eventstream.TextContent{Type: "text", Text: "second exact Final Message"},
			},
		}, {
			Kind:       eventstream.KindLifecycle,
			SessionID:  "session-1",
			TurnID:     "task-1:2",
			Scope:      eventstream.ScopeSubagent,
			ScopeID:    "task-1",
			Cursor:     "current-state-cursor",
			OccurredAt: startedAt.Add(18 * time.Second),
			ParentTool: &eventstream.ParentToolRelation{
				ToolCallID: "spawn-1", ToolName: "StartThread",
			},
			Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateCompleted},
		}},
	})
	model = next.(*Model)
	if got := model.taskStreamCursors["task-1"]; got != "current-state-cursor" {
		t.Fatalf("Task cursor = %q, want rebuilt current-state boundary", got)
	}
	plain := strings.Join(renderedPlainRows(model.subagentOutputRows(view, 96, 40)), "\n")
	for _, want := range []string{
		"complete rebuilt first reasoning",
		"first exact Final Message",
		"complete rebuilt second reasoning",
		"second exact Final Message",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rebuilt multi-Turn replacement omitted %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "stale partial prefix") {
		t.Fatalf("rebuilt multi-Turn replacement retained stale prefix:\n%s", plain)
	}
	if len(view.turnBlocks) != 2 {
		t.Fatalf("rebuilt internal Turn groups = %d, want two", len(view.turnBlocks))
	}
	for _, duration := range []string{"4.0s", "8.0s"} {
		if !strings.Contains(plain, duration) {
			t.Fatalf("rebuilt multi-Turn replacement omitted footer %q:\n%s", duration, plain)
		}
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
	listCalls               atomic.Int32
	subscribeCalls          atomic.Int32
	subscription            *tuiProtocolTaskSubscription
	directoryMisses         int32
	descriptorHandle        string
	descriptorParticipantID string
	subscribeFailures       int32
}

type tuiProtocolTaskSubscription struct {
	events     chan eventstream.Envelope
	deliveries chan taskstream.Delivery
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func newTUIProtocolTaskSubscription() *tuiProtocolTaskSubscription {
	subscription := &tuiProtocolTaskSubscription{
		events: make(chan eventstream.Envelope), deliveries: make(chan taskstream.Delivery),
	}
	go func() {
		defer close(subscription.deliveries)
		sequence := uint64(0)
		for envelope := range subscription.events {
			sequence++
			envelope = tuiExactEnvelope(envelope, fmt.Sprintf("tui-test-cursor-%d", sequence), sequence)
			subscription.deliveries <- taskstream.Delivery{
				Kind: taskstream.DeliveryAppendPage, Source: taskstream.SourceExact,
				Events: []eventstream.Envelope{envelope}, NextCursor: envelope.Cursor,
			}
		}
	}()
	return subscription
}

func tuiExactEnvelope(envelope eventstream.Envelope, fallbackCursor string, sequence uint64) eventstream.Envelope {
	if envelope.Cursor == "" {
		envelope.Cursor = fallbackCursor
	}
	if envelope.Position == nil {
		envelope.Position = &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Generation: "tui-test", Sequence: sequence,
		}}
	}
	if envelope.Delivery == nil {
		envelope.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
	}
	return envelope
}

func (s *tuiProtocolTaskSubscription) Deliveries() <-chan taskstream.Delivery { return s.deliveries }
func (*tuiProtocolTaskSubscription) Err() error                               { return nil }
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
		State: task.StateRunning, Running: true, ParticipantID: strings.TrimSpace(s.descriptorParticipantID),
		ParentTool: taskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "StartThread"},
	}}}, nil
}

func (*tuiRetryTaskStreamService) Events(context.Context, taskstream.Principal, taskstream.ReadRequest) (taskstream.ReadResult, error) {
	return taskstream.ReadResult{}, nil
}

func (s *tuiRetryTaskStreamService) Subscribe(context.Context, taskstream.Principal, taskstream.SubscribeRequest) (taskstream.SubscribeResult, error) {
	failures := s.subscribeFailures
	if failures <= 0 {
		failures = 1
	}
	if s.subscribeCalls.Add(1) <= failures {
		return taskstream.SubscribeResult{}, errorcode.New(errorcode.Unavailable, "task stream temporarily unavailable")
	}
	return taskstream.SubscribeResult{Subscription: s.subscription}, nil
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

func (s *tuiTestTaskStreamService) Events(context.Context, controltaskstream.Principal, controltaskstream.ReadRequest) (controltaskstream.ReadResult, error) {
	return controltaskstream.ReadResult{}, nil
}

func (s *tuiTestTaskStreamService) Subscribe(_ context.Context, _ controltaskstream.Principal, request controltaskstream.SubscribeRequest) (controltaskstream.SubscribeResult, error) {
	s.requests <- request
	return controltaskstream.SubscribeResult{Subscription: s.subscription}, nil
}

type tuiTestTaskSubscription struct {
	records    chan controltaskstream.Record
	deliveries chan controltaskstream.Delivery
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func newTUITestTaskSubscription() *tuiTestTaskSubscription {
	subscription := &tuiTestTaskSubscription{
		records: make(chan controltaskstream.Record, 8), deliveries: make(chan controltaskstream.Delivery, 8),
	}
	go func() {
		defer close(subscription.deliveries)
		for record := range subscription.records {
			subscription.deliveries <- controltaskstream.Delivery{
				Kind: controltaskstream.DeliveryAppendPage, Source: controltaskstream.SourceExact,
				Records: []controltaskstream.Record{record}, NextCursor: record.Cursor,
			}
		}
	}()
	return subscription
}

func (s *tuiTestTaskSubscription) Deliveries() <-chan controltaskstream.Delivery { return s.deliveries }
func (s *tuiTestTaskSubscription) Err() error                                    { return nil }
func (s *tuiTestTaskSubscription) Close() error {
	s.closeOnce.Do(func() {
		s.closeCalls.Add(1)
		close(s.records)
	})
	return nil
}
