package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
	prompt := strings.TrimSpace(req.Input)
	if prompt == "" {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: TASK write for SPAWN task %q requires a follow-up prompt", task.ref.TaskID)
	}
	if err := tm.authorizeSubagentControl(task, req.Principal, "write"); err != nil {
		return taskapi.Snapshot{}, err
	}
	if task.runner == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: SPAWN task %q cannot continue because its child session runner is unavailable", task.ref.TaskID)
	}

	phase := continuePhaseOfTask(task)
	switch phase {
	case continuePhasePending, continuePhaseUnknownOutcome:
		return task.snapshot(), fmt.Errorf("agent-sdk/runtime: subagent continue %q has %s; refusing blind re-issue of the remote turn", task.ref.TaskID, phase)
	case continuePhasePrepared, continuePhasePostEffect:
		storedDigest := taskStringValue(task.metadata["continue_digest"])
		digest, err := continueRequestDigest(prompt, req.ContextPrelude, continueTurnSeqOfTask(task))
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
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: SPAWN task %q is %s; use TASK wait until completed before TASK write", task.ref.TaskID, state)
	}

	checkpoint := task.beginContinuationTurn()
	turnSeq := task.turnSeq
	digest, err := continueRequestDigest(prompt, req.ContextPrelude, turnSeq)
	if err != nil {
		task.restoreContinuationTurn(checkpoint, true)
		return taskapi.Snapshot{}, err
	}
	if err := tm.markSubagentContinuePhase(ctx, task, continuePhasePrepared, prompt, req.ContextPrelude, digest, turnSeq, ""); err != nil {
		task.restoreContinuationTurn(checkpoint, true)
		return taskapi.Snapshot{}, err
	}
	return tm.executeClaimedSubagentContinue(ctx, task, int(req.Yield/time.Millisecond))
}

func (tm *taskRuntime) executeClaimedSubagentContinue(ctx context.Context, task *subagentTask, yieldMS int) (taskapi.Snapshot, error) {
	prompt := taskStringValue(task.metadata["continue_prompt"])
	prelude := taskStringValue(task.metadata["continue_prelude"])
	digest := taskStringValue(task.metadata["continue_digest"])
	turnSeq := continueTurnSeqOfTask(task)

	if err := tm.appendSideSubagentUserEvent(ctx, task, prompt); err != nil {
		// Intent is durable; leave prepared so retry re-appends via idempotent keys.
		return task.snapshot(), err
	}
	if err := tm.markSubagentContinuePhase(ctx, task, continuePhasePending, prompt, prelude, digest, turnSeq, ""); err != nil {
		return task.snapshot(), err
	}
	result, err := task.runner.Continue(ctx, delegation.CloneAnchor(task.anchor), delegation.ContinueRequest{
		Agent:       task.agent,
		Prompt:      subagentPromptWithContext(prelude, prompt),
		YieldTimeMS: yieldMS,
	})
	if err != nil {
		_ = tm.markSubagentContinuePhase(context.WithoutCancel(ctx), task, continuePhaseUnknownOutcome, prompt, prelude, digest, turnSeq, err.Error())
		return task.snapshot(), err
	}
	task.mu.Lock()
	task.prompt = prompt
	task.applyResult(result)
	task.seedStreamFromResult(result)
	task.mu.Unlock()
	if err := tm.markSubagentContinuePhase(ctx, task, continuePhasePostEffect, prompt, prelude, digest, turnSeq, ""); err != nil {
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

func continueRequestDigest(prompt string, prelude string, turnSeq int64) (string, error) {
	payload := struct {
		Prompt  string `json:"prompt"`
		Prelude string `json:"prelude"`
		TurnSeq int64  `json:"turn_seq"`
	}{
		Prompt:  strings.TrimSpace(prompt),
		Prelude: strings.TrimSpace(prelude),
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
	if task == nil {
		return continuePhaseNone
	}
	return normalizeContinuePhase(taskStringValue(task.metadata["continue_phase"]))
}

func continueTurnSeqOfTask(task *subagentTask) int64 {
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
	prelude string,
	digest string,
	turnSeq int64,
	reason string,
) error {
	if task == nil {
		return nil
	}
	task.mu.Lock()
	if task.metadata == nil {
		task.metadata = map[string]any{}
	}
	if phase == continuePhaseNone {
		delete(task.metadata, "continue_phase")
		delete(task.metadata, "continue_prompt")
		delete(task.metadata, "continue_prelude")
		delete(task.metadata, "continue_digest")
		delete(task.metadata, "continue_turn_seq")
		delete(task.metadata, "continue_reason")
	} else {
		task.metadata["continue_phase"] = string(phase)
		task.metadata["continue_prompt"] = strings.TrimSpace(prompt)
		task.metadata["continue_prelude"] = strings.TrimSpace(prelude)
		task.metadata["continue_digest"] = strings.TrimSpace(digest)
		task.metadata["continue_turn_seq"] = turnSeq
		if strings.TrimSpace(reason) != "" {
			task.metadata["continue_reason"] = strings.TrimSpace(reason)
		}
	}
	entry := task.entrySnapshot(tm.runtime.now())
	if entry.Spec == nil {
		entry.Spec = map[string]any{}
	}
	if phase == continuePhaseNone {
		delete(entry.Spec, "continue_phase")
	} else {
		entry.Spec["continue_phase"] = string(phase)
		entry.Spec["continue_digest"] = strings.TrimSpace(digest)
		entry.Spec["continue_turn_seq"] = turnSeq
	}
	task.mu.Unlock()
	err := tm.persistSpawnEntry(ctx, entry)
	if err == nil {
		task.mu.Lock()
		task.revision = entry.Revision
		task.mu.Unlock()
	}
	return err
}

func (tm *taskRuntime) clearSubagentContinuePhase(ctx context.Context, task *subagentTask) error {
	return tm.markSubagentContinuePhase(ctx, task, continuePhaseNone, "", "", "", 0, "")
}
