package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	sdkcontroller "github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/acptest/draftpeer"
)

type draftQuarantineApproval func(context.Context, sdkcontroller.ApprovalRequest) (sdkcontroller.ApprovalResponse, error)

func (f draftQuarantineApproval) RequestControllerApproval(ctx context.Context, req sdkcontroller.ApprovalRequest) (sdkcontroller.ApprovalResponse, error) {
	return f(ctx, req)
}

func TestDraftControllerSteeringReceiptWithoutEchoDoesNotCommitOrReleaseOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	peer := &draftpeer.Peer{SessionID: "controller-remote"}
	ready := make(chan struct{})
	peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
		if peer.Prompts.Load() == 1 {
			if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
				return v2.PromptResponse{}, err
			}
			if err := peer.Echo(ctx, req.SessionId, message, "initial input"); err != nil {
				return v2.PromptResponse{}, err
			}
			close(ready)
		} else {
			if err := peer.Update(ctx, req.SessionId, map[string]any{"sessionUpdate": "agent_message", "messageId": "unproven-output", "content": []any{map[string]any{"type": "text", "text": "ambiguous remote output"}}}); err != nil {
				return v2.PromptResponse{}, err
			}
		}
		return v2.PromptResponse{MessageId: v2.MessageId(message), Meta: acpdraft.ActivityMeta(activity)}, nil
	}
	registry, err := subagent.NewRegistry([]subagent.AgentConfig{{Name: "helper", HostedAdapterID: "fixture", ACPDraftSchema: acpdraft.SchemaTag}})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Config{Registry: registry, EndpointResolver: peer})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Quiesce(context.Background()) })
	parent := session.Session{SessionRef: session.SessionRef{SessionID: "parent"}, CWD: t.TempDir()}
	binding, err := manager.Activate(ctx, sdkcontroller.HandoffRequest{Session: parent, Agent: "helper"})
	if err != nil {
		t.Fatal(err)
	}
	sources := make(chan agent.SourceEvent, 64)
	turn, err := manager.RunTurn(ctx, sdkcontroller.TurnRequest{Session: parent, SessionRef: parent.SessionRef, TurnID: "original-turn", Input: "initial input", Stream: true, Observer: agent.SourceEventObserverFunc(func(_ context.Context, event agent.SourceEvent) error {
		sources <- agent.CloneSourceEvent(event)
		return nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	manager.mu.RLock()
	run := manager.controllers[parent.SessionID]
	manager.mu.RUnlock()
	// A real SDK round trip fences processing of prior state and receipt bytes.
	if _, err := run.client.ListSessions(ctx, acp.ListSessionsRequest{}); err != nil {
		t.Fatal(err)
	}
	steerCtx, cancelSteer := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancelSteer()
	var commits atomic.Int32
	err = manager.SteerController(steerCtx, sdkcontroller.ControllerSteerRequest{SessionRef: parent.SessionRef, ControllerID: binding.ControllerID, ControllerEpoch: binding.EpochID, RemoteSessionID: binding.RemoteSessionID, TurnID: "original-turn", Input: "ambiguous input", Commit: func() error { commits.Add(1); return nil }})
	if !errorcode.Is(err, errorcode.UnknownOutcome) {
		t.Fatalf("steering receipt missing echo = %v, want unknown", err)
	}
	if commits.Load() != 0 {
		t.Fatal("unproven steering committed canonical input")
	}
	if err := turn.Handle.WaitCompletion(ctx); !errorcode.Is(err, errorcode.UnknownOutcome) {
		t.Fatalf("original turn falsely completed: %v", err)
	}
	manager.mu.RLock()
	retained := manager.controllers[parent.SessionID]
	manager.mu.RUnlock()
	if retained != nil {
		t.Fatal("ambiguous steering endpoint was not isolated")
	}
	for len(sources) != 0 {
		source := <-sources
		data, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "ambiguous remote output") {
			t.Fatalf("unproven output crossed canonical barrier: %s", data)
		}
	}
	if peer.Prompts.Load() != 2 || peer.Opens.Load() != 1 {
		t.Fatalf("steering retried: prompts=%d opens=%d", peer.Prompts.Load(), peer.Opens.Load())
	}
}

// The real Manager startup, reverse approval, prompt submission and completion
// paths consume an actual SDK peer. A receipt is never evidence of completion.
func TestDraftControllerAcceptedReceiptFailurePreservesUnknownAndApprovalOwner(t *testing.T) {
	for _, failure := range []string{"missing-echo", "missing-activity", "idle-wait-timeout"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			approvals := make(chan sdkcontroller.ApprovalRequest, 1)
			peer := &draftpeer.Peer{SessionID: "controller-remote"}
			peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
				response, err := peer.Permission(ctx, req.SessionId)
				if err != nil {
					return v2.PromptResponse{}, err
				}
				if response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "native-allow" {
					return v2.PromptResponse{}, fmt.Errorf("native option was replaced: %+v", response)
				}
				if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
					return v2.PromptResponse{}, err
				}
				if failure != "missing-echo" {
					if err := peer.Echo(ctx, req.SessionId, message, "accepted input"); err != nil {
						return v2.PromptResponse{}, err
					}
				}
				responseMeta := acpdraft.ActivityMeta(activity)
				if failure == "missing-activity" {
					responseMeta = nil
				}
				return v2.PromptResponse{MessageId: v2.MessageId(message), Meta: responseMeta}, nil
			}
			registry, err := subagent.NewRegistry([]subagent.AgentConfig{{Name: "helper", HostedAdapterID: "fixture", ACPDraftSchema: acpdraft.SchemaTag}})
			if err != nil {
				t.Fatal(err)
			}
			manager, err := NewManager(Config{Registry: registry, EndpointResolver: peer})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Quiesce(context.Background()) })
			parent := session.Session{SessionRef: session.SessionRef{AppName: "caelis", UserID: "owner", SessionID: "parent", WorkspaceKey: "isolated"}, CWD: t.TempDir()}
			binding, err := manager.Activate(ctx, sdkcontroller.HandoffRequest{Session: parent, Agent: "helper", Source: "draft-quarantine-test"})
			if err != nil {
				t.Fatal(err)
			}
			requester := draftQuarantineApproval(func(_ context.Context, req sdkcontroller.ApprovalRequest) (sdkcontroller.ApprovalResponse, error) {
				approvals <- req
				return sdkcontroller.ApprovalResponse{Outcome: "selected", OptionID: "native-allow", Approved: true}, nil
			})
			turnCtx, cancelTurn := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancelTurn()
			turn, err := manager.RunTurn(turnCtx, sdkcontroller.TurnRequest{Session: parent, SessionRef: parent.SessionRef, TurnID: "original-turn", Input: "original input", ApprovalRequester: requester})
			if err != nil {
				t.Fatal(err)
			}
			if err := turn.Handle.WaitCompletion(ctx); !errorcode.Is(err, errorcode.UnknownOutcome) {
				t.Fatalf("accepted receipt failure = %v, want unknown outcome", err)
			}
			var approval sdkcontroller.ApprovalRequest
			select {
			case approval = <-approvals:
			case <-ctx.Done():
				t.Fatal("original approval owner was lost")
			}
			if approval.EndpointSessionID != peer.SessionID || approval.SessionRef != parent.SessionRef || approval.Approval == nil || approval.Approval.Context == nil || len(approval.Approval.Context.NativeRequestID) == 0 || approval.Approval.Subject == nil || approval.Approval.Subject.Kind != "command" || len(approval.Approval.Subject.Targets) != 2 || approval.Approval.Subject.Targets[0].Text != "git status" || len(approval.Approval.Options) != 2 || approval.Approval.Options[0].ID != "native-allow" {
				t.Fatalf("approval identity/options/subject lost: %+v", approval)
			}
			manager.mu.RLock()
			run := manager.controllers[parent.SessionID]
			manager.mu.RUnlock()
			if run == nil || run.binding.ControllerID != binding.ControllerID || run.remoteSessionID != peer.SessionID {
				t.Fatalf("unknown outcome lost original controller binding: %+v", run)
			}
			if peer.Prompts.Load() != 1 || peer.Opens.Load() != 1 {
				t.Fatalf("unknown receipt retried: prompts=%d opens=%d", peer.Prompts.Load(), peer.Opens.Load())
			}
		})
	}
}
