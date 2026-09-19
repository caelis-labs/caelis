package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

const (
	bindingCommitEpoch      = "epoch-binding-commit"
	bindingCommitDurableID  = "remote-old"
	bindingCommitFreshID    = "remote-fresh"
	bindingCommitDurableSeq = 2
)

// faultingBindingProvider exposes the optional controller.BindingProvider
// capability with a configurable result so Runtime's epoch validation and
// failure paths can be exercised without a live endpoint.
type faultingBindingProvider struct {
	stubACPController
	binding session.ControllerBinding
	found   bool
	err     error
}

func (p faultingBindingProvider) ActiveControllerBinding(context.Context, session.SessionRef) (session.ControllerBinding, bool, error) {
	return session.CloneControllerBinding(p.binding), p.found, p.err
}

// seedBindingCommitSession seeds one ACP-controlled file-backed Session with one
// already-delivered dialogue turn, a durable remote binding sitting at a
// non-zero checkpoint, and the exact execution fence a Runtime Turn must carry.
func seedBindingCommitSession(t *testing.T, root, sessionID string) (*sessionfile.Store, session.Session, session.ControllerBinding, session.SessionFence) {
	t.Helper()
	ctx := context.Background()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "binding-commit",
		PreferredSessionID: sessionID,
		Workspace:          session.WorkspaceRef{Key: "ws-binding-commit", CWD: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	for _, event := range []*session.Event{userTextEvent("previous request"), assistantEvent("previous answer")} {
		appendTestEvent(t, store, active.SessionRef, event)
	}
	durable := session.ControllerBinding{
		Kind:            session.ControllerKindACP,
		ControllerID:    "acp-main",
		AgentName:       "codex",
		Label:           "ACP Main",
		EpochID:         bindingCommitEpoch,
		RemoteSessionID: bindingCommitDurableID,
		ContextSyncSeq:  bindingCommitDurableSeq,
		Source:          "test",
	}
	active, err = store.BindController(ctx, session.BindControllerRequest{SessionRef: active.SessionRef, Binding: durable})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	fence, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "binding-commit-host"})
	if err != nil {
		t.Fatalf("AcquireSessionFence() error = %v", err)
	}
	return store, active, durable, fence
}

// replacementRemoteBinding is the pre-prompt identity a backend exposes after
// reconnecting to a fresh remote Session that has received no context yet.
func replacementRemoteBinding(durable session.ControllerBinding) session.ControllerBinding {
	live := session.CloneControllerBinding(durable)
	live.RemoteSessionID = bindingCommitFreshID
	live.ContextSyncSeq = 0
	return live
}

func fullDialogueContext() agent.ContextTransfer {
	return agent.ContextTransfer{Turns: []agent.ContextTurn{{
		UserMessages:     []string{"previous request"},
		AssistantSummary: "previous answer",
	}}}
}

func fencedRunContext(fence session.SessionFence) context.Context {
	return session.ContextWithRuntimeFence(context.Background(), fence)
}

func TestRuntimeCommitBindingPublishesReplacementRemoteBeforePrompt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, active, durable, fence := seedBindingCommitSession(t, root, "sess-binding-commit-publish")
	ref := active.SessionRef
	live := replacementRemoteBinding(durable)

	committed := make(chan session.ControllerBinding, 1)
	turnRequests := make(chan controller.TurnRequest, 1)
	backend := bindingAwareACPController{
		binding: live,
		stubACPController: stubACPController{runTurn: func(runCtx context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
			if req.CommitBinding == nil {
				return controller.TurnResult{}, errors.New("controller TurnRequest is missing the CommitBinding callback")
			}
			if err := req.CommitBinding(runCtx); err != nil {
				return controller.TurnResult{}, fmt.Errorf("CommitBinding() error = %w", err)
			}
			// The replacement identity must be durable while the remote turn is
			// still live, before any completion or delivery acknowledgement.
			duringTurn, err := store.Session(runCtx, ref)
			if err != nil {
				return controller.TurnResult{}, err
			}
			committed <- session.CloneControllerBinding(duringTurn.Controller)
			turnRequests <- req
			handle := newTestControllerTurnHandle(nil)
			handle.publishEvent(assistantEvent("fresh answer"))
			handle.finish()
			return controller.TurnResult{Handle: handle}, nil
		}},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions:     store,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  backend,
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(fencedRunContext(fence), agent.RunRequest{SessionRef: ref, Input: "new request"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("Run() completion error = %v", err)
	}

	if got := <-committed; !reflect.DeepEqual(got, live) {
		t.Fatalf("committed controller = %#v, want live replacement %#v", got, live)
	}
	turnReq := <-turnRequests
	if !agent.ContextTransferEmpty(turnReq.Context) {
		t.Fatalf("admitted Context = %#v, want no already-delivered dialogue at durable checkpoint %d", turnReq.Context, bindingCommitDurableSeq)
	}
	if want := fullDialogueContext(); !reflect.DeepEqual(turnReq.FreshContext, want) {
		t.Fatalf("admitted FreshContext = %#v, want %#v", turnReq.FreshContext, want)
	}

	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	reloaded, err := reopened.Session(context.Background(), ref)
	if err != nil {
		t.Fatalf("reopened Session() error = %v", err)
	}
	if reloaded.Controller.RemoteSessionID != bindingCommitFreshID || reloaded.Controller.EpochID != bindingCommitEpoch {
		t.Fatalf("reopened controller = %#v, want %q under epoch %q", reloaded.Controller, bindingCommitFreshID, bindingCommitEpoch)
	}
	if reloaded.Controller.ContextSyncSeq == 0 {
		t.Fatalf("reopened controller ContextSyncSeq = 0; the terminal acknowledgement must remain the delivery authority")
	}
}

func TestRuntimeCommitBindingFailureKeepsDeliveredCheckpoint(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, active, durable, fence := seedBindingCommitSession(t, root, "sess-binding-commit-failure")
	ref := active.SessionRef
	live := replacementRemoteBinding(durable)

	committedSeq := make(chan uint64, 1)
	backend := bindingAwareACPController{
		binding: live,
		stubACPController: stubACPController{runTurn: func(runCtx context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
			if err := req.CommitBinding(runCtx); err != nil {
				return controller.TurnResult{}, fmt.Errorf("CommitBinding() error = %w", err)
			}
			duringTurn, err := store.Session(runCtx, ref)
			if err != nil {
				return controller.TurnResult{}, err
			}
			committedSeq <- duringTurn.Controller.ContextSyncSeq
			// The prompt that follows the pre-prompt commit fails before any
			// delivery acknowledgement, so the checkpoint must not advance.
			handle := newTestControllerTurnHandle(nil)
			handle.publishError(errors.New("remote prompt rejected"))
			handle.finish()
			return controller.TurnResult{Handle: handle}, nil
		}},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions:     store,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  backend,
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := runtime.Run(fencedRunContext(fence), agent.RunRequest{SessionRef: ref, Input: "first request"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err == nil {
		t.Fatal("Run() completed successfully, want the remote prompt failure to surface")
	}
	if got := <-committedSeq; got != 0 {
		t.Fatalf("committed ContextSyncSeq = %d, want 0 for a fresh remote", got)
	}

	// The failed prompt leaves checkpoint zero durable across a reopen.
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	reloaded, err := reopened.Session(context.Background(), ref)
	if err != nil {
		t.Fatalf("reopened Session() error = %v", err)
	}
	if reloaded.Controller.RemoteSessionID != bindingCommitFreshID || reloaded.Controller.EpochID != bindingCommitEpoch || reloaded.Controller.ContextSyncSeq != 0 {
		t.Fatalf("reopened controller = %#v, want %q under epoch %q at checkpoint 0", reloaded.Controller, bindingCommitFreshID, bindingCommitEpoch)
	}

	// A rebuilt route under the exact fence must resend the full history.
	rebuiltRequests := make(chan controller.TurnRequest, 1)
	rebuiltBackend := bindingAwareACPController{
		binding: live,
		stubACPController: stubACPController{runTurn: func(_ context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
			rebuiltRequests <- req
			handle := newTestControllerTurnHandle(nil)
			handle.finish()
			return controller.TurnResult{Handle: handle}, nil
		}},
	}
	rebuilt, err := New(testConfigWithACPForwarder(Config{
		Sessions:     reopened,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  rebuiltBackend,
	}))
	if err != nil {
		t.Fatalf("New(reopened) error = %v", err)
	}
	result, err = rebuilt.Run(fencedRunContext(fence), agent.RunRequest{SessionRef: ref, Input: "second request"})
	if err != nil {
		t.Fatalf("rebuilt Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("rebuilt Run() completion error = %v", err)
	}
	rebuiltReq := <-rebuiltRequests
	want := fullDialogueContext()
	if !reflect.DeepEqual(rebuiltReq.Context, want) {
		t.Fatalf("rebuilt Context = %#v, want full history %#v", rebuiltReq.Context, want)
	}
	if !reflect.DeepEqual(rebuiltReq.FreshContext, want) {
		t.Fatalf("rebuilt FreshContext = %#v, want full history %#v", rebuiltReq.FreshContext, want)
	}
}

func TestCommitControllerBindingRejectsInvalidProviderState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, active, durable, fence := seedBindingCommitSession(t, root, "sess-binding-commit-reject")
	ref := active.SessionRef
	wantDurable := session.CloneControllerBinding(durable)
	ctx := fencedRunContext(fence)

	mismatched := replacementRemoteBinding(durable)
	mismatched.EpochID = "epoch-other"

	cases := []struct {
		name     string
		provider controller.Backend
		wantErr  bool
	}{
		{name: "stale-epoch", provider: bindingAwareACPController{binding: mismatched}, wantErr: true},
		{name: "provider-error", provider: faultingBindingProvider{err: errors.New("endpoint unavailable")}, wantErr: true},
		{name: "provider-not-found", provider: faultingBindingProvider{found: false}, wantErr: true},
		{name: "unchanged-remote", provider: bindingAwareACPController{binding: wantDurable}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtime, err := New(testConfigWithACPForwarder(Config{Sessions: store, AgentFactory: chat.Factory{}, Controllers: tc.provider}))
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			before, err := store.Session(ctx, ref)
			if err != nil {
				t.Fatalf("Session(before) error = %v", err)
			}
			err = runtime.commitControllerBinding(ctx, ref)
			if tc.wantErr && err == nil {
				t.Fatal("commitControllerBinding() error = nil, want rejection")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("commitControllerBinding() error = %v, want no-op", err)
			}
			after, err := store.Session(ctx, ref)
			if err != nil {
				t.Fatalf("Session(after) error = %v", err)
			}
			if after.Revision != before.Revision || !reflect.DeepEqual(after.Controller, wantDurable) {
				t.Fatalf("controller after commit = %#v (revision %d), want unchanged %#v (revision %d)", after.Controller, after.Revision, wantDurable, before.Revision)
			}
		})
	}
}

func TestRuntimeCommitBindingAbsentLeavesDurableController(t *testing.T) {
	t.Parallel()

	store, active, durable, fence := seedBindingCommitSession(t, t.TempDir(), "sess-binding-commit-absent")
	ref := active.SessionRef
	wantDurable := session.CloneControllerBinding(durable)
	live := replacementRemoteBinding(durable)

	duringTurn := make(chan session.ControllerBinding, 1)
	backend := bindingAwareACPController{
		binding: live,
		stubACPController: stubACPController{runTurn: func(runCtx context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
			if req.CommitBinding == nil {
				return controller.TurnResult{}, errors.New("controller TurnRequest is missing the CommitBinding callback")
			}
			// The backend deliberately ignores the callback: Runtime must not
			// publish provider state the backend never committed.
			current, err := store.Session(runCtx, ref)
			if err != nil {
				return controller.TurnResult{}, err
			}
			duringTurn <- session.CloneControllerBinding(current.Controller)
			handle := newTestControllerTurnHandle(nil)
			handle.publishError(errors.New("remote prompt rejected"))
			handle.finish()
			return controller.TurnResult{Handle: handle}, nil
		}},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions:     store,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  backend,
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := runtime.Run(fencedRunContext(fence), agent.RunRequest{SessionRef: ref, Input: "new request"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err == nil {
		t.Fatal("Run() completed successfully, want the remote prompt failure to surface")
	}
	if got := <-duringTurn; !reflect.DeepEqual(got, wantDurable) {
		t.Fatalf("controller during turn = %#v, want unchanged durable %#v", got, wantDurable)
	}
	after, err := store.Session(context.Background(), ref)
	if err != nil {
		t.Fatalf("Session(after) error = %v", err)
	}
	if !reflect.DeepEqual(after.Controller, wantDurable) {
		t.Fatalf("controller after failure = %#v, want unchanged durable %#v", after.Controller, wantDurable)
	}
}
