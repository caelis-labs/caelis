package acpagentbridge

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	sdkstream "github.com/caelis-labs/caelis/agent-sdk/task/stream"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

func TestACPTaskStreamMuxProjectsOnlyRunCommandTerminalOutput(t *testing.T) {
	t.Parallel()

	sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 4)}
	service := &acpMuxTestService{
		requests: make(chan taskstream.SubscribeRequest, 1), sub: sub,
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
		}}},
	}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	meta := metautil.WithTerminalInfo(nil, "command-1")
	mux.Observe(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			RawOutput: map[string]any{"handle": "command", "state": "running", "target_kind": "command"}, Meta: meta,
		},
	})
	select {
	case request := <-service.requests:
		if request.SessionID != "session-1" || request.TaskID != "task-1" {
			t.Fatalf("Subscribe request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCommand Task stream was not subscribed")
	}

	terminalMeta := metautil.WithTerminalOutput(nil, "command-1", "line\n")
	sub.events <- eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1", Meta: terminalMeta},
	}
	select {
	case envelope := <-mux.Events():
		output, ok := metautil.TerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "line\n" {
			t.Fatalf("projected terminal output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCommand terminal output was not projected")
	}

	sub.events <- eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
		Update: schema.ToolCallUpdate{SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "nested", Meta: terminalMeta},
	}
	select {
	case envelope := <-mux.Events():
		t.Fatalf("subagent stream leaked into standard ACP: %#v", envelope)
	case <-time.After(30 * time.Millisecond):
	}
	exitCode := 7
	sub.events <- eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			Meta: metautil.WithTerminalExit(nil, "command-1", &exitCode, nil),
		},
	}
	select {
	case envelope := <-mux.Events():
		exit, ok := metautil.TerminalExit(eventstream.UpdateMeta(envelope.Update))
		if !ok || exit.ExitCode == nil || *exit.ExitCode != exitCode {
			t.Fatalf("projected terminal exit = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCommand terminal exit without trailing output was not projected")
	}
	mux.Close()
	if !sub.closed() {
		t.Fatal("mux close did not close delivery subscription")
	}
}

func TestACPTaskStreamMuxSilencesRecoverableSubagentGap(t *testing.T) {
	t.Parallel()

	sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 3)}
	service := &acpMuxTestService{
		requests: make(chan taskstream.SubscribeRequest, 1),
		sub:      sub,
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1",
			TaskID:    "task-1",
			Handle:    "maia",
			Kind:      task.KindSubagent,
			State:     task.StateRunning,
			Running:   true,
			ParentTool: taskstream.ParentTool{
				ToolCallID: "spawn-1",
				ToolName:   "Spawn",
			},
		}}},
	}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	mux.Observe(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "spawn-1",
			RawOutput: map[string]any{
				"handle": "maia", "state": "running", "target_kind": "subagent",
				"parent_call": "spawn-1", "parent_tool": "Spawn",
			},
			Meta: metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
				metautil.RuntimeToolName: "Spawn",
			}),
		},
	})
	select {
	case <-service.requests:
	case <-time.After(time.Second):
		t.Fatal("Spawn Task stream was not subscribed")
	}

	parent := &eventstream.ParentToolRelation{ToolCallID: "spawn-1", ToolName: "Spawn"}
	sub.events <- eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1",
		Scope: eventstream.ScopeSubagent, ScopeID: "task-1", ParentTool: parent,
		Notice: "transient Task output before this boundary is no longer available",
		Meta:   map[string]any{"task_stream": map[string]any{"transient_gap": true}},
	}
	select {
	case envelope := <-mux.Events():
		t.Fatalf("recoverable Task gap reached ACP child terminal: %#v", envelope)
	case <-time.After(30 * time.Millisecond):
	}

	sub.events <- eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1",
		Scope: eventstream.ScopeSubagent, ScopeID: "task-1", ParentTool: parent,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			MessageID:     "child-message",
			Content:       schema.TextContent{Type: "text", Text: "current child output"},
		},
	}
	select {
	case envelope := <-mux.Events():
		if envelope.Kind != eventstream.KindSessionUpdate {
			t.Fatalf("post-gap child envelope = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("post-gap child output was not forwarded")
	}
}

func TestACPTaskStreamMuxForwardsAgentCommunicationWithSenderIdentity(t *testing.T) {
	t.Parallel()

	sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
	service := &acpMuxTestService{
		requests: make(chan taskstream.SubscribeRequest, 1),
		sub:      sub,
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "maia", Kind: task.KindSubagent,
			State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "spawn-1", ToolName: "Spawn"},
		}}},
	}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	mux.Observe(acpMuxSubagentAnchor("maia"))
	select {
	case <-service.requests:
	case <-time.After(time.Second):
		t.Fatal("Spawn Task stream was not subscribed")
	}

	communication := eventstream.Envelope{
		Kind:      eventstream.KindAgentCommunication,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "task-1",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "Spawn",
		},
		AgentCommunication: &eventstream.AgentCommunication{
			Source: eventstream.ActorIdentity{
				Kind: "participant",
				ID:   "agent-maia",
				Role: "delegated",
				Name: "maia",
			},
			Text: "status from maia",
		},
	}
	sub.events <- communication

	select {
	case envelope := <-mux.Events():
		if envelope.AgentCommunication == nil || envelope.AgentCommunication.Text != "status from maia" {
			t.Fatalf("projected Agent communication = %#v", envelope)
		}
		if got := envelope.AgentCommunication.Source.Name; got != "maia" {
			t.Fatalf("projected Agent communication source = %q, want maia", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Agent communication was not forwarded to the subagent overlay")
	}
}

func TestACPTaskStreamMuxDetachedDeliveryOutlivesParentPrompt(t *testing.T) {
	t.Parallel()

	sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 2)}
	service := &acpMuxTestService{
		requests: make(chan taskstream.SubscribeRequest, 1), sub: sub,
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
		}}},
	}
	client, err := taskstream.BindClient(service, taskstream.Principal{ID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	agent := &RuntimeAgent{
		taskStreamClient: client,
		taskMuxes:        map[string]map[*acpTaskStreamMux]struct{}{},
	}
	mux := agent.startACPTaskStreamMux(context.Background(), "session-1")
	meta := acpMuxCommandMeta()
	mux.Observe(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			RawOutput: map[string]any{"handle": "command", "state": "running", "target_kind": "command"}, Meta: meta,
		},
	})
	select {
	case <-service.requests:
	case <-time.After(time.Second):
		t.Fatal("RunCommand Task stream was not subscribed before parent Prompt completion")
	}

	callbacks := &acpMuxPromptCallbacks{updates: make(chan acp.SessionNotification, 2)}
	agent.detachACPTaskStreamMux(context.Background(), mux, callbacks, "session-1", newACPNarrativeFilter(false))
	sub.events <- eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			Meta: metautil.WithTerminalOutput(nil, "command-1", "after parent\n"),
		},
	}
	select {
	case notification := <-callbacks.updates:
		output, ok := metautil.TerminalOutput(eventstream.UpdateMeta(notification.Update))
		if !ok || output.Data != "after parent\n" {
			t.Fatalf("detached Task delivery = %#v", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCommand output stopped when the parent Prompt completed")
	}
	exitCode := 0
	sub.events <- eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			Meta: metautil.WithTerminalExit(nil, "command-1", &exitCode, nil),
		},
	}
	select {
	case notification := <-callbacks.updates:
		exit, ok := metautil.TerminalExit(eventstream.UpdateMeta(notification.Update))
		if !ok || exit.ExitCode == nil || *exit.ExitCode != exitCode {
			t.Fatalf("detached terminal exit = %#v", notification)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCommand terminal exit stopped when the parent Prompt completed")
	}

	agent.closeACPTaskStreamMuxes("session-1")
	if !sub.closed() {
		t.Fatal("Session close did not release detached Task delivery")
	}
}

func TestEmitTaskAwareControlEnvelopeSuppressesChildStreamAndClosesParentOnceFromFallback(t *testing.T) {
	t.Parallel()

	taskEvents := make(chan eventstream.Envelope, 1)
	taskEvents <- eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeSubagent,
		ScopeID:   "task-yara",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "Spawn",
		},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			MessageID:     "child-message-1",
			Content:       schema.TextContent{Type: "text", Text: "retained child output"},
		},
	}
	var taskEventStream <-chan eventstream.Envelope = taskEvents
	boundary := make(chan struct{}, 1)
	mux := &acpTaskStreamMux{
		observations: map[string]*acpTaskStreamObservation{
			"spawn-1": {
				generation: &acpTaskStreamObservationGeneration{boundary: boundary},
			},
		},
	}
	mux.signalBoundary("spawn-1")

	completed := schema.ToolStatusCompleted
	waitEnvelope := eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Final:     true,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "wait-1",
			Status:        &completed,
			RawInput:      map[string]any{"action": "wait", "handle": "yara"},
			RawOutput: map[string]any{
				"action": "wait",
				"tasks": []any{map[string]any{
					"final_message": "fallback final",
					"handle":        "yara",
					"parent_call":   "spawn-1",
					"parent_tool":   "Spawn",
					"state":         "completed",
					"target_kind":   "subagent",
				}},
			},
		},
	}
	callbacks := &acpMuxPromptCallbacks{updates: make(chan acp.SessionNotification, 8)}
	agent := &RuntimeAgent{}
	filter := newACPNarrativeFilter(false)
	for range 2 {
		if err := agent.emitTaskAwareControlEnvelope(
			context.Background(),
			callbacks,
			"session-1",
			nil,
			mux,
			&taskEventStream,
			waitEnvelope,
			filter,
		); err != nil {
			t.Fatalf("emitTaskAwareControlEnvelope() error = %v", err)
		}
	}

	notifications := make([]acp.SessionNotification, 0, len(callbacks.updates))
	for len(callbacks.updates) > 0 {
		notifications = append(notifications, <-callbacks.updates)
	}
	parentCloses := 0
	for _, notification := range notifications {
		if chunk, ok := notification.Update.(schema.ContentChunk); ok {
			_, _, text, _ := acpContentChunkText(chunk)
			if strings.Contains(text, "retained child output") {
				t.Fatalf("child stream leaked as ACP narrative: %#v", notification)
			}
		}
		update, ok := notification.Update.(schema.ToolCallUpdate)
		if !ok || update.ToolCallID != "spawn-1" || update.Status == nil || *update.Status != schema.ToolStatusCompleted {
			continue
		}
		parentCloses++
		assertACPChildFinalResult(t, notification, schema.ToolStatusCompleted, "fallback final")
	}
	if parentCloses != 1 {
		t.Fatalf("Spawn parent closes = %d, want exactly one standard result after repeated Task wait", parentCloses)
	}
}

func TestACPTaskStreamMuxProjectsControlTaskRecordThroughACPAdapter(t *testing.T) {
	t.Parallel()

	sub := &acpMuxControlSubscription{records: make(chan controltaskstream.Record, 2)}
	controlService := &acpMuxControlService{
		requests: make(chan controltaskstream.SubscribeRequest, 1), sub: sub,
		list: controltaskstream.ListResult{Tasks: []controltaskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: controltaskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
		}}},
	}
	mux := newACPTaskStreamMux(context.Background(), taskstream.New(controlService), taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	meta := acpMuxCommandMeta()
	mux.Observe(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			RawOutput: map[string]any{"handle": "command", "state": "running", "target_kind": "command"}, Meta: meta,
		},
	})
	select {
	case request := <-controlService.requests:
		if request.SessionID != "session-1" || request.TaskID != "task-1" {
			t.Fatalf("Control Subscribe request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCommand did not reach the Control Task stream")
	}
	sub.records <- controltaskstream.Record{
		Cursor: "cursor-1", Generation: "generation-1", Sequence: 1,
		Task: controltaskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: controltaskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
		},
		Frame: &sdkstream.Frame{
			Ref:  sdkstream.Ref{SessionID: "session-1", TaskID: "task-1", TerminalID: "terminal-1"},
			Text: "from control\n", Running: true, Cursor: sdkstream.Cursor{Events: 1, Output: 13},
		},
	}

	select {
	case envelope := <-mux.Events():
		output, ok := metautil.TerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "from control\n" || envelope.Cursor != "cursor-1" {
			t.Fatalf("Control→ACP→mux terminal output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("Control Task record did not reach the ACP terminal mux")
	}
}

func TestACPTaskStreamMuxMakesSubscribeFailureVisible(t *testing.T) {
	t.Parallel()

	service := &acpMuxTestService{
		err: errors.New("stream backend unavailable"),
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
		}}},
	}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	meta := acpMuxCommandMeta()
	mux.Observe(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			RawOutput: map[string]any{"handle": "command", "state": "running", "target_kind": "command"}, Meta: meta,
		},
	})

	select {
	case envelope := <-mux.Events():
		if envelope.Kind != eventstream.KindNotice || envelope.Delivery == nil || envelope.Delivery.Mode != eventstream.DeliveryTransient ||
			strings.Contains(envelope.Notice, "stream backend unavailable") ||
			!strings.Contains(envelope.Notice, "Task command") ||
			!strings.Contains(envelope.Notice, "final Task result remains available") {
			t.Fatalf("subscribe failure envelope = %#v, want sanitized transient notice", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("Task stream subscribe failure was silent")
	}
}

func TestACPTaskStreamMuxRetriesAnchorAfterEarlyDirectoryMiss(t *testing.T) {
	t.Parallel()

	sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
	service := &acpMuxRetryService{
		requests: make(chan taskstream.SubscribeRequest, 1),
		sub:      sub,
		descriptor: taskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
		},
	}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	meta := acpMuxCommandMeta()
	anchor := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			RawOutput: map[string]any{"handle": "command", "state": "running", "target_kind": "command"}, Meta: meta,
		},
	}

	mux.Observe(anchor)
	select {
	case request := <-service.requests:
		if request.TaskID != "task-1" {
			t.Fatalf("retry Subscribe request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("early directory miss was not recovered within the grace window")
	}
	select {
	case envelope := <-mux.Events():
		t.Fatalf("recovered directory miss emitted a notice: %#v", envelope)
	case <-time.After(2 * acpTaskStreamResolveRetryDelay):
	}
	service.mu.Lock()
	listCalls := service.listCalls
	service.mu.Unlock()
	if listCalls != 2 {
		t.Fatalf("List calls = %d, want one miss followed by one successful retry", listCalls)
	}
}

func TestACPTaskStreamMuxExhaustsRetryWithOneSanitizedNotice(t *testing.T) {
	t.Parallel()

	const internalTaskID = "95537455f400"
	service := &acpMuxTestService{
		err: errorcode.New(errorcode.NotFound, `agent-sdk/runtime: task "`+internalTaskID+`" not found`),
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: internalTaskID, Handle: "command-43", Kind: task.KindCommand,
			State: task.StateCompleted, Running: false,
			ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
		}}},
	}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	meta := acpMuxCommandMeta()
	anchor := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
			RawOutput: map[string]any{"handle": "command-43", "state": "running", "target_kind": "command"}, Meta: meta,
		},
	}

	mux.Observe(anchor)
	select {
	case envelope := <-mux.Events():
		if envelope.Kind != eventstream.KindNotice ||
			strings.Contains(envelope.Notice, internalTaskID) ||
			strings.Contains(envelope.Notice, "not found") ||
			!strings.Contains(envelope.Notice, "recovery window") ||
			!strings.Contains(envelope.Notice, "final Task result remains available") {
			t.Fatalf("exhausted retry notice = %#v, want one sanitized availability notice", envelope)
		}
		meta, _ := envelope.Meta["task_stream"].(map[string]any)
		if meta["error_code"] != string(errorcode.NotFound) || meta["retry_exhausted"] != true {
			t.Fatalf("exhausted retry metadata = %#v", meta)
		}
	case <-time.After(time.Second):
		t.Fatal("exhausted Task stream retry emitted no notice")
	}
	if calls := service.subscribeCallCount(); calls != acpTaskStreamResolveMaxAttempts {
		t.Fatalf("Subscribe calls = %d, want bounded attempts %d", calls, acpTaskStreamResolveMaxAttempts)
	}

	mux.Observe(anchor)
	deadline := time.Now().Add(time.Second)
	for service.subscribeCallCount() != 2*acpTaskStreamResolveMaxAttempts && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := service.subscribeCallCount(); calls != 2*acpTaskStreamResolveMaxAttempts {
		t.Fatalf("Subscribe calls after repeated anchor = %d, want a fresh bounded resolve", calls)
	}
	select {
	case envelope := <-mux.Events():
		t.Fatalf("repeated retryable miss emitted a duplicate notice: %#v", envelope)
	case <-time.After(2 * acpTaskStreamResolveRetryDelay):
	}
}

func TestACPTaskStreamMuxLaterAnchorAttachesAfterRecoveryWindow(t *testing.T) {
	t.Parallel()

	service := &acpMuxTestService{
		err: errorcode.New(errorcode.NotFound, "Task registration is still pending"),
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
		}}},
	}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()
	anchor := acpMuxCommandAnchor("command")

	mux.Observe(anchor)
	select {
	case <-mux.Events():
	case <-time.After(time.Second):
		t.Fatal("initial bounded attach miss emitted no availability notice")
	}

	time.Sleep(100 * time.Millisecond)
	sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
	service.setSubscriptionResult(nil, sub)
	mux.Observe(anchor)
	deadline := time.Now().Add(time.Second)
	for service.subscribeCallCount() != acpTaskStreamResolveMaxAttempts+1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := service.subscribeCallCount(); calls != acpTaskStreamResolveMaxAttempts+1 {
		t.Fatalf("Subscribe calls = %d, want later anchor to attach after the expired recovery window", calls)
	}
	select {
	case <-mux.parentBoundary("command-1"):
		t.Fatal("later successful attach inherited the prior miss boundary")
	default:
	}

	sub.events <- acpMuxCommandOutputEnvelope("cursor-late", "late attach\n")
	select {
	case envelope := <-mux.Events():
		output, ok := metautil.TerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "late attach\n" {
			t.Fatalf("later anchor output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("later anchor attached no live Task output")
	}
	sub.finish(nil, "cursor-late")
	select {
	case <-mux.parentBoundary("command-1"):
	case <-time.After(time.Second):
		t.Fatal("completed later attachment did not signal its own boundary")
	}
}

func TestACPTaskStreamMuxRetryGenerationCannotSignalLaterAttachmentBoundary(t *testing.T) {
	t.Parallel()

	service := &acpMuxTestService{
		err: errorcode.New(errorcode.NotFound, "Task registration is still pending"),
		list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
			SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
		}}},
	}
	mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
	defer mux.Close()

	oldSignalReady := make(chan chan struct{}, 1)
	releaseOldSignal := make(chan struct{})
	oldSignalReleased := false
	defer func() {
		if !oldSignalReleased {
			close(releaseOldSignal)
		}
	}()
	var signalCount atomic.Int32
	mux.beforeBoundarySignal = func(_ string, boundary chan struct{}) {
		if signalCount.Add(1) != 1 {
			return
		}
		oldSignalReady <- boundary
		<-releaseOldSignal
	}

	anchor := acpMuxCommandAnchor("command")
	mux.Observe(anchor)
	var oldBoundary chan struct{}
	select {
	case oldBoundary = <-oldSignalReady:
	case <-time.After(time.Second):
		t.Fatal("retryable miss did not reach deferred generation cleanup")
	}
	select {
	case <-mux.Events():
	case <-time.After(time.Second):
		t.Fatal("retryable miss emitted no availability notice")
	}

	sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 1)}
	service.setSubscriptionResult(nil, sub)
	mux.Observe(anchor)
	newBoundary := mux.parentBoundary("command-1")
	if newBoundary == nil || oldBoundary == newBoundary {
		t.Fatal("later attachment did not receive a distinct observation boundary")
	}
	deadline := time.Now().Add(time.Second)
	for {
		mux.mu.Lock()
		phase := mux.observations["command-1"].phase
		mux.mu.Unlock()
		if phase == acpTaskStreamObservationAttached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("later anchor phase = %v, want attached", phase)
		}
		time.Sleep(time.Millisecond)
	}

	close(releaseOldSignal)
	oldSignalReleased = true
	select {
	case <-oldBoundary:
	case <-time.After(time.Second):
		t.Fatal("older retry generation did not finish its own boundary")
	}
	select {
	case <-newBoundary:
		t.Fatal("older retry generation released the later attachment boundary")
	default:
	}

	sub.events <- acpMuxCommandOutputEnvelope("cursor-later", "later generation\n")
	select {
	case envelope := <-mux.Events():
		output, ok := metautil.TerminalOutput(eventstream.UpdateMeta(envelope.Update))
		if !ok || output.Data != "later generation\n" {
			t.Fatalf("later generation output = %#v", envelope)
		}
	case <-time.After(time.Second):
		t.Fatal("later generation did not forward Task output")
	}
	sub.finish(nil, "cursor-later")
	select {
	case <-newBoundary:
	case <-time.After(time.Second):
		t.Fatal("later attachment did not release its own boundary")
	}
}

func TestACPTaskStreamMuxDoesNotRetryAfterParentTerminalOrSeal(t *testing.T) {
	t.Parallel()

	newUnavailableService := func() *acpMuxTestService {
		return &acpMuxTestService{
			err: errorcode.New(errorcode.NotFound, "Task registration is still pending"),
			list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
				SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
				State: task.StateRunning, Running: true,
				ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
			}}},
		}
	}
	t.Run("parent terminal", func(t *testing.T) {
		service := newUnavailableService()
		mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
		defer mux.Close()
		anchor := acpMuxCommandAnchor("command")
		mux.Observe(anchor)
		select {
		case <-mux.Events():
		case <-time.After(time.Second):
			t.Fatal("initial bounded attach miss emitted no availability notice")
		}
		calls := service.subscribeCallCount()
		completed := schema.ToolStatusCompleted
		terminal := anchor
		update := terminal.Update.(schema.ToolCallUpdate)
		update.Status = &completed
		update.RawOutput = map[string]any{"handle": "command", "state": "completed", "target_kind": "command"}
		terminal.Update = update
		mux.Observe(terminal)
		mux.Observe(anchor)
		time.Sleep(2 * acpTaskStreamResolveRetryDelay)
		if got := service.subscribeCallCount(); got != calls {
			t.Fatalf("Subscribe calls after parent terminal = %d, want %d", got, calls)
		}
	})
	t.Run("sealed", func(t *testing.T) {
		service := newUnavailableService()
		mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
		defer mux.Close()
		anchor := acpMuxCommandAnchor("command")
		mux.Observe(anchor)
		select {
		case <-mux.Events():
		case <-time.After(time.Second):
			t.Fatal("initial bounded attach miss emitted no availability notice")
		}
		calls := service.subscribeCallCount()
		mux.Seal()
		mux.Observe(anchor)
		time.Sleep(2 * acpTaskStreamResolveRetryDelay)
		if got := service.subscribeCallCount(); got != calls {
			t.Fatalf("Subscribe calls after Seal = %d, want %d", got, calls)
		}
	})
}

func TestACPTaskStreamAnchorUsesTypedToolStatusForParentTerminal(t *testing.T) {
	t.Parallel()

	rawTerminal := acpMuxCommandAnchor("command")
	rawUpdate := rawTerminal.Update.(schema.ToolCallUpdate)
	rawUpdate.RawOutput = map[string]any{"handle": "command", "state": "completed", "target_kind": "command"}
	rawTerminal.Update = rawUpdate
	anchor, ok := acpTaskStreamAnchorFromEnvelope(rawTerminal)
	if !ok {
		meta := eventstream.UpdateMeta(rawTerminal.Update)
		info, hasInfo := metautil.TerminalInfo(meta)
		update := rawTerminal.Update.(schema.ToolCallUpdate)
		output, _ := update.RawOutput.(map[string]any)
		t.Fatalf("raw-output anchor was not recognized: target_kind=%q terminal=%#v/%t meta=%#v output=%#v",
			display.ToolTaskTargetKind(nil, output, meta), info, hasInfo, meta, output)
	}
	if anchor.parentTerminal {
		t.Fatal("free-form output state permanently closed Task stream discovery")
	}

	typedTerminal := rawTerminal
	completed := schema.ToolStatusCompleted
	typedUpdate := typedTerminal.Update.(schema.ToolCallUpdate)
	typedUpdate.Status = &completed
	typedTerminal.Update = typedUpdate
	anchor, ok = acpTaskStreamAnchorFromEnvelope(typedTerminal)
	if !ok {
		t.Fatal("typed terminal anchor was not recognized")
	}
	if !anchor.parentTerminal {
		t.Fatal("typed ACP tool status did not close Task stream discovery")
	}
}

func TestACPTaskStreamAnchorDoesNotTrustRuntimeToolName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		toolName string
		callID   string
		output   map[string]any
	}{
		{name: "command", toolName: "RunCommand", callID: "command-spoof", output: map[string]any{"handle": "command"}},
		{name: "subagent", toolName: "Spawn", callID: "spawn-spoof", output: map[string]any{"handle": "helper"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			meta := metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
				metautil.RuntimeToolName: tc.toolName,
			})
			envelope := eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
				Update: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: tc.callID,
					RawOutput: tc.output, Meta: meta,
				},
			}
			if anchor, ok := acpTaskStreamAnchorFromEnvelope(envelope); ok {
				t.Fatalf("runtime tool-name spoof produced Task anchor %#v", anchor)
			}
		})
	}
}

func TestACPTaskStreamMuxStopsInFlightAttachRetryAtObservationBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		stop func(*acpTaskStreamMux, eventstream.Envelope)
	}{
		{
			name: "parent terminal",
			stop: func(mux *acpTaskStreamMux, anchor eventstream.Envelope) {
				completed := schema.ToolStatusCompleted
				update := anchor.Update.(schema.ToolCallUpdate)
				update.Status = &completed
				update.RawOutput = map[string]any{"handle": "command", "state": "completed", "target_kind": "command"}
				anchor.Update = update
				mux.Observe(anchor)
			},
		},
		{
			name: "sealed",
			stop: func(mux *acpTaskStreamMux, _ eventstream.Envelope) {
				mux.Seal()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{}, acpTaskStreamResolveMaxAttempts)
			gate := make(chan struct{})
			service := &acpMuxTestService{
				err:            errorcode.New(errorcode.NotFound, "Task registration is still pending"),
				subscribeStart: started,
				subscribeGate:  gate,
				list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{{
					SessionID: "session-1", TaskID: "task-1", Handle: "command", Kind: task.KindCommand,
					State: task.StateRunning, Running: true,
					ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
				}}},
			}
			mux := newACPTaskStreamMux(context.Background(), service, taskstream.Principal{ID: "user-1"}, "session-1")
			defer mux.Close()
			anchor := acpMuxCommandAnchor("command")
			mux.Observe(anchor)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("initial attach attempt did not start")
			}
			test.stop(mux, anchor)
			close(gate)
			time.Sleep(2 * acpTaskStreamResolveRetryDelay)
			if calls := service.subscribeCallCount(); calls != 1 {
				t.Fatalf("Subscribe calls after observation boundary = %d, want no retry", calls)
			}
		})
	}
}

func TestACPTaskStreamMuxKeepsHardResolutionReasonsExplicit(t *testing.T) {
	t.Parallel()

	descriptor := func(taskID string) taskstream.TaskDescriptor {
		return taskstream.TaskDescriptor{
			SessionID: "session-1", TaskID: taskID, Handle: "command", Kind: task.KindCommand,
			State: task.StateRunning, Running: true,
			ParentTool: taskstream.ParentTool{ToolCallID: "command-1", ToolName: "RunCommand"},
		}
	}
	tests := []struct {
		name    string
		service *acpMuxTestService
		reason  string
	}{
		{
			name: "permission denied",
			service: &acpMuxTestService{
				listErr: errorcode.New(errorcode.PermissionDenied, "opaque authorization detail"),
			},
			reason: "access to the Task live output was denied",
		},
		{
			name: "ambiguous directory",
			service: &acpMuxTestService{
				list: taskstream.ListResult{Tasks: []taskstream.TaskDescriptor{
					descriptor("task-1"),
					descriptor("task-2"),
				}},
			},
			reason: "Task stream identity was ambiguous or conflicted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mux := newACPTaskStreamMux(context.Background(), test.service, taskstream.Principal{ID: "user-1"}, "session-1")
			defer mux.Close()
			mux.Observe(eventstream.Envelope{
				Kind: eventstream.KindSessionUpdate, SessionID: "session-1", Scope: eventstream.ScopeMain,
				Update: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "command-1",
					RawOutput: map[string]any{"handle": "command", "state": "running", "target_kind": "command"},
					Meta:      acpMuxCommandMeta(),
				},
			})

			select {
			case envelope := <-mux.Events():
				if !strings.Contains(envelope.Notice, test.reason) ||
					strings.Contains(envelope.Notice, "opaque authorization detail") {
					t.Fatalf("hard resolution notice = %#v, want explicit sanitized reason", envelope)
				}
			case <-time.After(time.Second):
				t.Fatal("hard Task stream failure emitted no notice")
			}
			if calls := test.service.subscribeCallCount(); calls != 0 {
				t.Fatalf("Subscribe calls = %d, want no retry past hard resolution failure", calls)
			}
		})
	}
}
