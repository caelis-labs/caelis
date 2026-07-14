package controladapter

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"reflect"
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
	controlclientport "github.com/caelis-labs/caelis/ports/controlclient"
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

func TestControlSessionFeedPublishesDurableChildMirrorWithTaskScopeWhileRunning(t *testing.T) {
	ctx := context.Background()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string { return "session-1" }}))
	parent, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
		Reader: sessions, CursorCodec: codec, SubscriberQueue: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(parent.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	live, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()

	source := make(chan eventstream.Envelope, 2)
	streams := newBrokerTestStreamService()
	ingress := newLiveFeedBroker(
		newBrokerTestHandle(source),
		func() stream.Service { return streams },
		internalcontrolclient.NewChildRecorder(sessions),
	)
	feed.Attach(ingress.Events())

	source <- brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
	assertBrokerToolCallID(t, receiveBrokerEnvelope(t, live.Events()), "spawn-call-1")
	waitBrokerSignal(t, streams.started, "production task stream start")
	streams.frames <- stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Cursor:  stream.Cursor{Events: 1},
		Running: true,
		Event:   brokerChildMessageEvent("child-message-1", "visible before completion"),
	}
	child := receiveBrokerEnvelope(t, live.Events())
	assertDurableChildFeedEnvelope(t, child)

	page, err := sessions.EventsPage(ctx, session.EventPageRequest{
		SessionRef: parent.SessionRef, Visibility: session.EventPageClientReplay,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].ChildOrigin == nil || page.Events[0].ChildOrigin.ScopeID != "task-1" {
		t.Fatalf("stored child origin = %#v, want task scope", page.Events)
	}

	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(12, 0))
	close(source)
	assertBrokerMainTerminal(t, receiveBrokerEnvelope(t, live.Events()))
	waitBrokerDone(t, ingress)

	replay, err := feed.Subscribe(ctx, controlclientport.SubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Subscription.Close()
	var replayed eventstream.Envelope
	for _, envelope := range replay.Backfill {
		if envelope.Delivery != nil && envelope.Delivery.Mode == eventstream.DeliveryMirror {
			replayed = envelope
			break
		}
	}
	assertDurableChildFeedEnvelope(t, replayed)
	if replayed.Update != nil && child.Update != nil && !reflect.DeepEqual(replayed.Update, child.Update) {
		t.Fatalf("live/replay child update mismatch:\nlive=%#v\nreplay=%#v", child.Update, replayed.Update)
	}
}

func TestGatewayTurnDoesNotAttachPreparedFeedBeforeSurfaceClaimsEvents(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
		CursorCodec: codec, SubscriberQueue: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := feed.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	prepared, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()

	const burst = 64
	mainEvents := make(chan eventstream.Envelope, burst+1)
	for index := 0; index < burst; index++ {
		mainEvents <- brokerMainMessageEnvelope(fmt.Sprintf("fast-%03d", index))
	}
	mainEvents <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(19, 0))
	close(mainEvents)
	handle := newBrokerTestHandle(mainEvents)
	handle.eventsStarted = make(chan struct{})
	driver := &Adapter{stack: &RuntimeStack{ControlFeeds: feeds}}
	turn := driver.newGatewayTurnWithSubscription(handle, prepared, true)
	defer turn.Close()

	select {
	case <-handle.eventsStarted:
		t.Fatal("Turn ingress started before the Surface claimed Events")
	case <-time.After(50 * time.Millisecond):
	}
	if err := prepared.Err(); err != nil {
		t.Fatalf("prepared subscription failed before handoff: %v", err)
	}

	events := turn.Events()
	waitBrokerSignal(t, handle.eventsStarted, "deferred Turn ingress start")
	for index := 0; index < burst; index++ {
		envelope := receiveBrokerEnvelope(t, events)
		update, ok := envelope.Update.(schema.ContentChunk)
		want := fmt.Sprintf("fast-%03d", index)
		content, contentOK := update.Content.(schema.TextContent)
		if !ok || !contentOK || content.Text != want {
			t.Fatalf("burst envelope %d = %#v, want %q", index, envelope.Update, want)
		}
	}
	assertBrokerMainTerminal(t, receiveBrokerEnvelope(t, events))
	requireBrokerChannelClosed(t, events)
	if err := prepared.Err(); err != nil {
		t.Fatalf("prepared subscription error after complete handoff: %v", err)
	}
}

func TestPreparedGatewaySubscriptionDoesNotBlockDetachedChildBurst(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
		CursorCodec: codec, SubscriberQueue: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := feed.(interface{ Close() error }); ok {
		defer closer.Close()
	}

	active, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	prepared, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	futureMain := make(chan eventstream.Envelope)
	futureHandle := newBrokerTestHandleForTurn(futureMain, "handle-future", "run-future", "turn-future")
	futureHandle.eventsStarted = make(chan struct{})
	driver := &Adapter{stack: &RuntimeStack{ControlFeeds: feeds}}
	futureTurn := driver.newGatewayTurnWithSubscription(futureHandle, prepared, true)
	defer futureTurn.Close()

	detachedIngress := make(chan eventstream.Envelope)
	attachment := feed.Attach(detachedIngress)
	const burst = 8
	for index := 0; index < burst; index++ {
		want := fmt.Sprintf("detached-child-%02d", index)
		detachedIngress <- eventstream.Envelope{
			Kind:      eventstream.KindNotice,
			SessionID: "session-1",
			Scope:     eventstream.ScopeSubagent,
			ScopeID:   "detached-task-1",
			Notice:    want,
			Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		}
		got := receiveBrokerEnvelope(t, active.Events())
		if got.Scope != eventstream.ScopeSubagent || got.ScopeID != "detached-task-1" || got.Notice != want {
			t.Fatalf("detached child event %d = %#v, want %q", index, got, want)
		}
	}
	close(detachedIngress)
	waitBrokerAttachment(t, attachment, "detached child burst")

	deadline := time.Now().Add(time.Second)
	for !errors.Is(prepared.Err(), controlclientport.ErrSlowConsumer) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !errors.Is(prepared.Err(), controlclientport.ErrSlowConsumer) {
		t.Fatalf("unclaimed prepared subscription error = %v, want slow consumer", prepared.Err())
	}
	select {
	case <-futureHandle.eventsStarted:
		t.Fatal("future Turn ingress started without the Surface claiming Events")
	default:
	}
}

func TestGatewayTurnAttachToPermanentFailureEmitsFailedTerminal(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
		CursorCodec: codec, SubscriberQueue: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := feed.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	prepared, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	observer, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	observerTerminal := make(chan eventstream.Envelope, 2)
	go func() {
		for envelope := range observer.Events() {
			if eventstream.IsTerminalLifecycle(envelope) && envelope.HandleID == "handle-1" {
				observerTerminal <- envelope
			}
		}
	}()

	mainEvents := make(chan eventstream.Envelope, 1)
	mainEvents <- eventstream.Envelope{
		Kind:      eventstream.KindNotice,
		SessionID: "session-1",
		Scope:     eventstream.ScopeMain,
		Notice:    "invalid durable ingress",
		Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
	}
	handle := newBrokerTestHandle(mainEvents)
	producerRelease := make(chan struct{})
	var closeMain sync.Once
	handle.cancelFn = func() {
		go func() {
			<-producerRelease
			closeMain.Do(func() { close(mainEvents) })
		}()
	}
	driver := &Adapter{stack: &RuntimeStack{ControlFeeds: feeds}}
	turn := driver.newGatewayTurnWithSubscription(handle, prepared, true)
	defer turn.Close()

	events := turn.Events()
	deadline := time.Now().Add(time.Second)
	for handle.cancelCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("attachment failure cancelled Runtime %d times, want one", calls)
	}
	select {
	case envelope := <-events:
		t.Fatalf("attachment failure crossed producer barrier early: %#v", envelope)
	case <-time.After(30 * time.Millisecond):
	}
	close(producerRelease)
	failure := receiveBrokerEnvelope(t, events)
	if failure.Kind != eventstream.KindError || failure.Err == nil || !strings.Contains(failure.Err.Error(), "durable feed envelope requires a durable position") {
		t.Fatalf("attachment failure = %#v, want durable-position error", failure)
	}
	terminal := receiveBrokerEnvelope(t, events)
	if terminal.Lifecycle == nil || terminal.Lifecycle.State != eventstream.LifecycleStateFailed {
		t.Fatalf("attachment terminal = %#v, want failed", terminal)
	}
	requireBrokerChannelClosed(t, events)
	select {
	case siblingTerminal := <-observerTerminal:
		if siblingTerminal.Lifecycle == nil || siblingTerminal.Lifecycle.State != eventstream.LifecycleStateFailed ||
			siblingTerminal.HandleID != terminal.HandleID || siblingTerminal.RunID != terminal.RunID || siblingTerminal.TurnID != terminal.TurnID {
			t.Fatalf("sibling attachment terminal = %#v, want same failed state", siblingTerminal)
		}
	case <-time.After(time.Second):
		t.Fatal("sibling Session subscriber did not receive attachment failure terminal")
	}
	select {
	case duplicate := <-observerTerminal:
		t.Fatalf("sibling Session subscriber received duplicate main terminal: %#v", duplicate)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestGatewayTurnCancelReleasesBlockedSessionAttachmentWithoutClosingSessionFeed(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
		CursorCodec: codec, SubscriberQueue: 1, SubscriberStallTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := feed.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	prepared, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	mainEvents := make(chan eventstream.Envelope, 8)
	for index := 0; index < cap(mainEvents); index++ {
		mainEvents <- brokerMainMessageEnvelope(fmt.Sprintf("blocked-%02d", index))
	}
	handle := newBrokerTestHandle(mainEvents)
	var closeMain sync.Once
	handle.cancelFn = func() { closeMain.Do(func() { close(mainEvents) }) }
	driver := &Adapter{stack: &RuntimeStack{ControlFeeds: feeds}}
	turn := driver.newGatewayTurnWithSubscription(handle, prepared, true)
	events := turn.Events()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		position, _ := feed.Boundary()
		if position != nil && position.Transient != nil && position.Transient.Sequence >= 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	position, _ := feed.Boundary()
	if position == nil || position.Transient == nil || position.Transient.Sequence < 3 {
		t.Fatalf("blocked attachment boundary = %#v, want at least three accepted events", position)
	}

	turn.Cancel()
	turn.Cancel()
	terminalCount := 0
	for {
		envelope := receiveBrokerEnvelope(t, events)
		if !eventstream.IsTerminalLifecycle(envelope) {
			continue
		}
		terminalCount++
		if envelope.Lifecycle.State != eventstream.LifecycleStateCancelled {
			t.Fatalf("cancel terminal = %#v, want cancelled (not completed/interrupted)", envelope)
		}
		break
	}
	requireBrokerChannelClosed(t, events)
	if terminalCount != 1 {
		t.Fatalf("main terminal count = %d, want one", terminalCount)
	}
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("idempotent Cancel called Runtime %d times, want one", calls)
	}
	waitBrokerDone(t, turn.feed)

	// Cancel owns this Turn and prepared subscription only. A detached child
	// ingress remains publishable through the same Session broker.
	observer, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatalf("Session feed closed by Turn Cancel: %v", err)
	}
	defer observer.Close()
	detached := make(chan eventstream.Envelope, 1)
	detached <- eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1",
		Scope: eventstream.ScopeSubagent, ScopeID: "detached-task-1", Notice: "still live",
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
	}
	close(detached)
	attachment := feed.Attach(detached)
	child := receiveBrokerEnvelope(t, observer.Events())
	if child.Scope != eventstream.ScopeSubagent || child.ScopeID != "detached-task-1" || child.Notice != "still live" {
		t.Fatalf("detached child after Turn Cancel = %#v", child)
	}
	waitBrokerAttachment(t, attachment, "detached child after Turn Cancel")
	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayTurnCancelCrossesBlockingDurableReadAndProducerBarrier(t *testing.T) {
	reader := newGatewayCancellablePageReader(3)
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
		Reader: reader, CursorCodec: codec, SubscriberQueue: 1,
		SubscriberStallTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := feed.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	prepared, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	observer, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	observerTerminal := make(chan eventstream.Envelope, 1)
	go func() {
		for envelope := range observer.Events() {
			if eventstream.IsTerminalLifecycle(envelope) && envelope.HandleID == "handle-1" {
				observerTerminal <- envelope
				return
			}
		}
	}()

	mainEvents := make(chan eventstream.Envelope, 1)
	durable := brokerMainMessageEnvelope("durable read blocks")
	durable.EventID = "event-1"
	durable.ProjectionID = eventstream.FormatProjectionID("event-1", 0)
	durable.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryCanonical}
	durable.Position = &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{Seq: 1}}
	mainEvents <- durable
	handle := newBrokerTestHandle(mainEvents)
	producerRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseProducer := func() { releaseOnce.Do(func() { close(producerRelease) }) }
	defer releaseProducer()
	var closeMain sync.Once
	handle.cancelFn = func() {
		go func() {
			<-producerRelease
			closeMain.Do(func() { close(mainEvents) })
		}()
	}
	driver := &Adapter{stack: &RuntimeStack{ControlFeeds: feeds}}
	turn := driver.newGatewayTurnWithSubscription(handle, prepared, true)
	events := turn.Events()
	waitBrokerSignal(t, reader.started, "blocking durable feed read")

	turn.Cancel()
	turn.Cancel()
	waitBrokerSignal(t, reader.exited, "cancelled durable feed read")
	select {
	case envelope := <-events:
		t.Fatalf("Turn emitted before producer barrier closed: %#v", envelope)
	case <-time.After(30 * time.Millisecond):
	}
	releaseProducer()

	terminalCount := 0
	for {
		envelope := receiveBrokerEnvelope(t, events)
		if !eventstream.IsTerminalLifecycle(envelope) {
			continue
		}
		terminalCount++
		if envelope.Lifecycle.State != eventstream.LifecycleStateCancelled {
			t.Fatalf("terminal after durable cancellation = %#v, want cancelled", envelope)
		}
		break
	}
	requireBrokerChannelClosed(t, events)
	if terminalCount != 1 {
		t.Fatalf("main terminal count = %d, want one", terminalCount)
	}
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("idempotent Cancel called Runtime %d times, want one", calls)
	}
	waitBrokerDone(t, turn.feed)
	select {
	case terminal := <-observerTerminal:
		if terminal.Lifecycle == nil || terminal.Lifecycle.State != eventstream.LifecycleStateCancelled ||
			terminal.HandleID != "handle-1" || terminal.RunID != "run-1" || terminal.TurnID != "turn-1" {
			t.Fatalf("sibling Session subscriber terminal = %#v, want authoritative cancelled terminal", terminal)
		}
	case <-time.After(time.Second):
		t.Fatal("sibling Session subscriber did not receive cancellation terminal")
	}

	afterCancel, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatalf("Session feed unavailable after durable cancellation: %v", err)
	}
	defer afterCancel.Close()
	if err := feed.Publish(eventstream.Envelope{
		Kind: eventstream.KindNotice, SessionID: "session-1",
		Scope: eventstream.ScopeSubagent, ScopeID: "detached-task-1", Notice: "still live",
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
	}); err != nil {
		t.Fatalf("Session feed publish after durable cancellation: %v", err)
	}
	child := receiveBrokerEnvelope(t, afterCancel.Events())
	if child.ScopeID != "detached-task-1" || child.Notice != "still live" {
		t.Fatalf("Session feed after durable cancellation = %#v", child)
	}
	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayTurnOwnerContextCancellationUsesProducerAndSessionBarriers(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
		CursorCodec: codec, SubscriberQueue: 2, SubscriberStallTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := feed.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	prepared, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	observer, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	observerTerminal := make(chan eventstream.Envelope, 1)
	go func() {
		for envelope := range observer.Events() {
			if eventstream.IsTerminalLifecycle(envelope) && envelope.HandleID == "handle-1" {
				observerTerminal <- envelope
				return
			}
		}
	}()

	mainEvents := make(chan eventstream.Envelope, 1)
	mainEvents <- brokerMainMessageEnvelope("owner request is no longer reading")
	handle := newBrokerTestHandle(mainEvents)
	producerRelease := make(chan struct{})
	var closeMain sync.Once
	handle.cancelFn = func() {
		go func() {
			<-producerRelease
			closeMain.Do(func() { close(mainEvents) })
		}()
	}
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	driver := &Adapter{stack: &RuntimeStack{ControlFeeds: feeds}}
	turn := driver.newGatewayTurnWithSubscription(handle, prepared, true, ownerCtx)
	defer turn.Close()
	events := turn.Events()
	waitFeedBoundary(t, feed, 1)
	cancelOwner()
	deadline := time.Now().Add(time.Second)
	for handle.cancelCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("owner context cancellation reached Runtime %d times, want one", calls)
	}
	select {
	case envelope := <-events:
		t.Fatalf("owner context cancellation crossed producer barrier early: %#v", envelope)
	case <-time.After(30 * time.Millisecond):
	}
	close(producerRelease)

	terminal := receiveBrokerEnvelope(t, events)
	if terminal.Lifecycle == nil || terminal.Lifecycle.State != eventstream.LifecycleStateCancelled {
		t.Fatalf("owner context terminal = %#v, want cancelled", terminal)
	}
	requireBrokerChannelClosed(t, events)
	select {
	case siblingTerminal := <-observerTerminal:
		if siblingTerminal.Lifecycle == nil || siblingTerminal.Lifecycle.State != eventstream.LifecycleStateCancelled {
			t.Fatalf("owner context sibling terminal = %#v, want cancelled", siblingTerminal)
		}
	case <-time.After(time.Second):
		t.Fatal("sibling subscriber did not receive owner cancellation terminal")
	}
}

func TestGatewayTurnCloseStopsPreparedSessionDeliveryWithoutCancellingRuntime(t *testing.T) {
	newTurn := func(t *testing.T) (*gatewayTurn, *brokerTestHandle, controlclientport.FeedSubscription, chan eventstream.Envelope) {
		t.Helper()
		codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{
			Secret: []byte("0123456789abcdef0123456789abcdef"),
		})
		if err != nil {
			t.Fatal(err)
		}
		feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
			CursorCodec: codec, SubscriberQueue: 4,
		})
		if err != nil {
			t.Fatal(err)
		}
		feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if closer, ok := feed.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
		})
		prepared, err := feed.SubscribeFromNow(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		source := make(chan eventstream.Envelope, 2)
		handle := newBrokerTestHandle(source)
		driver := &Adapter{stack: &RuntimeStack{ControlFeeds: feeds}}
		return driver.newGatewayTurnWithSubscription(handle, prepared, true), handle, prepared, source
	}

	t.Run("unread output", func(t *testing.T) {
		turn, handle, prepared, source := newTurn(t)
		out := turn.Events()
		source <- brokerMainMessageEnvelope("unread")
		waitSubscriptionCursorChange(t, prepared, "")

		closed := make(chan error, 1)
		go func() { closed <- turn.Close() }()
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("production Close blocked behind unread output")
		}
		requireBrokerChannelClosed(t, out)
		if calls := handle.cancelCalls.Load(); calls != 0 {
			t.Fatalf("Close cancelled Runtime %d times, want zero", calls)
		}
		if calls := handle.closeCalls.Load(); calls != 1 {
			t.Fatalf("Close reached handle.Close %d times, want one", calls)
		}
	})

	t.Run("half-read output", func(t *testing.T) {
		turn, handle, prepared, source := newTurn(t)
		out := turn.Events()
		source <- brokerMainMessageEnvelope("first")
		first := receiveBrokerEnvelope(t, out)
		if content, ok := first.Update.(schema.ContentChunk); !ok || content.Content.(schema.TextContent).Text != "first" {
			t.Fatalf("first prepared output = %#v", first)
		}
		firstCursor := prepared.LastCursor()
		source <- brokerMainMessageEnvelope("second")
		waitSubscriptionCursorChange(t, prepared, firstCursor)

		if err := turn.Close(); err != nil {
			t.Fatal(err)
		}
		requireBrokerChannelClosed(t, out)
		if calls := handle.cancelCalls.Load(); calls != 0 {
			t.Fatalf("Close cancelled Runtime %d times, want zero", calls)
		}
		if calls := handle.closeCalls.Load(); calls != 1 {
			t.Fatalf("Close reached handle.Close %d times, want one", calls)
		}
	})
}

func TestGatewayTurnCloseDuringProducerBarrierEmitsNoTerminal(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
		CursorCodec: codec, SubscriberQueue: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := feed.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	prepared, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source := make(chan eventstream.Envelope)
	handle := newBrokerTestHandle(source)
	producerRelease := make(chan struct{})
	var closeSource sync.Once
	handle.cancelFn = func() {
		go func() {
			<-producerRelease
			closeSource.Do(func() { close(source) })
		}()
	}
	driver := &Adapter{stack: &RuntimeStack{ControlFeeds: feeds}}
	turn := driver.newGatewayTurnWithSubscription(handle, prepared, true)
	events := turn.Events()
	turn.Cancel()
	deadline := time.Now().Add(time.Second)
	for handle.cancelCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("producer-gated Cancel reached Runtime %d times, want one", calls)
	}

	closed := make(chan error, 1)
	go func() { closed <- turn.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked behind producer barrier")
	}
	requireBrokerChannelClosed(t, events)
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("Close added Runtime cancellation during producer barrier: %d calls", calls)
	}
	if calls := handle.closeCalls.Load(); calls != 1 {
		t.Fatalf("Close reached handle.Close %d times, want one", calls)
	}
	close(producerRelease)
}

func assertDurableChildFeedEnvelope(t *testing.T, child eventstream.Envelope) {
	t.Helper()
	if child.Scope != eventstream.ScopeSubagent || child.ScopeID != "task-1" {
		t.Fatalf("child scope = (%q, %q), want (subagent, task-1): %#v", child.Scope, child.ScopeID, child)
	}
	if child.ParentTool == nil || child.ParentTool.ToolCallID != "spawn-call-1" || child.ParentTool.ToolName != "SPAWN" {
		t.Fatalf("child parent relation = %#v, want Spawn call", child.ParentTool)
	}
	if child.Delivery == nil || child.Delivery.Mode != eventstream.DeliveryMirror || child.Position == nil || child.Position.Durable == nil || child.Cursor == "" {
		t.Fatalf("child durable delivery = %#v position=%#v cursor=%q", child.Delivery, child.Position, child.Cursor)
	}
	if child.Final {
		t.Fatalf("running child narrative is final: %#v", child)
	}
	update, ok := child.Update.(schema.ContentChunk)
	if !ok || update.SessionUpdate != schema.UpdateAgentMessage || update.MessageID != "child-message-1" {
		t.Fatalf("child update = %#v, want message child-message-1", child.Update)
	}
}

func TestLiveFeedBrokerRetriesChildSnapshotWithoutAdvancingCursorAfterRecorderFailure(t *testing.T) {
	ctx := context.Background()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{SessionIDGenerator: func() string { return "session-1" }}))
	if _, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "session-1",
	}); err != nil {
		t.Fatal(err)
	}
	appender := &recoverableChildAppender{delegate: sessions}
	source := make(chan eventstream.Envelope, 2)
	streams := newBrokerReadStreamService()
	turn := newGatewayTurn(
		newBrokerTestHandle(source),
		func() stream.Service { return streams },
		internalcontrolclient.NewChildRecorder(appender),
	)
	events := turn.Events()

	source <- brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
	receiveBrokerEnvelope(t, events)
	firstRead := waitBrokerRead(t, streams.reads, "first child mirror Read")
	frame := stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Cursor:  stream.Cursor{Events: 1},
		Running: true,
		Event:   brokerChildMessageEvent("child-message-1", "retry me"),
	}
	respondBrokerRead(t, firstRead, stream.Snapshot{
		Ref: firstRead.Request.Ref, Cursor: stream.Cursor{Events: 1}, Running: true, Frames: []stream.Frame{frame},
	})

	retryRead := waitBrokerRead(t, streams.reads, "child mirror retry Read")
	if retryRead.Request.Cursor.Events != 0 {
		t.Fatalf("recorder failure advanced source cursor to %+v, want unchanged cursor", retryRead.Request.Cursor)
	}
	respondBrokerRead(t, retryRead, stream.Snapshot{
		Ref: retryRead.Request.Ref, Cursor: stream.Cursor{Events: 1}, Running: true, Frames: []stream.Frame{frame},
	})
	child := receiveBrokerEnvelope(t, events)
	if child.Err != nil || child.Kind == eventstream.KindError {
		t.Fatalf("retried child projection = %#v, want semantic envelope", child)
	}
	if child.Scope != eventstream.ScopeSubagent || child.ScopeID != "task-1" || child.ParentTool == nil || child.ParentTool.ToolCallID != "spawn-call-1" {
		t.Fatalf("retried child relation = %#v, want task-1 under spawn-call-1", child)
	}
	if child.Delivery == nil || child.Delivery.Mode != eventstream.DeliveryMirror || child.EventID == "" {
		t.Fatalf("retried child delivery = %#v event=%q, want durable mirror", child.Delivery, child.EventID)
	}

	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(12, 0))
	close(source)
	finalRead := waitBrokerRead(t, streams.reads, "recorder retry final Read")
	respondBrokerRead(t, finalRead, stream.Snapshot{
		Ref: finalRead.Request.Ref, Cursor: stream.Cursor{Events: 1}, Running: true,
	})
	assertBrokerMainTerminal(t, receiveBrokerEnvelope(t, events))
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
	if calls := appender.calls.Load(); calls != 2 {
		t.Fatalf("child appender calls = %d, want one recoverable failure and one successful retry", calls)
	}
}

func TestLiveFeedBrokerFailsTurnAfterBoundedPermanentRecorderErrors(t *testing.T) {
	source := make(chan eventstream.Envelope, 1)
	streams := newBrokerReadStreamService()
	handle := newBrokerTestHandle(source)
	producerRelease := make(chan struct{})
	var closeSource sync.Once
	handle.cancelFn = func() {
		go func() {
			<-producerRelease
			closeSource.Do(func() { close(source) })
		}()
	}
	turn := newGatewayTurn(
		handle,
		func() stream.Service { return streams },
		internalcontrolclient.NewChildRecorder(permanentChildAppender{}),
	)
	events := turn.Events()

	source <- brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
	assertBrokerToolCallID(t, receiveBrokerEnvelope(t, events), "spawn-call-1")
	frame := stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Cursor:  stream.Cursor{Events: 1},
		Running: true,
		Event:   brokerChildMessageEvent("child-message-1", "cannot persist"),
	}
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		read := waitBrokerRead(t, streams.reads, fmt.Sprintf("permanent recorder attempt %d", attempt))
		if read.Request.Cursor.Events != 0 {
			t.Fatalf("attempt %d cursor = %+v, want unadvanced cursor", attempt, read.Request.Cursor)
		}
		respondBrokerRead(t, read, stream.Snapshot{
			Ref: read.Request.Ref, Cursor: stream.Cursor{Events: 1}, Running: true, Frames: []stream.Frame{frame},
		})
	}

	deadline := time.Now().Add(time.Second)
	for handle.cancelCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("delivery failure cancelled Runtime %d times, want one", calls)
	}
	select {
	case envelope := <-events:
		t.Fatalf("delivery failure crossed producer barrier early: %#v", envelope)
	case <-time.After(30 * time.Millisecond):
	}
	close(producerRelease)

	failure := receiveBrokerEnvelope(t, events)
	if failure.Kind != eventstream.KindError || failure.Err == nil {
		t.Fatalf("delivery failure = %#v, want typed error", failure)
	}
	terminal := receiveBrokerEnvelope(t, events)
	if !eventstream.IsTerminalLifecycle(terminal) || terminal.Lifecycle.State != eventstream.LifecycleStateFailed {
		t.Fatalf("delivery terminal = %#v, want failed", terminal)
	}
	if terminal.HandleID != "handle-1" || terminal.RunID != "run-1" || terminal.TurnID != "turn-1" {
		t.Fatalf("delivery terminal identity = (%q,%q,%q)", terminal.HandleID, terminal.RunID, terminal.TurnID)
	}
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)
	assertBrokerReadServiceStopped(t, streams)
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("delivery failure cancelled Runtime %d times after barrier, want one", calls)
	}
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

func TestLiveFeedBrokerSharesPhysicalSpawnStreamAcrossTaskWaitObservers(t *testing.T) {
	ctx := context.Background()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "session-1" },
	}))
	parent, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	mainEvents := make(chan eventstream.Envelope, 8)
	streams := newBrokerTestStreamService()
	turn := newGatewayTurn(
		newBrokerTestHandle(mainEvents),
		func() stream.Service { return streams },
		internalcontrolclient.NewChildRecorder(sessions),
	)
	events := turn.Events()

	mainEvents <- brokerRunningSubagentEnvelope("SPAWN", "spawn-call-1", "", "task-1", "spawn-terminal-1", "task-1:1", 0)
	assertBrokerToolCallID(t, receiveBrokerEnvelope(t, events), "spawn-call-1")
	waitBrokerSignal(t, streams.started, "physical Spawn stream start")

	const childFrames = 3
	for index := 1; index <= childFrames; index++ {
		id := fmt.Sprintf("child-message-%d", index)
		streams.frames <- stream.Frame{
			Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
			Cursor:  stream.Cursor{Events: int64(index)},
			Running: true,
			Event:   brokerChildMessageEvent(id, fmt.Sprintf("chunk-%d", index)),
		}
		child := receiveBrokerEnvelope(t, events)
		if child.ParentTool == nil || child.ParentTool.ToolCallID != "spawn-call-1" {
			t.Fatalf("child %d parent = %#v, want canonical Spawn", index, child.ParentTool)
		}
	}

	for index := 1; index <= 3; index++ {
		callID := fmt.Sprintf("task-wait-%d", index)
		mainEvents <- brokerRunningSubagentEnvelope(
			"TASK", callID, "wait", "task-1", "spawn-terminal-1", "task-1:1", childFrames,
		)
		assertBrokerToolCallID(t, receiveBrokerEnvelope(t, events), callID)
	}
	if got := brokerSourceCount(turn.feed); got != 1 {
		t.Fatalf("physical child stream sources = %d, want one across Spawn and three TASK waits", got)
	}

	streams.frames <- stream.Frame{
		Ref:    brokerStreamRef("task-1", "spawn-terminal-1"),
		Cursor: stream.Cursor{Events: childFrames},
		Closed: true,
		State:  "completed",
	}
	assertBrokerToolCallID(t, receiveBrokerEnvelope(t, events), "spawn-call-1")
	mainEvents <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(13, 0))
	close(mainEvents)
	assertBrokerMainTerminal(t, receiveBrokerEnvelope(t, events))
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, turn.feed)

	page, err := sessions.EventsPage(ctx, session.EventPageRequest{
		SessionRef: parent.SessionRef, Visibility: session.EventPageClientReplay,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != childFrames {
		t.Fatalf("durable child mirrors = %d, want %d (not %d observer copies)", len(page.Events), childFrames, childFrames*4)
	}
	seen := map[string]struct{}{}
	for _, event := range page.Events {
		if event.ChildOrigin == nil || event.ChildOrigin.ParentTool.CallID != "spawn-call-1" || event.ChildOrigin.ParentTool.Name != "SPAWN" {
			t.Fatalf("durable child origin = %#v, want canonical Spawn relation", event.ChildOrigin)
		}
		if _, duplicate := seen[event.ChildOrigin.SourceEventID]; duplicate {
			t.Fatalf("duplicate child source identity %q", event.ChildOrigin.SourceEventID)
		}
		seen[event.ChildOrigin.SourceEventID] = struct{}{}
	}
}

func TestControlSessionFeedRecoversDetachedChildGapAcrossTurnBrokers(t *testing.T) {
	ctx := context.Background()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "session-1" },
	}))
	parent, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := internalcontrolclient.NewFeedBroker(internalcontrolclient.FeedBrokerConfig{
		SessionRef: parent.SessionRef, Reader: sessions, CursorCodec: codec, SubscriberQueue: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Close()
	live, err := feed.SubscribeFromNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()

	streams := newBrokerReadStreamService()
	recorder := internalcontrolclient.NewChildRecorder(sessions)
	firstMain := make(chan eventstream.Envelope, 2)
	firstIngress := newLiveFeedBroker(
		newBrokerTestHandleForTurn(firstMain, "handle-1", "run-1", "turn-1"),
		func() stream.Service { return streams },
		recorder,
	)
	firstAttachment := feed.Attach(firstIngress.Events())

	firstMain <- brokerRunningSubagentEnvelope(
		"SPAWN", "spawn-call-1", "", "task-1", "spawn-terminal-1", "task-1:1", 0,
	)
	assertBrokerToolCallID(t, receiveBrokerEnvelope(t, live.Events()), "spawn-call-1")
	firstRead := waitBrokerRead(t, streams.reads, "first Turn child Read")
	if firstRead.Request.Cursor != (stream.Cursor{}) {
		t.Fatalf("first Turn child cursor = %+v, want zero", firstRead.Request.Cursor)
	}
	firstFrame := stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Cursor:  stream.Cursor{Events: 1},
		Running: true,
		Event:   brokerChildMessageEvent("child-message-1", "first"),
	}
	respondBrokerRead(t, firstRead, stream.Snapshot{
		Ref: firstRead.Request.Ref, Cursor: stream.Cursor{Events: 1}, Running: true, Frames: []stream.Frame{firstFrame},
	})
	firstChild := receiveBrokerEnvelope(t, live.Events())
	assertBrokerDurableChildRelation(t, firstChild, "spawn-call-1")

	firstMain <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(17, 0))
	close(firstMain)
	firstFinalRead := waitBrokerRead(t, streams.reads, "first Turn final child Read")
	if firstFinalRead.Request.Cursor.Events != 1 {
		t.Fatalf("first Turn final cursor = %+v, want accepted child event 1", firstFinalRead.Request.Cursor)
	}
	respondBrokerRead(t, firstFinalRead, stream.Snapshot{
		Ref: firstFinalRead.Request.Ref, Cursor: firstFinalRead.Request.Cursor, Running: true,
	})
	assertBrokerMainTerminal(t, receiveBrokerEnvelope(t, live.Events()))
	waitBrokerDone(t, firstIngress)
	waitBrokerAttachment(t, firstAttachment, "first Turn feed attachment")

	// The detached child materializes events 2 and 3 after the first Turn's
	// final-read barrier. A later TASK wait reports cursor 3, but that cursor is
	// Runtime's current position rather than Control's last accepted position.
	secondMain := make(chan eventstream.Envelope, 2)
	secondIngress := newLiveFeedBroker(
		newBrokerTestHandleForTurn(secondMain, "handle-2", "run-2", "turn-2"),
		func() stream.Service { return streams },
		recorder,
	)
	secondAttachment := feed.Attach(secondIngress.Events())
	secondStart := brokerRunningSubagentEnvelope(
		"TASK", "task-wait-1", "wait", "task-1", "spawn-terminal-1", "task-1:1", 3,
	)
	secondStart.HandleID = "handle-2"
	secondStart.RunID = "run-2"
	secondStart.TurnID = "turn-2"
	secondMain <- secondStart
	assertBrokerToolCallID(t, receiveBrokerEnvelope(t, live.Events()), "task-wait-1")

	secondRead := waitBrokerRead(t, streams.reads, "second Turn replay child Read")
	if secondRead.Request.Cursor != (stream.Cursor{}) {
		t.Fatalf("second Turn child cursor = %+v, want replay-safe zero instead of Runtime cursor 3", secondRead.Request.Cursor)
	}
	secondFrame := stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Cursor:  stream.Cursor{Events: 3},
		Running: true,
		Event:   brokerChildMessageEvent("child-message-2", "second"),
	}
	thirdFrame := stream.Frame{
		Ref:     brokerStreamRef("task-1", "spawn-terminal-1"),
		Cursor:  stream.Cursor{Events: 3},
		Running: false,
		Event:   brokerChildMessageEvent("child-message-3", "third"),
	}
	respondBrokerRead(t, secondRead, stream.Snapshot{
		Ref: secondRead.Request.Ref, Cursor: stream.Cursor{Events: 3}, State: "completed", Running: false,
		FinalText: "third", Frames: []stream.Frame{firstFrame, secondFrame, thirdFrame},
	})

	// The shared Session feed suppresses replayed event 1 by durable position;
	// only the gap is published before the original Spawn receives its close.
	secondChild := receiveBrokerEnvelope(t, live.Events())
	thirdChild := receiveBrokerEnvelope(t, live.Events())
	for index, child := range []eventstream.Envelope{secondChild, thirdChild} {
		assertBrokerDurableChildRelation(t, child, "spawn-call-1")
		update, ok := child.Update.(schema.ContentChunk)
		wantID := fmt.Sprintf("child-message-%d", index+2)
		if !ok || update.MessageID != wantID {
			t.Fatalf("recovered child %d = %#v, want %q", index+2, child.Update, wantID)
		}
	}
	spawnClose := receiveBrokerEnvelope(t, live.Events())
	assertBrokerToolCallID(t, spawnClose, "spawn-call-1")
	closeUpdate := spawnClose.Update.(schema.ToolCallUpdate)
	if closeUpdate.Status == nil || *closeUpdate.Status != schema.ToolStatusCompleted {
		t.Fatalf("recovered Spawn close = %#v, want completed", closeUpdate)
	}

	secondMain <- eventstream.TurnCompleted("handle-2", "run-2", "turn-2", time.Unix(18, 0))
	close(secondMain)
	secondTerminal := receiveBrokerEnvelope(t, live.Events())
	if !eventstream.IsTerminalLifecycle(secondTerminal) || secondTerminal.TurnID != "turn-2" {
		t.Fatalf("second Turn terminal = %#v", secondTerminal)
	}
	waitBrokerDone(t, secondIngress)
	waitBrokerAttachment(t, secondAttachment, "second Turn feed attachment")
	assertBrokerReadServiceStopped(t, streams)

	page, err := sessions.EventsPage(ctx, session.EventPageRequest{
		SessionRef: parent.SessionRef, Visibility: session.EventPageClientReplay,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 {
		t.Fatalf("durable child mirrors = %d, want recovered events 1..3 exactly once", len(page.Events))
	}
	for index, event := range page.Events {
		if event.ChildOrigin == nil || event.ChildOrigin.SourceEventID != fmt.Sprintf("session-1|task-1|spawn-terminal-1|spawn-call-1:%d", index+1) {
			t.Fatalf("durable child %d origin = %#v, want stable source sequence", index+1, event.ChildOrigin)
		}
	}
}

func TestLiveFeedBrokerPromotesBackgroundCommandWaitToRunCommandOwner(t *testing.T) {
	mainEvents := make(chan eventstream.Envelope, 2)
	streams := newBrokerReadStreamService()
	ingress := newLiveFeedBroker(
		newBrokerTestHandle(mainEvents),
		func() stream.Service { return streams },
	)
	events := ingress.Events()

	mainEvents <- brokerRunningCommandWaitEnvelope(
		"task-wait-1", "command-task-1", "command-terminal-1", "run-command-call-1", 91,
	)
	assertBrokerToolCallID(t, receiveBrokerEnvelope(t, events), "task-wait-1")
	firstRead := waitBrokerRead(t, streams.reads, "background command replay Read")
	if firstRead.Request.Cursor != (stream.Cursor{}) {
		t.Fatalf("background command cursor = %+v, want replay-safe zero instead of Runtime cursor 91", firstRead.Request.Cursor)
	}

	first := "\x1b[31m中"
	second := "文\x1b[0m\n"
	respondBrokerRead(t, firstRead, stream.Snapshot{
		Ref: firstRead.Request.Ref, Cursor: stream.Cursor{Output: int64(len([]byte(first)))}, Running: true,
		Frames: []stream.Frame{{
			Ref: firstRead.Request.Ref, Text: first,
			Cursor: stream.Cursor{Output: int64(len([]byte(first)))}, Running: true,
		}},
	})
	firstOutput := receiveBrokerEnvelope(t, events)
	assertBrokerTerminalFrame(t, firstOutput, "run-command-call-1", first, schema.ToolStatusInProgress)
	if firstOutput.ParentTool != nil {
		t.Fatalf("command output has erroneous parent relation %#v", firstOutput.ParentTool)
	}

	finalRead := waitBrokerRead(t, streams.reads, "background command completion Read")
	if finalRead.Request.Cursor.Output != int64(len([]byte(first))) {
		t.Fatalf("command completion cursor = %+v, want first UTF-8 byte boundary", finalRead.Request.Cursor)
	}
	exitCode := 7
	respondBrokerRead(t, finalRead, stream.Snapshot{
		Ref: finalRead.Request.Ref, Cursor: stream.Cursor{Output: int64(len([]byte(first + second)))},
		State: "failed", Running: false, ExitCode: &exitCode,
		Frames: []stream.Frame{{
			Ref: finalRead.Request.Ref, Text: second,
			Cursor: stream.Cursor{Output: int64(len([]byte(first + second)))}, Running: false,
		}},
	})
	secondOutput := receiveBrokerEnvelope(t, events)
	assertBrokerTerminalFrame(t, secondOutput, "run-command-call-1", second, schema.ToolStatusInProgress)
	final := receiveBrokerEnvelope(t, events)
	assertBrokerTerminalFrame(t, final, "run-command-call-1", "", schema.ToolStatusFailed)
	exit, ok := metautil.TerminalExit(final.Update.(schema.ToolCallUpdate).Meta)
	if !ok || exit.TerminalID != "run-command-call-1" || exit.ExitCode == nil || *exit.ExitCode != exitCode {
		t.Fatalf("background command exit = %#v, %v; want RunCommand exit %d", exit, ok, exitCode)
	}

	mainEvents <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(19, 0))
	close(mainEvents)
	assertBrokerMainTerminal(t, receiveBrokerEnvelope(t, events))
	requireBrokerChannelClosed(t, events)
	waitBrokerDone(t, ingress)
	assertBrokerReadServiceStopped(t, streams)
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
	if request.Cursor != (stream.Cursor{}) {
		t.Fatalf("command subscription cursor = %+v, want replay-safe zero", request.Cursor)
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
	if initial.Request.Cursor != (stream.Cursor{}) {
		t.Fatalf("initial command Read cursor = %+v, want replay-safe zero", initial.Request.Cursor)
	}
	const prefix = "prefix\n"
	respondBrokerRead(t, initial, stream.Snapshot{
		Ref: initial.Request.Ref, Cursor: stream.Cursor{Output: int64(len(prefix))}, Running: true,
		Frames: []stream.Frame{{
			Ref: initial.Request.Ref, Text: prefix,
			Cursor: stream.Cursor{Output: int64(len(prefix))}, Running: true,
		}},
	})
	prefixOutput := receiveBrokerEnvelope(t, events)
	assertBrokerTerminalFrame(t, prefixOutput, "command-call-1", prefix, schema.ToolStatusInProgress)

	first := "\x1b[31m中"
	second := "文\x1b[0m\n"
	exitCode := 7
	source <- eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(16, 0))
	close(source)
	finalRead := waitBrokerRead(t, streams.reads, "final command Read")
	if finalRead.Request.Cursor.Output != int64(len(prefix)) {
		t.Fatalf("final command Read cursor = %+v, want delivered prefix cursor %d", finalRead.Request.Cursor, len(prefix))
	}
	respondBrokerRead(t, finalRead, stream.Snapshot{
		Ref:      finalRead.Request.Ref,
		Cursor:   stream.Cursor{Output: int64(len(prefix + first + second)), Events: 2},
		State:    "failed",
		Running:  false,
		ExitCode: &exitCode,
		Frames: []stream.Frame{
			{
				Ref:     brokerStreamRef("command-task-1", "command-terminal-1"),
				Text:    first,
				Cursor:  stream.Cursor{Output: int64(len(prefix + first)), Events: 1},
				Running: true,
			},
			{
				Ref:     brokerStreamRef("command-task-1", "command-terminal-1"),
				Text:    second,
				Cursor:  stream.Cursor{Output: int64(len(prefix + first + second)), Events: 2},
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
		read := waitBrokerRead(t, streams.reads, "blocking task Read before cancel")
		turn.Cancel()
		respondBrokerRead(t, read, stream.Snapshot{
			Ref: read.Request.Ref, Cursor: read.Request.Cursor, State: "cancelled",
		})

		var terminal eventstream.Envelope
		for !eventstream.IsTerminalLifecycle(terminal) {
			terminal = receiveBrokerEnvelope(t, events)
		}
		if terminal.Lifecycle.State != eventstream.LifecycleStateCancelled {
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

func TestLiveFeedBrokerSourceFailureWaitsForProducerCloseBarrier(t *testing.T) {
	source := make(chan eventstream.Envelope, 1)
	handle := newBrokerTestHandle(source)
	producerRelease := make(chan struct{})
	var closeSource sync.Once
	handle.cancelFn = func() {
		go func() {
			<-producerRelease
			closeSource.Do(func() { close(source) })
		}()
	}
	streams := newBrokerReadStreamService()
	turn := newGatewayTurn(handle, func() stream.Service { return streams })
	defer turn.Close()
	events := turn.Events()

	source <- brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
	receiveBrokerEnvelope(t, events)
	for attempt := 1; attempt <= 3; attempt++ {
		read := waitBrokerRead(t, streams.reads, fmt.Sprintf("failing source Read %d", attempt))
		read.response <- brokerReadResult{err: errors.New("permanent task delivery failure")}
	}
	deadline := time.Now().Add(time.Second)
	for handle.cancelCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("source delivery failure cancelled owning Runtime %d times, want one", calls)
	}
	select {
	case envelope := <-events:
		t.Fatalf("source delivery failure crossed producer barrier early: %#v", envelope)
	case <-time.After(30 * time.Millisecond):
	}
	close(producerRelease)

	failure := receiveBrokerEnvelope(t, events)
	if failure.Kind != eventstream.KindError || failure.Err == nil || !strings.Contains(failure.Err.Error(), "permanent task delivery failure") {
		t.Fatalf("source delivery failure envelope = %#v", failure)
	}
	terminal := receiveBrokerEnvelope(t, events)
	if terminal.Lifecycle == nil || terminal.Lifecycle.State != eventstream.LifecycleStateFailed {
		t.Fatalf("source delivery terminal = %#v, want failed", terminal)
	}
	requireBrokerChannelClosed(t, events)
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("source delivery failure cancelled Runtime %d times after close, want one", calls)
	}
	waitBrokerDone(t, turn.feed)
	assertBrokerReadServiceStopped(t, streams)
}

func TestGatewayTurnCancelRacingSourceFailureUsesOneAuthoritativeTerminal(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
		CursorCodec: codec, SubscriberQueue: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := feed.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	prepared, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	observer, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	observerTerminals := make(chan eventstream.Envelope, 2)
	go func() {
		for envelope := range observer.Events() {
			if eventstream.IsTerminalLifecycle(envelope) && envelope.HandleID == "handle-1" {
				observerTerminals <- envelope
			}
		}
	}()

	source := make(chan eventstream.Envelope, 1)
	handle := newBrokerTestHandle(source)
	producerRelease := make(chan struct{})
	var closeSource sync.Once
	handle.cancelFn = func() {
		go func() {
			<-producerRelease
			closeSource.Do(func() { close(source) })
		}()
	}
	streams := newBrokerReadStreamService()
	ingress := newLiveFeedBroker(handle, func() stream.Service { return streams })
	turn := &gatewayTurn{
		handle:       handle,
		feed:         ingress,
		sessionFeed:  feed,
		subscription: prepared,
	}
	turn.attach = func() <-chan error { return feed.AttachTo(prepared, ingress.Events()) }
	defer turn.Close()
	events := turn.Events()

	source <- brokerRunningToolEnvelope("SPAWN", "spawn-call-1", "task-1", "spawn-terminal-1")
	receiveBrokerEnvelope(t, events)
	for attempt := 1; attempt <= 2; attempt++ {
		read := waitBrokerRead(t, streams.reads, fmt.Sprintf("pre-cancel source failure %d", attempt))
		read.response <- brokerReadResult{err: errors.New("racing permanent source failure")}
	}
	third := waitBrokerRead(t, streams.reads, "racing final source failure")
	turn.Cancel()
	third.response <- brokerReadResult{err: errors.New("racing permanent source failure")}

	deadline := time.Now().Add(time.Second)
	for handle.cancelCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("racing stop causes cancelled Runtime %d times, want one", calls)
	}
	select {
	case envelope := <-events:
		t.Fatalf("racing source failure crossed producer barrier early: %#v", envelope)
	case <-time.After(30 * time.Millisecond):
	}
	close(producerRelease)

	failure := receiveBrokerEnvelope(t, events)
	if failure.Kind != eventstream.KindError || failure.Err == nil || !strings.Contains(failure.Err.Error(), "racing permanent source failure") {
		t.Fatalf("racing source failure envelope = %#v", failure)
	}
	localTerminal := receiveBrokerEnvelope(t, events)
	if localTerminal.Lifecycle == nil || localTerminal.Lifecycle.State != eventstream.LifecycleStateFailed {
		t.Fatalf("local racing terminal = %#v, want authoritative failed", localTerminal)
	}
	requireBrokerChannelClosed(t, events)
	select {
	case siblingTerminal := <-observerTerminals:
		if siblingTerminal.Lifecycle == nil || siblingTerminal.Lifecycle.State != eventstream.LifecycleStateFailed ||
			siblingTerminal.HandleID != localTerminal.HandleID || siblingTerminal.RunID != localTerminal.RunID || siblingTerminal.TurnID != localTerminal.TurnID {
			t.Fatalf("sibling racing terminal = %#v, local = %#v", siblingTerminal, localTerminal)
		}
	case <-time.After(time.Second):
		t.Fatal("sibling did not receive racing source failure terminal")
	}
	select {
	case duplicate := <-observerTerminals:
		t.Fatalf("sibling received duplicate racing terminal: %#v", duplicate)
	case <-time.After(30 * time.Millisecond):
	}
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("racing stop causes cancelled Runtime %d times after terminal, want one", calls)
	}
}

func TestGatewayTurnSlowTargetSharesInterruptedTerminalAfterProducerBarrier(t *testing.T) {
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
		CursorCodec: codec, SubscriberQueue: 1, SubscriberStallTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(session.SessionRef{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := feed.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	prepared, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	source := make(chan eventstream.Envelope, 6)
	for index := 0; index < 6; index++ {
		source <- brokerMainMessageEnvelope(fmt.Sprintf("slow-%d", index))
	}
	handle := newBrokerTestHandle(source)
	producerRelease := make(chan struct{})
	var closeSource sync.Once
	handle.cancelFn = func() {
		go func() {
			<-producerRelease
			closeSource.Do(func() { close(source) })
		}()
	}
	driver := &Adapter{stack: &RuntimeStack{ControlFeeds: feeds}}
	turn := driver.newGatewayTurnWithSubscription(handle, prepared, true)
	defer turn.Close()
	events := turn.Events()
	receiveBrokerEnvelope(t, events)
	deadline := time.Now().Add(time.Second)
	for !errors.Is(prepared.Err(), controlclientport.ErrSlowConsumer) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !errors.Is(prepared.Err(), controlclientport.ErrSlowConsumer) {
		t.Fatalf("prepared target error = %v, want slow consumer", prepared.Err())
	}
	// Release the relay's blocked second live send so it can observe the typed
	// target disconnect and enter the producer-barriered interrupted path.
	receiveBrokerEnvelope(t, events)
	deadline = time.Now().Add(time.Second)
	for handle.cancelCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("slow target cancelled Runtime %d times, want one", calls)
	}

	observer, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	close(producerRelease)
	failure := receiveBrokerEnvelope(t, events)
	if failure.Kind != eventstream.KindError || !errors.Is(failure.Err, controlclientport.ErrSlowConsumer) {
		t.Fatalf("slow target local error = %#v", failure)
	}
	localTerminal := receiveBrokerEnvelope(t, events)
	if localTerminal.Lifecycle == nil || localTerminal.Lifecycle.State != eventstream.LifecycleStateInterrupted {
		t.Fatalf("slow target local terminal = %#v, want interrupted", localTerminal)
	}
	requireBrokerChannelClosed(t, events)

	var siblingTerminal eventstream.Envelope
	for !eventstream.IsTerminalLifecycle(siblingTerminal) {
		siblingTerminal = receiveBrokerEnvelope(t, observer.Events())
	}
	if siblingTerminal.Lifecycle.State != eventstream.LifecycleStateInterrupted ||
		siblingTerminal.HandleID != localTerminal.HandleID || siblingTerminal.RunID != localTerminal.RunID || siblingTerminal.TurnID != localTerminal.TurnID {
		t.Fatalf("slow target sibling terminal = %#v, local = %#v", siblingTerminal, localTerminal)
	}
	select {
	case duplicate := <-observer.Events():
		if eventstream.IsTerminalLifecycle(duplicate) {
			t.Fatalf("slow target sibling received duplicate terminal: %#v", duplicate)
		}
	case <-time.After(30 * time.Millisecond):
	}
}

type brokerTestHandle struct {
	events        <-chan eventstream.Envelope
	eventsStarted chan struct{}
	eventsOnce    sync.Once
	handleID      string
	runID         string
	turnID        string
	cancelFn      func()
	closeFn       func() error
	cancelCalls   atomic.Int32
	closeCalls    atomic.Int32
}

type recoverableChildAppender struct {
	delegate session.EventAppender
	calls    atomic.Int32
}

type gatewayCancellablePageReader struct {
	blockAt   int32
	calls     atomic.Int32
	started   chan struct{}
	exited    chan struct{}
	startOnce sync.Once
	exitOnce  sync.Once
}

func newGatewayCancellablePageReader(blockAt int32) *gatewayCancellablePageReader {
	return &gatewayCancellablePageReader{
		blockAt: blockAt,
		started: make(chan struct{}),
		exited:  make(chan struct{}),
	}
}

func (r *gatewayCancellablePageReader) EventsPage(ctx context.Context, _ session.EventPageRequest) (session.EventPage, error) {
	if r.calls.Add(1) != r.blockAt {
		return session.EventPage{}, nil
	}
	r.startOnce.Do(func() { close(r.started) })
	<-ctx.Done()
	r.exitOnce.Do(func() { close(r.exited) })
	return session.EventPage{}, ctx.Err()
}

type permanentChildAppender struct{}

func (permanentChildAppender) AppendEvent(context.Context, session.AppendEventRequest) (*session.Event, error) {
	return nil, errors.New("permanent child recorder failure")
}

func (a *recoverableChildAppender) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if a.calls.Add(1) == 1 {
		return nil, errors.New("recoverable child recorder failure")
	}
	return a.delegate.AppendEvent(ctx, req)
}

func newBrokerTestHandle(events <-chan eventstream.Envelope) *brokerTestHandle {
	return newBrokerTestHandleForTurn(events, "handle-1", "run-1", "turn-1")
}

func newBrokerTestHandleForTurn(events <-chan eventstream.Envelope, handleID string, runID string, turnID string) *brokerTestHandle {
	return &brokerTestHandle{events: events, handleID: handleID, runID: runID, turnID: turnID}
}

func (h *brokerTestHandle) HandleID() string { return h.handleID }
func (h *brokerTestHandle) RunID() string    { return h.runID }
func (h *brokerTestHandle) TurnID() string   { return h.turnID }
func (h *brokerTestHandle) SessionRef() session.SessionRef {
	return session.SessionRef{SessionID: "session-1"}
}
func (h *brokerTestHandle) CreatedAt() time.Time { return time.Time{} }
func (h *brokerTestHandle) ACPEvents() <-chan eventstream.Envelope {
	if h.eventsStarted != nil {
		h.eventsOnce.Do(func() { close(h.eventsStarted) })
	}
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

func brokerRunningSubagentEnvelope(
	toolName string,
	callID string,
	action string,
	taskID string,
	terminalID string,
	turnID string,
	eventCursor int,
) eventstream.Envelope {
	envelope := brokerRunningToolEnvelope(toolName, callID, taskID, terminalID)
	update := envelope.Update.(schema.ToolCallUpdate)
	update.RawInput = map[string]any{"action": action, "task_id": taskID}
	runtimeMeta := update.Meta["caelis"].(map[string]any)["runtime"].(map[string]any)
	taskMeta := runtimeMeta["task"].(map[string]any)
	taskMeta["kind"] = "subagent"
	taskMeta["turn_id"] = turnID
	taskMeta["event_cursor"] = int64(eventCursor)
	taskMeta["parent_call"] = "spawn-call-1"
	taskMeta["parent_tool"] = "SPAWN"
	if action != "" {
		runtimeMeta["tool"] = map[string]any{"name": toolName, "action": action, "target_kind": "subagent"}
	}
	update.Meta = map[string]any{"caelis": map[string]any{"runtime": runtimeMeta}}
	envelope.Update = update
	return envelope
}

func brokerRunningCommandWaitEnvelope(
	callID string,
	taskID string,
	terminalID string,
	parentCallID string,
	outputCursor int64,
) eventstream.Envelope {
	envelope := brokerRunningToolEnvelopeAtCursor("TASK", callID, taskID, terminalID, outputCursor)
	update := envelope.Update.(schema.ToolCallUpdate)
	update.RawInput = map[string]any{"action": "wait", "task_id": taskID}
	runtimeMeta := update.Meta["caelis"].(map[string]any)["runtime"].(map[string]any)
	taskMeta := runtimeMeta["task"].(map[string]any)
	taskMeta["kind"] = "command"
	taskMeta["parent_call"] = parentCallID
	runtimeMeta["tool"] = map[string]any{
		"name": "TASK", "action": "wait", "target_kind": "command",
	}
	update.Meta = map[string]any{"caelis": map[string]any{"runtime": runtimeMeta}}
	envelope.Update = update
	return envelope
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

func waitFeedBoundary(t *testing.T, feed controlclientport.SessionFeed, transientSequence uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		position, _ := feed.Boundary()
		if position != nil && position.Transient != nil && position.Transient.Sequence >= transientSequence {
			return
		}
		time.Sleep(time.Millisecond)
	}
	position, _ := feed.Boundary()
	t.Fatalf("Session feed boundary = %#v, want transient sequence >= %d", position, transientSequence)
}

func waitSubscriptionCursorChange(t *testing.T, subscription controlclientport.FeedSubscription, previous string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cursor := subscription.LastCursor()
		if cursor != "" && cursor != previous {
			return cursor
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("subscription cursor did not advance beyond %q", previous)
	return ""
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

func waitBrokerAttachment(t *testing.T, attachment <-chan error, name string) {
	t.Helper()
	select {
	case err, ok := <-attachment:
		if ok && err != nil {
			t.Fatalf("%s failed: %v", name, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
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

func assertBrokerDurableChildRelation(t *testing.T, env eventstream.Envelope, parentCallID string) {
	t.Helper()
	if env.Scope != eventstream.ScopeSubagent || env.ParentTool == nil || env.ParentTool.ToolCallID != parentCallID {
		t.Fatalf("durable child relation = %#v, want scoped relation to %q", env, parentCallID)
	}
	if env.Delivery == nil || env.Delivery.Mode != eventstream.DeliveryMirror || env.Position == nil || env.Position.Durable == nil {
		t.Fatalf("durable child delivery = %#v position=%#v, want mirror", env.Delivery, env.Position)
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
