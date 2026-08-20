package gatewayapp

import (
	"context"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

type controlRuntimeContextInputSender struct{}

func (controlRuntimeContextInputSender) SendAgentInput(context.Context, agent.AgentInput) error {
	return nil
}

func TestControlRuntimeContextPreservesOnlyAgentInputTransport(t *testing.T) {
	t.Parallel()

	type requestValueKey struct{}
	sender := controlRuntimeContextInputSender{}
	fallback := context.WithValue(context.Background(), requestValueKey{}, "request-only")
	fallback = agent.WithAgentInputSender(fallback, sender)
	stack := &Stack{composition: runtimeComposition{authorities: runtimeHostAuthorities{lifecycleCtx: context.Background()}}}

	runtimeCtx := stack.composition.controlRuntimeContext(fallback, session.Session{})
	if agent.AgentInputSenderFromContext(runtimeCtx) == nil {
		t.Fatal("Control runtime context dropped Agent input transport")
	}
	if got := runtimeCtx.Value(requestValueKey{}); got != nil {
		t.Fatalf("Control runtime context retained arbitrary request value %#v", got)
	}
}

func TestControlRuntimeContextBindsInputSenderForSpawnedChild(t *testing.T) {
	t.Parallel()

	stack := &Stack{
		composition: runtimeComposition{
			authorities: runtimeHostAuthorities{
				lifecycleCtx: context.Background(),
				hostedChildInput: func(context.Context, session.SessionRef, session.SessionRef, string, agent.AgentInput) error {
					return nil
				},
			},
		},
	}
	child := session.Session{
		SessionRef: session.SessionRef{SessionID: "child-1"},
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent:  sessionvisibility.SystemManagedAgentSubagent,
			sessionvisibility.MetadataSystemManagedParent: "parent-1",
			sessionvisibility.MetadataSystemManagedTask:   "task-1",
		},
	}

	runtimeCtx := stack.composition.controlRuntimeContext(context.Background(), child)
	if agent.AgentInputSenderFromContext(runtimeCtx) == nil {
		t.Fatal("spawned child runtime context has no parent/sibling input route")
	}

	mainCtx := stack.composition.controlRuntimeContext(context.Background(), session.Session{
		SessionRef: session.SessionRef{SessionID: "main-1"},
	})
	if agent.AgentInputSenderFromContext(mainCtx) != nil {
		t.Fatal("main Agent runtime context gained a parent input route")
	}
}
