package acpagentbridge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type backgroundPermissionCall struct {
	ctx     context.Context
	request acpsdk.RequestPermissionRequest
	answer  chan struct{}
}

type backgroundPermissionCallbacks struct {
	acpMuxPromptCallbacks
	calls       chan backgroundPermissionCall
	delayedCall acpsdk.ToolCallId
}

func (c *backgroundPermissionCallbacks) RequestPermission(ctx context.Context, req acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	call := backgroundPermissionCall{ctx: ctx, request: req, answer: make(chan struct{}, 1)}
	c.calls <- call
	if req.ToolCall.ToolCallId == c.delayedCall {
		// A response already in transport may arrive after request cancellation.
		<-call.answer
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeSelected("allow")}, nil
	}
	select {
	case <-ctx.Done():
		return acpsdk.RequestPermissionResponse{}, ctx.Err()
	case <-call.answer:
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeSelected("allow")}, nil
	}
}

func backgroundApprovalFixture(taskID, requestID string) *appserver.ActiveApproval {
	return &appserver.ActiveApproval{
		RequestID: eventstream.ApprovalRequestID(requestID), Scope: eventstream.ScopeSubagent, ScopeID: taskID,
		Target: appserver.TurnTarget{HandleID: taskID + "-handle", RunID: taskID + "-run", TurnID: taskID + "-turn"},
		Permission: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: taskID + "-call", Name: "Write"},
			Options: []session.ProtocolApprovalOption{{ID: "allow", Name: "Allow once", Kind: "allow_once"}}},
	}
}

func startBackgroundApprovalFixture(t *testing.T, lateReply bool) (*RuntimeAgent, *participantSessionClient, *backgroundPermissionCallbacks) {
	t.Helper()
	sessions := &participantSessionClient{
		state:    appserver.SessionState{SessionID: "session-1", Approval: appserver.ApprovalState{Active: backgroundApprovalFixture("task-1", "approval-1")}},
		feed:     &participantApprovalFeed{deliveries: make(chan appserver.FeedDelivery), closed: make(chan struct{})},
		resolved: make(chan appserver.ResolveApprovalRequest, 4),
	}
	streams := &multiTaskStreamService{requests: make(chan taskstream.SubscribeRequest, 4)}
	client, err := taskstream.BindClient(streams, taskstream.Principal{ID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	bridge := &RuntimeAgent{sessionClient: sessions, taskStreamClient: client}
	callbacks := &backgroundPermissionCallbacks{
		acpMuxPromptCallbacks: acpMuxPromptCallbacks{updates: make(chan eventstream.SessionNotification, 16)},
		calls:                 make(chan backgroundPermissionCall, 8),
	}
	if lateReply {
		callbacks.delayedCall = "task-1-call"
	}
	t.Cleanup(func() { bridge.clearSessionDelivery("session-1") })
	for _, taskID := range []string{"task-1", "task-2"} {
		if err := bridge.observeParticipantTask(t.Context(), controlprompt.AgentRunResult{SessionID: "session-1", TaskID: taskID}, callbacks); err != nil {
			t.Fatal(err)
		}
		receiveACPTaskStreamRequest(t, streams.requests)
	}
	return bridge, sessions, callbacks
}

func receiveBackgroundPermission(t *testing.T, ctx context.Context, callbacks *backgroundPermissionCallbacks, callID string) backgroundPermissionCall {
	t.Helper()
	select {
	case call := <-callbacks.calls:
		t.Cleanup(func() {
			select {
			case call.answer <- struct{}{}:
			default:
			}
		})
		if call.request.ToolCall.ToolCallId != acpsdk.ToolCallId(callID) {
			t.Fatalf("permission call = %s, want %s", call.request.ToolCall.ToolCallId, callID)
		}
		return call
	case <-ctx.Done():
		t.Fatal("next background approval was blocked")
		return backgroundPermissionCall{}
	}
}

func publishBackgroundApprovalChange(t *testing.T, ctx context.Context, sessions *participantSessionClient, active *appserver.ActiveApproval) {
	t.Helper()
	sessions.mu.Lock()
	sessions.state.Approval.Active = active
	sessions.mu.Unlock()
	envelope := acpMuxExactEnvelope(eventstream.Envelope{
		Kind: eventstream.KindLifecycle, SessionID: "session-1", Scope: eventstream.ScopeSubagent,
		ScopeID: "task-1", ApprovalRequestID: "approval-1", Lifecycle: &eventstream.Lifecycle{State: "resolved"},
	}, 1)
	select {
	case sessions.feed.deliveries <- appserver.FeedDelivery{Kind: appserver.FeedDeliveryAppendPage, Source: appserver.FeedSourceExact, Events: []eventstream.Envelope{envelope}, NextCursor: envelope.Cursor}:
	case <-ctx.Done():
		t.Fatal("approval feed was not consumed")
	}
}

func TestACPBackgroundApprovalHeadChangeCancelsPendingPermission(t *testing.T) {
	for _, nextID := range []string{"approval-2", "approval-1"} {
		t.Run(nextID, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			bridge, sessions, callbacks := startBackgroundApprovalFixture(t, true)
			first := receiveBackgroundPermission(t, ctx, callbacks, "task-1-call")
			publishBackgroundApprovalChange(t, ctx, sessions, backgroundApprovalFixture("task-2", nextID))
			select {
			case <-first.ctx.Done():
			case <-ctx.Done():
				t.Fatal("settled or replaced approval retained its permission request")
			}
			receiveBackgroundPermission(t, ctx, callbacks, "task-2-call")
			first.answer <- struct{}{}
			bridge.clearSessionDelivery("session-1")
			if len(sessions.resolved) != 0 {
				t.Fatal("late reply submitted a decision after head change")
			}
			if len(callbacks.updates) != 0 {
				t.Fatal("superseded approval emitted a failure notice")
			}
		})
	}
}

func TestACPBackgroundApprovalClearedHeadCancelsPendingPermission(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, sessions, callbacks := startBackgroundApprovalFixture(t, false)
	first := receiveBackgroundPermission(t, ctx, callbacks, "task-1-call")
	publishBackgroundApprovalChange(t, ctx, sessions, nil)
	select {
	case <-first.ctx.Done():
	case <-ctx.Done():
		t.Fatal("cleared approval retained its permission request")
	}
}

func TestACPBackgroundApprovalFailureDoesNotResubmitOnForegroundReplay(t *testing.T) {
	for _, outcome := range []appserver.Outcome{appserver.OutcomeRejected, appserver.OutcomeUnknown} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			bridge, sessions, callbacks := startBackgroundApprovalFixture(t, false)
			first := receiveBackgroundPermission(t, ctx, callbacks, "task-1-call")
			var attempts atomic.Int32
			sessions.mu.Lock()
			sessions.resolveApprovalFn = func(context.Context, appserver.ResolveApprovalRequest) (appserver.CommandResult, error) {
				attempts.Add(1)
				return appserver.CommandResult{Outcome: outcome}, errors.New("submission failed")
			}
			sessions.mu.Unlock()
			first.answer <- struct{}{}
			awaitReceiptNotice(t, ctx, callbacks.updates, "Background participant approval failed:")
			if err := bridge.emitControlEnvelope(ctx, callbacks, "session-1", nil, eventstream.Envelope{
				Kind: eventstream.KindRequestPermission, Scope: eventstream.ScopeSubagent, ScopeID: "task-1", ApprovalRequestID: "approval-1",
			}, nil); err != nil {
				t.Fatal(err)
			}
			publishBackgroundApprovalChange(t, ctx, sessions, backgroundApprovalFixture("task-2", "approval-2"))
			receiveBackgroundPermission(t, ctx, callbacks, "task-2-call")
			if attempts.Load() != 1 {
				t.Fatalf("failed or uncertain approval was resubmitted %d times", attempts.Load())
			}
			if len(callbacks.updates) != 0 {
				t.Fatal("failed approval emitted repeated notices")
			}
		})
	}
}
