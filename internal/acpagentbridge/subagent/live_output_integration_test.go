package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	internalcontrolclient "github.com/caelis-labs/caelis/internal/controlclient"
	"github.com/caelis-labs/caelis/internal/controlclient/turningress"
	controlclientport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestExternalACPChildStreamsThroughRuntimeRecorderAndSessionFeedBeforeCompletion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "parent-session" },
	})
	sessions := store
	parent, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "parent-session",
		Workspace: session.WorkspaceRef{Key: "workspace-1", CWD: root},
	})
	if err != nil {
		t.Fatal(err)
	}

	external := &Runner{clock: time.Now}
	harness := &liveOutputSubagentHarness{
		ready: make(chan *childRun, 1),
	}
	parentRelease := make(chan struct{})
	parentWaiting := make(chan struct{})
	parentModel := &liveOutputParentModel{
		release: parentRelease,
		waiting: parentWaiting,
	}
	core, err := sdkruntime.New(sdkruntime.Config{
		Sessions:                sessions,
		AgentFactory:            chat.Factory{SystemPrompt: "Use the Spawn tool."},
		Subagents:               harness,
		TaskStore:               sessionfile.NewTaskStore(store),
		ControllerContextRouter: liveOutputContextRouter{},
		RunIDGenerator:          func() string { return "run-live-output" },
	})
	if err != nil {
		t.Fatal(err)
	}

	run, err := core.Run(ctx, agent.RunRequest{
		SessionRef: parent.SessionRef,
		Input:      "spawn a child",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: parentModel,
			Tools: []tool.Tool{spawn.New([]delegation.Agent{{Name: "helper"}})},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
		Reader: sessions, CursorCodec: codec, SubscriberQueue: 64,
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

	mainEvents := make(chan eventstream.Envelope, 32)
	handle := &liveOutputTurnHandle{
		events: mainEvents,
		ref:    parent.SessionRef,
		runID:  run.Handle.RunID(),
	}
	ingress := turningress.New(handle, core.Streams, internalcontrolclient.NewChildRecorder(sessions))
	feed.Attach(ingress.Events())
	anchorSeen := make(chan acpprojector.StreamRequest, 1)
	projectionDone := make(chan error, 1)
	go projectLiveOutputRuntimeEvents(run.Handle, handle, mainEvents, anchorSeen, projectionDone)

	child := receiveLiveOutputValue(t, harness.ready, "runtime Spawn child")
	request := receiveLiveOutputValue(t, anchorSeen, "production Spawn stream anchor")
	if request.CallID != "spawn-call-1" || request.ToolName != spawn.ToolName || request.Ref.TaskID == "" || request.Ref.TerminalID == "" {
		t.Fatalf("production Spawn stream request = %#v", request)
	}
	anchorEnvelope := receiveLiveOutputEnvelope(t, live.Events(), func(env eventstream.Envelope) bool {
		candidate, ok := turningress.StreamRequestFromACPEvent(env)
		return ok && candidate.CallID == "spawn-call-1"
	}, "production Spawn running update")
	anchorRequest, ok := turningress.StreamRequestFromACPEvent(anchorEnvelope)
	if !ok || anchorRequest.Ref.TaskID != request.Ref.TaskID || anchorRequest.Ref.TerminalID != request.Ref.TerminalID {
		t.Fatalf("feed Spawn stream request = %#v, want %#v", anchorRequest, request)
	}
	receiveLiveOutputSignal(t, parentWaiting, "parent model waiting after running Spawn result")

	updates := []struct {
		name                string
		update              any
		updateType          string
		messageID           string
		text                string
		toolCallID          string
		blankMessageIDDelta bool
		narrativeSequence   string
	}{
		{name: "m1 first delta", update: liveOutputContentChunk(schema.UpdateAgentMessage, "m1", "first "), updateType: schema.UpdateAgentMessage, messageID: "m1", text: "first "},
		{name: "m1 second delta", update: liveOutputContentChunk(schema.UpdateAgentMessage, "m1", "second"), updateType: schema.UpdateAgentMessage, messageID: "m1", text: "second"},
		{name: "thought", update: liveOutputContentChunk(schema.UpdateAgentThought, "thought-1", "reasoning"), updateType: schema.UpdateAgentThought, messageID: "thought-1", text: "reasoning"},
		{name: "tool call", update: client.ToolCall{SessionUpdate: schema.UpdateToolCall, ToolCallID: "child-call-1", Kind: "execute", Title: "inspect", Status: "pending"}, updateType: schema.UpdateToolCall, toolCallID: "child-call-1"},
		{name: "tool update running", update: client.ToolCallUpdate{SessionUpdate: schema.UpdateToolCallInfo, ToolCallID: "child-call-1", Kind: liveOutputString("execute"), Title: liveOutputString("inspect"), Status: liveOutputString("in_progress")}, updateType: schema.UpdateToolCallInfo, toolCallID: "child-call-1"},
		{name: "tool update completed", update: client.ToolCallUpdate{SessionUpdate: client.UpdateToolCallState, ToolCallID: "child-call-1", Kind: liveOutputString("execute"), Title: liveOutputString("inspect"), Status: liveOutputString("completed")}, updateType: client.UpdateToolCallState, toolCallID: "child-call-1"},
		{name: "plan", update: client.PlanUpdate{SessionUpdate: schema.UpdatePlan, Entries: []client.PlanEntry{{Content: "inspect", Status: "completed"}}}, updateType: schema.UpdatePlan},
		{name: "repeated delta first chunk", update: liveOutputContentChunk(schema.UpdateAgentMessage, "repeat-1", "ha"), updateType: schema.UpdateAgentMessage, messageID: "repeat-1", text: "ha", narrativeSequence: "repeated delta"},
		{name: "repeated delta identical chunk", update: liveOutputContentChunk(schema.UpdateAgentMessage, "repeat-1", "ha"), updateType: schema.UpdateAgentMessage, messageID: "repeat-1", text: "ha", narrativeSequence: "repeated delta"},
		{name: "prefix-growing delta first chunk", update: liveOutputContentChunk(schema.UpdateAgentMessage, "m2", "a"), updateType: schema.UpdateAgentMessage, messageID: "m2", text: "a", narrativeSequence: "prefix-growing delta"},
		{name: "prefix-growing delta second chunk", update: liveOutputContentChunk(schema.UpdateAgentMessage, "m2", "ab"), updateType: schema.UpdateAgentMessage, messageID: "m2", text: "ab", narrativeSequence: "prefix-growing delta"},
		{name: "blank message id thought barrier", update: liveOutputContentChunk(schema.UpdateAgentThought, "thought-2", "prepare final answer"), updateType: schema.UpdateAgentThought, messageID: "thought-2", text: "prepare final answer"},
		{name: "blank message id first delta", update: liveOutputContentChunk(schema.UpdateAgentMessage, "", "当前"), updateType: schema.UpdateAgentMessage, text: "当前", blankMessageIDDelta: true},
		{name: "blank message id second delta", update: liveOutputContentChunk(schema.UpdateAgentMessage, "", "目录下共有 12 个文件"), updateType: schema.UpdateAgentMessage, text: "目录下共有 12 个文件", blankMessageIDDelta: true},
		{name: "blank message id final punctuation delta", update: liveOutputContentChunk(schema.UpdateAgentMessage, "", "。"), updateType: schema.UpdateAgentMessage, text: "。", blankMessageIDDelta: true},
	}
	const blankMessageIDResult = "当前目录下共有 12 个文件。"
	var blankMessageIDLive strings.Builder
	blankMessageIDIndexes := make([]int, 0, 3)
	narrativeSequenceIndexes := map[string][]int{}
	narrativeSequenceLive := map[string]string{}
	liveChildren := make([]eventstream.Envelope, 0, len(updates))
	for _, one := range updates {
		external.handleUpdate(child, client.UpdateEnvelope{SessionID: "external-child-session", Update: one.update})
		envelope := receiveLiveOutputEnvelope(t, live.Events(), func(env eventstream.Envelope) bool {
			if env.Scope != eventstream.ScopeSubagent || eventstream.UpdateType(env.Update) != one.updateType {
				return false
			}
			if one.text != "" {
				chunk, ok := env.Update.(schema.ContentChunk)
				return ok && chunk.MessageID == one.messageID && schema.ExtractTextValue(chunk.Content) == one.text
			}
			if one.toolCallID != "" {
				return liveOutputToolCallID(env.Update) == one.toolCallID
			}
			return true
		}, one.name)
		assertLiveOutputChildEnvelope(t, envelope, child.taskID)
		if !liveOutputChildRunning(child) {
			t.Fatalf("child stopped before %s became visible", one.name)
		}
		if one.blankMessageIDDelta {
			if envelope.Final {
				t.Fatalf("%s live envelope Final = true, want an open narrative delta", one.name)
			}
			chunk, ok := envelope.Update.(schema.ContentChunk)
			if !ok || chunk.MessageID != "" {
				t.Fatalf("%s live update = %#v, want a blank-message-id content chunk", one.name, envelope.Update)
			}
			blankMessageIDIndexes = append(blankMessageIDIndexes, len(liveChildren))
			blankMessageIDLive.WriteString(schema.ExtractTextValue(chunk.Content))
		}
		if one.narrativeSequence != "" {
			chunk, ok := envelope.Update.(schema.ContentChunk)
			if !ok {
				t.Fatalf("%s live update = %#v, want narrative content chunk", one.name, envelope.Update)
			}
			narrativeSequenceIndexes[one.narrativeSequence] = append(narrativeSequenceIndexes[one.narrativeSequence], len(liveChildren))
			narrativeSequenceLive[one.narrativeSequence] += schema.ExtractTextValue(chunk.Content)
		}
		liveChildren = append(liveChildren, envelope)
	}
	if got := blankMessageIDLive.String(); got != blankMessageIDResult {
		t.Fatalf("blank-message-id live narrative = %q, want %q", got, blankMessageIDResult)
	}
	for name, want := range map[string]string{
		"repeated delta":       "haha",
		"prefix-growing delta": "aab",
	} {
		if got := narrativeSequenceLive[name]; got != want {
			t.Fatalf("%s live narrative = %q, want %q", name, got, want)
		}
	}

	close(parentRelease)
	if err := receiveLiveOutputValue(t, projectionDone, "parent runtime projection completion"); err != nil {
		t.Fatal(err)
	}
	receiveLiveOutputSignal(t, ingress.Done(), "turningress completion")

	replay, err := feed.Subscribe(ctx, controlclientport.SubscribeRequest{SessionID: parent.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Subscription.Close()
	replayedChildren := collectLiveOutputChildEnvelopes(t, replay.Subscription, len(liveChildren))
	if got, want := liveOutputEnvelopeSignatures(replayedChildren), liveOutputEnvelopeSignatures(liveChildren); !reflect.DeepEqual(got, want) {
		t.Fatalf("live/replay child envelopes differ:\nlive=%#v\nreplay=%#v", want, got)
	}
	var blankMessageIDReplay strings.Builder
	for _, index := range blankMessageIDIndexes {
		if index < 0 || index >= len(replayedChildren) {
			t.Fatalf("blank-message-id replay index %d outside %d child envelopes", index, len(replayedChildren))
		}
		envelope := replayedChildren[index]
		if envelope.Final {
			t.Fatalf("blank-message-id replay envelope %d Final = true, want an open narrative delta", index)
		}
		chunk, ok := envelope.Update.(schema.ContentChunk)
		if !ok || chunk.MessageID != "" {
			t.Fatalf("blank-message-id replay update %d = %#v, want a blank-message-id content chunk", index, envelope.Update)
		}
		blankMessageIDReplay.WriteString(schema.ExtractTextValue(chunk.Content))
	}
	if got := blankMessageIDReplay.String(); got != blankMessageIDResult {
		t.Fatalf("blank-message-id replay narrative = %q, want %q", got, blankMessageIDResult)
	}
	for name, want := range map[string]string{
		"repeated delta":       "haha",
		"prefix-growing delta": "aab",
	} {
		var replayed strings.Builder
		for _, index := range narrativeSequenceIndexes[name] {
			if index < 0 || index >= len(replayedChildren) {
				t.Fatalf("%s replay index %d outside %d child envelopes", name, index, len(replayedChildren))
			}
			chunk, ok := replayedChildren[index].Update.(schema.ContentChunk)
			if !ok {
				t.Fatalf("%s replay update %d = %#v, want narrative content chunk", name, index, replayedChildren[index].Update)
			}
			replayed.WriteString(schema.ExtractTextValue(chunk.Content))
		}
		if got := replayed.String(); got != want {
			t.Fatalf("%s replay narrative = %q, want %q", name, got, want)
		}
	}

	page, err := sessions.EventsPage(ctx, session.EventPageRequest{
		SessionRef: parent.SessionRef, Visibility: session.EventPageClientReplay,
	})
	if err != nil {
		t.Fatal(err)
	}
	mirrors := 0
	for _, event := range page.Events {
		if session.IsMirror(event) && event.ChildOrigin != nil {
			mirrors++
		}
	}
	if mirrors != len(updates) {
		t.Fatalf("durable child mirror count = %d, want %d", mirrors, len(updates))
	}

	reopenedSessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	contextModel := &liveOutputContextCaptureModel{requests: make(chan string, 1)}
	reopenedRuntime, err := sdkruntime.New(sdkruntime.Config{
		Sessions:     reopenedSessions,
		AgentFactory: chat.Factory{SystemPrompt: "Answer tersely."},
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := reopenedRuntime.Run(ctx, agent.RunRequest{
		SessionRef: parent.SessionRef,
		Input:      "continue parent",
		AgentSpec:  agent.AgentSpec{Name: "chat", Model: contextModel},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, seqErr := range next.Handle.Events() {
		if seqErr != nil {
			t.Fatal(seqErr)
		}
	}
	modelContext := receiveLiveOutputValue(t, contextModel.requests, "reopened parent model context")
	for _, childText := range []string{"first second", "reasoning", "haha", "aab"} {
		if strings.Contains(modelContext, childText) {
			t.Fatalf("durable child mirror leaked into parent model context: %q", modelContext)
		}
	}
}

type liveOutputSubagentHarness struct {
	ready chan *childRun
	mu    sync.Mutex
	run   *childRun
}

func (h *liveOutputSubagentHarness) Spawn(_ context.Context, spawnContext tasksubagent.SpawnContext, request delegation.Request) (delegation.Anchor, delegation.Result, error) {
	anchor := delegation.Anchor{
		TaskID:    spawnContext.TaskID,
		SessionID: "external-child-session",
		Agent:     strings.TrimSpace(request.Agent),
		AgentID:   "external-helper-1",
	}
	run := &childRun{
		anchor:    anchor,
		taskID:    spawnContext.TaskID,
		sink:      spawnContext.Streams,
		state:     delegation.StateRunning,
		running:   true,
		updatedAt: time.Now(),
		done:      make(chan struct{}),
	}
	h.mu.Lock()
	h.run = run
	h.mu.Unlock()
	h.ready <- run
	return anchor, delegation.Result{TaskID: spawnContext.TaskID, State: delegation.StateRunning, Running: true, Yielded: true}, nil
}

func (*liveOutputSubagentHarness) Continue(context.Context, delegation.Anchor, delegation.ContinueRequest) (delegation.Result, error) {
	return delegation.Result{}, fmt.Errorf("continuation is not used by the live output integration")
}

func (h *liveOutputSubagentHarness) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	h.mu.Lock()
	run := h.run
	h.mu.Unlock()
	if run == nil {
		return delegation.Result{}, fmt.Errorf("live output child is unavailable")
	}
	run.mu.RLock()
	defer run.mu.RUnlock()
	return delegation.Result{TaskID: run.taskID, State: run.state, Running: run.running, Yielded: run.running, UpdatedAt: run.updatedAt}, nil
}

func (h *liveOutputSubagentHarness) Cancel(context.Context, delegation.Anchor) error {
	h.mu.Lock()
	run := h.run
	h.mu.Unlock()
	if run == nil {
		return nil
	}
	run.mu.Lock()
	run.state = delegation.StateCancelled
	run.running = false
	run.updatedAt = time.Now()
	run.mu.Unlock()
	return nil
}

type liveOutputParentModel struct {
	calls   atomic.Int32
	release <-chan struct{}
	waiting chan struct{}
	once    sync.Once
}

func (*liveOutputParentModel) Name() string { return "live-output-parent" }

func (*liveOutputParentModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true}
}

func (m *liveOutputParentModel) Generate(ctx context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	call := m.calls.Add(1)
	return func(yield func(*model.StreamEvent, error) bool) {
		if call == 1 {
			yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: &model.Response{
				Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
					ID: "spawn-call-1", Name: spawn.ToolName,
					Args: string(liveOutputJSON(map[string]any{"agent": "helper", "prompt": "inspect"})),
				}}, ""),
				TurnComplete: true, StepComplete: true,
				Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonToolCalls,
			}}, nil)
			return
		}
		m.once.Do(func() { close(m.waiting) })
		select {
		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		case <-m.release:
		}
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: &model.Response{
			Message:      model.NewTextMessage(model.RoleAssistant, "parent continues"),
			TurnComplete: true, StepComplete: true,
			Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonStop,
		}}, nil)
	}
}

type liveOutputContextCaptureModel struct {
	requests chan string
}

func (*liveOutputContextCaptureModel) Name() string { return "live-output-context-capture" }

func (m *liveOutputContextCaptureModel) Generate(_ context.Context, request *model.Request) iter.Seq2[*model.StreamEvent, error] {
	var contextText strings.Builder
	for _, message := range request.Messages {
		contextText.WriteString(message.TextContent())
		contextText.WriteByte('\n')
	}
	m.requests <- contextText.String()
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: &model.Response{
			Message:      model.NewTextMessage(model.RoleAssistant, "continued"),
			TurnComplete: true, StepComplete: true,
			Status: model.ResponseStatusCompleted, FinishReason: model.FinishReasonStop,
		}}, nil)
	}
}

type liveOutputContextRouter struct{}

func (liveOutputContextRouter) ControllerContext(context.Context, controller.ControllerContextRequest) (controller.ContextRoute, error) {
	return controller.ContextRoute{}, nil
}

func (liveOutputContextRouter) ParticipantContext(context.Context, controller.ParticipantContextRequest) (controller.ContextRoute, error) {
	return controller.ContextRoute{}, nil
}

func (liveOutputContextRouter) Checkpoint(context.Context, session.SessionRef, string) (uint64, error) {
	return 0, nil
}

type liveOutputTurnHandle struct {
	events <-chan eventstream.Envelope
	ref    session.SessionRef
	runID  string
}

func (*liveOutputTurnHandle) HandleID() string                 { return "handle-live-output" }
func (h *liveOutputTurnHandle) RunID() string                  { return h.runID }
func (*liveOutputTurnHandle) TurnID() string                   { return "" }
func (h *liveOutputTurnHandle) SessionRef() session.SessionRef { return h.ref }
func (*liveOutputTurnHandle) CreatedAt() time.Time             { return time.Unix(1, 0) }
func (h *liveOutputTurnHandle) ACPEvents() <-chan eventstream.Envelope {
	return h.events
}
func (*liveOutputTurnHandle) Submit(context.Context, gateway.SubmitRequest) error { return nil }
func (*liveOutputTurnHandle) Cancel() agent.CancelResult                          { return agent.CancelResult{} }
func (*liveOutputTurnHandle) Close() error                                        { return nil }

func projectLiveOutputRuntimeEvents(
	runner agent.Runner,
	handle *liveOutputTurnHandle,
	out chan<- eventstream.Envelope,
	anchorSeen chan<- acpprojector.StreamRequest,
	done chan<- error,
) {
	defer close(out)
	var projectionErr error
	for event, err := range runner.Events() {
		if err != nil {
			projectionErr = err
			break
		}
		base := acpprojector.EnvelopeBaseFromSessionEvent(handle.ref, event, acpprojector.SessionEventTransport{
			HandleID: handle.HandleID(), RunID: handle.RunID(), TurnID: handle.TurnID(),
		})
		for _, envelope := range acpprojector.ProjectSessionEventEnvelope(base, event) {
			if request, ok := turningress.StreamRequestFromACPEvent(envelope); ok && request.CallID == "spawn-call-1" {
				select {
				case anchorSeen <- request:
				default:
				}
			}
			out <- envelope
		}
	}
	terminal := eventstream.TurnCompleted(handle.HandleID(), handle.RunID(), handle.TurnID(), time.Now())
	terminal.SessionID = handle.ref.SessionID
	terminal.ScopeID = handle.ref.SessionID
	out <- terminal
	done <- projectionErr
}

func liveOutputContentChunk(updateType, messageID, text string) client.ContentChunk {
	raw, _ := json.Marshal(client.TextChunk{Type: "text", Text: text})
	return client.ContentChunk{SessionUpdate: updateType, MessageID: messageID, Content: raw}
}

func liveOutputString(value string) *string { return &value }

func liveOutputJSON(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func liveOutputChildRunning(run *childRun) bool {
	if run == nil {
		return false
	}
	run.mu.RLock()
	defer run.mu.RUnlock()
	return run.running
}

func liveOutputToolCallID(update any) string {
	switch value := update.(type) {
	case schema.ToolCall:
		return value.ToolCallID
	case schema.ToolCallUpdate:
		return value.ToolCallID
	default:
		return ""
	}
}

func assertLiveOutputChildEnvelope(t *testing.T, envelope eventstream.Envelope, taskID string) {
	t.Helper()
	if envelope.Scope != eventstream.ScopeSubagent || envelope.ScopeID != taskID {
		t.Fatalf("child scope = (%q, %q), want (subagent, %q): %#v", envelope.Scope, envelope.ScopeID, taskID, envelope)
	}
	if envelope.ParentTool == nil || envelope.ParentTool.ToolCallID != "spawn-call-1" || envelope.ParentTool.ToolName != spawn.ToolName {
		t.Fatalf("child parent tool = %#v", envelope.ParentTool)
	}
	if envelope.Delivery == nil || envelope.Delivery.Mode != eventstream.DeliveryMirror || envelope.Position == nil || envelope.Position.Durable == nil || envelope.Cursor == "" {
		t.Fatalf("child durable delivery = %#v position=%#v cursor=%q", envelope.Delivery, envelope.Position, envelope.Cursor)
	}
}

type liveOutputEnvelopeSignature struct {
	Scope        eventstream.Scope
	ScopeID      string
	ParentCallID string
	UpdateType   string
	MessageID    string
	Text         string
	ToolCallID   string
	Final        bool
}

func liveOutputEnvelopeSignatures(envelopes []eventstream.Envelope) []liveOutputEnvelopeSignature {
	out := make([]liveOutputEnvelopeSignature, 0, len(envelopes))
	for _, envelope := range envelopes {
		signature := liveOutputEnvelopeSignature{
			Scope: envelope.Scope, ScopeID: envelope.ScopeID,
			UpdateType: eventstream.UpdateType(envelope.Update), Final: envelope.Final,
			ToolCallID: liveOutputToolCallID(envelope.Update),
		}
		if envelope.ParentTool != nil {
			signature.ParentCallID = envelope.ParentTool.ToolCallID
		}
		if chunk, ok := envelope.Update.(schema.ContentChunk); ok {
			signature.MessageID = chunk.MessageID
			signature.Text = schema.ExtractTextValue(chunk.Content)
		}
		out = append(out, signature)
	}
	return out
}

func collectLiveOutputChildEnvelopes(t *testing.T, subscription controlclientport.FeedSubscription, count int) []eventstream.Envelope {
	t.Helper()
	out := make([]eventstream.Envelope, 0, count)
	for len(out) < count {
		envelope := receiveLiveOutputEnvelope(t, subscription.Backfill(), func(env eventstream.Envelope) bool {
			return env.Scope == eventstream.ScopeSubagent && env.ParentTool != nil && env.ParentTool.ToolCallID == "spawn-call-1"
		}, "replayed child envelope")
		out = append(out, envelope)
	}
	return out
}

func receiveLiveOutputEnvelope(
	t *testing.T,
	events <-chan eventstream.Envelope,
	match func(eventstream.Envelope) bool,
	name string,
) eventstream.Envelope {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case envelope, ok := <-events:
			if !ok {
				t.Fatalf("%s: event stream closed", name)
			}
			if match(envelope) {
				return envelope
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", name)
		}
	}
}

func receiveLiveOutputValue[T any](t *testing.T, values <-chan T, name string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		var zero T
		t.Fatalf("timed out waiting for %s", name)
		return zero
	}
}

func receiveLiveOutputSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
