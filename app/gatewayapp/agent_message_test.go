package gatewayapp

import (
	"context"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestDeliverAgentMessageAttachesIdleTurnAndPreservesRuntimeContext(t *testing.T) {
	t.Parallel()

	sessions := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "owner", PreferredSessionID: "agent-message-idle",
	})
	if err != nil {
		t.Fatal(err)
	}
	active, err = sessions.BindController(context.Background(), session.BindControllerRequest{
		SessionRef: active.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindKernel, ControllerID: "main-controller", AgentName: "main", EpochID: "epoch-1",
		},
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
	feeds, err := appserver.NewFeedRegistry(appserver.FeedRegistryConfig{Reader: sessions, CursorCodec: codec})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &agentMessageActivationRuntime{
		started: make(chan context.Context, 1),
		release: make(chan struct{}),
	}
	gateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: sessions, Runtime: runtime, Resolver: headlessSessionTestResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycleCtx, stopLifecycle := context.WithCancel(context.Background())
	defer stopLifecycle()
	stack := &Stack{
		composition: runtimeComposition{
			authorities: runtimeHostAuthorities{controlFeeds: feeds, lifecycleCtx: lifecycleCtx},
			sessions:    sessions,
			gateway:     gateway,
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

	admissionCtx, cancelAdmission := context.WithCancel(context.Background())
	admissionCtx = agentmessage.WithSender(admissionCtx, controlRuntimeContextMessageSender{})
	expectedRevision := active.Revision
	delivery, err := stack.AgentMessageDelivery().Deliver(admissionCtx, DeliverAgentMessageRequest{
		SessionRef: active.SessionRef, ExpectedRevision: &expectedRevision,
		Message: agentmessage.Request{
			MessageID: "message-1", Text: "continue",
			From: session.ActorRef{Kind: session.ActorKindController, ID: "parent-controller", Name: "main"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !delivery.Accepted || delivery.Turn == nil {
		t.Fatalf("delivery = %#v, want accepted idle Turn", delivery)
	}
	defer delivery.Turn.Close()

	var runtimeCtx context.Context
	select {
	case runtimeCtx = <-runtime.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent-message Turn did not reach Runtime")
	}
	cancelAdmission()
	if agentmessage.SenderFromContext(runtimeCtx) == nil {
		t.Fatal("Agent-message Turn dropped negotiated outbound message sender")
	}
	select {
	case <-runtimeCtx.Done():
		t.Fatalf("Agent-message Turn inherited admission cancellation: %v", runtimeCtx.Err())
	default:
	}
	close(runtime.release)

	select {
	case envelope := <-subscription.Events():
		if envelope.SessionID != active.SessionID {
			t.Fatalf("attached envelope Session = %q, want %q", envelope.SessionID, active.SessionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Agent-message Turn output was not attached to the authoritative Session feed")
	}
}

type agentMessageActivationRuntime struct {
	started chan context.Context
	release chan struct{}
}

func (runtime *agentMessageActivationRuntime) Run(ctx context.Context, request agent.RunRequest) (agent.RunResult, error) {
	runtime.started <- ctx
	select {
	case <-runtime.release:
	case <-ctx.Done():
		return agent.RunResult{}, ctx.Err()
	}
	message := model.NewTextMessage(model.RoleAssistant, "continued")
	return agent.RunResult{
		Session: session.Session{SessionRef: request.SessionRef},
		Handle: &headlessSessionTestRunner{
			ref: request.SessionRef,
			events: []*session.Event{{
				ID: "agent-message-output", SessionID: request.SessionRef.SessionID,
				Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical,
				Message: &message, Text: message.TextContent(),
			}},
		},
	}, nil
}

func (*agentMessageActivationRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}
