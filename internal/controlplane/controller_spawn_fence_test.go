package controlplane

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	localruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	"github.com/caelis-labs/caelis/internal/acpbridge"
)

func TestControllerSpawnRetainsTurnFenceThroughCleanupAndReceipt(t *testing.T) {
	for _, cancelTurn := range []bool{true, false} {
		name := "completion"
		if cancelTurn {
			name = "cancellation"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				root := t.TempDir()
				store := &controllerSpawnReceiptStore{
					Store:   sessionfile.NewStore(sessionfile.Config{RootDir: root}),
					started: make(chan struct{}), release: make(chan struct{}),
				}
				releaseReceipt := sync.OnceFunc(func() { close(store.release) })
				defer releaseReceipt()
				active, err := store.StartSession(ctx, session.StartSessionRequest{
					AppName: "caelis", UserID: "test", PreferredSessionID: "controller-spawn-drain",
					Workspace: session.WorkspaceRef{Key: "workspace", CWD: t.TempDir()},
				})
				if err != nil {
					t.Fatal(err)
				}
				active, err = store.BindController(ctx, session.BindControllerRequest{
					SessionRef: active.SessionRef,
					Binding:    session.ControllerBinding{Kind: session.ControllerKindACP, ControllerID: "external", AgentName: "external", EpochID: "epoch", RemoteSessionID: "remote"},
				})
				if err != nil {
					t.Fatal(err)
				}
				backend := &spawnDrainController{started: make(chan struct{}), done: make(chan struct{})}
				child := &spawnDrainChild{started: make(chan struct{}), cleaning: make(chan struct{}), release: make(chan struct{})}
				releaseCleanup := sync.OnceFunc(func() { close(child.release) })
				defer releaseCleanup()
				router, err := NewContextRouter(store)
				if err != nil {
					t.Fatal(err)
				}
				coordinator, err := NewCoordinator(CoordinatorConfig{Sessions: store, Controllers: backend, Context: router, FenceOwnerID: "host"})
				if err != nil {
					t.Fatal(err)
				}
				core, err := localruntime.New(localruntime.Config{
					Sessions: store, AgentFactory: chat.Factory{}, Controllers: backend,
					ControllerContextRouter: router, ControllerRecovery: coordinator,
					ControllerEventForwarder: acpbridge.NewControllerForwarder(store),
					Subagents:                child, TaskStore: sessionfile.NewTaskStore(store.Store),
				})
				if err != nil {
					t.Fatal(err)
				}
				fenced, err := NewFencedRuntime(FencedRuntimeConfig{Runtime: core, Fences: store, OwnerID: "host"})
				if err != nil {
					t.Fatal(err)
				}
				run, err := fenced.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "delegate"})
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					releaseCleanup()
					releaseReceipt()
					_ = run.Handle.Close()
				}()
				<-backend.started
				base := spawn.New([]delegation.Agent{{Name: "helper"}})
				call := tool.Call{ID: "controller-spawn", Name: spawn.ToolName, Input: []byte(`{"agent":"helper","prompt":"work","handle":"worker"}`)}
				spawnDone := make(chan error, 1)
				spawnFinished := make(chan struct{})
				go func() {
					defer close(spawnFinished)
					_, err := core.CallControllerSpawn(ctx, active.SessionRef, "epoch", base, call)
					spawnDone <- err
				}()
				defer func() {
					_ = run.Handle.Cancel()
					releaseCleanup()
					releaseReceipt()
					<-spawnFinished
				}()
				<-child.started
				if cancelTurn {
					if result := run.Handle.Cancel(); result.Err != nil {
						t.Fatal(result.Err)
					}
				} else {
					close(backend.done)
				}
				<-child.cleaning
				turnDone := make(chan error, 1)
				go func() { turnDone <- run.Handle.WaitCompletion(ctx) }()
				assertDraining := func(stage string) {
					t.Helper()
					synctest.Wait()
					select {
					case err := <-turnDone:
						t.Fatalf("Turn completed during %s: %v", stage, err)
					default:
					}
					if _, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "next-turn"}); !errors.Is(err, session.ErrFenceConflict) {
						t.Fatalf("fence acquisition during %s = %v, want conflict", stage, err)
					}
					late := call
					late.ID = "late-spawn"
					if _, err := core.CallControllerSpawn(ctx, active.SessionRef, "epoch", base, late); err == nil {
						t.Fatalf("late Spawn admitted during %s", stage)
					}
					if got := child.calls.Load(); got != 1 {
						t.Fatalf("child startup calls = %d, want 1", got)
					}
				}
				assertDraining("child cleanup")
				releaseCleanup()
				<-store.started
				assertDraining("terminal receipt")
				releaseReceipt()
				if err := <-spawnDone; !errors.Is(err, context.Canceled) || errors.Is(err, session.ErrFenceConflict) {
					t.Fatalf("Spawn settlement = %v, want cancellation without fence conflict", err)
				}
				if err := <-turnDone; (cancelTurn && !errors.Is(err, context.Canceled)) || (!cancelTurn && err != nil) {
					t.Fatalf("Turn completion = %v", err)
				}

				// Reopen durable truth after the real fence wrapper has released it.
				reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
				entries, err := sessionfile.NewTaskStore(reopened).ListSession(ctx, active.SessionRef)
				if err != nil || len(entries) != 1 {
					t.Fatalf("reopened Tasks = %#v, %v", entries, err)
				}
				if entry := entries[0]; entry.State != task.StateFailed || entry.Running || entry.Handle != "" {
					t.Fatalf("unsettled Spawn Task: %#v", entry)
				}
				events, err := reopened.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
				if err != nil {
					t.Fatal(err)
				}
				var receiptSeq, turnSeq uint64
				terminal := session.ExecutionSucceeded
				if cancelTurn {
					terminal = session.ExecutionCancelled
				}
				for _, event := range events {
					if event.Journal == nil {
						continue
					}
					if record := event.Journal.ToolExecution; record != nil && record.Key.ToolCallID == call.ID && record.Status == session.ToolExecutionCancelled {
						receiptSeq = event.Seq
					}
					if record := event.Journal.Execution; record != nil && record.Kind == session.JournalKindTurn && record.RunID == run.Handle.RunID() && record.Status == terminal {
						turnSeq = event.Seq
					}
				}
				if receiptSeq == 0 || turnSeq <= receiptSeq {
					t.Fatalf("terminal order: receipt=%d Turn=%d", receiptSeq, turnSeq)
				}
				next, err := reopened.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "next-turn"})
				if err != nil {
					t.Fatalf("fence remained after settlement: %v", err)
				}
				if err := reopened.ReleaseSessionFence(ctx, session.SessionFenceReleaseRequest(next)); err != nil {
					t.Fatal(err)
				}
				if _, err := core.CallControllerSpawn(ctx, active.SessionRef, "epoch", base, call); err == nil {
					t.Fatal("completed Turn admitted Spawn")
				}
			})
		})
	}
}

type controllerSpawnReceiptStore struct {
	*sessionfile.Store
	started chan struct{}
	release chan struct{}
}

func (s *controllerSpawnReceiptStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if req.Event.Journal != nil && req.Event.Journal.ToolExecution != nil && req.Event.Journal.ToolExecution.Status == session.ToolExecutionCancelled {
		close(s.started)
		<-s.release
	}
	return s.Store.AppendEvent(ctx, req)
}

type spawnDrainChild struct {
	subagent.Runner
	started  chan struct{}
	cleaning chan struct{}
	release  chan struct{}
	calls    atomic.Int32
}

func (r *spawnDrainChild) Spawn(ctx context.Context, _ subagent.SpawnContext, _ delegation.Request) (delegation.Anchor, delegation.Result, error) {
	r.calls.Add(1)
	close(r.started)
	<-ctx.Done()
	close(r.cleaning)
	// ACP initialization failure joins process cleanup independently of caller
	// cancellation before it can prove that no child started.
	<-r.release
	return delegation.Anchor{}, delegation.Result{}, subagent.MarkSpawnNotStarted(ctx.Err())
}

type spawnDrainController struct {
	recordingControllerBackend
	started chan struct{}
	done    chan struct{}
}

func (b *spawnDrainController) RunTurn(context.Context, controller.TurnRequest) (controller.TurnResult, error) {
	close(b.started)
	return controller.TurnResult{Handle: b}, nil
}

func (b *spawnDrainController) WaitCompletion(ctx context.Context) error {
	// Match the production ACP handle: cancellation may complete this waiter
	// while a separate authenticated Host tool is still cleaning up.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.done:
		return nil
	}
}

func (*spawnDrainController) Cancel() controller.CancelResult {
	return controller.CancelResult{Status: controller.CancelStatusCancelled}
}

func (*spawnDrainController) Close() error { return nil }
