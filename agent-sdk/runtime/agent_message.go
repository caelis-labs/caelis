package runtime

import (
	"context"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

// SendAgentMessage routes one trusted message within the current Session
// topology. Parent delivery never starts an idle main turn; child delivery may
// start the next Turn of any idle spawned child. Per-Turn terminal state does
// not retire the stable child identity.
func (r *Runtime) SendAgentMessage(ctx context.Context, ref session.SessionRef, raw agentmessage.Request) (agentmessage.Response, error) {
	if r == nil || r.sessions == nil || r.tasks == nil {
		return agentmessage.Response{}, fmt.Errorf("agent-sdk/runtime: Agent message service is unavailable")
	}
	req := agentmessage.NormalizeRequest(raw)
	if req.MessageID == "" || req.To == "" || req.Text == "" || !session.ActorRefHasIdentity(req.From) {
		return agentmessage.Response{}, fmt.Errorf("agent-sdk/runtime: Agent message id, target, text, and source are required")
	}
	ref = session.NormalizeSessionRef(ref)
	if strings.EqualFold(req.To, agentmessage.Parent) {
		return r.deliverAgentMessageToMain(ctx, ref, req)
	}
	return r.tasks.sendSubagentMessage(ctx, ref, req)
}

func (r *Runtime) deliverAgentMessageToMain(ctx context.Context, ref session.SessionRef, req agentmessage.Request) (agentmessage.Response, error) {
	release, err := r.acquireSessionWrite(ctx, ref)
	if err != nil {
		return agentmessage.Response{}, err
	}
	defer release()

	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return agentmessage.Response{}, err
	}
	active := r.activeRunForSession(ref)
	turnID := strings.TrimSpace(active.turnID)
	appendResult, err := agentmessage.AppendContext(
		ctx, r.sessions, ref,
		session.ControlMutationGuard(session.ControlMutationPurposeAgentMessage),
		defaultScope(activeSession, turnID), req,
	)
	if err != nil {
		return agentmessage.Response{}, err
	}
	persisted := appendResult.Event
	if !appendResult.Appended {
		return agentmessage.Response{MessageID: req.MessageID, Accepted: true, State: agentmessage.StatePending}, nil
	}
	if active.handle == nil {
		active = r.activeRunForSession(ref)
	}
	state := agentmessage.StatePending
	if active.handle != nil {
		submission := agent.Submission{
			Kind: agent.SubmissionKindAgentMessage, Text: req.Text,
			MessageID: req.MessageID, Actor: req.From, Scope: persisted.Scope,
			Metadata: session.CloneState(persisted.Meta), Persisted: true,
		}
		if submitErr := active.handle.Submit(submission); submitErr == nil {
			state = agentmessage.StateDelivered
		}
	}
	return agentmessage.Response{MessageID: req.MessageID, Accepted: true, State: state}, nil
}

func (r *Runtime) activeRunForSession(ref session.SessionRef) activeRun {
	if r == nil {
		return activeRun{}
	}
	sessionID := strings.TrimSpace(session.NormalizeSessionRef(ref).SessionID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, candidate := range r.activeRunners {
		if candidate.handle != nil && strings.TrimSpace(candidate.ref.SessionID) == sessionID {
			return candidate
		}
	}
	return activeRun{}
}

func (tm *taskRuntime) sendSubagentMessage(ctx context.Context, ref session.SessionRef, req agentmessage.Request) (agentmessage.Response, error) {
	identity, err := tm.resolveTaskHandle(ctx, ref, req.To)
	if err != nil {
		return agentmessage.Response{}, err
	}
	if identity.kind != taskapi.KindSubagent {
		return agentmessage.Response{}, fmt.Errorf("agent-sdk/runtime: message target %q is not a subagent", req.To)
	}
	release, claimed := tm.tryClaimSubagentOperation(ref, identity.taskID)
	if !claimed {
		return agentmessage.Response{}, fmt.Errorf("agent-sdk/runtime: subagent %q already has an operation in progress", req.To)
	}
	defer release()
	task, err := tm.lookupSubagentCanonical(ctx, ref, identity.taskID)
	if err != nil {
		return agentmessage.Response{}, err
	}
	runner, ok := task.runner.(subagent.MessageRunner)
	if !ok || runner == nil {
		return agentmessage.Response{}, fmt.Errorf("agent-sdk/runtime: subagent %q does not support Agent messages", req.To)
	}
	task.mu.Lock()
	running := task.running
	state := task.state
	turnID := subagentTurnID(task.ref.TaskID, task.turnSeq)
	task.mu.Unlock()
	messageReq := subagent.MessageRequest{Request: req}
	if running {
		result, err := runner.Message(ctx, delegation.CloneAnchor(task.anchor), messageReq)
		if err != nil {
			return agentmessage.Response{}, err
		}
		if result.State == delegation.StateUnknownOutcome {
			// Dispatch may have committed remotely, but acceptance cannot be
			// asserted and a new MessageID could duplicate the delivery. Return a
			// terminal, non-error observation so the model does not blind-retry.
			return agentmessage.Response{
				MessageID: req.MessageID, State: agentmessage.StateUnknownOutcome, TurnID: turnID,
			}, nil
		}
		// The runner owns delivery now; the parent-side mirror is observation
		// only and must not turn accepted ownership into a retryable failure.
		_ = tm.appendSubagentMessageMirror(ctx, task, req)
		return agentmessage.Response{
			MessageID: req.MessageID, Accepted: true, State: string(result.State), TurnID: turnID,
		}, nil
	}
	if !subagentTaskStateCanStartTurn(state) {
		return agentmessage.Response{}, fmt.Errorf("agent-sdk/runtime: subagent %q is %s", req.To, state)
	}
	activeSession, err := tm.runtime.sessions.Session(ctx, ref)
	if err != nil {
		return agentmessage.Response{}, err
	}
	task.mu.Lock()
	reconnect := &subagent.ReconnectRequest{
		Spawn: subagent.SpawnContext{
			SessionRef: session.NormalizeSessionRef(ref), Session: session.CloneSession(activeSession),
			CWD: strings.TrimSpace(activeSession.CWD), TaskID: strings.TrimSpace(task.ref.TaskID),
			Handle: strings.TrimSpace(task.handle), Role: subagentParticipantRole(task),
			ParentCallID: taskStringValue(task.metadata["parent_call"]), Mode: strings.TrimSpace(task.mode),
			ApprovalMode: strings.TrimSpace(task.approvalMode), Streams: tm,
			ActivityObserver:    newSubagentActivityObserver(tm, task.ref.TaskID),
			ActivityAfterCursor: task.activityDurableCursor,
		},
		Target: delegation.CloneTargetRequest(delegation.TargetRequest{Target: task.target}).Target,
	}
	task.mu.Unlock()
	reconnect.Spawn.ApprovalRequester = newSubagentApprovalRequester(
		tm.runtime, reconnect.Spawn.Mode, nil, activeSession, ref,
	)
	checkpoint := task.beginMessageTurn()
	task.mu.Lock()
	turnSeq := task.turnSeq
	turnID = subagentTurnID(task.ref.TaskID, turnSeq)
	task.mu.Unlock()
	messageReq.Completion = newSubagentCompletionSink(ctx, tm, task.ref.TaskID, turnSeq)
	reconnect.Spawn.Completion = messageReq.Completion
	messageReq.Reconnect = reconnect
	result, sendErr := runner.Message(ctx, delegation.CloneAnchor(task.anchor), messageReq)
	if sendErr != nil {
		task.restoreMessageTurn(checkpoint, true)
		return agentmessage.Response{}, sendErr
	}
	// Runner acceptance makes the idle-to-running transition real even
	// though target consumption and the new Turn continue asynchronously.
	tm.notifyTaskStreamActivity(task.sessionRef.SessionID, task.ref.TaskID)
	_ = tm.appendSubagentMessageMirror(ctx, task, req)
	task.mu.Lock()
	task.applyResult(result)
	task.seedStreamFromResult(result)
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistObservedSubagentTurn(ctx, task, entry); err != nil {
		// The runner already owns delivery. Local Task persistence is observation
		// state and must not turn the queued effect into a retryable delivery
		// failure; expose the degraded state to the caller instead.
		return agentmessage.Response{
			MessageID: req.MessageID, Accepted: true,
			State: agentmessage.StateAcceptedUnpersisted, TurnID: turnID, StartedTurn: true,
		}, nil
	}
	return agentmessage.Response{
		MessageID: req.MessageID, Accepted: true, State: string(result.State), TurnID: turnID, StartedTurn: true,
	}, nil
}
