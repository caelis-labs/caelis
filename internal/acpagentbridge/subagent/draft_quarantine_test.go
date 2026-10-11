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

func TestDraftChildSteeringReceiptWithoutEchoKeepsOriginalStreamAndQuarantinesLaterOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	peer := &draftpeer.Peer{SessionID: "child-remote"}
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
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.client.ListSessions(ctx, acp.ListSessionsRequest{}); err != nil {
		t.Fatal(err)
	}
	steerCtx, cancelSteer := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancelSteer()
	_, err = submitChildInputTest(runner, events, steerCtx, agent.ChildInputRequest{Target: run.slot.target, Source: session.ActorRef{Kind: session.ActorKindController, ID: "original-controller"}, Input: "ambiguous input"})
	if !errorcode.Is(err, errorcode.UnknownOutcome) {
		t.Fatalf("steer receipt missing echo = %v, want unknown", err)
	}
	observedOriginalOutput := false
	for {
		select {
		case event := <-events:
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			// Output still belongs to the already admitted original Activity.
			// Continuous same-Activity output may precede insertion confirmation;
			// uncertainty must not rewrite it as the steering sender's output.
			if strings.Contains(string(data), "ambiguous remote output") {
				observedOriginalOutput = true
				if event.ActivityID != spawn.ActivityID || event.Frame == nil || event.Frame.Event == nil || event.Frame.Event.Actor.ID != anchor.AgentID {
					t.Fatalf("original output changed owner: %+v", event)
				}
			}
			if strings.Contains(string(data), "ambiguous input") {
				t.Fatalf("unproven input acquired canonical acceptance: %s", data)
			}
			if event.Result == nil {
				continue
			}
			if event.Result.State != delegation.StateUnknownOutcome || event.Result.TaskID != anchor.TaskID || event.Result.Result != "" {
				t.Fatalf("original child falsely settled: %+v", event.Result)
			}
			if !observedOriginalOutput {
				t.Fatal("existing original Activity stream was dropped")
			}
			if peer.Prompts.Load() != 2 || peer.Opens.Load() != 1 {
				t.Fatalf("unproven steering retried: prompts=%d opens=%d", peer.Prompts.Load(), peer.Opens.Load())
			}
			retained, err := runner.lookup(anchor)
			if err != nil || retained != run {
				t.Fatalf("unknown steering lost original task owner: %v", err)
			}
			// A notification already decoded by the SDK may reach the callback
			// after transport isolation. The real owner must reject that late output.
			runner.handleUpdate(run, client.UpdateEnvelope{SessionID: anchor.SessionID, Update: client.ContentChunk{SessionUpdate: client.UpdateAgentMessage, MessageID: "late-message", Content: json.RawMessage(`{"type":"text","text":"late quarantined output"}`)}})
			for len(events) != 0 {
				late := <-events
				data, err := json.Marshal(late)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "late quarantined output") {
					t.Fatalf("late output crossed quarantine: %s", data)
				}
			}
			return
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
