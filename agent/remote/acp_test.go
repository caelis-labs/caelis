package remote

import (
	"context"
	"encoding/json"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/acp"
	"github.com/OnslaughtSnail/caelis/acp/client"
	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/trace"
)

func TestACPAgentNormalizesExternalUpdatesIntoInvocationSessionEvents(t *testing.T) {
	finalFalse := false
	finalTrue := true
	factory := &fakeACPClientFactory{}
	factory.client.promptFn = func(_ context.Context, sessionID string, text string) (acp.PromptResponse, error) {
		factory.callbacks.OnUpdate(client.UpdateEnvelope{
			SessionID: "remote-session",
			Update: acp.ContentChunk{
				SessionUpdate: acp.UpdateAgentMessage,
				Content:       json.RawMessage(`{"type":"text","text":"partial"}`),
				Final:         &finalFalse,
			},
		})
		factory.callbacks.OnUpdate(client.UpdateEnvelope{
			SessionID: "remote-session",
			Update: acp.ContentChunk{
				SessionUpdate: acp.UpdateAgentMessage,
				Content:       json.RawMessage(`{"type":"text","text":"done"}`),
				Final:         &finalTrue,
			},
		})
		return acp.PromptResponse{StopReason: "end_turn"}, nil
	}

	remoteAgent := NewACP(Config{
		Name:          "remote-reviewer",
		Description:   "remote ACP reviewer",
		ClientFactory: factory,
	})
	events := collectAgentEvents(t, remoteAgent.Run(newRemoteInvocation("local-session")))

	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].SessionRef.SessionID != "local-session" || events[1].SessionRef.SessionID != "local-session" {
		t.Fatalf("session refs = %#v %#v, want local invocation session", events[0].SessionRef, events[1].SessionRef)
	}
	if events[0].Visibility != session.VisibilityUIOnly {
		t.Fatalf("first visibility = %q, want ui_only", events[0].Visibility)
	}
	if events[1].Visibility != session.VisibilityCanonical {
		t.Fatalf("second visibility = %q, want canonical", events[1].Visibility)
	}
	if events[1].AssistantPayload == nil || events[1].AssistantPayload.Parts[0].Text != "done" {
		t.Fatalf("final assistant payload = %#v", events[1].AssistantPayload)
	}
	if events[1].ProviderMeta["acp_session_id"] != "remote-session" {
		t.Fatalf("provider meta = %#v, want remote ACP session id", events[1].ProviderMeta)
	}
	if factory.client.promptSessionID != "remote-session" || factory.client.promptText != "review this" {
		t.Fatalf("prompt = session %q text %q", factory.client.promptSessionID, factory.client.promptText)
	}
}

func TestACPAgentBridgesPermissionRequestsToApprovalRequester(t *testing.T) {
	factory := &fakeACPClientFactory{}
	factory.client.promptFn = func(ctx context.Context, _ string, _ string) (acp.PromptResponse, error) {
		resp, err := factory.callbacks.OnPermissionRequest(ctx, acp.RequestPermissionRequest{
			SessionID: "remote-session",
			ToolCall: acp.ToolCallUpdate{
				SessionUpdate: acp.UpdateToolCall,
				ToolCallID:    "call-1",
				Title:         "RUN_COMMAND",
				RawInput:      map[string]any{"cmd": "make test"},
			},
			Options: []acp.PermissionOption{
				{OptionID: "allow-once", Kind: "allow_once"},
				{OptionID: "reject-once", Kind: "reject_once"},
			},
		})
		if err != nil {
			return acp.PromptResponse{}, err
		}
		factory.permissionResponse = resp
		return acp.PromptResponse{StopReason: "end_turn"}, nil
	}
	approver := &fakeApprovalRequester{response: agent.ApprovalResponse{Approved: true, Reason: "allowed"}}

	remoteAgent := NewACP(Config{
		Name:              "remote-reviewer",
		ClientFactory:     factory,
		ApprovalRequester: approver,
	})
	_ = collectAgentEvents(t, remoteAgent.Run(newRemoteInvocation("local-session")))

	if approver.request.CallID != "call-1" || approver.request.ToolName != "RUN_COMMAND" {
		t.Fatalf("approval request = %#v", approver.request)
	}
	if factory.permissionResponse.Outcome.OptionID != "allow-once" {
		t.Fatalf("permission response = %#v, want allow-once", factory.permissionResponse)
	}
}

func TestACPAgentSendsRemoteCancelWhenInvocationContextCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inv := newRemoteInvocation("local-session")
	inv.Context = ctx
	factory := &fakeACPClientFactory{}
	promptStarted := make(chan struct{})
	factory.client.promptFn = func(ctx context.Context, _ string, _ string) (acp.PromptResponse, error) {
		close(promptStarted)
		<-ctx.Done()
		return acp.PromptResponse{}, ctx.Err()
	}

	remoteAgent := NewACP(Config{
		Name:          "remote-reviewer",
		ClientFactory: factory,
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range remoteAgent.Run(inv) {
		}
	}()

	select {
	case <-promptStarted:
	case <-time.After(time.Second):
		t.Fatal("remote prompt did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after invocation cancellation")
	}
	if got := factory.client.cancelSessionID(); got != "remote-session" {
		t.Fatalf("cancel session id = %q, want remote-session", got)
	}
}

func TestACPAgentContinuationLoadsExistingRemoteSession(t *testing.T) {
	finalTrue := true
	factory := &fakeACPClientFactory{}
	factory.client.promptFn = func(_ context.Context, sessionID string, prompt string) (acp.PromptResponse, error) {
		factory.callbacks.OnUpdate(client.UpdateEnvelope{
			SessionID: sessionID,
			Update: acp.ContentChunk{
				SessionUpdate: acp.UpdateAgentMessage,
				Content:       json.RawMessage(`{"type":"text","text":"` + prompt + ` done"}`),
				Final:         &finalTrue,
			},
		})
		return acp.PromptResponse{StopReason: "end_turn"}, nil
	}
	remoteAgent := NewACP(Config{
		Name:          "remote-reviewer",
		ClientFactory: factory,
	})

	first := collectAgentEvents(t, remoteAgent.Run(newRemoteInvocationWithText("local-session", "first")))
	second := collectAgentEvents(t, remoteAgent.Run(newRemoteInvocationWithText("local-session", "second")))

	if len(first) != 1 || first[0].TextContent() != "first done" {
		t.Fatalf("first events = %#v", first)
	}
	if len(second) != 1 || second[0].TextContent() != "second done" {
		t.Fatalf("second events = %#v", second)
	}
	if factory.client.newSessionCalls != 1 {
		t.Fatalf("NewSession calls = %d, want 1", factory.client.newSessionCalls)
	}
	if len(factory.client.loadedSessions) != 1 || factory.client.loadedSessions[0] != "remote-session" {
		t.Fatalf("loaded sessions = %#v, want remote-session", factory.client.loadedSessions)
	}
	if got := second[0].ProviderMeta["acp_session_id"]; got != "remote-session" {
		t.Fatalf("second remote session meta = %#v", got)
	}
}

type fakeACPClientFactory struct {
	callbacks client.ACPClientCallbacks
	client    fakeACPClient

	permissionResponse acp.RequestPermissionResponse
}

func (f *fakeACPClientFactory) Start(_ context.Context, callbacks client.ACPClientCallbacks) (client.ACPClient, error) {
	f.callbacks = callbacks
	return &f.client, nil
}

type fakeACPClient struct {
	mu              sync.Mutex
	promptFn        func(context.Context, string, string) (acp.PromptResponse, error)
	promptSessionID string
	promptText      string
	cancelled       string
	newSessionCalls int
	loadedSessions  []string
}

func (c *fakeACPClient) Initialize(context.Context) (acp.InitializeResponse, error) {
	return acp.InitializeResponse{}, nil
}

func (c *fakeACPClient) NewSession(_ context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.newSessionCalls++
	return acp.NewSessionResponse{SessionID: "remote-session"}, nil
}

func (c *fakeACPClient) LoadSession(_ context.Context, req acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadedSessions = append(c.loadedSessions, req.SessionID)
	return acp.LoadSessionResponse{}, nil
}

func (c *fakeACPClient) PromptText(ctx context.Context, sessionID string, text string) (acp.PromptResponse, error) {
	c.mu.Lock()
	c.promptSessionID = sessionID
	c.promptText = text
	c.mu.Unlock()
	if c.promptFn != nil {
		return c.promptFn(ctx, sessionID, text)
	}
	return acp.PromptResponse{StopReason: "end_turn"}, nil
}

func (c *fakeACPClient) Cancel(_ context.Context, sessionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelled = sessionID
	return nil
}

func (c *fakeACPClient) cancelSessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancelled
}

func (c *fakeACPClient) Close() error { return nil }

type fakeApprovalRequester struct {
	request  agent.ApprovalRequest
	response agent.ApprovalResponse
}

func (a *fakeApprovalRequester) RequestApproval(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	a.request = req
	return a.response, nil
}

func collectAgentEvents(t *testing.T, seq iter.Seq2[session.Event, error]) []session.Event {
	t.Helper()
	var events []session.Event
	for event, err := range seq {
		if err != nil {
			t.Fatalf("Run error = %v", err)
		}
		events = append(events, event)
	}
	return events
}

type remoteInvocation struct {
	context.Context
	sess session.Session
	text string
}

func newRemoteInvocation(sessionID string) *remoteInvocation {
	return newRemoteInvocationWithText(sessionID, "review this")
}

func newRemoteInvocationWithText(sessionID string, text string) *remoteInvocation {
	return &remoteInvocation{
		Context: context.Background(),
		text:    text,
		sess: session.Session{
			Ref: session.Ref{
				AppName:      "test",
				UserID:       "user",
				WorkspaceKey: "workspace",
				SessionID:    sessionID,
			},
			Workspace: session.Workspace{Root: "/tmp/workspace"},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}
}

func (i *remoteInvocation) Agent() agent.Agent { return nil }

func (i *remoteInvocation) Session() session.Session { return i.sess }

func (i *remoteInvocation) InvocationID() string { return "inv-1" }

func (i *remoteInvocation) Branch() string { return "main" }

func (i *remoteInvocation) UserMessage() model.Message {
	return model.Message{Role: model.RoleUser, Content: []model.Part{{Text: i.text}}}
}

func (i *remoteInvocation) PriorMessages() []model.Message { return nil }

func (i *remoteInvocation) RunConfig() *agent.RunConfig { return agent.DefaultRunConfig() }

func (i *remoteInvocation) Hooks() []agent.Hook { return nil }

func (i *remoteInvocation) Tracer() trace.Tracer { return nil }

func (i *remoteInvocation) EndInvocation() {}

func (i *remoteInvocation) Ended() bool { return false }
