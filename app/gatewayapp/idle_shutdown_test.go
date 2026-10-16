package gatewayapp

import (
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver"
	"testing"
)

func TestIdleShutdownUsesRuntimeOwnerAndIgnoresSubscriptions(t *testing.T) {
	for _, activity := range []string{"command", "activation", "release"} {
		t.Run(activity, func(t *testing.T) {
			runtime := &sessionRuntime{sessionID: "resident"}
			registry := &sessionRuntimeRegistry{sessions: map[string]*sessionRuntime{"resident": runtime}, observers: map[string]uint64{"resident": 1}}
			stack := &Stack{sessionRuntimes: registry}
			switch activity {
			case "command":
				runtime.inUse = 1
			case "activation":
				registry.building = 1
			case "release":
				runtime.releasing = true
			}
			if err := stack.PrepareIdleShutdown(t.Context()); errorcode.CodeOf(err) != errorcode.Conflict {
				t.Fatalf("busy shutdown: %v", err)
			}
			if registry.closed || stack.composition.isClosing() {
				t.Fatal("busy check sealed admission")
			}
			runtime.inUse = 0
			runtime.workRefs = 0
			registry.building = 0
			runtime.releasing = false
			if err := stack.PrepareIdleShutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !registry.closed || !stack.composition.isClosing() {
				t.Fatal("idle shutdown left admission open")
			}
			if _, err := registry.retainObservation(session.SessionRef{SessionID: "resident"}); errorcode.CodeOf(err) != errorcode.Unavailable {
				t.Fatalf("new observation after drain: %v", err)
			}
			if err := stack.PrepareIdleShutdown(t.Context()); err != nil {
				t.Fatalf("repeat shutdown: %v", err)
			}
		})
	}
}

func TestIdleShutdownIgnoresDetachedTaskAndChildReference(t *testing.T) {
	workspace := newWorkspaceRuntimeTestDir(t, "upgrade-background", "Upgrade background task.")
	stack, err := NewLocalStack(Config{StoreDir: t.TempDir(), WorkspaceKey: "upgrade-background", WorkspaceCWD: workspace, SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	client, err := appserver.BindSessionClient(stack.ControlClient(), appserver.Principal{ID: "local-user"})
	if err != nil {
		t.Fatal(err)
	}
	id := createWorkspaceRuntimeTestSession(t, client, "create-background", "upgrade-background", "upgrade-background", workspace)
	observed, err := client.Reconnect(t.Context(), appserver.ReconnectRequest{SessionID: id})
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Subscription.Close()
	runtime := activateSessionRuntime(t, stack, id)
	release := stack.sessionRuntimes.retainRuntimeWork(runtime, session.SessionRef{SessionID: id})
	defer release()
	if err := stack.composition.authorities.taskStore.Upsert(t.Context(), &taskapi.Entry{TaskID: "detached", Kind: taskapi.KindSubagent, Session: session.SessionRef{SessionID: id}, State: taskapi.StateRunning, Running: true}); err != nil {
		t.Fatal(err)
	}
	if err := stack.PrepareIdleShutdown(t.Context()); err != nil {
		t.Fatalf("background-only work pinned Host: %v", err)
	}
}
