package gatewayapp

import (
	"context"
	"errors"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelprofile"
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

func TestControlParticipantPlacementRejectsOnlyInvalidSelections(t *testing.T) {
	store := newAppConfigStore(t.TempDir())
	profile := modelprofile.ModelProfile{
		ID:          "acp:claude:opus",
		DisplayName: "Claude Opus",
		Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{
			AgentID: "claude", RemoteModelID: "opus",
		}},
		Effort: modelprofile.EffortCapability{
			DefaultEffort: "xhigh",
			Choices:       []modelprofile.EffortChoice{{Canonical: "xhigh", WireValue: "max"}},
			ACPConfigID:   "effort",
		},
	}
	if err := store.Save(AppConfig{
		ExternalAgents: controlagents.Configuration{
			Connections: []controlagents.Connection{{
				ID: "claude", Launcher: controlagents.Launcher{Command: "claude-acp"},
			}},
			Agents: []controlagents.Agent{{ID: "claude", ConnectionID: "claude"}},
		},
		ModelProfiles: modelprofile.Configuration{Profiles: []modelprofile.ModelProfile{profile}},
	}); err != nil {
		t.Fatal(err)
	}
	stack := &Stack{store: store}
	for _, selection := range []struct {
		profileID string
		effort    string
	}{
		{profileID: "acp:missing", effort: "xhigh"},
		{profileID: profile.ID, effort: "low"},
	} {
		_, err := stack.resolveControlParticipantPlacement(context.Background(), selection.profileID, selection.effort)
		var outcomeErr *controlport.OutcomeError
		if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != controlport.OutcomeRejected || errorcode.CodeOf(err) != errorcode.InvalidArgument {
			t.Fatalf("resolveControlParticipantPlacement(%q, %q) = %v, want rejected invalid_argument", selection.profileID, selection.effort, err)
		}
	}
}

func TestControlParticipantPlacementStoreFailureRemainsUnknown(t *testing.T) {
	store := newAppConfigStore(t.TempDir())
	store.path = t.TempDir()
	stack := &Stack{store: store}
	_, err := stack.resolveControlParticipantPlacement(context.Background(), "acp:claude:opus", "xhigh")
	if err == nil || errorcode.CodeOf(err) == errorcode.InvalidArgument {
		t.Fatalf("resolveControlParticipantPlacement(store failure) = %v, want internal failure", err)
	}
	classified := classifyControlBackendError(err)
	var outcomeErr *controlport.OutcomeError
	if !errors.As(classified, &outcomeErr) || outcomeErr.Outcome != controlport.OutcomeUnknown || errorcode.CodeOf(classified) != errorcode.Unknown {
		t.Fatalf("classifyControlBackendError(store failure) = %v, want unknown outcome", classified)
	}
}

func TestAttachControlClientHandleDoesNotReadTaskStream(t *testing.T) {
	t.Parallel()

	sessions := sessionmemory.NewStore(sessionmemory.Config{
		SessionIDGenerator: func() string { return "session-1" },
	})
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
	select {
	case <-taskStream.started:
		t.Fatal("Session ingress read the Task stream")
	case <-time.After(50 * time.Millisecond):
	}

	mainEvents <- eventstream.TurnCompleted(handle.HandleID(), handle.RunID(), handle.TurnID(), time.Now())
	close(mainEvents)
	terminal := receiveControlClientIngressEnvelope(t, subscription.Events())
	if !eventstream.IsTerminalLifecycle(terminal) {
		t.Fatalf("last envelope = %#v, want terminal lifecycle", terminal)
	}
}

func TestAttachControlClientHandleFailureCancelsAndPublishesAfterProducerBarrier(t *testing.T) {
	t.Parallel()

	handle := newControlClientAttachmentFailureHandle()
	published := make(chan eventstream.Envelope, 4)
	var attachCalls atomic.Int32
	feed := &controlClientSessionFeed{attachFn: func(events <-chan eventstream.Envelope) <-chan error {
		call := attachCalls.Add(1)
		result := make(chan error, 1)
		if call == 1 {
			result <- errors.New("injected feed publish failure")
			close(result)
			return result
		}
		go func() {
			defer close(result)
			for envelope := range events {
				published <- envelope
			}
		}()
		return result
	}}
	stack := &Stack{controlFeeds: controlClientFeedRegistry{feed: feed}}
	stack.attachControlClientHandle(handle)

	select {
	case <-handle.cancelRequested:
	case <-time.After(2 * time.Second):
		t.Fatal("attachment failure did not cancel the owning producer")
	}
	select {
	case envelope := <-published:
		t.Fatalf("envelope before producer barrier = %#v", envelope)
	case <-time.After(30 * time.Millisecond):
	}
	close(handle.releaseProducer)

	terminal := receiveControlClientIngressEnvelope(t, published)
	if !eventstream.IsTerminalLifecycle(terminal) || terminal.Lifecycle.State != eventstream.LifecycleStateFailed {
		t.Fatalf("terminal = %#v, want one failed terminal", terminal)
	}
	select {
	case <-handle.producerDone:
	default:
		t.Fatal("failed terminal arrived before producer completion")
	}
	if calls := handle.cancelCalls.Load(); calls != 1 {
		t.Fatalf("Cancel calls = %d, want one", calls)
	}
	if calls := attachCalls.Load(); calls != 2 {
		t.Fatalf("Attach calls = %d, want initial failure plus one fallback", calls)
	}
	select {
	case duplicate := <-published:
		t.Fatalf("duplicate terminal = %#v", duplicate)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestCommittedCommandKeepsOutcomeWhenFeedPrimeFailsAndLedgerReplays(t *testing.T) {
	t.Parallel()

	sessions := sessionmemory.NewStore(sessionmemory.Config{})
	kernel, err := kernelimpl.New(kernelimpl.Config{
		Sessions: sessions, Runtime: controlClientIngressRuntime{}, Resolver: controlClientIngressResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	feed := &controlClientSessionFeed{primeErr: errors.New("injected prime failure")}
	stack := &Stack{
		Sessions: sessions, AppName: "caelis", controlFeeds: controlClientFeedRegistry{feed: feed}, gateway: kernel,
	}
	backend := &countingControlClientBackend{backend: stack}
	commands, err := internalcontrolclient.NewCommandService(internalcontrolclient.CommandServiceConfig{
		Authorizer: controlClientAllowAuthorizer{},
		Operations: internalcontrolclient.NewMemoryOperationStore(),
		Backend:    backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := controlport.CreateSessionRequest{
		WriteBase:          controlport.WriteBase{OperationID: "operation-prime-failure"},
		PreferredSessionID: "session-prime-failure",
	}
	principal := controlport.Principal{ID: "owner"}
	first, err := commands.CreateSession(context.Background(), principal, request)
	if err != nil || first.Outcome != controlport.OutcomeCommitted || first.Detail != controlFeedCatchUpWarning {
		t.Fatalf("first result = %#v, %v, want committed result with feed warning", first, err)
	}
	replayed, err := commands.CreateSession(context.Background(), principal, request)
	if err != nil || replayed != first {
		t.Fatalf("replayed result = %#v, %v, want %#v", replayed, err, first)
	}
	if calls := backend.calls.Load(); calls != 1 {
		t.Fatalf("backend calls = %d, want one dispatch", calls)
	}
	if calls := feed.primeCalls.Load(); calls != 1 {
		t.Fatalf("Prime calls = %d, want one post-commit catch-up", calls)
	}
}

func TestControlClientClosePersistsGatePublishesLiveAndRejectsLaterPrompt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sessions := sessionmemory.NewStore(sessionmemory.Config{})
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
	sessions := sessionmemory.NewStore(sessionmemory.Config{})
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

type controlClientAttachmentFailureHandle struct {
	events          chan eventstream.Envelope
	cancelRequested chan struct{}
	releaseProducer chan struct{}
	producerDone    chan struct{}
	cancelOnce      sync.Once
	cancelCalls     atomic.Int32
}

func newControlClientAttachmentFailureHandle() *controlClientAttachmentFailureHandle {
	return &controlClientAttachmentFailureHandle{
		events:          make(chan eventstream.Envelope),
		cancelRequested: make(chan struct{}),
		releaseProducer: make(chan struct{}),
		producerDone:    make(chan struct{}),
	}
}

func (*controlClientAttachmentFailureHandle) HandleID() string { return "handle-attachment-failure" }
func (*controlClientAttachmentFailureHandle) RunID() string    { return "run-attachment-failure" }
func (*controlClientAttachmentFailureHandle) TurnID() string   { return "turn-attachment-failure" }
func (*controlClientAttachmentFailureHandle) SessionRef() session.SessionRef {
	return session.SessionRef{SessionID: "session-attachment-failure"}
}
func (*controlClientAttachmentFailureHandle) CreatedAt() time.Time { return time.Time{} }
func (handle *controlClientAttachmentFailureHandle) ACPEvents() <-chan eventstream.Envelope {
	return handle.events
}
func (*controlClientAttachmentFailureHandle) Submit(context.Context, gateway.SubmitRequest) error {
	return nil
}
func (handle *controlClientAttachmentFailureHandle) Cancel() agent.CancelResult {
	handle.cancelCalls.Add(1)
	handle.cancelOnce.Do(func() {
		close(handle.cancelRequested)
		go func() {
			<-handle.releaseProducer
			close(handle.producerDone)
			close(handle.events)
		}()
	})
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (*controlClientAttachmentFailureHandle) Close() error { return nil }

type controlClientFeedRegistry struct {
	feed controlport.SessionFeed
	err  error
}

func (registry controlClientFeedRegistry) Session(session.SessionRef) (controlport.SessionFeed, error) {
	return registry.feed, registry.err
}

type controlClientSessionFeed struct {
	primeErr   error
	primeCalls atomic.Int32
	attachFn   func(<-chan eventstream.Envelope) <-chan error
}

func (feed *controlClientSessionFeed) Prime(context.Context) error {
	feed.primeCalls.Add(1)
	return feed.primeErr
}
func (*controlClientSessionFeed) Publish(eventstream.Envelope) error { return nil }
func (*controlClientSessionFeed) Subscribe(context.Context, controlport.SubscribeRequest) (controlport.SubscribeResult, error) {
	return controlport.SubscribeResult{}, errors.New("test feed does not support Subscribe")
}
func (*controlClientSessionFeed) SubscribeFromNow(context.Context) (controlport.FeedSubscription, error) {
	return nil, errors.New("test feed does not support SubscribeFromNow")
}
func (feed *controlClientSessionFeed) Attach(events <-chan eventstream.Envelope) <-chan error {
	if feed.attachFn != nil {
		return feed.attachFn(events)
	}
	result := make(chan error)
	go func() {
		for range events {
		}
		close(result)
	}()
	return result
}
func (feed *controlClientSessionFeed) AttachTo(_ controlport.FeedSubscription, events <-chan eventstream.Envelope) <-chan error {
	return feed.Attach(events)
}
func (*controlClientSessionFeed) Boundary() (*eventstream.FeedPosition, string) { return nil, "" }

type controlClientAllowAuthorizer struct{}

func (controlClientAllowAuthorizer) Authorize(context.Context, controlport.Principal, controlport.Action, string) error {
	return nil
}

type countingControlClientBackend struct {
	backend controlport.CommandBackend
	calls   atomic.Int32
}

func (backend *countingControlClientBackend) ExecuteControlCommand(
	ctx context.Context,
	principal controlport.Principal,
	action controlport.Action,
	request any,
) (controlport.CommandResult, error) {
	backend.calls.Add(1)
	return backend.backend.ExecuteControlCommand(ctx, principal, action, request)
}

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
