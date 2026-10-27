package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpdraft"
	"github.com/caelis-labs/caelis/internal/acptest/draftpeer"
)

func TestDraftChildAcceptedReceiptFailureQuarantinesOriginalTaskAndApproval(t *testing.T) {
	for _, failure := range []string{"missing-echo", "missing-activity", "idle-wait-timeout"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			peer := &draftpeer.Peer{SessionID: "child-remote"}
			approvals := make(chan tasksubagent.ApprovalRequest, 1)
			peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
				second := peer.Prompts.Load() == 2
				if second {
					permission, err := peer.Permission(ctx, req.SessionId)
					if err != nil {
						return v2.PromptResponse{}, err
					}
					if permission.Outcome.Selected == nil || permission.Outcome.Selected.OptionId != "native-allow" {
						return v2.PromptResponse{}, fmt.Errorf("original native resolve lost: %+v", permission)
					}
				}
				if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
					return v2.PromptResponse{}, err
				}
				if !second || failure != "missing-echo" {
					if err := peer.Echo(ctx, req.SessionId, message, "accepted input"); err != nil {
						return v2.PromptResponse{}, err
					}
				}
				if !second {
					if err := peer.State(ctx, req.SessionId, activity, "idle", "end_turn"); err != nil {
						return v2.PromptResponse{}, err
					}
				}
				meta := acpdraft.ActivityMeta(activity)
				if second && failure == "missing-activity" {
					meta = nil
				}
				return v2.PromptResponse{MessageId: v2.MessageId(message), Meta: meta}, nil
			}
			registry, err := NewRegistry([]AgentConfig{{Name: "helper", HostedAdapterID: "fixture", ACPDraftSchema: acpdraft.SchemaTag}})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := NewRunner(RunnerConfig{Registry: registry, EndpointResolver: peer})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runner.Quiesce(context.Background()) })
			events := make(chan childInputTestEvent, 64)
			spawn := childInputSpawnContext(t, "original-task", events)
			spawn.ParentCallID = "original-spawn-call"
			spawn.ApprovalRequester = subagentApprovalRequesterFunc(func(_ context.Context, req tasksubagent.ApprovalRequest) (tasksubagent.ApprovalResponse, error) {
				approvals <- req
				return agent.ApprovalResponse{Outcome: "selected", OptionID: "native-allow", Approved: true}, nil
			})
			anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "initial input"})
			if err != nil {
				t.Fatal(err)
			}
			initial := waitChildActivityTerminalFor(t, ctx, events, spawn.ActivityID)
			if initial.Result == nil || initial.Result.State != delegation.StateCompleted {
				t.Fatalf("initial activity failed: %+v", initial)
			}
			run, err := runner.lookup(anchor)
			if err != nil {
				t.Fatal(err)
			}
			// Spawn intentionally detaches caller deadlines. Install a bounded
			// producer context at its owning run, then use the public idle input path.
			producerCtx, cancelProducer := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancelProducer()
			run.mu.Lock()
			run.ctx = producerCtx
			run.cancel = cancelProducer
			run.mu.Unlock()
			started, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "original-controller"}, Input: "followup input"})
			if err != nil || !started.StartedActivity {
				t.Fatalf("idle input admission = %+v, %v", started, err)
			}
			terminal := waitChildActivityTerminalFor(t, ctx, events, started.ActivityID)
			if terminal.Result == nil || terminal.Result.State != delegation.StateUnknownOutcome || terminal.Result.TaskID != anchor.TaskID || terminal.Result.Result != "" {
				t.Fatalf("receipt failure falsely settled or lost task: %+v", terminal)
			}
			var approval tasksubagent.ApprovalRequest
			select {
			case approval = <-approvals:
			case <-ctx.Done():
				t.Fatal("original child approval owner lost")
			}
			if approval.TaskID != anchor.TaskID || approval.ParentCallID != spawn.ParentCallID || approval.Origin.SessionID != anchor.SessionID || approval.Origin.ParentSessionID != spawn.SessionRef.SessionID || approval.Approval == nil || approval.Approval.Context == nil || len(approval.Approval.Context.NativeRequestID) == 0 || approval.Approval.Subject == nil || approval.Approval.Subject.Kind != "command" || len(approval.Approval.Subject.Targets) != 2 || approval.Approval.Subject.Targets[0].Text != "git status" || len(approval.Approval.Options) != 2 || approval.Approval.Options[0].ID != "native-allow" {
				t.Fatalf("child approval owner/options/subject lost: %+v", approval)
			}
			retained, err := runner.lookup(anchor)
			if err != nil || retained != run {
				t.Fatalf("original task owner lost: %v", err)
			}
			run.mu.RLock()
			state, bound, remote := run.state, run.anchor, run.client
			run.mu.RUnlock()
			if state != delegation.StateUnknownOutcome || bound != anchor || remote == nil {
				t.Fatalf("quarantine discarded owner: state=%s anchor=%+v client=%v", state, bound, remote != nil)
			}
			if !run.slot.activityCheckpoint().outputQuarantined {
				t.Fatal("unknown new activity retained live output admission")
			}
			runner.handleUpdate(run, client.UpdateEnvelope{SessionID: anchor.SessionID, Update: client.ContentChunk{SessionUpdate: client.UpdateAgentMessage, MessageID: "late-new-activity", Content: json.RawMessage(`{"type":"text","text":"late quarantined new activity"}`)}})
			for len(events) != 0 {
				late := <-events
				data, err := json.Marshal(late)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "late quarantined new activity") {
					t.Fatalf("late unknown activity output was admitted: %s", data)
				}
			}
			if peer.Prompts.Load() != 2 || peer.Opens.Load() != 1 {
				t.Fatalf("unknown followup was retried: prompts=%d opens=%d", peer.Prompts.Load(), peer.Opens.Load())
			}
		})
	}
}

func TestDraftChildSteeringTimeoutPreservesOutputAndExplicitNextMessage(t *testing.T) {
	for _, failure := range []string{"missing-echo", "late-receipt"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			peer := &draftpeer.Peer{SessionID: "child-remote"}
			ready := make(chan string, 1)
			release := make(chan struct{})
			lateReturned := make(chan struct{})
			peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
				switch peer.Prompts.Load() {
				case 1:
					if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
						return v2.PromptResponse{}, err
					}
					if err := peer.Echo(ctx, req.SessionId, message, "initial input"); err != nil {
						return v2.PromptResponse{}, err
					}
					ready <- activity
				case 2:
					if failure == "late-receipt" {
						<-release
						defer close(lateReturned)
						if err := peer.Echo(ctx, req.SessionId, message, "ambiguous input"); err != nil {
							return v2.PromptResponse{}, err
						}
					}
				default:
					if err := peer.Echo(ctx, req.SessionId, message, "explicit next message"); err != nil {
						return v2.PromptResponse{}, err
					}
				}
				return v2.PromptResponse{MessageId: v2.MessageId(message), Meta: acpdraft.ActivityMeta(activity)}, nil
			}
			registry, err := NewRegistry([]AgentConfig{{Name: "helper", HostedAdapterID: "fixture", ACPDraftSchema: acpdraft.SchemaTag}})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := NewRunner(RunnerConfig{Registry: registry, EndpointResolver: peer})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runner.Quiesce(context.Background()) })
			events := make(chan childInputTestEvent, 64)
			spawn := childInputSpawnContext(t, "original-task", events)
			anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "initial input"})
			if err != nil {
				t.Fatal(err)
			}
			activity := <-ready
			run, err := runner.lookup(anchor)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := run.client.ListSessions(ctx, acp.ListSessionsRequest{}); err != nil {
				t.Fatal(err)
			}
			steerCtx, cancelSteer := context.WithTimeout(ctx, 50*time.Millisecond)
			_, err = submitChildInputTest(runner, events, steerCtx, agent.ChildInputRequest{Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "sender"}, Input: "ambiguous input"})
			cancelSteer()
			if !errorcode.Is(err, errorcode.UnknownOutcome) {
				t.Fatalf("input outcome = %v", err)
			}
			run.mu.RLock()
			running, state := run.running, run.state
			run.mu.RUnlock()
			if !running || state != delegation.StateRunning || run.slot.activityCheckpoint().outputQuarantined {
				t.Fatalf("message timeout escaped to Activity: running=%v state=%s", running, state)
			}
			if failure == "late-receipt" {
				close(release)
				<-lateReturned
			}
			// The next send is an explicit decision, never an automatic replay.
			next, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "sender"}, Input: "explicit next message"})
			if err != nil || next.StartedActivity || next.ActivityID != spawn.ActivityID {
				t.Fatalf("next message = %+v, %v", next, err)
			}
			if err := peer.Update(ctx, v2.SessionId(anchor.SessionID), map[string]any{"sessionUpdate": "agent_message", "messageId": "later-output", "content": []any{map[string]any{"type": "text", "text": "continued original Activity"}}}); err != nil {
				t.Fatal(err)
			}
			if err := peer.State(ctx, v2.SessionId(anchor.SessionID), activity, "idle", "end_turn"); err != nil {
				t.Fatal(err)
			}
			terminal := waitChildActivityTerminalFor(t, ctx, events, spawn.ActivityID)
			if terminal.Result == nil || terminal.Result.State != delegation.StateCompleted || terminal.Result.Result != "continued original Activity" {
				t.Fatalf("original completion = %+v", terminal.Result)
			}
			if peer.Prompts.Load() != 3 || peer.Opens.Load() != 1 || peer.Cancels.Load() != 0 {
				t.Fatalf("unexpected retry/reconnect/cancel: %d/%d/%d", peer.Prompts.Load(), peer.Opens.Load(), peer.Cancels.Load())
			}
		})
	}
}

func TestDraftChildTerminalReleasesSteeringWithoutEchoOrInputDeadline(t *testing.T) {
	ctx := t.Context()
	peer := &draftpeer.Peer{SessionID: "child-remote"}
	ready, steering := make(chan string, 1), make(chan struct{})
	peer.OnPrompt = func(ctx context.Context, req v2.PromptRequest, activity, message string) (v2.PromptResponse, error) {
		if peer.Prompts.Load() == 1 {
			if err := peer.State(ctx, req.SessionId, activity, "running", ""); err != nil {
				return v2.PromptResponse{}, err
			}
			if err := peer.Echo(ctx, req.SessionId, message, "initial input"); err != nil {
				return v2.PromptResponse{}, err
			}
			ready <- activity
		} else {
			close(steering)
		}
		return v2.PromptResponse{MessageId: v2.MessageId(message), Meta: acpdraft.ActivityMeta(activity)}, nil
	}
	registry, err := NewRegistry([]AgentConfig{{Name: "helper", HostedAdapterID: "fixture", ACPDraftSchema: acpdraft.SchemaTag}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry, EndpointResolver: peer})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runner.Quiesce(context.Background()) }()
	events := make(chan childInputTestEvent, 64)
	spawn := childInputSpawnContext(t, "original-task", events)
	anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "initial input"})
	if err != nil {
		t.Fatal(err)
	}
	activity := <-ready
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.client.ListSessions(ctx, acp.ListSessionsRequest{}); err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() {
		_, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "sender"}, Input: "missing echo"})
		sent <- err
	}()
	<-steering
	if err := peer.Update(ctx, v2.SessionId(anchor.SessionID), map[string]any{"sessionUpdate": "agent_message", "messageId": "final", "content": []any{map[string]any{"type": "text", "text": "original completed"}}}); err != nil {
		t.Fatal(err)
	}
	if err := peer.State(ctx, v2.SessionId(anchor.SessionID), activity, "idle", "end_turn"); err != nil {
		t.Fatal(err)
	}
	// Only this assertion has a deadline; the production input wait does not.
	assertion, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	terminal := waitChildActivityTerminalFor(t, assertion, events, spawn.ActivityID)
	if terminal.Result == nil || terminal.Result.State != delegation.StateCompleted || terminal.Result.Result != "original completed" {
		t.Fatalf("terminal = %+v", terminal.Result)
	}
	select {
	case err := <-sent:
		if !errorcode.Is(err, errorcode.UnknownOutcome) {
			t.Fatalf("message outcome = %v", err)
		}
	case <-assertion.Done():
		t.Fatal("steering still waits after Activity completion")
	}
	if peer.Cancels.Load() != 0 || peer.Prompts.Load() != 2 {
		t.Fatalf("cancel/replay = %d/%d", peer.Cancels.Load(), peer.Prompts.Load())
	}
}
