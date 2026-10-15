package gatewayapp

import (
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"testing"
)

func TestIdleShutdownUsesRuntimeOwnerAndIgnoresSubscriptions(t *testing.T) {
	for _, activity := range []string{"command", "producer", "activation", "release"} {
		t.Run(activity, func(t *testing.T) {
			runtime := &sessionRuntime{sessionID: "resident"}
			registry := &sessionRuntimeRegistry{sessions: map[string]*sessionRuntime{"resident": runtime}, observers: map[string]uint64{"resident": 1}}
			stack := &Stack{sessionRuntimes: registry}
			switch activity {
			case "command":
				runtime.inUse = 1
			case "producer":
				runtime.workRefs = 1
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
