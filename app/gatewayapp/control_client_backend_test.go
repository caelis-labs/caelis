package gatewayapp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/app/controlserver"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/modelprofile"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/controlplane"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/internal/testenv"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	acptaskstream "github.com/caelis-labs/caelis/control/appserver/taskstream"
)

func TestClassifyControlBackendErrorTreatsFenceConflictAsConflict(t *testing.T) {
	err := classifyControlBackendError(&session.FenceConflictError{SessionID: "session-1", Detail: "active execution fence"})
	var outcomeErr *appserver.OutcomeError
	if !errors.As(err, &outcomeErr) || outcomeErr.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("classifyControlBackendError() = %v, want conflicted outcome", err)
	}
}

func TestControlCommandBackendRejectsCompactForACPController(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stack, active := newStackWithAssemblyForToolTest(t, assembly.ResolvedAssembly{})
	active, err := stack.composition.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: active.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "codex", AgentName: "codex", EpochID: "epoch-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := active.Revision
	result, err := (&controlCommandBackend{composition: &stack.composition}).ExecuteControlCommand(
		ctx,
		appserver.Principal{},
		appserver.ActionSessionCompact,
		appserver.CompactSessionRequest{WriteBase: appserver.WriteBase{
			OperationID: "compact-acp", SessionID: active.SessionID, ExpectedRevision: &expected,
		}},
	)
	if result.Outcome != appserver.OutcomeRejected || errorcode.CodeOf(err) != errorcode.Unsupported {
		t.Fatalf("CompactSession() = %#v, %v; want rejected unsupported", result, err)
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
	codec, err := appserver.NewCursorCodec(appserver.CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := appserver.NewFeedRegistry(appserver.FeedRegistryConfig{Reader: sessions, Spool: newGatewayTestStreamSpool(t), CursorCodec: codec})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := feeds.Session(active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	_, cursor := feed.Boundary()
	subscribed, err := feed.Subscribe(context.Background(), appserver.SubscribeRequest{
		SessionID: active.SessionID,
		Cursor:    cursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	subscription := subscribed.Subscription
	defer subscription.Close()
	taskLifecycle := &recordingTaskOutputLifecycle{}
	stack := &Stack{
		composition: runtimeComposition{
			authorities: runtimeHostAuthorities{
				controlFeeds: feeds, controlFeedLifecycle: feeds, taskOutputLifecycle: taskLifecycle,
			},
			sessions: sessions, gateway: kernel,
		},
	}
	expected := active.Revision
	result, err := testControlCommandBackend(stack).ExecuteControlCommand(ctx, appserver.Principal{ID: "owner"}, appserver.ActionSessionClose, appserver.CloseSessionRequest{
		WriteBase: appserver.WriteBase{SessionID: active.SessionID, ExpectedRevision: &expected},
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision <= active.Revision {
		t.Fatalf("CloseSession result = %#v, %v", result, err)
	}
	events, _, err := nextGatewayFeedEvents(t.Context(), subscription, &appserver.FeedDeliveryAssembler{})
	if err != nil || len(events) != 1 {
		t.Fatalf("live close delivery = %#v, %v", events, err)
	}
	envelope := events[0]
	if envelope.Lifecycle == nil || envelope.Lifecycle.State != "closed" || envelope.Position == nil || envelope.Position.Durable == nil {
		t.Fatalf("live close envelope = %#v", envelope)
	}
	taskLifecycle.mu.Lock()
	releasedSessions := append([]session.SessionRef(nil), taskLifecycle.sessions...)
	taskLifecycle.mu.Unlock()
	if len(releasedSessions) != 1 || releasedSessions[0].SessionID != active.SessionID {
		t.Fatalf("released Session traces = %#v", releasedSessions)
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

func TestControlClientParticipantDetachReleasesSubagentTaskTrace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sessions := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "session-detach-trace",
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := active.Revision
	active, err = sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &expected,
		Binding: session.ParticipantBinding{
			ID: "participant-1", Kind: session.ParticipantKindSubagent,
			DelegationID: "task-1", SessionID: "child-session-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controlClientLifecycleRuntime{session: active}
	kernel, err := kernelimpl.New(kernelimpl.Config{
		Sessions: sessions, Runtime: runtime, Control: runtime, Resolver: controlClientIngressResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &recordingTaskOutputLifecycle{}
	stack := &Stack{composition: runtimeComposition{
		authorities: runtimeHostAuthorities{taskOutputLifecycle: lifecycle}, sessions: sessions, gateway: kernel,
	}}
	expected = active.Revision
	result, err := testControlCommandBackend(stack).ExecuteControlCommand(ctx, appserver.Principal{ID: "owner"}, appserver.ActionParticipantDetach, appserver.DetachParticipantRequest{
		WriteBase:     appserver.WriteBase{SessionID: active.SessionID, ExpectedRevision: &expected},
		ParticipantID: "participant-1",
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("DetachParticipant result = %#v, %v", result, err)
	}
	lifecycle.mu.Lock()
	released := append([]taskapi.Ref(nil), lifecycle.tasks...)
	lifecycle.mu.Unlock()
	if len(released) != 1 || released[0].SessionID != active.SessionID || released[0].TaskID != "task-1" {
		t.Fatalf("released Task traces = %#v", released)
	}
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
	codec, err := appserver.NewCursorCodec(appserver.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := appserver.NewFeedRegistry(appserver.FeedRegistryConfig{
		Reader: sessions, Spool: newGatewayTestStreamSpool(t), CursorCodec: codec,
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
	codec, err := appserver.NewCursorCodec(appserver.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := appserver.NewFeedRegistry(appserver.FeedRegistryConfig{
		Reader: sessions, Spool: newGatewayTestStreamSpool(t), CursorCodec: codec,
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
	codec, err := appserver.NewCursorCodec(appserver.CursorCodecConfig{
		Secret: []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := appserver.NewFeedRegistry(appserver.FeedRegistryConfig{
		Reader: sessions, Spool: newGatewayTestStreamSpool(t), CursorCodec: codec,
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

func (controlClientNoopTaskStreams) Events(context.Context, acptaskstream.Principal, acptaskstream.ReadRequest) (acptaskstream.ReadResult, error) {
	return acptaskstream.ReadResult{}, nil
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

func (runtime *controlClientFencedRuntime) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	close(runtime.runner.started)
	if req.SourceObserver != nil {
		message := model.NewTextMessage(model.RoleAssistant, "working")
		if err := req.SourceObserver.ObserveSourceEvent(ctx, agent.SourceEvent{Canonical: &session.Event{
			ID: "assistant-fenced-http", SessionID: runtime.runner.ref.SessionID, Type: session.EventTypeAssistant,
			Visibility: session.VisibilityCanonical, Message: &message, Text: "working",
		}}); err != nil {
			return agent.RunResult{}, err
		}
	}
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
	assembler := &appserver.FeedDeliveryAssembler{}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case delivery, ok := <-subscription.Deliveries():
			if !ok {
				t.Fatal("client B feed closed before observing the active Turn target")
			}
			delivered, _, err := assembler.Accept(delivery)
			if err != nil {
				t.Fatal(err)
			}
			for _, envelope := range delivered {
				if envelope.HandleID != "" && envelope.RunID != "" && envelope.TurnID != "" {
					return appserver.TurnTarget{HandleID: envelope.HandleID, RunID: envelope.RunID, TurnID: envelope.TurnID}
				}
			}
		case <-timer.C:
			t.Fatal("client B did not observe the active Turn target")
		}
	}
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

type controlClientIngressRuntime struct{}

func (controlClientIngressRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{}, nil
}

func (controlClientIngressRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

type controlClientIngressResolver struct{}

func (controlClientIngressResolver) ResolveTurn(context.Context, kernelimpl.TurnIntent) (kernelimpl.ResolvedTurn, error) {
	return kernelimpl.ResolvedTurn{}, nil
}

type controlClientFeedRegistry struct {
	feed appserver.SessionFeed
	err  error
}

func (registry controlClientFeedRegistry) Session(session.SessionRef) (appserver.SessionFeed, error) {
	return registry.feed, registry.err
}

type recordingTaskOutputLifecycle struct {
	mu       sync.Mutex
	tasks    []taskapi.Ref
	sessions []session.SessionRef
}

func (r *recordingTaskOutputLifecycle) ReleaseTask(_ context.Context, ref taskapi.Ref) error {
	r.mu.Lock()
	r.tasks = append(r.tasks, ref)
	r.mu.Unlock()
	return nil
}

func (r *recordingTaskOutputLifecycle) ReleaseSession(_ context.Context, ref session.SessionRef) error {
	r.mu.Lock()
	r.sessions = append(r.sessions, ref)
	r.mu.Unlock()
	return nil
}

func (*recordingTaskOutputLifecycle) Close(context.Context) error { return nil }

type controlClientSessionFeed struct {
	primeErr     error
	publishErr   error
	primeCalls   atomic.Int32
	publishCalls atomic.Int32
}

func (feed *controlClientSessionFeed) Prime(context.Context) error {
	feed.primeCalls.Add(1)
	return feed.primeErr
}
func (feed *controlClientSessionFeed) Publish(eventstream.Envelope) error {
	feed.publishCalls.Add(1)
	return feed.publishErr
}
func (*controlClientSessionFeed) Subscribe(context.Context, appserver.SubscribeRequest) (appserver.SubscribeResult, error) {
	return appserver.SubscribeResult{}, errors.New("test feed does not support Subscribe")
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
