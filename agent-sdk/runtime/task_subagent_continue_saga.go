package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
//	post_effect — remote succeeded and result is durable; parent final may remain
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
			return tm.executeClaimedSubagentContinue(ctx, task, int(req.Yield/time.Millisecond))
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
	return tm.executeClaimedSubagentContinue(ctx, task, int(req.Yield/time.Millisecond))
}

func (tm *taskRuntime) executeClaimedSubagentContinue(ctx context.Context, task *subagentTask, yieldMS int) (taskapi.Snapshot, error) {
	_, prompt, contextTransfer, digest, turnSeq := continueStateOfTask(task)

	if err := tm.appendSideSubagentUserEvent(ctx, task, prompt); err != nil {
		// Intent is durable; leave prepared so retry re-appends via idempotent keys.
		return task.snapshot(), err
	}
	if err := tm.markSubagentContinuePhase(ctx, task, continuePhasePending, prompt, contextTransfer, digest, turnSeq, ""); err != nil {
		return task.snapshot(), err
	}
	result, err := task.runner.Continue(ctx, delegation.CloneAnchor(task.anchor), delegation.ContinueRequest{
		Prompt:      contextprompt.ComposeTextPrompt(contextTransfer, prompt),
		YieldTimeMS: yieldMS,
	})
	if err != nil {
		persistErr := tm.markSubagentContinueUnknown(context.WithoutCancel(ctx), task, err.Error())
		return task.snapshot(), errors.Join(err, persistErr)
	}
	if err := tm.markSubagentContinuePostEffect(ctx, task, prompt, contextTransfer, digest, turnSeq, result); err != nil {
		return task.snapshot(), err
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
		return tm.executeClaimedSubagentContinue(ctx, task, 0)
	case continuePhasePostEffect:
		// Parent final dual-write only — never re-issue the remote Continue.
	case continuePhaseNone:
		return task.snapshot(), nil
	default:
		return task.snapshot(), fmt.Errorf("agent-sdk/runtime: cannot advance subagent continue from phase %q", phase)
	}
	if err := tm.appendSideSubagentFinalEvent(ctx, task); err != nil {
		return task.snapshot(), err
	}
	if err := tm.clearSubagentContinuePhase(context.WithoutCancel(ctx), task); err != nil {
		return task.snapshot(), err
	}
	snapshot := task.snapshot()
	if shouldDropInactiveSubagentTask(snapshot) {
		tm.mu.Lock()
		delete(tm.subagents, task.ref.TaskID)
		tm.mu.Unlock()
	}
	_ = tm.updateSubagentParticipant(ctx, task, "updated")
	return snapshot, nil
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
	return tm.persistSubagentContinuePhase(ctx, task, phase, prompt, contextTransfer, digest, turnSeq, reason, nil)
}

func (tm *taskRuntime) markSubagentContinuePostEffect(
	ctx context.Context,
	task *subagentTask,
	prompt string,
	contextTransfer agent.ContextTransfer,
	digest string,
	turnSeq int64,
	result delegation.Result,
) error {
	return tm.persistSubagentContinuePhase(ctx, task, continuePhasePostEffect, prompt, contextTransfer, digest, turnSeq, "", &result)
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
	result *delegation.Result,
) error {
	if task == nil {
		return nil
	}
	task.mu.Lock()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if result != nil {
		desired := tm.rehydrateSubagentTask(entry)
		desired.prompt = strings.TrimSpace(prompt)
		desired.applyResult(*result)
		desired.seedStreamFromResult(*result)
		entry = desired.entrySnapshot(tm.runtime.now())
	}
	applyContinuePhaseToEntry(entry, phase, prompt, contextTransfer, digest, turnSeq, reason)
	if phase == continuePhaseUnknownOutcome {
		reason = firstNonEmpty(strings.TrimSpace(reason), "remote continuation outcome is unknown")
		entry.Running = false
		entry.State = taskapi.StateUnknownOutcome
		entry.SupportsInput = false
		if entry.Result == nil {
			entry.Result = map[string]any{}
		}
		entry.Result["state"] = string(taskapi.StateUnknownOutcome)
		entry.Result["error"] = reason
		if entry.Spec == nil {
			entry.Spec = map[string]any{}
		}
		entry.Spec["spawn_result"] = taskapi.SanitizeResultForPersistence(entry.Result, taskapi.ResultPersistenceCanonical)
	}
	if err := tm.persistSpawnEntry(ctx, entry); err != nil {
		return err
	}
	task.mu.Lock()
	if result != nil {
		task.prompt = strings.TrimSpace(prompt)
		task.applyResult(*result)
		task.seedStreamFromResult(*result)
	}
	applyContinuePhaseToMetadata(&task.metadata, phase, prompt, contextTransfer, digest, turnSeq, reason)
	if phase == continuePhaseUnknownOutcome {
		task.running = false
		task.state = taskapi.StateUnknownOutcome
		if task.result == nil {
			task.result = map[string]any{}
		}
		task.result["state"] = string(taskapi.StateUnknownOutcome)
		task.result["error"] = reason
	}
	task.revision = entry.Revision
	task.lease = taskapi.CloneLease(entry.Lease)
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

func (tm *taskRuntime) clearSubagentContinuePhase(ctx context.Context, task *subagentTask) error {
	return tm.markSubagentContinuePhase(ctx, task, continuePhaseNone, "", agent.ContextTransfer{}, "", 0, "")
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
