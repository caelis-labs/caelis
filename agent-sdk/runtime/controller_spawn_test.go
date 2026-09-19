package runtime

import (
	"context"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
)

func TestControllerSpawnUsesCanonicalTaskPlacementAndLiveFence(t *testing.T) {
	t.Run("active", func(t *testing.T) { testControllerSpawn(t, false) })
	t.Run("restored", func(t *testing.T) { testControllerSpawn(t, true) })
}

func testControllerSpawn(t *testing.T, recoverController bool) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	child := &recordingSubagentRunner{spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "child result"}}
	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "test", PreferredSessionID: "controller-spawn", Workspace: session.WorkspaceRef{Key: "work", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	remote := newTestControllerTurnHandle(nil)
	started := make(chan struct{})
	calls := 0
	backend := stubACPController{runTurn: func(context.Context, controller.TurnRequest) (controller.TurnResult, error) {
		calls++
		if recoverController && calls == 1 {
			return controller.TurnResult{}, controller.ErrNotActive
		}
		close(started)
		return controller.TurnResult{Handle: remote}, nil
	}}
	epoch := "epoch"
	if recoverController {
		epoch = "recovered"
	}
	recovery := controllerRecoveryFunc(func(ctx context.Context, req controller.RecoveryRequest) (session.Session, error) {
		binding := req.Session.Controller
		binding.EpochID = epoch
		return sessions.BindController(ctx, session.BindControllerRequest{SessionRef: req.SessionRef, Binding: binding, MutationGuard: session.RuntimeMutationGuard(ctx)})
	})
	r, err := New(testConfigWithACPForwarder(Config{ControllerRecovery: recovery, Sessions: sessions, AgentFactory: chat.Factory{}, Subagents: child, TaskStore: sessionfile.NewTaskStore(sessions), Controllers: backend}))
	if err != nil {
		t.Fatal(err)
	}
	active, err = r.sessions.BindController(ctx, session.BindControllerRequest{SessionRef: active.SessionRef, Binding: session.ControllerBinding{Kind: session.ControllerKindACP, ControllerID: "external", AgentName: "external", EpochID: "epoch", RemoteSessionID: "remote"}})
	if err != nil {
		t.Fatal(err)
	}
	store := r.sessions.(session.SessionFenceService)
	fence, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "host"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = store.ReleaseSessionFence(context.WithoutCancel(ctx), session.SessionFenceReleaseRequest(fence))
	}()
	run, err := r.Run(session.ContextWithRuntimeFence(ctx, fence), agent.RunRequest{SessionRef: active.SessionRef, Input: "delegate"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	frozen := mustSealPlacement(t, placement.Placement{Kind: placement.KindAgent, ProfileID: "acp:child:model", Agent: "child", Model: "child", ConfigFingerprint: "config", ReasoningEffort: "none", SessionConfigValues: map[string]string{"service_tier": "priority"}})
	base := spawn.NewWithTargets([]delegation.Agent{{Name: "helper"}}, map[string]spawn.Target{"helper": {Selector: "helper", Placement: frozen}})
	call := tool.Call{ID: "external-spawn", Name: spawn.ToolName, Input: []byte(`{"agent":"helper","prompt":"work"}`)}
	stale := "old-epoch"
	if recoverController {
		stale = "epoch"
	}
	if _, err := r.CallControllerSpawn(ctx, active.SessionRef, stale, base, call); err == nil {
		t.Fatal("stale epoch created a child")
	}
	result, err := r.CallControllerSpawn(ctx, active.SessionRef, epoch, base, call)
	if err != nil || result.IsError {
		t.Fatalf("Spawn: %#v %v", result, err)
	}
	entries, err := r.tasks.store.ListSession(ctx, active.SessionRef)
	if err != nil || len(entries) != 1 {
		t.Fatalf("canonical Tasks: %#v %v", entries, err)
	}
	entry := entries[0]
	current, err := r.sessions.Session(ctx, active.SessionRef)
	if err != nil || len(current.Participants) != 1 {
		t.Fatalf("participants: %#v %v", current.Participants, err)
	}
	p := current.Participants[0]
	if p.DelegationID != entry.TaskID || p.SessionID != "child-1" || p.Placement.Fingerprint != frozen.Fingerprint || child.spawnTargetRequest.Target.Placement.Fingerprint != frozen.Fingerprint {
		t.Fatalf("lost ownership: %#v %#v", p, entry)
	}
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Journal != nil && event.Journal.ToolExecution != nil {
			record := event.Journal.ToolExecution
			if record.Key.ToolCallID == call.ID && record.Key.RunID == run.Handle.RunID() && record.Key.TurnID != "" && record.Status == session.ToolExecutionSucceeded {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("external Spawn missed canonical tool execution journal")
	}
	remote.finish()
	if err := run.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CallControllerSpawn(ctx, active.SessionRef, epoch, base, call); err == nil {
		t.Fatal("completed controller Turn retained execution authority")
	}
}
