package eval

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	internalcontrolclient "github.com/caelis-labs/caelis/internal/controlclient"
	"github.com/caelis-labs/caelis/internal/evalharness"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	tuiapp "github.com/caelis-labs/caelis/surfaces/tui/app"
)

func TestControlSessionFeedAdapterStreamsSpawnNarrativeIntoTUIAndReplaysEquivalently(t *testing.T) {
	ctx := context.Background()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return "session-1" },
	}))
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
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
		Reader: sessions, CursorCodec: codec, SubscriberQueue: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}

	handleEvents := make(chan eventstream.Envelope, 4)
	handle := &spawnFeedTurnHandle{events: handleEvents, ref: active.SessionRef}
	gatewayService := &spawnFeedGateway{active: active, handle: handle}
	driver, err := controladapter.NewAdapterForSession(ctx, &controladapter.RuntimeStack{
		Gateway: controladapter.GatewayRuntimeDeps{
			TurnServiceFn:         func() controladapter.GatewayTurnService { return gatewayService },
			ControlPlaneServiceFn: func() controladapter.GatewayControlPlaneService { return gatewayService },
			StreamProviderFn:      func() controladapter.GatewayStreamProvider { return gatewayService },
		},
		ControlFeeds: feeds,
		Session: controladapter.SessionRuntimeDeps{
			Store: sessions, AppName: "caelis", UserID: "user-1",
		},
	}, active, "tui", "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := driver.Submit(ctx, controladapter.Submission{Text: "spawn child"})
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()

	liveModel := newSpawnFeedTUIModel(t)
	handleEvents <- spawnFeedRunningEnvelope()
	liveModel = updateSpawnFeedTUIModel(t, liveModel, receiveSpawnFeedEnvelope(t, turn.Events(), "Spawn running update", func(envelope eventstream.Envelope) bool {
		call, ok := envelope.Update.(schema.ToolCall)
		return ok && call.ToolCallID == "spawn-call-1"
	}))

	recorder := internalcontrolclient.NewChildRecorder(sessions)
	first := recordSpawnFeedChild(t, ctx, recorder, active.SessionRef, "child-source-1", "m1", "first ")
	publishSpawnFeedEnvelope(t, feed, first)
	liveModel = updateSpawnFeedTUIModel(t, liveModel, receiveSpawnFeedEnvelope(t, turn.Events(), "first child chunk", func(envelope eventstream.Envelope) bool {
		chunk, ok := envelope.Update.(schema.ContentChunk)
		return ok && envelope.Scope == eventstream.ScopeSubagent && schema.ExtractTextValue(chunk.Content) == "first "
	}))
	firstFrame := evalharness.NormalizeFrame(liveModel.View().Content)
	if !strings.Contains(firstFrame, "first") || strings.Contains(firstFrame, "(wait subagent output)") {
		t.Fatalf("first child chunk was not visible before completion:\n%s", firstFrame)
	}

	second := recordSpawnFeedChild(t, ctx, recorder, active.SessionRef, "child-source-2", "m1", "second")
	publishSpawnFeedEnvelope(t, feed, second)
	liveModel = updateSpawnFeedTUIModel(t, liveModel, receiveSpawnFeedEnvelope(t, turn.Events(), "second child chunk", func(envelope eventstream.Envelope) bool {
		chunk, ok := envelope.Update.(schema.ContentChunk)
		return ok && envelope.Scope == eventstream.ScopeSubagent && schema.ExtractTextValue(chunk.Content) == "second"
	}))
	secondFrame := evalharness.NormalizeFrame(liveModel.View().Content)
	if !strings.Contains(secondFrame, "first second") {
		t.Fatalf("Spawn panel did not merge child chunks incrementally:\n%s", secondFrame)
	}

	childTerminal := eventstream.TurnLifecycle(
		"handle-1", "run-1", "child-turn-1", eventstream.LifecycleStateCompleted, "", "", time.Unix(303, 0),
	)
	childTerminal.SessionID = "session-1"
	childTerminal.Scope = eventstream.ScopeSubagent
	childTerminal.ScopeID = "task-1"
	childTerminal.ParentTool = &eventstream.ParentToolRelation{ToolCallID: "spawn-call-1", ToolName: "SPAWN"}
	publishSpawnFeedEnvelope(t, feed, childTerminal)
	liveModel = updateSpawnFeedTUIModel(t, liveModel, receiveSpawnFeedEnvelope(t, turn.Events(), "child terminal", func(envelope eventstream.Envelope) bool {
		return envelope.Scope == eventstream.ScopeSubagent && eventstream.IsTerminalLifecycle(envelope)
	}))

	parentContinues := recordSpawnFeedParentMessage(t, ctx, sessions, active.SessionRef, "parent continues after child")
	publishSpawnFeedEnvelope(t, feed, parentContinues)
	liveModel = updateSpawnFeedTUIModel(t, liveModel, receiveSpawnFeedEnvelope(t, turn.Events(), "parent continuation", func(envelope eventstream.Envelope) bool {
		chunk, ok := envelope.Update.(schema.ContentChunk)
		return ok && schema.ExtractTextValue(chunk.Content) == "parent continues after child"
	}))
	if frame := evalharness.NormalizeFrame(liveModel.View().Content); !strings.Contains(frame, "parent continues after child") {
		t.Fatalf("child terminal prevented later parent output:\n%s", frame)
	}

	parentFinal := recordSpawnFeedParentEvent(t, ctx, sessions, active.SessionRef, true)
	publishSpawnFeedEnvelope(t, feed, parentFinal)
	liveModel = updateSpawnFeedTUIModel(t, liveModel, receiveSpawnFeedEnvelope(t, turn.Events(), "parent Spawn result", func(envelope eventstream.Envelope) bool {
		update, ok := envelope.Update.(schema.ToolCallUpdate)
		return ok && update.ToolCallID == "spawn-call-1"
	}))
	mainTerminal := eventstream.TurnCompleted("handle-1", "run-1", "turn-1", time.Unix(305, 0))
	mainTerminal.SessionID = "session-1"
	mainTerminal.Scope = eventstream.ScopeMain
	mainTerminal.ScopeID = "session-1"
	publishSpawnFeedEnvelope(t, feed, mainTerminal)
	liveModel = updateSpawnFeedTUIModel(t, liveModel, receiveSpawnFeedEnvelope(t, turn.Events(), "parent terminal", func(envelope eventstream.Envelope) bool {
		return envelope.Scope == eventstream.ScopeMain && eventstream.IsTerminalLifecycle(envelope)
	}))
	liveFrame := evalharness.NormalizeFrame(liveModel.View().Content)
	if strings.Count(liveFrame, "first second") != 1 {
		t.Fatalf("live TUI did not retain exactly one Spawn output:\n%s", liveFrame)
	}

	replay, err := driver.Replay(ctx, eventstream.ReplayRequest{SessionID: "session-1", IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	replayModel := newSpawnFeedTUIModel(t)
	hasParentAnchor := false
	for _, envelope := range replay.Events {
		call, ok := envelope.Update.(schema.ToolCall)
		if ok && call.ToolCallID == "spawn-call-1" {
			hasParentAnchor = true
			break
		}
	}
	if !hasParentAnchor {
		replayModel = updateSpawnFeedTUIModel(t, replayModel, spawnFeedRunningEnvelope())
	}
	for _, envelope := range replay.Events {
		replayModel = updateSpawnFeedTUIModel(t, replayModel, envelope)
	}
	replayFrame := evalharness.NormalizeFrame(replayModel.View().Content)
	if normalizeSpawnFeedFrame(replayFrame) != normalizeSpawnFeedFrame(liveFrame) {
		t.Fatalf("live/replay TUI transcript differs:\n--- live ---\n%s\n--- replay ---\n%s", liveFrame, replayFrame)
	}
}

func normalizeSpawnFeedFrame(frame string) string {
	lines := strings.Split(frame, "\n")
	out := lines[:0]
	for _, line := range lines {
		if strings.Contains(line, "──") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func spawnFeedRunningEnvelope() eventstream.Envelope {
	return eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: "session-1",
		HandleID:  "handle-1",
		RunID:     "run-1",
		TurnID:    "turn-1",
		Scope:     eventstream.ScopeMain,
		ScopeID:   "session-1",
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "spawn-call-1",
			Title:         "SPAWN helper: inspect",
			Kind:          schema.ToolKindExecute,
			Status:        schema.ToolStatusInProgress,
			RawInput:      map[string]any{"agent": "helper", "prompt": "inspect"},
			Meta: metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
				metautil.RuntimeToolName: "SPAWN",
			}),
		},
	}
}

func newSpawnFeedTUIModel(t *testing.T) *tuiapp.Model {
	t.Helper()
	model := tuiapp.NewModel(tuiapp.Config{
		NoColor: true, NoAnimation: true,
		ExecuteLine: func(tuiapp.Submission) tuiapp.TaskResultMsg {
			return tuiapp.TaskResultMsg{ContinueRunning: true, SuppressTurnDivider: true}
		},
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	model = updated.(*tuiapp.Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: "spawn child"}))
	model = updated.(*tuiapp.Model)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	return updated.(*tuiapp.Model)
}

func updateSpawnFeedTUIModel(t *testing.T, model *tuiapp.Model, envelope eventstream.Envelope) *tuiapp.Model {
	t.Helper()
	updated, _ := model.Update(envelope)
	typed, ok := updated.(*tuiapp.Model)
	if !ok {
		t.Fatalf("TUI model = %T", updated)
	}
	updated, _ = typed.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	return updated.(*tuiapp.Model)
}

func recordSpawnFeedParentEvent(t *testing.T, ctx context.Context, sessions session.Service, ref session.SessionRef, final bool) eventstream.Envelope {
	t.Helper()
	eventType := session.EventTypeToolCall
	status := schema.ToolStatusInProgress
	if final {
		eventType = session.EventTypeToolResult
		status = schema.ToolStatusCompleted
	}
	event := &session.Event{
		Type:       eventType,
		Visibility: session.VisibilityCanonical,
		Tool: &session.EventTool{
			ID: "spawn-call-1", Name: "SPAWN", Kind: schema.ToolKindExecute,
			Title: "SPAWN helper: inspect", Status: status,
			Input: map[string]any{"agent": "helper", "prompt": "inspect"},
		},
	}
	if final {
		event.Tool.Output = map[string]any{"final_message": "done"}
	}
	stored, err := sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, Event: event})
	if err != nil {
		t.Fatal(err)
	}
	return projectSingleSpawnFeedEvent(t, ref, stored)
}

func recordSpawnFeedParentMessage(t *testing.T, ctx context.Context, sessions session.Service, ref session.SessionRef, text string) eventstream.Envelope {
	t.Helper()
	message := model.NewTextMessage(model.RoleAssistant, text)
	stored, err := sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref,
		Event: &session.Event{
			Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical,
			Message: &message, Text: text,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return projectSingleSpawnFeedEvent(t, ref, stored)
}

func recordSpawnFeedChild(t *testing.T, ctx context.Context, recorder *internalcontrolclient.ChildRecorder, ref session.SessionRef, sourceID, messageID, text string) eventstream.Envelope {
	t.Helper()
	stored, err := recorder.Record(ctx, internalcontrolclient.ChildRecordRequest{
		SessionRef: ref,
		Event: &session.Event{
			Type:       session.EventTypeAssistant,
			Visibility: session.VisibilityUIOnly,
			Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				MessageID:     messageID, Content: session.ProtocolTextContent(text),
			}},
		},
		Origin: session.EventChildOrigin{
			Scope: session.EventChildScopeSubagent, ScopeID: "task-1", TaskID: "task-1", DelegationID: "task-1",
			ParticipantID: "child-1", ACPSessionID: "child-session-1", SourceEventID: sourceID,
			ParentTool: session.EventParentTool{CallID: "spawn-call-1", Name: "SPAWN"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return projectSingleSpawnFeedEvent(t, ref, stored)
}

func projectSingleSpawnFeedEvent(t *testing.T, ref session.SessionRef, event *session.Event) eventstream.Envelope {
	t.Helper()
	base := acpprojector.EnvelopeBaseFromSessionEvent(ref, event, acpprojector.SessionEventTransport{
		HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1",
	})
	projected := acpprojector.ProjectSessionEventEnvelope(base, event)
	if len(projected) != 1 {
		t.Fatalf("projected envelopes = %#v, want one", projected)
	}
	return projected[0]
}

func publishSpawnFeedEnvelope(t *testing.T, feed interface {
	Publish(eventstream.Envelope) error
}, envelope eventstream.Envelope) {
	t.Helper()
	if err := feed.Publish(envelope); err != nil {
		t.Fatal(err)
	}
}

func receiveSpawnFeedEnvelope(t *testing.T, events <-chan eventstream.Envelope, name string, match func(eventstream.Envelope) bool) eventstream.Envelope {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case envelope, ok := <-events:
			if !ok {
				t.Fatalf("%s: adapter Turn closed", name)
			}
			if match(envelope) {
				return envelope
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", name)
			return eventstream.Envelope{}
		}
	}
}

type spawnFeedTurnHandle struct {
	events <-chan eventstream.Envelope
	ref    session.SessionRef
}

func (*spawnFeedTurnHandle) HandleID() string                 { return "handle-1" }
func (*spawnFeedTurnHandle) RunID() string                    { return "run-1" }
func (*spawnFeedTurnHandle) TurnID() string                   { return "turn-1" }
func (h *spawnFeedTurnHandle) SessionRef() session.SessionRef { return h.ref }
func (*spawnFeedTurnHandle) CreatedAt() time.Time             { return time.Unix(300, 0) }
func (h *spawnFeedTurnHandle) ACPEvents() <-chan eventstream.Envelope {
	return h.events
}
func (*spawnFeedTurnHandle) Submit(context.Context, gateway.SubmitRequest) error { return nil }
func (*spawnFeedTurnHandle) Cancel() agent.CancelResult                          { return agent.CancelResult{} }
func (*spawnFeedTurnHandle) Close() error                                        { return nil }

type spawnFeedGateway struct {
	active session.Session
	handle gateway.TurnHandle
}

func (g *spawnFeedGateway) BeginTurn(context.Context, gateway.BeginTurnRequest) (gateway.BeginTurnResult, error) {
	return gateway.BeginTurnResult{Session: g.active, Handle: g.handle}, nil
}
func (*spawnFeedGateway) SubmitActiveTurn(context.Context, gateway.SubmitActiveTurnRequest) error {
	return nil
}
func (*spawnFeedGateway) Interrupt(context.Context, gateway.InterruptRequest) error {
	return nil
}
func (*spawnFeedGateway) ActiveTurns() []gateway.ActiveTurnState { return nil }
func (*spawnFeedGateway) Streams() stream.Service                { return nil }
func (g *spawnFeedGateway) ControlPlaneState(context.Context, gateway.ControlPlaneStateRequest) (gateway.ControlPlaneState, error) {
	return gateway.ControlPlaneState{SessionRef: g.active.SessionRef}, nil
}
func (g *spawnFeedGateway) HandoffController(context.Context, gateway.HandoffControllerRequest) (session.Session, error) {
	return g.active, nil
}
func (g *spawnFeedGateway) AttachParticipant(context.Context, gateway.AttachParticipantRequest) (session.Session, error) {
	return g.active, nil
}
func (g *spawnFeedGateway) PromptParticipant(context.Context, gateway.PromptParticipantRequest) (gateway.BeginTurnResult, error) {
	return gateway.BeginTurnResult{Session: g.active, Handle: g.handle}, nil
}
func (g *spawnFeedGateway) StartParticipant(context.Context, gateway.StartParticipantRequest) (gateway.BeginTurnResult, error) {
	return gateway.BeginTurnResult{Session: g.active, Handle: g.handle}, nil
}
func (g *spawnFeedGateway) DetachParticipant(context.Context, gateway.DetachParticipantRequest) (session.Session, error) {
	return g.active, nil
}
