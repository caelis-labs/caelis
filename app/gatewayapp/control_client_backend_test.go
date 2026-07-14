package gatewayapp

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	internalcontrolclient "github.com/caelis-labs/caelis/internal/controlclient"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestClassifyControlBackendErrorTreatsLeaseConflictAsConflict(t *testing.T) {
	err := classifyControlBackendError(&session.LeaseConflictError{SessionID: "session-1", Detail: "active execution lease"})
	var outcomeErr *controlport.OutcomeError
	if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != controlport.OutcomeConflicted {
		t.Fatalf("classifyControlBackendError() = %v, want conflicted outcome", err)
	}
}

func TestClassifyControlBackendErrorAddsTypedHTTPCategories(t *testing.T) {
	for _, tt := range []struct {
		name    string
		err     error
		outcome controlport.Outcome
		code    errorcode.Code
	}{
		{
			name: "validation",
			err: &gateway.Error{
				Kind: gateway.KindValidation, Code: gateway.CodeInvalidRequest, Message: "invalid prompt",
			},
			outcome: controlport.OutcomeRejected,
			code:    errorcode.InvalidArgument,
		},
		{
			name:    "internal",
			err:     &gateway.Error{Kind: gateway.KindInternal, Code: gateway.CodeInternal, Message: "private failure"},
			outcome: controlport.OutcomeUnknown,
			code:    errorcode.Unknown,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyControlBackendError(tt.err)
			var outcomeErr *controlport.OutcomeError
			if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != tt.outcome || errorcode.CodeOf(err) != tt.code {
				t.Fatalf("classifyControlBackendError() = %v (outcome %#v, code %q)", err, outcomeErr, errorcode.CodeOf(err))
			}
		})
	}
}

func TestClassifyControlBackendErrorTreatsUnclassifiedFailureAsUnknown(t *testing.T) {
	err := classifyControlBackendError(errors.New("effect boundary failed without proof"))
	var outcomeErr *controlport.OutcomeError
	if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != controlport.OutcomeUnknown {
		t.Fatalf("classifyControlBackendError() = %v, want unknown outcome", err)
	}
}

func TestAttachControlClientHandleUsesSharedTaskIngress(t *testing.T) {
	t.Parallel()

	sessions := sessionmemory.NewService(sessionmemory.NewStore(sessionmemory.Config{
		SessionIDGenerator: func() string { return "session-1" },
	}))
	active, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "session-1",
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
	feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
		Reader: sessions, CursorCodec: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	taskStream := &controlClientIngressStream{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	runtime := controlClientIngressRuntime{streams: taskStream}
	kernel, err := kernelimpl.New(kernelimpl.Config{
		Sessions: sessions,
		Runtime:  runtime,
		Resolver: controlClientIngressResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	stack := &Stack{Sessions: sessions, controlFeeds: feeds, gateway: kernel}
	feed, err := feeds.Session(active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	mainEvents := make(chan eventstream.Envelope, 2)
	handle := &controlClientIngressHandle{events: mainEvents}
	stack.attachControlClientHandle(handle)

	status := schema.ToolStatusInProgress
	title := "RUN_COMMAND"
	mainEvents <- eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: active.SessionID,
		HandleID:  handle.HandleID(),
		RunID:     handle.RunID(),
		TurnID:    handle.TurnID(),
		Scope:     eventstream.ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "run-command-1",
			Title:         &title,
			Status:        &status,
		},
		Meta: map[string]any{
			gateway.EventMetaRoot: map[string]any{
				gateway.EventMetaRuntime: map[string]any{
					gateway.EventMetaRuntimeTool: map[string]any{
						gateway.EventMetaRuntimeToolName: "RUN_COMMAND",
					},
					gateway.EventMetaRuntimeTask: map[string]any{
						gateway.EventMetaRuntimeTaskID:         "task-1",
						gateway.EventMetaRuntimeTaskTerminalID: "terminal-1",
					},
				},
			},
		},
	}
	first := receiveControlClientIngressEnvelope(t, subscription.Events())
	assertControlClientIngressTool(t, first, "run-command-1")
	waitControlClientIngressSignal(t, taskStream.started)
	close(taskStream.release)

	projected := receiveControlClientIngressEnvelope(t, subscription.Events())
	assertControlClientIngressTool(t, projected, "run-command-1")
	if projected.Delivery == nil || projected.Delivery.Mode != eventstream.DeliveryTransient {
		t.Fatalf("task stream delivery = %#v, want transient", projected.Delivery)
	}

	mainEvents <- eventstream.TurnCompleted(handle.HandleID(), handle.RunID(), handle.TurnID(), time.Now())
	close(mainEvents)
	terminal := receiveControlClientIngressEnvelope(t, subscription.Events())
	if !eventstream.IsTerminalLifecycle(terminal) {
		t.Fatalf("last envelope = %#v, want terminal lifecycle", terminal)
	}
}

func TestControlClientClosePersistsGatePublishesLiveAndRejectsLaterPrompt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sessions := sessionmemory.NewService(sessionmemory.NewStore(sessionmemory.Config{}))
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "session-close",
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := kernelimpl.New(kernelimpl.Config{
		Sessions: sessions, Runtime: controlClientBlockingRuntime{}, Resolver: controlClientIngressResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := kernel.BeginTurn(ctx, gateway.BeginTurnRequest{SessionRef: active.SessionRef, Input: "wait"})
	if err != nil {
		t.Fatal(err)
	}
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{Reader: sessions, CursorCodec: codec})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := feed.SubscribeFromNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	stack := &Stack{Sessions: sessions, controlFeeds: feeds, gateway: kernel}
	expected := active.Revision
	result, err := stack.ExecuteControlCommand(ctx, controlport.Principal{ID: "owner"}, controlport.ActionSessionClose, controlport.CloseSessionRequest{
		WriteBase: controlport.WriteBase{SessionID: active.SessionID, ExpectedRevision: &expected},
	})
	if err != nil || result.Outcome != controlport.OutcomeCommitted || result.Revision <= active.Revision {
		t.Fatalf("CloseSession result = %#v, %v", result, err)
	}
	select {
	case envelope := <-subscription.Events():
		if envelope.Lifecycle == nil || envelope.Lifecycle.State != "closed" || envelope.Position == nil || envelope.Position.Durable == nil {
			t.Fatalf("live close envelope = %#v", envelope)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for live close lifecycle")
	}
	if _, ok := kernel.ActiveTurn(active.SessionID); ok {
		t.Fatal("close left an active turn")
	}

	current, err := sessions.Session(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	result, err = stack.ExecuteControlCommand(ctx, controlport.Principal{ID: "owner"}, controlport.ActionPrompt, controlport.PromptRequest{
		WriteBase: controlport.WriteBase{SessionID: active.SessionID}, Input: "must be rejected",
	})
	if !errors.Is(err, internalcontrolclient.ErrSessionClosed) || result.Revision != current.Revision {
		t.Fatalf("prompt after close = %#v, %v", result, err)
	}
	_ = turn.Handle.Close()
}

func TestControlClientCancelParticipantRejectsMainTurnWithArbitraryParticipantID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sessions := sessionmemory.NewService(sessionmemory.NewStore(sessionmemory.Config{}))
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "session-main",
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := kernelimpl.New(kernelimpl.Config{
		Sessions: sessions, Runtime: controlClientBlockingRuntime{}, Resolver: controlClientIngressResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := kernel.BeginTurn(ctx, gateway.BeginTurnRequest{SessionRef: active.SessionRef, Input: "wait"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := kernel.Interrupt(ctx, gateway.InterruptRequest{SessionRef: active.SessionRef}); err != nil {
			t.Errorf("interrupt main turn: %v", err)
		}
	}()
	stack := &Stack{Sessions: sessions, gateway: kernel}
	result, err := stack.ExecuteControlCommand(ctx, controlport.Principal{ID: "owner"}, controlport.ActionParticipantCancel, controlport.CancelParticipantRequest{
		WriteBase: controlport.WriteBase{SessionID: active.SessionID}, ParticipantID: "not-the-main-turn",
		Target: controlport.TurnTarget{HandleID: started.Handle.HandleID(), RunID: started.Handle.RunID(), TurnID: started.Handle.TurnID()},
	})
	var outcomeErr *controlport.OutcomeError
	if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != controlport.OutcomeConflicted || result.Outcome != controlport.OutcomeCommitted {
		t.Fatalf("participant cancel against main = %#v, %v", result, err)
	}
	if _, ok := kernel.ActiveTurn(active.SessionID); !ok {
		t.Fatal("invalid participant cancel stopped the main turn")
	}
}

type controlClientBlockingRuntime struct{}

func (controlClientBlockingRuntime) Run(ctx context.Context, _ agent.RunRequest) (agent.RunResult, error) {
	<-ctx.Done()
	return agent.RunResult{}, ctx.Err()
}

func (controlClientBlockingRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type controlClientIngressRuntime struct {
	streams stream.Service
}

func (controlClientIngressRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{}, nil
}

func (controlClientIngressRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (runtime controlClientIngressRuntime) Streams() stream.Service {
	return runtime.streams
}

type controlClientIngressResolver struct{}

func (controlClientIngressResolver) ResolveTurn(context.Context, gateway.TurnIntent) (gateway.ResolvedTurn, error) {
	return gateway.ResolvedTurn{}, nil
}

type controlClientIngressStream struct {
	started chan struct{}
	release chan struct{}
}

func (service *controlClientIngressStream) Read(ctx context.Context, request stream.ReadRequest) (stream.Snapshot, error) {
	select {
	case service.started <- struct{}{}:
	default:
	}
	select {
	case <-service.release:
	case <-ctx.Done():
		return stream.Snapshot{}, ctx.Err()
	}
	exitCode := 0
	return stream.Snapshot{
		Ref:      request.Ref,
		Cursor:   stream.Cursor{Output: 12, Events: 1},
		Running:  false,
		State:    "completed",
		ExitCode: &exitCode,
		Frames: []stream.Frame{{
			Ref: request.Ref, Text: "task output\n", State: "completed",
			Cursor: stream.Cursor{Output: 12, Events: 1}, Closed: true, ExitCode: &exitCode,
		}},
	}, nil
}

func (*controlClientIngressStream) Subscribe(context.Context, stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	return func(func(*stream.Frame, error) bool) {}
}

type controlClientIngressHandle struct {
	events <-chan eventstream.Envelope
}

func (*controlClientIngressHandle) HandleID() string { return "handle-1" }
func (*controlClientIngressHandle) RunID() string    { return "run-1" }
func (*controlClientIngressHandle) TurnID() string   { return "turn-1" }
func (*controlClientIngressHandle) SessionRef() session.SessionRef {
	return session.SessionRef{SessionID: "session-1"}
}
func (*controlClientIngressHandle) CreatedAt() time.Time { return time.Time{} }
func (handle *controlClientIngressHandle) ACPEvents() <-chan eventstream.Envelope {
	return handle.events
}
func (*controlClientIngressHandle) Submit(context.Context, gateway.SubmitRequest) error { return nil }
func (*controlClientIngressHandle) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (*controlClientIngressHandle) Close() error { return nil }

func receiveControlClientIngressEnvelope(t *testing.T, events <-chan eventstream.Envelope) eventstream.Envelope {
	t.Helper()
	select {
	case envelope := <-events:
		return envelope
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Control client ingress envelope")
		return eventstream.Envelope{}
	}
}

func waitControlClientIngressSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task stream read")
	}
}

func assertControlClientIngressTool(t *testing.T, envelope eventstream.Envelope, callID string) {
	t.Helper()
	update, ok := envelope.Update.(schema.ToolCallUpdate)
	if !ok || update.ToolCallID != callID {
		t.Fatalf("tool update = %#v, want call %q", envelope.Update, callID)
	}
}
