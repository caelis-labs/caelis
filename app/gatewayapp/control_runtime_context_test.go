package gatewayapp

import (
	"context"
	"testing"

	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
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

	runtimeCtx := stack.controlRuntimeContext(fallback)
	if agentmessage.SenderFromContext(runtimeCtx) == nil {
		t.Fatal("Control runtime context dropped negotiated Agent message transport")
	}
	if got := runtimeCtx.Value(requestValueKey{}); got != nil {
		t.Fatalf("Control runtime context retained arbitrary request value %#v", got)
	}
}
