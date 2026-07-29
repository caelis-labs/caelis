package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	contextprompt "github.com/caelis-labs/caelis/agent-sdk/runtime/contexttransfer"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
)

// continuePhase is the durable recovery phase for one parent→child Continue
// dual-write. Phases mirror the spawn effect boundary model with fewer steps:
//
//	prepared  — turn identity + prompt recorded; remote Continue not claimed
//	pending   — claimed for remote Continue; restart refuses blind re-issue
//	post_effect — remote Continue was accepted; terminal belongs to CompletionSink
//	(cleared) — parent final + task state committed for this turn
//	unknown   — remote failed or process restarted after the external claim
type continuePhase string

const (
	continuePhaseNone           continuePhase = ""
	continuePhasePrepared       continuePhase = "continue_prepared"
	continuePhasePending        continuePhase = "continue_pending"
	continuePhasePostEffect     continuePhase = "continue_post_effect"
	continuePhaseUnknownOutcome continuePhase = "continue_unknown_outcome"
)

const subagentContinueUnknownDiagnostic = "subagent continuation outcome could not be confirmed"

func (tm *taskRuntime) continueSubagent(ctx context.Context, task *subagentTask, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	releaseOperation, claimed := tm.tryClaimSubagentOperation(task.sessionRef, task.ref.TaskID)
	if !claimed {
		return task.snapshot(), fmt.Errorf("agent-sdk/runtime: subagent continue %q already has an operation in progress", task.ref.TaskID)
	}
	defer releaseOperation()
	return tm.continueSubagentClaimed(ctx, task, req)
}

func (tm *taskRuntime) continueSubagentClaimed(ctx context.Context, task *subagentTask, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	prompt := strings.TrimSpace(req.Input)
	if prompt == "" {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: Task write for Spawn task %q requires a follow-up prompt", task.ref.TaskID)
	}
	if err := tm.authorizeSubagentControl(task, req.Principal, "write"); err != nil {
		return taskapi.Snapshot{}, err
	}
	if task.runner == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: Spawn task %q cannot continue because its child session runner is unavailable", task.ref.TaskID)
	}

	phase, _, _, storedDigest, storedTurnSeq := continueStateOfTask(task)
	switch phase {
	case continuePhasePending:
		reason := "runtime restarted or lost ownership after the remote continuation claim"
		if err := tm.markSubagentContinueUnknown(context.WithoutCancel(ctx), task, reason); err != nil {
			return task.snapshot(), err
		}
		return task.snapshot(), fmt.Errorf("agent-sdk/runtime: subagent continue %q has %s; refusing blind re-issue of the remote turn", task.ref.TaskID, continuePhaseUnknownOutcome)
	case continuePhaseUnknownOutcome:
		return task.snapshot(), fmt.Errorf("agent-sdk/runtime: subagent continue %q has %s; refusing blind re-issue of the remote turn", task.ref.TaskID, phase)
	case continuePhasePrepared, continuePhasePostEffect:
		digest, err := continueRequestDigest(prompt, req.Context, storedTurnSeq)
		if err != nil {
			return taskapi.Snapshot{}, err
		}
		if storedDigest != "" && storedDigest != digest {
			return task.snapshot(), fmt.Errorf("agent-sdk/runtime: subagent continue %q has an in-flight turn with a different prompt; recover the pending turn first", task.ref.TaskID)
		}
		if phase == continuePhasePrepared {
			return tm.executeClaimedSubagentContinue(ctx, task)
		}
		return tm.advanceSubagentContinue(ctx, task)
	}

	task.mu.Lock()
	state := task.state
	running := task.running
	task.mu.Unlock()
	if running || state != taskapi.StateCompleted {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: Spawn task %q is %s; use Task wait until completed before Task write", task.ref.TaskID, state)
	}

	checkpoint := task.beginContinuationTurn()
	turnSeq := task.turnSeq
	digest, err := continueRequestDigest(prompt, req.Context, turnSeq)
	if err != nil {
		task.restoreContinuationTurn(checkpoint, true)
		return taskapi.Snapshot{}, err
	}
	if err := tm.markSubagentContinuePhase(ctx, task, continuePhasePrepared, prompt, req.Context, digest, turnSeq, ""); err != nil {
		task.restoreContinuationTurn(checkpoint, true)
		return taskapi.Snapshot{}, err
	}
	return tm.executeClaimedSubagentContinue(ctx, task)
}

func (tm *taskRuntime) executeClaimedSubagentContinue(ctx context.Context, task *subagentTask) (taskapi.Snapshot, error) {
	_, prompt, contextTransfer, digest, turnSeq := continueStateOfTask(task)

	if err := tm.appendSideSubagentUserEvent(ctx, task, prompt); err != nil {
		// Intent is durable; leave prepared so retry re-appends via idempotent keys.
		return task.snapshot(), err
	}
	if err := tm.markSubagentContinuePhase(ctx, task, continuePhasePending, prompt, contextTransfer, digest, turnSeq, ""); err != nil {
		return task.snapshot(), err
	}
	_, err := task.runner.Continue(ctx, delegation.CloneAnchor(task.anchor), delegation.ContinueRequest{
		Prompt: contextprompt.ComposeTextPrompt(contextTransfer, prompt),
		// Continue only issues the remote generation. Task lifecycle is driven
		// by CompletionSink, and callers that want to observe it use Task wait.
		YieldTimeMS: 0,
		Completion:  newSubagentCompletionSink(ctx, tm, task.ref.TaskID, turnSeq),
	})
	if err != nil {
		if drained, drainErr := tm.drainPendingSubagentCompletionClaimed(task); drained != subagentCompletionDrainNone {
			return task.snapshot(), drainErr
		}
		persistErr := tm.markSubagentContinueUnknown(context.WithoutCancel(ctx), task, err.Error())
		return task.snapshot(), errors.Join(err, persistErr)
	}
	if drained, drainErr := tm.drainPendingSubagentCompletionClaimed(task); drained != subagentCompletionDrainNone {
		return task.snapshot(), drainErr
	}
	if err := tm.markSubagentContinuePostEffect(ctx, task, prompt, contextTransfer, digest, turnSeq); err != nil {
		// The remote effect was already accepted after a durable pending claim.
		// Returning an ordinary retryable error could issue the same turn again;
		// retain pending and let CompletionSink/recovery establish the outcome.
		return task.snapshot(), nil
	}
	return tm.advanceSubagentContinue(ctx, task)
}

func (tm *taskRuntime) advanceSubagentContinue(ctx context.Context, task *subagentTask) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	phase := continuePhaseOfTask(task)
	switch phase {
	case continuePhasePrepared:
		return tm.executeClaimedSubagentContinue(ctx, task)
	case continuePhasePostEffect:
		// The external effect crossed its durable boundary. Terminal lifecycle
		// and Side final dialogue are owned only by the producer CompletionSink.
	case continuePhaseNone:
		return task.snapshot(), nil
	default:
		return task.snapshot(), fmt.Errorf("agent-sdk/runtime: cannot advance subagent continue from phase %q", phase)
	}
	return task.snapshot(), nil
}

func continueRequestDigest(prompt string, contextTransfer agent.ContextTransfer, turnSeq int64) (string, error) {
	payload := struct {
		Prompt  string                `json:"prompt"`
		Context agent.ContextTransfer `json:"context"`
		TurnSeq int64                 `json:"turn_seq"`
	}{
		Prompt:  strings.TrimSpace(prompt),
		Context: agent.CloneContextTransfer(contextTransfer),
		TurnSeq: turnSeq,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("agent-sdk/runtime: encode continue identity: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func continuePhaseOfTask(task *subagentTask) continuePhase {
	phase, _, _, _, _ := continueStateOfTask(task)
	return phase
}

func continueStateOfTask(task *subagentTask) (continuePhase, string, agent.ContextTransfer, string, int64) {
	if task == nil {
		return continuePhaseNone, "", agent.ContextTransfer{}, "", 0
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return normalizeContinuePhase(taskStringValue(task.metadata["continue_phase"])),
		taskStringValue(task.metadata["continue_prompt"]),
		taskContextTransferValue(task.metadata["continue_context"]),
		taskStringValue(task.metadata["continue_digest"]),
		continueTurnSeqOfTaskLocked(task)
}

func continueTurnSeqOfTask(task *subagentTask) int64 {
	if task == nil {
		return 0
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return continueTurnSeqOfTaskLocked(task)
}

func continueTurnSeqOfTaskLocked(task *subagentTask) int64 {
	if task == nil {
		return 0
	}
	if seq := task.turnSeq; seq > 0 {
		return seq
	}
	if task.metadata == nil {
		return 0
	}
	return taskTurnSeqFromSpec(map[string]any{"turn_seq": task.metadata["continue_turn_seq"]})
}

func normalizeContinuePhase(raw string) continuePhase {
	switch phase := continuePhase(strings.TrimSpace(raw)); phase {
	case continuePhasePrepared, continuePhasePending, continuePhasePostEffect, continuePhaseUnknownOutcome:
		return phase
	default:
		return continuePhaseNone
	}
}

func (tm *taskRuntime) markSubagentContinuePhase(
	ctx context.Context,
	task *subagentTask,
	phase continuePhase,
	prompt string,
	contextTransfer agent.ContextTransfer,
	digest string,
	turnSeq int64,
	reason string,
) error {
	return tm.persistSubagentContinuePhase(ctx, task, phase, prompt, contextTransfer, digest, turnSeq, reason)
}

func (tm *taskRuntime) markSubagentContinuePostEffect(
	ctx context.Context,
	task *subagentTask,
	prompt string,
	contextTransfer agent.ContextTransfer,
	digest string,
	turnSeq int64,
) error {
	return tm.persistSubagentContinuePhase(ctx, task, continuePhasePostEffect, prompt, contextTransfer, digest, turnSeq, "")
}

func (tm *taskRuntime) persistSubagentContinuePhase(
	ctx context.Context,
	task *subagentTask,
	phase continuePhase,
	prompt string,
	contextTransfer agent.ContextTransfer,
	digest string,
	turnSeq int64,
	reason string,
) error {
	if task == nil {
		return nil
	}
	task.mu.Lock()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if phase == continuePhaseUnknownOutcome {
		reason = subagentContinueUnknownDiagnostic
	}
	applyContinuePhaseToEntry(entry, phase, prompt, contextTransfer, digest, turnSeq, reason)
	switch phase {
	case continuePhasePrepared, continuePhasePending, continuePhasePostEffect:
		entry.State = taskapi.StateRunning
		entry.Running = true
		entry.SupportsInput = false
		entry.Metadata["state"] = string(taskapi.StateRunning)
		entry.Metadata["running"] = true
		entry.FailureDiagnostic = ""
		delete(entry.Result, "error")
		delete(entry.Result, "result")
		delete(entry.Result, "final_message")
		delete(entry.Result, "output_preview")
		entry.Result["state"] = string(taskapi.StateRunning)
		entry.Spec["prompt"] = strings.TrimSpace(prompt)
		entry.Spec["spawn_result"] = taskapi.SanitizeResultForPersistence(entry.Result, taskapi.ResultPersistenceCanonical)
	case continuePhaseUnknownOutcome:
		entry.Running = false
		entry.State = taskapi.StateUnknownOutcome
		entry.SupportsInput = false
		normalizeSubagentEntryResult(entry, reason)
		if entry.Spec == nil {
			entry.Spec = map[string]any{}
		}
		entry.Spec["spawn_result"] = taskapi.SanitizeResultForPersistence(entry.Result, taskapi.ResultPersistenceCanonical)
	}
	if err := tm.persistSpawnEntry(ctx, entry); err != nil {
		return err
	}
	task.mu.Lock()
	applyContinuePhaseToMetadata(&task.metadata, phase, prompt, contextTransfer, digest, turnSeq, reason)
	switch phase {
	case continuePhasePrepared, continuePhasePending, continuePhasePostEffect:
		task.prompt = strings.TrimSpace(prompt)
		task.running = true
		task.state = taskapi.StateRunning
		delete(task.result, "error")
		delete(task.result, "result")
		delete(task.result, "final_message")
		delete(task.result, "output_preview")
		task.result["state"] = string(taskapi.StateRunning)
		task.metadata["state"] = string(taskapi.StateRunning)
		task.metadata["running"] = true
	case continuePhaseUnknownOutcome:
		task.running = false
		task.state = taskapi.StateUnknownOutcome
		normalizeSubagentResultForState(&task.result, taskapi.StateUnknownOutcome, reason)
	}
	task.revision = entry.Revision
	task.lease = taskapi.CloneLease(entry.Lease)
	task.notifyStreamChangeLocked()
	task.mu.Unlock()
	return nil
}

func applyContinuePhaseToEntry(entry *taskapi.Entry, phase continuePhase, prompt string, contextTransfer agent.ContextTransfer, digest string, turnSeq int64, reason string) {
	if entry == nil {
		return
	}
	applyContinuePhaseToMetadata(&entry.Metadata, phase, prompt, contextTransfer, digest, turnSeq, reason)
	if entry.Spec == nil {
		entry.Spec = map[string]any{}
	}
	if phase == continuePhaseNone {
		for _, key := range []string{"continue_phase", "continue_digest", "continue_turn_seq"} {
			delete(entry.Spec, key)
		}
	} else {
		entry.Spec["continue_phase"] = string(phase)
		entry.Spec["continue_digest"] = strings.TrimSpace(digest)
		entry.Spec["continue_turn_seq"] = turnSeq
	}
}

func applyContinuePhaseToMetadata(metadata *map[string]any, phase continuePhase, prompt string, contextTransfer agent.ContextTransfer, digest string, turnSeq int64, reason string) {
	if metadata == nil {
		return
	}
	if *metadata == nil {
		*metadata = map[string]any{}
	}
	values := *metadata
	if phase == continuePhaseNone {
		for _, key := range []string{"continue_phase", "continue_prompt", "continue_context", "continue_digest", "continue_turn_seq", "continue_reason"} {
			delete(values, key)
		}
		return
	}
	values["continue_phase"] = string(phase)
	values["continue_prompt"] = strings.TrimSpace(prompt)
	values["continue_context"] = agent.CloneContextTransfer(contextTransfer)
	values["continue_digest"] = strings.TrimSpace(digest)
	values["continue_turn_seq"] = turnSeq
	if strings.TrimSpace(reason) == "" {
		delete(values, "continue_reason")
	} else {
		values["continue_reason"] = strings.TrimSpace(reason)
	}
}

func (tm *taskRuntime) markSubagentContinueUnknown(ctx context.Context, task *subagentTask, reason string) error {
	if task == nil {
		return nil
	}
	reason = firstNonEmpty(strings.TrimSpace(reason), "remote continuation outcome is unknown")
	task.mu.Lock()
	prompt := taskStringValue(task.metadata["continue_prompt"])
	contextTransfer := taskContextTransferValue(task.metadata["continue_context"])
	digest := taskStringValue(task.metadata["continue_digest"])
	turnSeq := continueTurnSeqOfTaskLocked(task)
	task.mu.Unlock()
	return tm.markSubagentContinuePhase(ctx, task, continuePhaseUnknownOutcome, prompt, contextTransfer, digest, turnSeq, reason)
}

func taskContextTransferValue(raw any) agent.ContextTransfer {
	if typed, ok := raw.(agent.ContextTransfer); ok {
		return agent.CloneContextTransfer(typed)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return agent.ContextTransfer{}
	}
	var decoded agent.ContextTransfer
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return agent.ContextTransfer{}
	}
	return agent.CloneContextTransfer(decoded)
}
