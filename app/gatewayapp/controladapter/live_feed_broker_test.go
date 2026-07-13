package controladapter

import (
	"context"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	internalcontrolclient "github.com/caelis-labs/caelis/internal/controlclient"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestLiveFeedBrokerFansInSpawnSemanticsInOrder(t *testing.T) {
	source := make(chan eventstream.Envelope, 4)
	handle := newBrokerTestHandle(source)
	streams := newBrokerTestStreamService()
	turn := newGatewayTurn(handle, func() stream.Service { return streams })
	events := turn.Events()

	running := brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
	source <- running
	assertBrokerToolCallID(t, receiveBrokerEnvelope(t, events), "spawn-call-1")
	waitBrokerSignal(t, streams.started, "task stream subscription start")

	streams.frames <- stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Running: true,
		Event:   brokerChildMessageEvent("child-message-1", "child message"),
	}
	message := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, message, "spawn-call-1")
	if update, ok := message.Update.(schema.ContentChunk); !ok || update.SessionUpdate != schema.UpdateAgentMessage {
		t.Fatalf("child message = %#v, want agent message chunk", message)
	}

	streams.frames <- stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Running: true,
		Event:   brokerChildThoughtEvent("child-thought-1", "child thought"),
	}
	thought := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, thought, "spawn-call-1")
	if update, ok := thought.Update.(schema.ContentChunk); !ok || update.SessionUpdate != schema.UpdateAgentThought {
		t.Fatalf("child thought = %#v, want agent thought chunk", thought)
	}

	streams.frames <- stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Running: true,
		Event:   brokerChildToolCallEvent(),
	}
	childToolCall := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, childToolCall, "spawn-call-1")
	if update, ok := childToolCall.Update.(schema.ToolCall); !ok || update.ToolCallID != "child-tool-1" {
		t.Fatalf("child tool call = %#v, want distinct child tool call", childToolCall)
	}

	streams.frames <- stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Running: true,
		Event:   brokerChildToolEvent(),
	}
	childTool := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, childTool, "spawn-call-1")
	toolUpdate, ok := childTool.Update.(schema.ToolCallUpdate)
	if !ok {
		t.Fatalf("child tool = %#v, want tool_call_update", childTool)
	}
	if toolUpdate.ToolCallID != "child-tool-1" || toolUpdate.ToolCallID == "spawn-call-1" {
		t.Fatalf("child tool id = %q, want distinct child id related to parent spawn", toolUpdate.ToolCallID)
	}
	if len(toolUpdate.Content) != 1 || toolUpdate.Content[0].Type != "diff" || len(toolUpdate.Locations) != 1 {
		t.Fatalf("child tool = %#v, want diff and location", toolUpdate)
	}

	streams.frames <- stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Text:    "materialized child text\n",
		Cursor:  stream.Cursor{Output: 29, Events: 4},
		Running: true,
		Event:   brokerChildPlanEvent(),
	}
	plan := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, plan, "spawn-call-1")
	if _, ok := plan.Update.(schema.PlanUpdate); !ok {
		t.Fatalf("child plan = %#v, want plan update", plan)
	}

	streams.frames <- stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Running: true,
		Event:   brokerChildMessageEvent("child-message-final", "child final result"),
	}
	childFinal := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, childFinal, "spawn-call-1")
	if update, ok := childFinal.Update.(schema.ContentChunk); !ok || update.MessageID != "child-message-final" {
		t.Fatalf("child final = %#v, want final child message", childFinal)
	}

	streams.frames <- stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Running: true,
		Event:   brokerChildLifecycleEvent(),
	}
	childTerminal := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, childTerminal, "spawn-call-1")
	if !eventstream.IsTerminalLifecycle(childTerminal) || childTerminal.Scope != eventstream.ScopeSubagent {
		t.Fatalf("scoped lifecycle = %#v, want subagent terminal", childTerminal)
	}
	if childTerminal.TurnID != "child-turn-1" {
		t.Fatalf("scoped lifecycle turn id = %q, want child execution id", childTerminal.TurnID)
	}

	streams.frames <- stream.Frame{
		Ref:    brokerStreamRef("task-1", "spawn-terminal-1"),
		Text:   "child final result",
		Closed: true,
		State:  "completed",
	}
	parentFinal := receiveBrokerEnvelope(t, events)
	assertBrokerToolCallID(t, parentFinal, "spawn-call-1")
	if output, ok := brokerTerminalOutput(parentFinal); ok || output != "" {
		t.Fatalf("delegated parent final output = %q, %v; want no terminal replay", output, ok)
	}

	afterChild := brokerMainMessageEnvelope("main continues after child terminal")
	source <- eventstream.TurnCompleted("other-handle", "other-run", "other-turn", time.Unix(10, 0))
	source <- afterChild
	if got := receiveBrokerEnvelope(t, events); got.Update == nil || got.Scope != eventstream.ScopeMain {
		t.Fatalf("main event after scoped terminal = %#v, want main feed event", got)
	}

	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(11, 0))
	close(source)
	terminal := receiveBrokerEnvelope(t, events)
	if !eventstream.IsTerminalLifecycle(terminal) || terminal.Scope != eventstream.ScopeMain || terminal.Lifecycle.State != eventstream.LifecycleStateCompleted {
		t.Fatalf("main terminal = %#v, want one completed main terminal", terminal)
	}
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
}

func TestLiveFeedBrokerEventOnlyPlanHasTransientChildDelivery(t *testing.T) {
	source := make(chan eventstream.Envelope, 2)
	handle := newBrokerTestHandle(source)
	streams := newBrokerTestStreamService()
	turn := newGatewayTurn(handle, func() stream.Service { return streams })
	events := turn.Events()

	source <- brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
	receiveBrokerEnvelope(t, events)
	waitBrokerSignal(t, streams.started, "task stream subscription start")
	streams.frames <- stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Running: true,
		Event:   brokerChildPlanEvent(),
	}
	plan := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, plan, "spawn-call-1")
	if plan.Delivery == nil || plan.Delivery.Mode != eventstream.DeliveryTransient {
		t.Fatalf("event-only plan delivery = %#v, want transient child delivery", plan.Delivery)
	}

	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(12, 0))
	close(source)
	receiveBrokerEnvelope(t, events)
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
}

func TestLiveFeedBrokerPersistsChildMirrorBeforePublishing(t *testing.T) {
	ctx := context.Background()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string { return "session-1" }}))
	parent, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	source := make(chan eventstream.Envelope, 2)
	streams := newBrokerTestStreamService()
	turn := newGatewayTurn(
		newBrokerTestHandle(source),
		func() stream.Service { return streams },
		internalcontrolclient.NewChildRecorder(sessions),
	)
	events := turn.Events()

	source <- brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
	receiveBrokerEnvelope(t, events)
	waitBrokerSignal(t, streams.started, "task stream subscription start")
	streams.frames <- stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Cursor:  stream.Cursor{Events: 1},
		Running: true,
		Event:   brokerChildMessageEvent("child-source-1", "durable child message"),
	}
	child := receiveBrokerEnvelope(t, events)
	if child.Delivery == nil || child.Delivery.Mode != eventstream.DeliveryMirror || child.EventID == "" {
		t.Fatalf("published child = %#v, want stored mirror identity", child)
	}
	if child.ParentTool == nil || child.ParentTool.ToolCallID != "spawn-call-1" || child.ScopeID != "task-1" {
		t.Fatalf("published child relation = %#v", child)
	}
	page, err := sessions.EventsPage(ctx, session.EventPageRequest{SessionRef: parent.SessionRef, Visibility: session.EventPageClientReplay})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].ID != child.EventID || page.Events[0].ChildOrigin == nil || page.Events[0].ChildOrigin.SourceEventID == "" {
		t.Fatalf("durable child page = %#v", page)
	}

	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(12, 0))
	close(source)
	assertBrokerMainTerminal(t, receiveBrokerEnvelope(t, events))
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
}

func TestLiveFeedBrokerForwardsControlPublishedChildPermission(t *testing.T) {
	source := make(chan eventstream.Envelope, 2)
	handle := newBrokerTestHandle(source)
	streams := newBrokerTestStreamService()
	turn := newGatewayTurn(handle, func() stream.Service { return streams })
	events := turn.Events()

	source <- brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
	assertBrokerToolCallID(t, receiveBrokerEnvelope(t, events), "spawn-call-1")
	waitBrokerSignal(t, streams.started, "task stream subscription start")

	source <- brokerControlChildPermissionEnvelope()
	permission := receiveBrokerEnvelope(t, events)
	if permission.Kind != eventstream.KindRequestPermission || permission.ApprovalRequestID != "approval-child-1" {
		t.Fatalf("child permission = %#v, want Control-published request_permission", permission)
	}
	assertBrokerChildRelation(t, permission, "spawn-call-1")
	if permission.Permission == nil || permission.Permission.ToolCall.ToolCallID != "shared-child-call" || len(permission.Permission.Options) != 1 || permission.Permission.Options[0].OptionID != "allow_once" {
		t.Fatalf("child permission ACP payload = %#v, want original tool call and options", permission.Permission)
	}
	raw, ok := permission.Permission.ToolCall.RawInput.(map[string]any)
	if !ok || raw["path"] != "child.txt" || len(permission.Permission.ToolCall.Content) != 1 {
		t.Fatalf("child permission ACP details = %#v, want raw input and content", permission.Permission.ToolCall)
	}

	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(12, 0))
	close(source)
	assertBrokerMainTerminal(t, receiveBrokerEnvelope(t, events))
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
}

func TestLiveFeedBrokerAcceptsMainTerminalWithMissingTransportIDs(t *testing.T) {
	source := make(chan eventstream.Envelope, 1)
	turn := newGatewayTurn(newBrokerTestHandle(source), nil)
	events := turn.Events()

	terminal := eventstream.TurnCompleted("", "", "", time.Unix(12, 0))
	terminal.Scope = eventstream.ScopeMain
	source <- terminal
	close(source)

	got := receiveBrokerEnvelope(t, events)
	if !eventstream.IsTerminalLifecycle(got) || got.Lifecycle.State != eventstream.LifecycleStateCompleted {
		t.Fatalf("terminal = %#v, want completed main lifecycle", got)
	}
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
}

func TestLiveFeedBrokerDropsKnownForeignMainEvents(t *testing.T) {
	source := make(chan eventstream.Envelope, 3)
	turn := newGatewayTurn(newBrokerTestHandle(source), nil)
	events := turn.Events()

	source <- eventstream.Envelope{
		Kind:     eventstream.KindError,
		HandleID: "other-handle",
		RunID:    "other-run",
		TurnID:   "other-turn",
		Scope:    eventstream.ScopeMain,
		Error:    "foreign failure",
	}
	source <- brokerMainMessageEnvelope("main event")
	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(13, 0))
	close(source)

	main := receiveBrokerEnvelope(t, events)
	update, ok := main.Update.(schema.ContentChunk)
	content, contentOK := update.Content.(schema.TextContent)
	if !ok || !contentOK || content.Text != "main event" {
		t.Fatalf("main event = %#v, want matching main update", main)
	}
	terminal := receiveBrokerEnvelope(t, events)
	if !eventstream.IsTerminalLifecycle(terminal) || terminal.Lifecycle.State != eventstream.LifecycleStateCompleted {
		t.Fatalf("terminal = %#v, want completed lifecycle after foreign error was dropped", terminal)
	}
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
}

func TestLiveFeedBrokerDeduplicatesRunningTaskSnapshots(t *testing.T) {
	source := make(chan eventstream.Envelope, 3)
	handle := newBrokerTestHandle(source)
	streams := newBrokerTestStreamService()
	turn := newGatewayTurn(handle, func() stream.Service { return streams })
	events := turn.Events()

	running := brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
	source <- running
	receiveBrokerEnvelope(t, events)
	waitBrokerSignal(t, streams.started, "first task stream subscription start")
	source <- running
	receiveBrokerEnvelope(t, events)
	source <- brokerMainMessageEnvelope("dedupe barrier")
	if got := receiveBrokerEnvelope(t, events); got.Update == nil {
		t.Fatalf("dedupe barrier = %#v, want main update", got)
	}
	if sources := brokerSourceCount(turn.feed); sources != 1 {
		t.Fatalf("task stream sources = %d, want one for repeated StreamRequest.Key", sources)
	}

	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(13, 0))
	close(source)
	receiveBrokerEnvelope(t, events)
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
}

func TestLiveFeedBrokerFinalReadFlushesMaterializedSourceBeforeMainTerminal(t *testing.T) {
	source := make(chan eventstream.Envelope, 2)
	handle := newBrokerTestHandle(source)
	streams := newBrokerMaterializedStreamService(brokerStreamRef("task-1", "spawn-terminal-1"))
	turn := newGatewayTurn(handle, func() stream.Service { return streams })
	events := turn.Events()

	source <- brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
	assertBrokerToolCallID(t, receiveBrokerEnvelope(t, events), "spawn-call-1")
	initial := waitBrokerReadRequest(t, streams.reads, "initial source Read")
	if initial.Cursor != (stream.Cursor{}) {
		t.Fatalf("initial source Read cursor = %+v, want zero", initial.Cursor)
	}
	streams.releaseRead()
	waitBrokerSignal(t, streams.completed, "initial empty snapshot capture")

	// The source retains this child frame before the main terminal. The broker
	// must issue the final Read instead of waiting for the next 100ms poll.
	streams.materialize(stream.Frame{
		Ref:     streams.ref,
		Running: true,
		Event:   brokerChildMessageEvent("child-message-final", "materialized child message"),
	})
	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(13, 0))
	close(source)
	finalRead := waitBrokerReadRequest(t, streams.reads, "main-terminal final Read")
	if finalRead.Cursor != (stream.Cursor{}) {
		t.Fatalf("final source Read cursor = %+v, want initial cursor before materialized frame", finalRead.Cursor)
	}
	streams.releaseRead()

	child := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, child, "spawn-call-1")
	terminal := receiveBrokerEnvelope(t, events)
	assertBrokerMainTerminal(t, terminal)
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
	assertBrokerMaterializedServiceStopped(t, streams)
	if calls := handle.cancelCalls.Load(); calls != 0 {
		t.Fatalf("main terminal cancelled task runtime %d times, want zero", calls)
	}
}

func TestLiveFeedBrokerFinalReadDeliversWholeSnapshotInOrder(t *testing.T) {
	source := make(chan eventstream.Envelope, 2)
	streams := newBrokerReadStreamService()
	turn := newGatewayTurn(newBrokerTestHandle(source), func() stream.Service { return streams })
	events := turn.Events()

	source <- brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
	receiveBrokerEnvelope(t, events)
	initial := waitBrokerRead(t, streams.reads, "initial source Read")
	respondBrokerRead(t, initial, stream.Snapshot{Ref: initial.Request.Ref, Cursor: initial.Request.Cursor, Running: true})

	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(14, 0))
	close(source)
	finalRead := waitBrokerRead(t, streams.reads, "whole-snapshot final Read")
	respondBrokerRead(t, finalRead, stream.Snapshot{
		Ref:     finalRead.Request.Ref,
		Cursor:  stream.Cursor{Output: 29, Events: 3},
		Running: true,
		Frames: []stream.Frame{
			{
				Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
				Cursor:  stream.Cursor{Events: 1},
				Running: true,
				Event:   brokerChildMessageEvent("child-message-1", "child message"),
			},
			{
				Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
				Cursor:  stream.Cursor{Events: 2},
				Running: true,
				Event:   brokerChildToolEvent(),
			},
			{
				Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
				Text:    "materialized child text\n",
				Cursor:  stream.Cursor{Output: 29, Events: 3},
				Running: true,
				Event:   brokerChildPlanEvent(),
			},
		},
	})

	message := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, message, "spawn-call-1")
	tool := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, tool, "spawn-call-1")
	if update, ok := tool.Update.(schema.ToolCallUpdate); !ok || update.ToolCallID != "child-tool-1" {
		t.Fatalf("child tool = %#v, want one child tool update", tool)
	}
	plan := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, plan, "spawn-call-1")
	if _, ok := plan.Update.(schema.PlanUpdate); !ok {
		t.Fatalf("child plan = %#v, want one plan from the final snapshot", plan)
	}
	terminal := receiveBrokerEnvelope(t, events)
	assertBrokerMainTerminal(t, terminal)
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
	assertBrokerReadServiceStopped(t, streams)
}

func TestLiveFeedBrokerFinalReadAdvancesCursorAfterWholeAcceptedSnapshot(t *testing.T) {
	source := make(chan eventstream.Envelope, 2)
	streams := newBrokerMaterializedStreamService(brokerStreamRef("task-1", "spawn-terminal-1"))
	turn := newGatewayTurn(newBrokerTestHandle(source), func() stream.Service { return streams })
	events := turn.Events()

	source <- brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
	receiveBrokerEnvelope(t, events)
	initial := waitBrokerReadRequest(t, streams.reads, "initial materialized Read")
	if initial.Cursor.Events != 0 {
		t.Fatalf("initial materialized Read cursor = %+v, want zero", initial.Cursor)
	}
	streams.materialize(
		stream.Frame{Ref: streams.ref, Running: true, Event: brokerChildMessageEvent("child-message-1", "first")},
		stream.Frame{Ref: streams.ref, Running: true, Event: brokerChildThoughtEvent("child-thought-1", "second")},
	)
	streams.releaseRead()
	first := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, first, "spawn-call-1")
	second := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, second, "spawn-call-1")

	// Keep both accepted frames in the source's materialized history, add one
	// more frame, and then terminal. A cursor advanced after only the first
	// frame would replay the thought from this final snapshot.
	streams.materialize(stream.Frame{Ref: streams.ref, Running: true, Event: brokerChildToolEvent()})
	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(15, 0))
	close(source)
	finalRequest := waitBrokerReadRequest(t, streams.reads, "cursor final Read")
	if finalRequest.Cursor.Events != 2 {
		t.Fatalf("final Read cursor = %+v, want all accepted snapshot events", finalRequest.Cursor)
	}
	streams.releaseRead()
	third := receiveBrokerEnvelope(t, events)
	assertBrokerChildRelation(t, third, "spawn-call-1")
	if update, ok := third.Update.(schema.ToolCallUpdate); !ok || update.ToolCallID != "child-tool-1" {
		t.Fatalf("final snapshot update = %#v, want only new child tool", third)
	}
	terminal := receiveBrokerEnvelope(t, events)
	assertBrokerMainTerminal(t, terminal)
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
	assertBrokerMaterializedServiceStopped(t, streams)
}

func TestLiveFeedBrokerPreservesRunCommandTerminalBytesAndCursor(t *testing.T) {
	source := make(chan eventstream.Envelope, 2)
	handle := newBrokerTestHandle(source)
	streams := newBrokerTestStreamService()
	turn := newGatewayTurn(handle, func() stream.Service { return streams })
	events := turn.Events()

	source <- brokerRunningToolEnvelopeAtCursor("RUN_COMMAND", "command-call-1", "command-task-1", "command-terminal-1", 7)
	receiveBrokerEnvelope(t, events)
	request := waitBrokerSignal(t, streams.started, "command stream subscription start")
	if request.Cursor.Output != 7 {
		t.Fatalf("command subscription cursor = %+v, want output cursor 7", request.Cursor)
	}

	first := "\x1b[31m中"
	second := "文\x1b[0m\n"
	streams.frames <- stream.Frame{
		Ref:     brokerStreamRef("command-task-1", "command-terminal-1"),
		Text:    first,
		Cursor:  stream.Cursor{Output: int64(len(first)), Events: 1},
		Running: true,
	}
	firstOutput := receiveBrokerEnvelope(t, events)
	assertBrokerTerminalFrame(t, firstOutput, "command-call-1", first, schema.ToolStatusInProgress)

	streams.frames <- stream.Frame{
		Ref:     brokerStreamRef("command-task-1", "command-terminal-1"),
		Text:    second,
		Cursor:  stream.Cursor{Output: int64(len(first + second)), Events: 2},
		Running: true,
	}
	secondOutput := receiveBrokerEnvelope(t, events)
	assertBrokerTerminalFrame(t, secondOutput, "command-call-1", second, schema.ToolStatusInProgress)

	streams.frames <- stream.Frame{
		Ref:    brokerStreamRef("command-task-1", "command-terminal-1"),
		Cursor: stream.Cursor{Output: int64(len(first + second)), Events: 3},
		Closed: true,
		State:  "failed",
	}
	final := receiveBrokerEnvelope(t, events)
	assertBrokerTerminalFrame(t, final, "command-call-1", "", schema.ToolStatusFailed)
	if _, ok := brokerTerminalOutput(final); ok {
		t.Fatalf("final command frame = %#v, must not replay already streamed bytes", final)
	}
	if exit, ok := metautil.TerminalExit(final.Update.(schema.ToolCallUpdate).Meta); !ok || exit.TerminalID != "command-call-1" || exit.ExitCode != nil {
		t.Fatalf("final command terminal exit = %#v, %v; want failed no-code exit for command call", exit, ok)
	}

	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(14, 0))
	close(source)
	receiveBrokerEnvelope(t, events)
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
}

func TestLiveFeedBrokerFinalReadPreservesRunCommandSnapshot(t *testing.T) {
	source := make(chan eventstream.Envelope, 2)
	streams := newBrokerReadStreamService()
	turn := newGatewayTurn(newBrokerTestHandle(source), func() stream.Service { return streams })
	events := turn.Events()

	source <- brokerRunningToolEnvelopeAtCursor("RUN_COMMAND", "command-call-1", "command-task-1", "command-terminal-1", 7)
	receiveBrokerEnvelope(t, events)
	initial := waitBrokerRead(t, streams.reads, "initial command Read")
	if initial.Request.Cursor.Output != 7 {
		t.Fatalf("initial command Read cursor = %+v, want output cursor 7", initial.Request.Cursor)
	}
	respondBrokerRead(t, initial, stream.Snapshot{Ref: initial.Request.Ref, Cursor: initial.Request.Cursor, Running: true})

	first := "\x1b[31m中"
	second := "文\x1b[0m\n"
	exitCode := 7
	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(16, 0))
	close(source)
	finalRead := waitBrokerRead(t, streams.reads, "final command Read")
	if finalRead.Request.Cursor.Output != 7 {
		t.Fatalf("final command Read cursor = %+v, want unchanged cursor before final snapshot", finalRead.Request.Cursor)
	}
	respondBrokerRead(t, finalRead, stream.Snapshot{
		Ref:      finalRead.Request.Ref,
		Cursor:   stream.Cursor{Output: 7 + int64(len(first+second)), Events: 2},
		State:    "failed",
		Running:  false,
		ExitCode: &exitCode,
		Frames: []stream.Frame{
			{
				Ref:     brokerStreamRef("command-task-1", "command-terminal-1"),
				Text:    first,
				Cursor:  stream.Cursor{Output: 7 + int64(len(first)), Events: 1},
				Running: true,
			},
			{
				Ref:     brokerStreamRef("command-task-1", "command-terminal-1"),
				Text:    second,
				Cursor:  stream.Cursor{Output: 7 + int64(len(first+second)), Events: 2},
				Running: false,
			},
		},
	})

	firstOutput := receiveBrokerEnvelope(t, events)
	assertBrokerTerminalFrame(t, firstOutput, "command-call-1", first, schema.ToolStatusInProgress)
	secondOutput := receiveBrokerEnvelope(t, events)
	assertBrokerTerminalFrame(t, secondOutput, "command-call-1", second, schema.ToolStatusInProgress)
	final := receiveBrokerEnvelope(t, events)
	assertBrokerTerminalFrame(t, final, "command-call-1", "", schema.ToolStatusFailed)
	exit, ok := metautil.TerminalExit(final.Update.(schema.ToolCallUpdate).Meta)
	if !ok || exit.TerminalID != "command-call-1" || exit.ExitCode == nil || *exit.ExitCode != exitCode {
		t.Fatalf("final command terminal exit = %#v, %v; want exit code %d", exit, ok, exitCode)
	}
	terminal := receiveBrokerEnvelope(t, events)
	assertBrokerMainTerminal(t, terminal)
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
	assertBrokerReadServiceStopped(t, streams)
}

func TestLiveFeedBrokerCancelAndCloseStopOwnedDelivery(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		source := make(chan eventstream.Envelope, 1)
		var closeSource sync.Once
		handle := newBrokerTestHandle(source)
		handle.cancelFn = func() { closeSource.Do(func() { close(source) }) }
		streams := newBrokerReadStreamService()
		turn := newGatewayTurn(handle, func() stream.Service { return streams })
		events := turn.Events()

		source <- brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
		receiveBrokerEnvelope(t, events)
		waitBrokerRead(t, streams.reads, "blocking task Read before cancel")
		turn.Cancel()

		terminal := receiveBrokerEnvelope(t, events)
		if !eventstream.IsTerminalLifecycle(terminal) || terminal.Lifecycle.State != eventstream.LifecycleStateCancelled {
			t.Fatalf("cancel terminal = %#v, want cancelled main terminal", terminal)
		}
		if calls := handle.cancelCalls.Load(); calls != 1 {
			t.Fatalf("handle cancel calls = %d, want one", calls)
		}
		requireBrokerChannelClosed(t, events)
		waitBrokerDone(t, turn.feed)
		assertBrokerReadServiceStopped(t, streams)
	})

	t.Run("close", func(t *testing.T) {
		source := make(chan eventstream.Envelope, 1)
		handle := newBrokerTestHandle(source)
		streams := newBrokerReadStreamService()
		turn := newGatewayTurn(handle, func() stream.Service { return streams })
		events := turn.Events()

		source <- brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
		receiveBrokerEnvelope(t, events)
		waitBrokerRead(t, streams.reads, "blocking task Read before close")
		if err := turn.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if calls := handle.cancelCalls.Load(); calls != 0 {
			t.Fatalf("Close() cancelled task runtime %d times, want no cancellation", calls)
		}
		if calls := handle.closeCalls.Load(); calls != 1 {
			t.Fatalf("handle close calls = %d, want one", calls)
		}
		requireBrokerChannelClosed(t, events)
		waitBrokerDone(t, turn.feed)
		assertBrokerReadServiceStopped(t, streams)
	})
}

type brokerTestHandle struct {
	events      <-chan eventstream.Envelope
	cancelFn    func()
	closeFn     func() error
	cancelCalls atomic.Int32
	closeCalls  atomic.Int32
}

func newBrokerTestHandle(events <-chan eventstream.Envelope) *brokerTestHandle {
	return &brokerTestHandle{events: events}
}

func (h *brokerTestHandle) HandleID() string { return "handle-1" }
func (h *brokerTestHandle) RunID() string    { return "run-1" }
func (h *brokerTestHandle) TurnID() string   { return "turn-1" }
func (h *brokerTestHandle) SessionRef() session.SessionRef {
	return session.SessionRef{SessionID: "session-1"}
}
func (h *brokerTestHandle) CreatedAt() time.Time { return time.Time{} }
func (h *brokerTestHandle) ACPEvents() <-chan eventstream.Envelope {
	return h.events
}
func (h *brokerTestHandle) Submit(context.Context, gateway.SubmitRequest) error { return nil }
func (h *brokerTestHandle) Cancel() agent.CancelResult {
	h.cancelCalls.Add(1)
	if h.cancelFn != nil {
		h.cancelFn()
	}
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (h *brokerTestHandle) Close() error {
	h.closeCalls.Add(1)
	if h.closeFn != nil {
		return h.closeFn()
	}
	return nil
}

type brokerTestStreamService struct {
	frames         chan stream.Frame
	started        chan stream.ReadRequest
	startedOnce    sync.Once
	calls          atomic.Int32
	subscribeCalls atomic.Int32
}

func newBrokerTestStreamService() *brokerTestStreamService {
	return &brokerTestStreamService{
		frames:  make(chan stream.Frame, 16),
		started: make(chan stream.ReadRequest, 1),
	}
}

func (s *brokerTestStreamService) Read(ctx context.Context, req stream.ReadRequest) (stream.Snapshot, error) {
	s.calls.Add(1)
	s.startedOnce.Do(func() {
		select {
		case s.started <- req:
		case <-ctx.Done():
		}
	})
	select {
	case <-ctx.Done():
		return stream.Snapshot{}, ctx.Err()
	case frame := <-s.frames:
		frame = stream.CloneFrame(frame)
		return stream.Snapshot{
			Ref:      firstNonEmptyStreamRef(frame.Ref, req.Ref),
			Cursor:   frame.Cursor,
			Frames:   []stream.Frame{frame},
			State:    frame.State,
			Running:  !frame.Closed,
			ExitCode: frame.ExitCode,
		}, nil
	default:
		return stream.Snapshot{Ref: req.Ref, Cursor: req.Cursor, Running: true}, nil
	}
}

func (s *brokerTestStreamService) Subscribe(context.Context, stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	s.subscribeCalls.Add(1)
	return func(func(*stream.Frame, error) bool) {}
}

type brokerReadCall struct {
	Request  stream.ReadRequest
	response chan brokerReadResult
}

type brokerReadResult struct {
	snapshot stream.Snapshot
	err      error
}

// brokerReadStreamService models the production pull contract: each worker
// issues one Read with its current cursor and waits for a source snapshot. It
// never pushes directly into the broker's delivery queue.
type brokerReadStreamService struct {
	reads          chan brokerReadCall
	activeReads    atomic.Int32
	subscribeCalls atomic.Int32
}

func newBrokerReadStreamService() *brokerReadStreamService {
	return &brokerReadStreamService{reads: make(chan brokerReadCall, 4)}
}

func (s *brokerReadStreamService) Read(ctx context.Context, request stream.ReadRequest) (stream.Snapshot, error) {
	s.activeReads.Add(1)
	defer s.activeReads.Add(-1)
	call := brokerReadCall{Request: request, response: make(chan brokerReadResult, 1)}
	select {
	case s.reads <- call:
	case <-ctx.Done():
		return stream.Snapshot{}, ctx.Err()
	}
	select {
	case result := <-call.response:
		if result.err != nil {
			return stream.Snapshot{}, result.err
		}
		return stream.CloneSnapshot(result.snapshot), nil
	case <-ctx.Done():
		return stream.Snapshot{}, ctx.Err()
	}
}

func (s *brokerReadStreamService) Subscribe(context.Context, stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	s.subscribeCalls.Add(1)
	return func(func(*stream.Frame, error) bool) {}
}

func waitBrokerRead(t *testing.T, reads <-chan brokerReadCall, name string) brokerReadCall {
	t.Helper()
	return waitBrokerSignal(t, reads, name)
}

func respondBrokerRead(t *testing.T, call brokerReadCall, snapshot stream.Snapshot) {
	t.Helper()
	select {
	case call.response <- brokerReadResult{snapshot: snapshot}:
	default:
		t.Fatal("broker Read response was already supplied")
	}
}

func assertBrokerReadServiceStopped(t *testing.T, service *brokerReadStreamService) {
	t.Helper()
	if service == nil {
		t.Fatal("broker Read service is nil")
	}
	if active := service.activeReads.Load(); active != 0 {
		t.Fatalf("active broker Read calls = %d, want zero", active)
	}
	if calls := service.subscribeCalls.Load(); calls != 0 {
		t.Fatalf("stream Subscribe calls = %d, want zero", calls)
	}
}

// brokerMaterializedStreamService retains source frames and implements cursor
// slicing like a runtime stream Service. The test gate makes each Read
// deterministic without relying on the 100ms poll interval.
type brokerMaterializedStreamService struct {
	ref            stream.Ref
	reads          chan stream.ReadRequest
	release        chan struct{}
	completed      chan struct{}
	mu             sync.Mutex
	frames         []stream.Frame
	activeReads    atomic.Int32
	subscribeCalls atomic.Int32
}

func newBrokerMaterializedStreamService(ref stream.Ref) *brokerMaterializedStreamService {
	return &brokerMaterializedStreamService{
		ref:       stream.NormalizeRef(ref),
		reads:     make(chan stream.ReadRequest, 4),
		release:   make(chan struct{}, 4),
		completed: make(chan struct{}, 4),
	}
}

func (s *brokerMaterializedStreamService) Read(ctx context.Context, request stream.ReadRequest) (stream.Snapshot, error) {
	s.activeReads.Add(1)
	defer s.activeReads.Add(-1)
	select {
	case s.reads <- request:
	case <-ctx.Done():
		return stream.Snapshot{}, ctx.Err()
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return stream.Snapshot{}, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	start := int(request.Cursor.Events)
	if start < 0 {
		start = 0
	}
	if start > len(s.frames) {
		start = len(s.frames)
	}
	frames := make([]stream.Frame, 0, len(s.frames)-start)
	for _, frame := range s.frames[start:] {
		frames = append(frames, stream.CloneFrame(frame))
	}
	snapshot := stream.Snapshot{
		Ref:     s.ref,
		Cursor:  stream.Cursor{Events: int64(len(s.frames))},
		Frames:  frames,
		Running: true,
	}
	select {
	case s.completed <- struct{}{}:
	case <-ctx.Done():
		return stream.Snapshot{}, ctx.Err()
	}
	return snapshot, nil
}

func (s *brokerMaterializedStreamService) Subscribe(context.Context, stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	s.subscribeCalls.Add(1)
	return func(func(*stream.Frame, error) bool) {}
}

func (s *brokerMaterializedStreamService) materialize(frames ...stream.Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, frame := range frames {
		frame = stream.CloneFrame(frame)
		frame.Ref = firstNonEmptyStreamRef(frame.Ref, s.ref)
		frame.Cursor.Events = int64(len(s.frames) + 1)
		s.frames = append(s.frames, frame)
	}
}

func (s *brokerMaterializedStreamService) releaseRead() {
	s.release <- struct{}{}
}

func waitBrokerReadRequest(t *testing.T, reads <-chan stream.ReadRequest, name string) stream.ReadRequest {
	t.Helper()
	return waitBrokerSignal(t, reads, name)
}

func assertBrokerMaterializedServiceStopped(t *testing.T, service *brokerMaterializedStreamService) {
	t.Helper()
	if service == nil {
		t.Fatal("broker materialized stream service is nil")
	}
	if active := service.activeReads.Load(); active != 0 {
		t.Fatalf("active materialized Read calls = %d, want zero", active)
	}
	if calls := service.subscribeCalls.Load(); calls != 0 {
		t.Fatalf("stream Subscribe calls = %d, want zero", calls)
	}
}

func firstNonEmptyStreamRef(first stream.Ref, fallback stream.Ref) stream.Ref {
	if first != (stream.Ref{}) {
		return stream.NormalizeRef(first)
	}
	return stream.NormalizeRef(fallback)
}

func brokerRunningToolEnvelope(toolName string, callID string, taskID string, terminalID string) eventstream.Envelope {
	return brokerRunningToolEnvelopeAtCursor(toolName, callID, taskID, terminalID, 0)
}

func brokerRunningToolEnvelopeAtCursor(toolName string, callID string, taskID string, terminalID string, cursor int64) eventstream.Envelope {
	status := schema.ToolStatusInProgress
	return eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    callID,
			Kind:          &toolName,
			Status:        &status,
			RawInput:      map[string]any{"command": "fixture"},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"task": map[string]any{
							"task_id":       taskID,
							"terminal_id":   terminalID,
							"output_cursor": cursor,
						},
					},
				},
			},
		},
	}
}

func brokerMainMessageEnvelope(text string) eventstream.Envelope {
	return eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: text},
		},
	}
}

func brokerStreamRef(taskID string, terminalID string) stream.Ref {
	return stream.Ref{SessionID: "session-1", TaskID: taskID, TerminalID: terminalID}
}

func brokerChildScope() *session.EventScope {
	return &session.EventScope{
		TurnID: "child-turn-1",
		Participant: session.ParticipantRef{
			ID:           "child-1",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			DelegationID: "task-1",
		},
		ACP: session.ACPRef{SessionID: "child-session-1"},
	}
}

func brokerChildMessageEvent(id string, text string) *session.Event {
	return &session.Event{
		ID:         id,
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityUIOnly,
		Text:       text,
		Scope:      brokerChildScope(),
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				MessageID:     id,
				Content:       session.ProtocolTextContent(text),
			},
		},
	}
}

func brokerChildThoughtEvent(id string, text string) *session.Event {
	return &session.Event{
		ID:         id,
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityUIOnly,
		Text:       text,
		Scope:      brokerChildScope(),
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentThought),
				Content:       session.ProtocolTextContent(text),
			},
		},
	}
}

func brokerChildToolCallEvent() *session.Event {
	return &session.Event{
		ID:         "child-tool-call-event-1",
		Type:       session.EventTypeToolCall,
		Visibility: session.VisibilityUIOnly,
		Scope:      brokerChildScope(),
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
				ToolCallID:    "child-tool-1",
				Kind:          "PATCH",
				Title:         "Apply child patch",
				Status:        schema.ToolStatusInProgress,
			},
		},
	}
}

func brokerChildToolEvent() *session.Event {
	line := 12
	return &session.Event{
		ID:         "child-tool-event-1",
		Type:       session.EventTypeToolResult,
		Visibility: session.VisibilityUIOnly,
		Scope:      brokerChildScope(),
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
				ToolCallID:    "child-tool-1",
				Kind:          "PATCH",
				Status:        schema.ToolStatusCompleted,
				Locations: []session.ProtocolToolCallLocation{{
					Path: "/workspace/demo.txt",
					Line: &line,
				}},
				Content: []session.ProtocolToolCallContent{{
					Type:    "diff",
					Path:    "/workspace/demo.txt",
					NewText: "new content\n",
				}},
			},
		},
	}
}

func brokerChildPlanEvent() *session.Event {
	return &session.Event{
		ID:         "child-plan-1",
		Type:       session.EventTypePlan,
		Visibility: session.VisibilityUIOnly,
		Scope:      brokerChildScope(),
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypePlan),
				Entries: []session.ProtocolPlanEntry{{
					Content:  "inspect output",
					Status:   "in_progress",
					Priority: "high",
				}},
			},
		},
	}
}

func brokerChildLifecycleEvent() *session.Event {
	return &session.Event{
		ID:         "child-lifecycle-1",
		Type:       session.EventTypeLifecycle,
		Visibility: session.VisibilityUIOnly,
		Scope:      brokerChildScope(),
		Lifecycle:  &session.EventLifecycle{Status: eventstream.LifecycleStateCompleted},
	}
}

func brokerControlChildPermissionEnvelope() eventstream.Envelope {
	kind := "edit"
	title := "Write child file"
	status := "pending"
	return eventstream.Envelope{
		Kind:              eventstream.KindRequestPermission,
		SessionID:         "session-1",
		HandleID:          "handle-1",
		RunID:             "run-1",
		TurnID:            "turn-1",
		Scope:             eventstream.ScopeSubagent,
		ScopeID:           "task-1",
		ParticipantID:     "child-1",
		ApprovalRequestID: "approval-child-1",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-call-1",
			ToolName:   "SPAWN",
		},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Permission: &schema.RequestPermissionRequest{
			SessionID: "session-1",
			ToolCall: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "shared-child-call",
				Kind:          &kind,
				Title:         &title,
				Status:        &status,
				RawInput:      map[string]any{"path": "child.txt"},
				RawOutput:     map[string]any{"preview": "new text"},
				Content: []schema.ToolCallContent{{
					Type:    "content",
					Content: schema.TextContent{Type: "text", Text: "child permission detail"},
				}},
			},
			Options: []schema.PermissionOption{{
				OptionID: "allow_once", Name: "Allow once", Kind: "allow_once",
			}},
		},
	}
}

func receiveBrokerEnvelope(t *testing.T, events <-chan eventstream.Envelope) eventstream.Envelope {
	t.Helper()
	select {
	case env, ok := <-events:
		if !ok {
			t.Fatal("turn feed closed before expected envelope")
		}
		return env
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for turn feed envelope")
		return eventstream.Envelope{}
	}
}

func waitBrokerSignal[T any](t *testing.T, signal <-chan T, name string) T {
	t.Helper()
	select {
	case value := <-signal:
		return value
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
		var zero T
		return zero
	}
}

func requireBrokerChannelClosed(t *testing.T, events <-chan eventstream.Envelope) {
	t.Helper()
	select {
	case env, ok := <-events:
		if ok {
			t.Fatalf("turn feed emitted extra envelope after main terminal: %#v", env)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("turn feed did not close after main terminal")
	}
}

func waitBrokerDone(t *testing.T, broker *liveFeedBroker) {
	t.Helper()
	if broker == nil {
		t.Fatal("live feed broker is nil")
	}
	waitBrokerSignal(t, broker.Done(), "broker shutdown")
}

func brokerSourceCount(broker *liveFeedBroker) int {
	if broker == nil {
		return 0
	}
	return broker.SourceCount()
}

func assertBrokerMainTerminal(t *testing.T, env eventstream.Envelope) {
	t.Helper()
	if !eventstream.IsTerminalLifecycle(env) || env.Scope != eventstream.ScopeMain || env.Lifecycle.State != eventstream.LifecycleStateCompleted {
		t.Fatalf("main terminal = %#v, want one completed main lifecycle", env)
	}
}

func assertBrokerChildRelation(t *testing.T, env eventstream.Envelope, parentCallID string) {
	t.Helper()
	if env.Scope != eventstream.ScopeSubagent || env.ParentTool == nil || env.ParentTool.ToolCallID != parentCallID {
		t.Fatalf("child envelope relation = %#v, want scoped relation to %q", env, parentCallID)
	}
	if env.Delivery == nil || env.Delivery.Mode != eventstream.DeliveryTransient {
		t.Fatalf("child delivery = %#v, want transient delivery", env.Delivery)
	}
}

func assertBrokerToolCallID(t *testing.T, env eventstream.Envelope, want string) {
	t.Helper()
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok || strings.TrimSpace(update.ToolCallID) != want {
		t.Fatalf("tool update = %#v, want tool call %q", env.Update, want)
	}
}

func brokerTerminalOutput(env eventstream.Envelope) (string, bool) {
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok {
		return "", false
	}
	output, ok := metautil.TerminalOutput(update.Meta)
	if !ok {
		return "", false
	}
	return output.Data, true
}

func assertBrokerTerminalFrame(t *testing.T, env eventstream.Envelope, callID string, text string, status string) {
	t.Helper()
	update, ok := env.Update.(schema.ToolCallUpdate)
	if !ok || update.ToolCallID != callID || update.Status == nil || *update.Status != status {
		t.Fatalf("terminal update = %#v, want %q/%q", env.Update, callID, status)
	}
	output, ok := brokerTerminalOutput(env)
	if text == "" {
		if ok {
			t.Fatalf("terminal output = %q, want no replayed output", output)
		}
		return
	}
	if !ok || output != text {
		t.Fatalf("terminal output = %q, %v; want exact %q", output, ok, text)
	}
}
