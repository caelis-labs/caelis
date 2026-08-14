package gatewayapp

import (
	"context"
	"testing"

	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

type controlRuntimeContextMessageSender struct{}

func (controlRuntimeContextMessageSender) SendMessage(context.Context, agentmessage.Request) (agentmessage.Response, error) {
	return agentmessage.Response{Accepted: true}, nil
}

func TestControlRuntimeContextPreservesOnlyAgentMessageTransport(t *testing.T) {
	t.Parallel()

	type requestValueKey struct{}
	sender := controlRuntimeContextMessageSender{}
	fallback := context.WithValue(context.Background(), requestValueKey{}, "request-only")
	fallback = agentmessage.WithSender(fallback, sender)
	stack := &Stack{lifecycleCtx: context.Background()}

	runtimeCtx := stack.controlRuntimeContext(fallback, session.Session{})
	if agentmessage.SenderFromContext(runtimeCtx) == nil {
		t.Fatal("Control runtime context dropped negotiated Agent message transport")
	}
	if got := runtimeCtx.Value(requestValueKey{}); got != nil {
		t.Fatalf("Control runtime context retained arbitrary request value %#v", got)
	}
}

func TestControlRuntimeContextBindsMailboxForSpawnedChild(t *testing.T) {
	t.Parallel()

	stack := &Stack{
		lifecycleCtx: context.Background(),
		hostedChildMailbox: func(context.Context, session.SessionRef, agentmessage.Request) (agentmessage.Response, error) {
			return agentmessage.Response{Accepted: true}, nil
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

	runtimeCtx := stack.controlRuntimeContext(context.Background(), child)
	if agentmessage.SenderFromContext(runtimeCtx) == nil {
		t.Fatal("spawned child runtime context has no parent/sibling mailbox")
	}

	mainCtx := stack.controlRuntimeContext(context.Background(), session.Session{
		SessionRef: session.SessionRef{SessionID: "main-1"},
	})
	if agentmessage.SenderFromContext(mainCtx) != nil {
		t.Fatal("main Agent runtime context gained a parent mailbox")
	}
}
