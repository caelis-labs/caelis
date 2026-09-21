package acpagentbridge

import (
	"context"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/acppermission"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type participantSessionClient struct {
	appserver.SessionClient
	mu       sync.Mutex
	state    appserver.SessionState
	feed     *participantApprovalFeed
	resolved chan appserver.ResolveApprovalRequest
}

func (c *participantSessionClient) InspectSession(context.Context, appserver.StateRequest) (appserver.SessionState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state, nil
}
func (c *participantSessionClient) Reconnect(context.Context, appserver.ReconnectRequest) (appserver.ReconnectResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return appserver.ReconnectResult{State: c.state, Subscription: c.feed}, nil
}
func (c *participantSessionClient) ResolveApproval(_ context.Context, req appserver.ResolveApprovalRequest) (appserver.CommandResult, error) {
	c.mu.Lock()
	c.state.Approval.Active = nil
	c.mu.Unlock()
	c.resolved <- req
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted}, nil
}

type participantApprovalFeed struct {
	deliveries chan appserver.FeedDelivery
	once       sync.Once
	closed     chan struct{}
}

func (f *participantApprovalFeed) Deliveries() <-chan appserver.FeedDelivery { return f.deliveries }
func (*participantApprovalFeed) Err() error                                  { return nil }
func (f *participantApprovalFeed) Close() error                              { f.once.Do(func() { close(f.closed) }); return nil }

type participantCallbacks struct {
	acpMuxPromptCallbacks
	permissions chan acpsdk.RequestPermissionRequest
	release     chan struct{}
}

func (c *participantCallbacks) RequestPermission(ctx context.Context, req acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	c.permissions <- req
	select {
	case <-ctx.Done():
		return acpsdk.RequestPermissionResponse{}, ctx.Err()
	case <-c.release:
	}
	return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeSelected("allow")}, nil
}

func TestACPBackgroundParticipantOutputApprovalAndFollowup(t *testing.T) {
	for _, foreground := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "foreground"}[foreground], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			child := controlprompt.AgentRunResult{SessionID: "session-1", TaskID: "task-1"}
			target := appserver.TurnTarget{HandleID: "child-h", RunID: "child-r", TurnID: "child-t"}
			sessions := &participantSessionClient{state: appserver.SessionState{SessionID: child.SessionID, Revision: 12,
				Controller: session.ControllerBinding{EpochID: "epoch"},
				Approval: appserver.ApprovalState{Active: &appserver.ActiveApproval{RequestID: "approval", Scope: eventstream.ScopeSubagent, ScopeID: child.TaskID, Target: target,
					Permission: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "echo-1", Name: "RunCommand"}, Options: []session.ProtocolApprovalOption{{ID: "allow", Name: "Allow once", Kind: "allow_once"}}},
				}},
			}, feed: &participantApprovalFeed{deliveries: make(chan appserver.FeedDelivery, 4), closed: make(chan struct{})}, resolved: make(chan appserver.ResolveApprovalRequest, 2)}
			if foreground {
				sessions.state.Run = appserver.RunState{Active: true, HandleID: "main-h", RunID: "main-r", TurnID: "main-t"}
			}
			sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope, 8)}
			service := &acpMuxTestService{requests: make(chan taskstream.SubscribeRequest, 4), sub: sub}
			client, err := taskstream.BindClient(service, taskstream.Principal{ID: "owner"})
			if err != nil {
				t.Fatal(err)
			}
			bridge := &RuntimeAgent{sessionClient: sessions, taskStreamClient: client}
			callbacks := &participantCallbacks{acpMuxPromptCallbacks: acpMuxPromptCallbacks{updates: make(chan eventstream.SessionNotification, 16)}, permissions: make(chan acpsdk.RequestPermissionRequest, 4), release: make(chan struct{})}
			defer bridge.clearSessionDelivery(child.SessionID)
			promptCtx, endPrompt := context.WithCancel(ctx)
			result := controlprompt.Result{Handled: true, ParticipantTask: &child}
			if err := bridge.emitPromptRouterResult(promptCtx, session.Session{SessionRef: session.SessionRef{SessionID: child.SessionID}}, result, callbacks, true); err != nil {
				t.Fatal(err)
			}
			endPrompt()
			req := receiveACPTaskStreamRequest(t, service.requests)
			if req.TaskID != child.TaskID || !req.Follow {
				t.Fatalf("wrong Task subscription: %#v", req)
			}
			select {
			case req := <-callbacks.permissions:
				if req.ToolCall.ToolCallId != "echo-1" {
					t.Fatal(req)
				}
			case <-ctx.Done():
				t.Fatal("bootstrap approval missing")
			}
			publish := func(turn, text string) {
				env := acpMuxSubagentMessageEnvelope("", turn, turn+"-message", text)
				env.ParentTool = nil
				sub.events <- env
				select {
				case update := <-callbacks.updates:
					chunk, ok := update.Update.(eventstream.ContentChunk)
					content, _ := chunk.Content.(eventstream.TextContent)
					if !ok || content.Text != text {
						t.Fatalf("unexpected output: %#v", update)
					}
				case <-ctx.Done():
					t.Fatal("background output missing")
				}
			}
			publish("pending", "output while approval waits")
			// The foreground feed sees the same head while the background request is
			// awaiting a response. It must neither prompt again nor use the main target.
			wire, err := acppermission.EncodePermissionRequest(session.SessionRef{SessionID: child.SessionID}, sessions.state.Approval.Active.Permission, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := bridge.emitControlEnvelope(ctx, callbacks, child.SessionID, nil, eventstream.Envelope{Kind: eventstream.KindRequestPermission, Scope: eventstream.ScopeSubagent, ScopeID: child.TaskID, ApprovalRequestID: "approval", Permission: &wire}, nil); err != nil {
				t.Fatal(err)
			}
			close(callbacks.release)
			select {
			case resolved := <-sessions.resolved:
				if resolved.Target != target || resolved.OptionID != "allow" || !resolved.Approved {
					t.Fatalf("wrong approval target: %#v", resolved)
				}
			case <-ctx.Done():
				t.Fatal("approval was not resolved")
			}
			publish("first", "first answer")
			terminal := acpMuxSubagentLifecycleEnvelope("", "first", "completed")
			terminal.ParentTool = nil
			sub.events <- terminal
			if err := bridge.emitPromptRouterResult(ctx, session.Session{SessionRef: session.SessionRef{SessionID: child.SessionID}}, result, callbacks, true); err != nil {
				t.Fatal(err)
			}
			publish("second", "followup answer")
			bridge.clearSessionDelivery(child.SessionID)
			if !sub.closed() {
				t.Fatal("Session close retained Task subscription")
			}
			select {
			case <-sessions.feed.closed:
			default:
				t.Fatal("Session close retained approval feed")
			}
			service.mu.Lock()
			count := service.subscribeCalls
			service.mu.Unlock()
			if count != 1 {
				t.Fatalf("follow-up replayed Task history through %d subscriptions", count)
			}
			if len(callbacks.permissions) != 0 || len(sessions.resolved) != 0 {
				t.Fatal("approval was duplicated")
			}
		})
	}
}

func TestACPBackgroundParticipantCloseDismissesPendingApproval(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	sessions := &participantSessionClient{
		state: appserver.SessionState{SessionID: "session-1", Approval: appserver.ApprovalState{Active: &appserver.ActiveApproval{
			RequestID: "pending", Scope: eventstream.ScopeSubagent, ScopeID: "task-1",
			Target:     appserver.TurnTarget{HandleID: "h", RunID: "r", TurnID: "t"},
			Permission: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "echo", Name: "RunCommand"}, Options: []session.ProtocolApprovalOption{{ID: "allow", Name: "Allow", Kind: "allow_once"}}},
		}}},
		feed:     &participantApprovalFeed{deliveries: make(chan appserver.FeedDelivery), closed: make(chan struct{})},
		resolved: make(chan appserver.ResolveApprovalRequest, 1),
	}
	sub := &acpMuxTestSubscription{events: make(chan eventstream.Envelope)}
	service := &acpMuxTestService{requests: make(chan taskstream.SubscribeRequest, 1), sub: sub}
	client, err := taskstream.BindClient(service, taskstream.Principal{ID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	bridge := &RuntimeAgent{sessionClient: sessions, taskStreamClient: client}
	callbacks := &participantCallbacks{acpMuxPromptCallbacks: acpMuxPromptCallbacks{updates: make(chan eventstream.SessionNotification, 4)}, permissions: make(chan acpsdk.RequestPermissionRequest, 1), release: make(chan struct{})}
	defer bridge.clearSessionDelivery("session-1")
	if err := bridge.observeParticipantTask(ctx, controlprompt.AgentRunResult{SessionID: "session-1", TaskID: "task-1"}, callbacks); err != nil {
		t.Fatal(err)
	}
	receiveACPTaskStreamRequest(t, service.requests)
	select {
	case <-callbacks.permissions:
	case <-ctx.Done():
		t.Fatal("approval not requested")
	}
	done := make(chan struct{})
	go func() { bridge.clearSessionDelivery("session-1"); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("Session close blocked on pending permission")
	}
	if !sub.closed() || len(sessions.resolved) != 0 {
		t.Fatal("detach leaked subscription or answered permission")
	}
}
