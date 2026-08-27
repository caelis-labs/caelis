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
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/modelprofile"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/controlplane"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/internal/testenv"

	acptaskstream "github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestClassifyControlBackendErrorTreatsFenceConflictAsConflict(t *testing.T) {
	err := classifyControlBackendError(&session.FenceConflictError{SessionID: "session-1", Detail: "active execution fence"})
	var outcomeErr *appserver.OutcomeError
	if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("classifyControlBackendError() = %v, want conflicted outcome", err)
	}
}

func TestClassifyControlBackendErrorAddsTypedHTTPCategories(t *testing.T) {
	for _, tt := range []struct {
		name    string
		err     error
		outcome appserver.Outcome
		code    errorcode.Code
	}{
		{
			name: "validation",
			err: &kernelimpl.Error{
				Kind: kernelimpl.KindValidation, Code: kernelimpl.CodeInvalidRequest, Message: "invalid prompt",
			},
			outcome: appserver.OutcomeRejected,
			code:    errorcode.InvalidArgument,
		},
		{
			name:    "internal",
			err:     &kernelimpl.Error{Kind: kernelimpl.KindInternal, Code: kernelimpl.CodeInternal, Message: "private failure"},
			outcome: appserver.OutcomeUnknown,
			code:    errorcode.Unknown,
		},
		{
			name: "host closing",
			err: &kernelimpl.Error{
				Kind: kernelimpl.KindUnavailable, Code: kernelimpl.CodeHostClosing, Message: "gateway: host is closing",
			},
			outcome: appserver.OutcomeRejected,
			code:    errorcode.Unavailable,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyControlBackendError(tt.err)
			var outcomeErr *appserver.OutcomeError
			if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != tt.outcome || errorcode.CodeOf(err) != tt.code {
				t.Fatalf("classifyControlBackendError() = %v (outcome %#v, code %q)", err, outcomeErr, errorcode.CodeOf(err))
			}
		})
	}
}

func TestClassifyControlBackendErrorTreatsUnclassifiedFailureAsUnknown(t *testing.T) {
	err := classifyControlBackendError(errors.New("effect boundary failed without proof"))
	var outcomeErr *appserver.OutcomeError
	if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != appserver.OutcomeUnknown {
		t.Fatalf("classifyControlBackendError() = %v, want unknown outcome", err)
	}
}

func TestClassifyControlSteerErrorPreservesEffectCertainty(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		code    errorcode.Code
		outcome appserver.Outcome
	}{
		{name: "unsupported", code: errorcode.Unsupported, outcome: appserver.OutcomeRejected},
		{name: "not injected", code: errorcode.FailedPrecondition, outcome: appserver.OutcomeRejected},
		{name: "stale target", code: errorcode.Conflict, outcome: appserver.OutcomeConflicted},
		{name: "ambiguous remote effect", code: errorcode.UnknownOutcome, outcome: appserver.OutcomeUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			classified := classifyControlSteerError(errorcode.New(test.code, test.name))
			var outcomeErr *appserver.OutcomeError
			if !errors.As(classified, &outcomeErr) || outcomeErr.Outcome != test.outcome || errorcode.CodeOf(classified) != test.code {
				t.Fatalf("classifyControlSteerError() = %v, want %s/%s", classified, test.outcome, test.code)
			}
		})
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		classified := classifyControlSteerError(cause)
		var outcomeErr *appserver.OutcomeError
		if !errors.As(classified, &outcomeErr) || outcomeErr.Outcome != appserver.OutcomeRejected || errorcode.CodeOf(classified) != errorcode.FailedPrecondition {
			t.Fatalf("classifyControlSteerError(%v) = %v, want rejected pre-dispatch failure", cause, classified)
		}
	}
	gatewayUnsupported := &kernelimpl.Error{
		Kind: kernelimpl.KindUnsupported, Code: kernelimpl.CodeSubmissionUnsupported,
		Message: "active Turn finished before runner admission",
	}
	classified := classifyControlSteerError(gatewayUnsupported)
	var outcomeErr *appserver.OutcomeError
	if !errors.As(classified, &outcomeErr) || outcomeErr.Outcome != appserver.OutcomeRejected || errorcode.CodeOf(classified) != errorcode.FailedPrecondition {
		t.Fatalf("classifyControlSteerError(finished Turn) = %v, want rejected failed_precondition", classified)
	}
	unknownCancellation := errorcode.Wrap(errorcode.UnknownOutcome, "remote response was lost", context.Canceled)
	classified = classifyControlSteerError(unknownCancellation)
	if !errors.As(classified, &outcomeErr) || outcomeErr.Outcome != appserver.OutcomeUnknown || errorcode.CodeOf(classified) != errorcode.UnknownOutcome {
		t.Fatalf("classifyControlSteerError(remote cancellation) = %v, want unknown outcome", classified)
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
	stack := &Stack{composition: runtimeComposition{authorities: runtimeHostAuthorities{store: store}}}
	for _, selection := range []struct {
		profileID string
		effort    string
	}{
		{profileID: "acp:missing", effort: "xhigh"},
		{profileID: profile.ID, effort: "low"},
	} {
		_, err := stack.composition.resolveControlParticipantPlacement(context.Background(), selection.profileID, selection.effort)
		var outcomeErr *appserver.OutcomeError
		if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != appserver.OutcomeRejected || errorcode.CodeOf(err) != errorcode.InvalidArgument {
			t.Fatalf("resolveControlParticipantPlacement(%q, %q) = %v, want rejected invalid_argument", selection.profileID, selection.effort, err)
		}
	}
}

func TestControlParticipantPlacementStoreFailureRemainsUnknown(t *testing.T) {
	store := newAppConfigStore(t.TempDir())
	store.path = t.TempDir()
	stack := &Stack{composition: runtimeComposition{authorities: runtimeHostAuthorities{store: store}}}
	_, err := stack.composition.resolveControlParticipantPlacement(context.Background(), "acp:claude:opus", "xhigh")
	if err == nil || errorcode.CodeOf(err) == errorcode.InvalidArgument {
		t.Fatalf("resolveControlParticipantPlacement(store failure) = %v, want internal failure", err)
	}
	classified := classifyControlBackendError(err)
	var outcomeErr *appserver.OutcomeError
	if !errors.As(classified, &outcomeErr) || outcomeErr.Outcome != appserver.OutcomeUnknown || errorcode.CodeOf(classified) != errorcode.Unknown {
		t.Fatalf("classifyControlBackendError(store failure) = %v, want unknown outcome", classified)
	}
}

func TestControlHandlePlacementRejectsDeterministicSelectionFailure(t *testing.T) {
	stack := &Stack{composition: runtimeComposition{authorities: runtimeHostAuthorities{store: newAppConfigStore(t.TempDir())}}}
	_, err := stack.composition.resolveControlHandlePlacement(context.Background(), agentbinding.Handle("missing"))
	var outcomeErr *appserver.OutcomeError
	if !errors.As(err, &outcomeErr) ||
		outcomeErr.Outcome != appserver.OutcomeRejected ||
		errorcode.CodeOf(err) != errorcode.FailedPrecondition {
		t.Fatalf("resolveControlHandlePlacement(missing) = %v, want rejected failed_precondition", err)
	}
}

func TestControlHandlePlacementStoreFailureRemainsUnknown(t *testing.T) {
	store := newAppConfigStore(t.TempDir())
	store.path = t.TempDir()
	stack := &Stack{composition: runtimeComposition{authorities: runtimeHostAuthorities{store: store}}}
	_, err := stack.composition.resolveControlHandlePlacement(context.Background(), agentbinding.HandleReviewer)
	if err == nil || errorcode.CodeOf(err) == errorcode.FailedPrecondition {
		t.Fatalf("resolveControlHandlePlacement(store failure) = %v, want internal failure", err)
	}
	classified := classifyControlBackendError(err)
	var outcomeErr *appserver.OutcomeError
	if !errors.As(classified, &outcomeErr) || outcomeErr.Outcome != appserver.OutcomeUnknown {
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
	feeds, err := appserver.NewFeedRegistry(appserver.FeedRegistryConfig{
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
	stack := &Stack{
		composition: runtimeComposition{
			authorities: runtimeHostAuthorities{controlFeeds: feeds}, sessions: sessions, gateway: kernel,
		},
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

	mainEvents := make(chan eventstream.Envelope, 2)
	handle := &controlClientIngressHandle{events: mainEvents}
	stack.composition.attachControlClientHandle(handle)

	status := schema.ToolStatusInProgress
	title := "RunCommand"
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
			metautil.Root: map[string]any{
				metautil.Runtime: map[string]any{
					metautil.RuntimeTool: map[string]any{
						metautil.RuntimeToolName: "RunCommand",
					},
					metautil.RuntimeTask: map[string]any{
						metautil.RuntimeTaskID:         "task-1",
						metautil.RuntimeTaskTerminalID: "terminal-1",
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
	stack := &Stack{composition: runtimeComposition{authorities: runtimeHostAuthorities{
		controlFeeds: controlClientFeedRegistry{feed: feed},
	}}}
	stack.composition.attachControlClientHandle(handle)

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
		composition: runtimeComposition{
			authorities: runtimeHostAuthorities{appName: "caelis", controlFeeds: controlClientFeedRegistry{feed: feed}},
			sessions:    sessions,
			gateway:     kernel,
		},
	}
	backend := &countingControlClientBackend{backend: &controlCommandBackend{composition: &stack.composition}}
	commands, err := appserver.NewCommandService(appserver.CommandServiceConfig{
		Authorizer: controlClientAllowAuthorizer{},
		Operations: appserver.NewMemoryOperationStore(),
		Backend:    backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := appserver.CreateSessionRequest{
		WriteBase:          appserver.WriteBase{OperationID: "operation-prime-failure"},
		PreferredSessionID: "session-prime-failure",
	}
	principal := appserver.Principal{ID: "owner"}
	first, err := commands.CreateSession(context.Background(), principal, request)
	if err != nil || first.Outcome != appserver.OutcomeCommitted || first.Detail != controlFeedCatchUpWarning {
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
	turn, err := kernel.BeginTurn(ctx, kernelimpl.BeginTurnRequest{SessionRef: active.SessionRef, Input: "wait"})
	if err != nil {
		t.Fatal(err)
	}
	codec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := appserver.NewFeedRegistry(appserver.FeedRegistryConfig{Reader: sessions, CursorCodec: codec})
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
	stack := &Stack{
		composition: runtimeComposition{
			authorities: runtimeHostAuthorities{controlFeeds: feeds}, sessions: sessions, gateway: kernel,
		},
	}
	expected := active.Revision
	result, err := testControlCommandBackend(stack).ExecuteControlCommand(ctx, appserver.Principal{ID: "owner"}, appserver.ActionSessionClose, appserver.CloseSessionRequest{
		WriteBase: appserver.WriteBase{SessionID: active.SessionID, ExpectedRevision: &expected},
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision <= active.Revision {
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
	result, err = stack.commandBackend.ExecuteControlCommand(ctx, appserver.Principal{ID: "owner"}, appserver.ActionPrompt, appserver.PromptRequest{
		WriteBase: appserver.WriteBase{SessionID: active.SessionID}, Input: "must be rejected",
	})
	if !errors.Is(err, appserver.ErrSessionClosed) || result.Revision != current.Revision {
		t.Fatalf("prompt after close = %#v, %v", result, err)
	}
	_ = turn.Handle.Close()
}

func TestControlClientPromptUsesHostLifecycleAfterAdmission(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sessions := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "session-host-lifecycle",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controlClientLifecycleRuntime{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	kernel, err := kernelimpl.New(kernelimpl.Config{
		Sessions: sessions, Runtime: runtime, Resolver: controlClientIngressResolver{},
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
	feeds, err := appserver.NewFeedRegistry(appserver.FeedRegistryConfig{
		Reader: sessions, CursorCodec: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostCtx, cancelHost := context.WithCancel(context.Background())
	stack := &Stack{
		composition: runtimeComposition{
			authorities: runtimeHostAuthorities{controlFeeds: feeds, lifecycleCtx: hostCtx},
			sessions:    sessions,
			gateway:     kernel,
		},
		lifecycleCancel: cancelHost,
	}

	admissionCtx, cancelAdmission := context.WithCancel(context.Background())
	result, err := testControlCommandBackend(stack).ExecuteControlCommand(
		admissionCtx,
		appserver.Principal{ID: "owner"},
		appserver.ActionPrompt,
		appserver.PromptRequest{
			WriteBase: appserver.WriteBase{
				OperationID: "operation-host-lifecycle",
				SessionID:   active.SessionID,
			},
			Input: "keep running",
		},
	)
	if err != nil || result.Target.HandleID == "" || result.Target.RunID == "" || result.Target.TurnID == "" {
		t.Fatalf("Prompt result = %#v, %v", result, err)
	}
	select {
	case <-runtime.started:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not start")
	}

	cancelAdmission()
	select {
	case <-runtime.stopped:
		t.Fatal("request cancellation stopped the Host-owned Turn")
	case <-time.After(50 * time.Millisecond):
	}

	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stack.Close did not stop the Host-owned Turn")
	}
}

func TestControlClientParticipantPromptUsesHostLifecycleAfterAdmission(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sessions := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "session-participant-host-lifecycle",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controlClientLifecycleRuntime{
		session:            active,
		participantStarted: make(chan struct{}),
		participantStopped: make(chan struct{}),
	}
	kernel, err := kernelimpl.New(kernelimpl.Config{
		Sessions: sessions, Runtime: runtime, Control: runtime, Resolver: controlClientIngressResolver{},
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
	feeds, err := appserver.NewFeedRegistry(appserver.FeedRegistryConfig{
		Reader: sessions, CursorCodec: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostCtx, cancelHost := context.WithCancel(context.Background())
	stack := &Stack{
		composition: runtimeComposition{
			authorities: runtimeHostAuthorities{controlFeeds: feeds, lifecycleCtx: hostCtx},
			sessions:    sessions,
			gateway:     kernel,
		},
		lifecycleCancel: cancelHost,
	}

	admissionCtx, cancelAdmission := context.WithCancel(context.Background())
	result, err := testControlCommandBackend(stack).ExecuteControlCommand(
		admissionCtx,
		appserver.Principal{ID: "owner"},
		appserver.ActionParticipantPrompt,
		appserver.PromptParticipantRequest{
			WriteBase: appserver.WriteBase{
				OperationID: "operation-participant-host-lifecycle",
				SessionID:   active.SessionID,
			},
			ParticipantID: "participant-1",
			Input:         "keep running",
		},
	)
	if err != nil || result.Target.HandleID == "" || result.Target.RunID == "" || result.Target.TurnID == "" {
		t.Fatalf("PromptParticipant result = %#v, %v", result, err)
	}
	select {
	case <-runtime.participantStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("participant Runtime did not start")
	}

	cancelAdmission()
	select {
	case <-runtime.participantStopped:
		t.Fatal("request cancellation stopped the Host-owned participant Turn")
	case <-time.After(50 * time.Millisecond):
	}

	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.participantStopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stack.Close did not stop the Host-owned participant Turn")
	}
}

func TestControlHTTPClientControlsHostOwnedTurnAcrossRequests(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sessions := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "session-remote-host",
	})
	if err != nil {
		t.Fatal(err)
	}
	inner := newControlClientFencedRuntime(active)
	fenced, err := controlplane.NewFencedRuntime(controlplane.FencedRuntimeConfig{
		Runtime: inner, Fences: sessions, OwnerID: "host-epoch-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := kernelimpl.New(kernelimpl.Config{
		Sessions: sessions, Runtime: fenced, Resolver: controlClientIngressResolver{},
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
	feeds, err := appserver.NewFeedRegistry(appserver.FeedRegistryConfig{
		Reader: sessions, CursorCodec: codec,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostCtx, cancelHost := context.WithCancel(context.Background())
	stack := &Stack{
		composition: runtimeComposition{
			authorities: runtimeHostAuthorities{controlFeeds: feeds, lifecycleCtx: hostCtx},
			sessions:    sessions,
			gateway:     kernel,
		},
		lifecycleCancel: cancelHost,
	}
	runtimeStateReader, err := newControlRuntimeStateReader(&stack.composition)
	if err != nil {
		t.Fatal(err)
	}
	state, err := appserver.NewStateService(appserver.StateServiceConfig{
		Sessions: sessions, Runtime: runtimeStateReader, Feeds: feeds,
	})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := appserver.NewCommandService(appserver.CommandServiceConfig{
		Authorizer: appserver.SessionAuthorizer{Sessions: sessions},
		Operations: appserver.NewMemoryOperationStore(),
		Backend:    testControlCommandBackend(stack),
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := appserver.NewClient(appserver.ClientConfig{
		Commands: commands,
		State:    state,
		Feeds:    feeds,
		Authorizer: appserver.SessionAuthorizer{
			Sessions: sessions,
		},
		Sessions: sessions,
	})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := controlserver.BearerTokenAuthenticator(
		"0123456789abcdef0123456789abcdef",
		appserver.Principal{ID: "owner"},
	)
	if err != nil {
		t.Fatal(err)
	}
	server, err := controlserver.New(controlserver.HandlerConfig{
		Services: gatewayTestAppServerServices(service, gatewayTestStatusService{}), Authenticator: authenticator,
		AllowedHosts: []string{"127.0.0.1", "localhost", "::1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := testenv.NewHTTPServer(t, server.Handler())
	defer httpServer.Close()
	remoteA, err := httpclient.New(httpclient.Config{
		BaseURL: httpServer.URL, BearerToken: "0123456789abcdef0123456789abcdef",
		HTTPClient:    httpServer.Client(),
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteB, err := httpclient.New(httpclient.Config{
		BaseURL: httpServer.URL, BearerToken: "0123456789abcdef0123456789abcdef",
		HTTPClient:    httpServer.Client(),
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}
	observerState, err := remoteB.InspectSession(context.Background(), appserver.StateRequest{SessionID: active.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := remoteB.Reconnect(context.Background(), appserver.ReconnectRequest{
		SessionID: active.SessionID, Cursor: observerState.BoundaryCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Subscription.Close()

	admissionCtx, cancelAdmission := context.WithCancel(context.Background())
	prompt, err := remoteA.Prompt(admissionCtx, appserver.PromptRequest{
		WriteBase: appserver.WriteBase{
			OperationID: "operation-remote-host-prompt",
			SessionID:   active.SessionID,
		},
		Input: "continue after this HTTP request",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-inner.runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not start through the Control Host")
	}
	beforeSteer, err := sessions.SessionFence(context.Background(), active.SessionRef)
	if err != nil || beforeSteer.FenceID == "" || beforeSteer.OwnerID != "host-epoch-a" {
		t.Fatalf("fence after prompt admission = %#v, %v", beforeSteer, err)
	}
	cancelAdmission()
	select {
	case <-inner.runner.done:
		t.Fatal("completed HTTP request owned the accepted Turn lifetime")
	case <-time.After(50 * time.Millisecond):
	}

	observedTarget := waitForControlClientTurnTarget(t, observed.Subscription)
	if observedTarget != prompt.Target {
		t.Fatalf("client B observed target = %#v, want prompt target %#v", observedTarget, prompt.Target)
	}
	if _, err := remoteB.Steer(context.Background(), appserver.SteerRequest{
		WriteBase: appserver.WriteBase{
			OperationID: "operation-remote-host-steer",
			SessionID:   active.SessionID,
		},
		Target: observedTarget,
		Input:  "midturn input from the second client",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case submission := <-inner.runner.submissions:
		if submission.Text != "midturn input from the second client" {
			t.Fatalf("runner submission = %#v", submission)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second client Steer did not reach the active runner")
	}
	durable, err := sessions.SessionFence(context.Background(), active.SessionRef)
	if err != nil || durable.FenceID == "" || durable.OwnerID != "host-epoch-a" {
		t.Fatalf("fence after second-client observation/steer = %#v, %v", durable, err)
	}
	if durable.FenceID != beforeSteer.FenceID || durable.OwnerID != beforeSteer.OwnerID || durable.FencingToken != beforeSteer.FencingToken {
		t.Fatalf("second-client observation/steer changed durable fence: before=%#v after=%#v", beforeSteer, durable)
	}

	if _, err := remoteB.Cancel(context.Background(), appserver.CancelRequest{
		WriteBase: appserver.WriteBase{
			OperationID: "operation-remote-host-cancel",
			SessionID:   active.SessionID,
		},
		Target: prompt.Target,
		Reason: "cancel from a second client request",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inner.runner.done:
	case <-time.After(2 * time.Second):
		t.Fatal("remote Cancel did not stop the Host-owned Turn")
	}
	waitForControlClientFenceRelease(t, sessions, active.SessionRef)
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
}

type gatewayTestStatusService struct{}

func (gatewayTestStatusService) SessionStatus(context.Context, appserver.Principal, appserver.StatusRequest) (controlstatus.StatusSnapshot, error) {
	return controlstatus.StatusSnapshot{}, nil
}

type controlClientNoopTaskStreams struct{}

func (controlClientNoopTaskStreams) List(context.Context, acptaskstream.Principal, acptaskstream.ListRequest) (acptaskstream.ListResult, error) {
	return acptaskstream.ListResult{}, nil
}

func (controlClientNoopTaskStreams) Events(context.Context, acptaskstream.Principal, acptaskstream.ReadRequest) (acptaskstream.Batch, error) {
	return acptaskstream.Batch{}, nil
}

func (controlClientNoopTaskStreams) Subscribe(context.Context, acptaskstream.Principal, acptaskstream.SubscribeRequest) (acptaskstream.SubscribeResult, error) {
	return acptaskstream.SubscribeResult{}, nil
}

var _ acptaskstream.Service = controlClientNoopTaskStreams{}

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
	started, err := kernel.BeginTurn(ctx, kernelimpl.BeginTurnRequest{SessionRef: active.SessionRef, Input: "wait"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := kernel.Interrupt(ctx, kernelimpl.InterruptRequest{SessionRef: active.SessionRef}); err != nil {
			t.Errorf("interrupt main turn: %v", err)
		}
	}()
	stack := &Stack{
		composition: runtimeComposition{sessions: sessions, gateway: kernel},
	}
	result, err := testControlCommandBackend(stack).ExecuteControlCommand(ctx, appserver.Principal{ID: "owner"}, appserver.ActionParticipantCancel, appserver.CancelParticipantRequest{
		WriteBase: appserver.WriteBase{SessionID: active.SessionID}, ParticipantID: "not-the-main-turn",
		Target: appserver.TurnTarget{HandleID: started.Handle.HandleID(), RunID: started.Handle.RunID(), TurnID: started.Handle.TurnID()},
	})
	var outcomeErr *appserver.OutcomeError
	if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != appserver.OutcomeConflicted || result.Outcome != appserver.OutcomeCommitted {
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

type controlClientLifecycleRuntime struct {
	session            session.Session
	started            chan struct{}
	stopped            chan struct{}
	participantStarted chan struct{}
	participantStopped chan struct{}
}

type controlClientFencedRuntime struct {
	session session.Session
	runner  *controlClientFencedRunner
}

func newControlClientFencedRuntime(active session.Session) *controlClientFencedRuntime {
	return &controlClientFencedRuntime{
		session: active,
		runner: &controlClientFencedRunner{
			ref:         active.SessionRef,
			started:     make(chan struct{}),
			done:        make(chan struct{}),
			submissions: make(chan agent.Submission, 1),
		},
	}
}

func (runtime *controlClientFencedRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	close(runtime.runner.started)
	return agent.RunResult{Session: runtime.session, Handle: runtime.runner}, nil
}

func (*controlClientFencedRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{Status: agent.RunLifecycleStatusRunning}, nil
}

func (*controlClientFencedRuntime) RunnerCompletionWaiterGuaranteed() {}

type controlClientFencedRunner struct {
	ref         session.SessionRef
	started     chan struct{}
	done        chan struct{}
	submissions chan agent.Submission
	finishOnce  sync.Once
}

func (*controlClientFencedRunner) RunID() string { return "run-fenced-http" }

func (runner *controlClientFencedRunner) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		message := model.NewTextMessage(model.RoleAssistant, "working")
		if !yield(&session.Event{
			ID: "assistant-fenced-http", SessionID: runner.ref.SessionID, Type: session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical, Message: &message, Text: "working",
		}, nil) {
			return
		}
		<-runner.done
	}
}

func (runner *controlClientFencedRunner) Submit(submission agent.Submission) error {
	select {
	case <-runner.done:
		return errors.New("fenced test runner is closed")
	case runner.submissions <- submission:
		return nil
	}
}

func (runner *controlClientFencedRunner) Cancel() agent.CancelResult {
	runner.finishOnce.Do(func() { close(runner.done) })
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}

func (runner *controlClientFencedRunner) Close() error {
	runner.finishOnce.Do(func() { close(runner.done) })
	return nil
}

func (runner *controlClientFencedRunner) WaitCompletion(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-runner.done:
		return nil
	}
}

func waitForControlClientFenceRelease(t *testing.T, reader session.SessionFenceReader, ref session.SessionRef) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		fence, err := reader.SessionFence(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		if fence.FenceID == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Session fence remained held after producer completion: %#v", fence)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForControlClientTurnTarget(t *testing.T, subscription appserver.FeedSubscription) appserver.TurnTarget {
	t.Helper()
	if subscription == nil {
		t.Fatal("client B reconnect returned no subscription")
	}
	backfill := subscription.Backfill()
	events := subscription.Events()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for backfill != nil || events != nil {
		select {
		case envelope, ok := <-backfill:
			if !ok {
				backfill = nil
				continue
			}
			if envelope.HandleID != "" && envelope.RunID != "" && envelope.TurnID != "" {
				return appserver.TurnTarget{HandleID: envelope.HandleID, RunID: envelope.RunID, TurnID: envelope.TurnID}
			}
		case envelope, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if envelope.HandleID != "" && envelope.RunID != "" && envelope.TurnID != "" {
				return appserver.TurnTarget{HandleID: envelope.HandleID, RunID: envelope.RunID, TurnID: envelope.TurnID}
			}
		case <-timer.C:
			t.Fatal("client B did not observe the active Turn target")
		}
	}
	t.Fatal("client B feed closed before observing the active Turn target")
	return appserver.TurnTarget{}
}

func (runtime *controlClientLifecycleRuntime) Run(ctx context.Context, _ agent.RunRequest) (agent.RunResult, error) {
	close(runtime.started)
	<-ctx.Done()
	close(runtime.stopped)
	return agent.RunResult{}, ctx.Err()
}

func (*controlClientLifecycleRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (runtime *controlClientLifecycleRuntime) HandoffController(context.Context, agent.HandoffControllerRequest) (session.Session, error) {
	return runtime.session, nil
}

func (runtime *controlClientLifecycleRuntime) AttachParticipant(context.Context, agent.AttachParticipantRequest) (session.Session, error) {
	return runtime.session, nil
}

func (runtime *controlClientLifecycleRuntime) PromptParticipant(ctx context.Context, _ agent.PromptParticipantRequest) (agent.RunResult, error) {
	close(runtime.participantStarted)
	<-ctx.Done()
	close(runtime.participantStopped)
	return agent.RunResult{}, ctx.Err()
}

func (runtime *controlClientLifecycleRuntime) DetachParticipant(context.Context, agent.DetachParticipantRequest) (session.Session, error) {
	return runtime.session, nil
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

func (controlClientIngressResolver) ResolveTurn(context.Context, kernelimpl.TurnIntent) (kernelimpl.ResolvedTurn, error) {
	return kernelimpl.ResolvedTurn{}, nil
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
func (*controlClientIngressHandle) Submit(context.Context, kernelimpl.SubmitRequest) error {
	return nil
}
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
func (*controlClientAttachmentFailureHandle) Submit(context.Context, kernelimpl.SubmitRequest) error {
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
	feed appserver.SessionFeed
	err  error
}

func (registry controlClientFeedRegistry) Session(session.SessionRef) (appserver.SessionFeed, error) {
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
func (*controlClientSessionFeed) Subscribe(context.Context, appserver.SubscribeRequest) (appserver.SubscribeResult, error) {
	return appserver.SubscribeResult{}, errors.New("test feed does not support Subscribe")
}
func (*controlClientSessionFeed) SubscribeFromNow(context.Context) (appserver.FeedSubscription, error) {
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
func (feed *controlClientSessionFeed) AttachTo(_ appserver.FeedSubscription, events <-chan eventstream.Envelope) <-chan error {
	return feed.Attach(events)
}
func (*controlClientSessionFeed) Boundary() (*eventstream.FeedPosition, string) { return nil, "" }

type controlClientAllowAuthorizer struct{}

func (controlClientAllowAuthorizer) Authorize(context.Context, appserver.Principal, appserver.Action, string) error {
	return nil
}

type countingControlClientBackend struct {
	backend appserver.CommandBackend
	calls   atomic.Int32
}

func (backend *countingControlClientBackend) ExecuteControlCommand(
	ctx context.Context,
	principal appserver.Principal,
	action appserver.Action,
	request any,
) (appserver.CommandResult, error) {
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
