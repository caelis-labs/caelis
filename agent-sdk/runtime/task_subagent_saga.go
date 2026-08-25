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
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/agenthandle"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
)

const subagentSagaRecoveryTimeout = 5 * time.Second

// spawnPhase is the durable recovery phase for subagent spawn.
// Phases are intentionally few:
//
//	intent            — durable request recorded; external effect not claimed
//	external_pending  — claimed for remote spawn; never blind-respawn on restart
//	post_spawn        — remote spawn recorded; local attach/events still needed
//	committed         — fully durable
//	compensating / child_cancelled / compensated / unknown_outcome — failure path
//
// Legacy intermediate markers (participant_attached, canonical_committing,
// canonical_committed) still resume as post_spawn so in-flight entries advance.
type spawnPhase string

const (
	spawnPhaseIntent          spawnPhase = "prepared"
	spawnPhaseExternalPending spawnPhase = "spawning"
	spawnPhasePostSpawn       spawnPhase = "spawned"
	spawnPhaseCommitted       spawnPhase = "committed"
	spawnPhaseCompensating    spawnPhase = "compensating"
	spawnPhaseChildCancelled  spawnPhase = "child_cancelled"
	spawnPhaseCompensated     spawnPhase = "compensated"
	spawnPhaseUnknownOutcome  spawnPhase = "unknown_outcome"

	// Legacy markers accepted only on read for restart compatibility.
	spawnPhaseLegacyParticipantAttached = "participant_attached"
	spawnPhaseLegacyCanonicalCommitting = "canonical_committing"
	spawnPhaseLegacyCanonicalCommitted  = "canonical_committed"
)

const (
	subagentSpawnUnknownDiagnostic             = "subagent spawn outcome could not be confirmed"
	subagentSpawnCompensationDiagnostic        = "subagent spawn could not be committed"
	subagentSpawnCompensationUnknownDiagnostic = "subagent spawn rollback outcome could not be confirmed"
	subagentSpawnCompensatedDiagnostic         = "subagent spawn was rolled back"
)

// Compatibility aliases used by tests and older call sites.
const (
	spawnStatusPrepared            = string(spawnPhaseIntent)
	spawnStatusSpawning            = string(spawnPhaseExternalPending)
	spawnStatusSpawned             = string(spawnPhasePostSpawn)
	spawnStatusCommitted           = string(spawnPhaseCommitted)
	spawnStatusCompensating        = string(spawnPhaseCompensating)
	spawnStatusChildCancelled      = string(spawnPhaseChildCancelled)
	spawnStatusCompensated         = string(spawnPhaseCompensated)
	spawnStatusUnknownOutcome      = string(spawnPhaseUnknownOutcome)
	spawnStatusParticipantAttached = spawnPhaseLegacyParticipantAttached
	spawnStatusCanonicalCommitting = spawnPhaseLegacyCanonicalCommitting
	spawnStatusCanonicalCommitted  = spawnPhaseLegacyCanonicalCommitted
)

type spawnBeginOutcome struct {
	Entry       *taskapi.Entry
	Snapshot    taskapi.Snapshot
	Terminal    bool // already finished or permanently blocked
	ShouldSpawn bool // caller must perform external Spawn
}

func (tm *taskRuntime) StartSubagent(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runner subagent.Runner,
	req taskapi.SubagentStartRequest,
) (taskapi.Snapshot, error) {
	return tm.startSubagentTarget(ctx, activeSession, ref, runner, delegation.AgentTarget(req.Agent), req, true)
}

func (tm *taskRuntime) StartSubagentTarget(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runner subagent.Runner,
	target delegation.Target,
	req taskapi.SubagentStartRequest,
) (taskapi.Snapshot, error) {
	return tm.startSubagentTarget(ctx, activeSession, ref, runner, target, req, false)
}

func (tm *taskRuntime) startSubagentTarget(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runner subagent.Runner,
	target delegation.Target,
	req taskapi.SubagentStartRequest,
	legacyDigest bool,
) (taskapi.Snapshot, error) {
	if runner == nil {
		return taskapi.Snapshot{}, fmt.Errorf("subagent execution is required")
	}
	taskID, err := subagentSpawnTaskID(ref, req.SpawnID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = strings.TrimSpace(tm.runtime.defaultPolicyMode)
	}
	spawnID := firstNonEmpty(strings.TrimSpace(req.SpawnID), taskID)
	role, err := normalizeSubagentParticipantRole(req.Role)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	target = delegation.NormalizeTarget(target)
	if err := delegation.ValidateTarget(target); err != nil {
		return taskapi.Snapshot{}, err
	}
	if delegationTargetRequiresPlacementRunner(target) {
		if _, ok := runner.(subagent.PlacementRunner); !ok {
			return taskapi.Snapshot{}, fmt.Errorf("subagent execution does not support typed placements")
		}
	}
	requestedHandle, err := agenthandle.CanonicalRequested(req.Handle)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	req.Handle = requestedHandle
	var requestDigest string
	if legacyDigest {
		requestDigest, err = subagentSpawnRequestDigest(req, mode, role)
	} else {
		requestDigest, err = subagentSpawnTargetRequestDigest(target, req, mode, role)
	}
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	outcome, err := tm.beginSubagentSpawn(ctx, activeSession, ref, taskID, spawnID, requestDigest, target, req, mode, role, runner)
	if err != nil || outcome.Terminal {
		return outcome.Snapshot, err
	}
	handle := firstNonEmpty(outcome.Entry.Handle, taskSpecString(outcome.Entry.Spec, "handle"))
	var task *subagentTask
	var initialResult *delegation.Result
	if outcome.ShouldSpawn {
		childPrompt := contextprompt.ComposeTextPrompt(spawnContextFromSpec(outcome.Entry.Spec), strings.TrimSpace(req.Prompt))
		spawnContext := subagent.SpawnContext{
			SessionRef: session.NormalizeSessionRef(ref), Session: session.CloneSession(activeSession), CWD: strings.TrimSpace(activeSession.CWD),
			TaskID: taskID, Handle: handle, Role: role, ParentCallID: strings.TrimSpace(req.ParentCall), Mode: mode, ApprovalMode: strings.TrimSpace(req.ApprovalMode),
			ApprovalRequester: req.Approval, Streams: tm,
			ActivityObserver: newSubagentActivityObserver(tm, taskID),
			Completion:       newSubagentCompletionSink(ctx, tm, taskID, 1),
		}
		anchor, result, err := spawnSubagentTarget(ctx, runner, spawnContext, target, childPrompt)
		if err != nil {
			outcome.Entry.State = taskapi.StateUnknownOutcome
			outcome.Entry.Running = false
			normalizeSubagentEntryResult(outcome.Entry, subagentSpawnUnknownDiagnostic)
			setSpawnEntryPhase(outcome.Entry, spawnPhaseUnknownOutcome, subagentSpawnUnknownDiagnostic)
			_ = tm.persistSpawnEntry(context.WithoutCancel(ctx), outcome.Entry)
			return taskapi.Snapshot{}, err
		}
		anchor = delegation.CloneAnchor(anchor)
		result = delegation.CloneResult(result)
		if anchor.TaskID == "" {
			anchor.TaskID = taskID
		}
		if result.TaskID == "" {
			result.TaskID = taskID
		}
		// Validate before any durable post_spawn commit so a crash cannot
		// roll-forward an invalid anchor (empty AgentID, mismatched agent, etc.).
		if validationErr := validateSubagentSpawnResult(taskID, anchor, result); validationErr != nil {
			task = newSubagentTaskFromSpawn(ref, taskID, spawnID, requestDigest, target, req, mode, role, handle, runner, anchor, result, outcome.Entry.Revision, tm.runtime.now(), spawnPhaseExternalPending)
			return taskapi.Snapshot{}, tm.compensateSubagentSpawn(ctx, task, validationErr)
		}
		task = newSubagentTaskFromSpawn(ref, taskID, spawnID, requestDigest, target, req, mode, role, handle, runner, anchor, result, outcome.Entry.Revision, tm.runtime.now(), spawnPhasePostSpawn)
		// Persist a compatibility Final cursor before the post_spawn crash
		// boundary. If real ACP frames raced Spawn, installation below replaces
		// this fallback before exposing the live Task stream.
		task.seedStreamFromResult(result)
		initialResult = &result
		spawnedEntry := task.entrySnapshot(tm.runtime.now())
		if err := tm.persistSpawnEntry(ctx, spawnedEntry); err != nil {
			return taskapi.Snapshot{}, tm.compensateSubagentSpawn(ctx, task, err)
		}
		task.revision = spawnedEntry.Revision
	} else {
		task = tm.rehydrateSubagentTask(outcome.Entry)
		task.runner = runner
	}
	// Hold the task's stream-order lock while making it discoverable and
	// applying earlier pending frames. A concurrent publisher that resolves the
	// newly installed task must not apply a later frame first.
	task.streamMu.Lock()
	tm.mu.Lock()
	tm.subagents[taskID] = task
	pending := append([]stream.Frame(nil), tm.pending[taskID]...)
	delete(tm.pending, taskID)
	tm.order[strings.TrimSpace(ref.SessionID)] = append(tm.order[strings.TrimSpace(ref.SessionID)], taskID)
	tm.mu.Unlock()
	if initialResult != nil && subagentFramesContainStructuredAssistantText(pending) {
		task.mu.Lock()
		task.discardInitialResultStreamFallbackLocked()
		task.mu.Unlock()
	}
	task.applyStreamFramesLocked(pending)
	if initialResult != nil {
		task.mu.Lock()
		// The result is only a compatibility fallback. Earlier ACP frames may
		// have arrived before Task installation, so apply them first and avoid
		// manufacturing a second full Final beside the real delta chain.
		task.seedStreamFromResult(*initialResult)
		if task.state == taskapi.StateCompleted && taskOutputHasNonBlankLine(initialResult.Result) {
			// applyResult ran before pending ACP delivery. Re-protect the exact
			// Final now so semantic current-state replay does not retain both the
			// real assistant delta and the result fallback for this Turn.
			task.retainCompletedFinalLocked(initialResult.Result)
		}
		task.mu.Unlock()
	}
	task.streamMu.Unlock()
	snapshot, err := tm.advanceSubagentSpawn(ctx, activeSession, task, strings.TrimSpace(req.ParentCall), strings.TrimSpace(req.Prompt))
	if err != nil {
		return snapshot, err
	}
	task.mu.Lock()
	task.completionReady = true
	task.mu.Unlock()
	tm.kickSubagentCompletion(taskID)
	return snapshot, nil
}

func (tm *taskRuntime) beginSubagentSpawn(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	taskID string,
	spawnID string,
	requestDigest string,
	target delegation.Target,
	req taskapi.SubagentStartRequest,
	mode string,
	role session.ParticipantRole,
	runner subagent.Runner,
) (spawnBeginOutcome, error) {
	if tm == nil || tm.store == nil {
		return spawnBeginOutcome{}, errors.New("durable Task store is required before subagent spawn")
	}
	if _, ok := tm.store.(taskapi.CASStore); !ok {
		return spawnBeginOutcome{}, errors.New("subagent spawn requires task.CASStore")
	}
	if existing, err := tm.store.Get(ctx, taskID); err == nil && existing != nil {
		if taskSpecString(existing.Spec, "spawn_identity") != strings.TrimSpace(spawnID) ||
			taskSpecString(existing.Spec, "spawn_request_digest") != strings.TrimSpace(requestDigest) {
			return spawnBeginOutcome{}, fmt.Errorf("spawn identity %q conflicts with durable intent", spawnID)
		}
		return tm.resumeExistingSpawn(ctx, existing, spawnID, runner)
	}
	handle, err := tm.reserveTaskHandleValue(ctx, activeSession, ref, taskapi.KindSubagent, target.Selector, strings.TrimSpace(req.Handle))
	if err != nil {
		return spawnBeginOutcome{}, err
	}

	now := tm.runtime.now()
	entry := &taskapi.Entry{
		TaskID: taskID, Handle: handle, Kind: taskapi.KindSubagent, Session: session.NormalizeSessionRef(ref),
		Title: "Spawn " + target.Selector, State: taskapi.StatePrepared, CreatedAt: now, UpdatedAt: now,
		SupportsCancel: true,
		Spec: map[string]any{
			"spawn_identity": strings.TrimSpace(spawnID), "spawn_request_digest": strings.TrimSpace(requestDigest),
			"target":              target,
			"prompt":              strings.TrimSpace(req.Prompt),
			"include_context":     req.IncludeContext,
			"context_unsupported": req.ContextUnsupported,
			"context":             agent.CloneContextTransfer(req.Context),
			"mode":                strings.TrimSpace(mode),
			"approval_mode":       strings.TrimSpace(req.ApprovalMode), "parent_call": strings.TrimSpace(req.ParentCall),
			"participant_role": string(role),
			"handle":           strings.TrimSpace(handle), "terminal_id": subagentTerminalID(taskID), "turn_seq": int64(1),
			"spawn_phase": string(spawnPhaseIntent),
		},
		Metadata: map[string]any{
			"spawn_status": string(spawnPhaseIntent), "spawn_identity": strings.TrimSpace(spawnID),
			"spawn_request_digest": strings.TrimSpace(requestDigest),
			"include_context":      req.IncludeContext, "context_unsupported": req.ContextUnsupported,
		},
	}
	if err := tm.persistSpawnEntry(ctx, entry); err != nil {
		if !session.IsCommitted(err) {
			tm.forgetTaskHandle(ref.SessionID, handle)
		}
		return spawnBeginOutcome{}, err
	}
	// Claim the external-effect boundary in one additional CAS write. A crash
	// between intent and claim leaves prepared, which is safe to re-claim.
	outcome, err := tm.claimSpawnExternalEffect(ctx, entry, spawnID)
	if err != nil {
		return outcome, err
	}
	return outcome, nil
}

func (tm *taskRuntime) resumeExistingSpawn(ctx context.Context, existing *taskapi.Entry, spawnID string, runner subagent.Runner) (spawnBeginOutcome, error) {
	phase := spawnPhaseOf(existing)
	switch phase {
	case spawnPhaseCommitted:
		task := tm.rehydrateSubagentTask(existing)
		tm.rememberRehydratedSubagent(task)
		return spawnBeginOutcome{Entry: existing, Snapshot: task.snapshot(), Terminal: true}, nil
	case spawnPhaseIntent:
		outcome, err := tm.claimSpawnExternalEffect(ctx, existing, spawnID)
		if err != nil {
			return outcome, err
		}
		return outcome, nil
	case spawnPhasePostSpawn:
		task := tm.rehydrateSubagentTask(existing)
		task.runner = runner
		tm.rememberRehydratedSubagent(task)
		return spawnBeginOutcome{Entry: existing, Snapshot: task.snapshot()}, nil
	case spawnPhaseCompensating, spawnPhaseChildCancelled:
		task := tm.rehydrateSubagentTask(existing)
		task.runner = runner
		tm.rememberRehydratedSubagent(task)
		return spawnBeginOutcome{Entry: existing, Snapshot: task.snapshot()}, nil
	case spawnPhaseExternalPending:
		recovered := taskapi.CloneEntry(existing)
		recovered.State = taskapi.StateUnknownOutcome
		recovered.Running = false
		normalizeSubagentEntryResult(recovered, subagentSpawnUnknownDiagnostic)
		setSpawnEntryPhase(recovered, spawnPhaseUnknownOutcome, subagentSpawnUnknownDiagnostic)
		persistErr := tm.persistSpawnEntry(context.WithoutCancel(ctx), recovered)
		return spawnBeginOutcome{Entry: recovered, Snapshot: snapshotFromTaskEntry(recovered), Terminal: true}, errors.Join(
			fmt.Errorf("subagent spawn %q crossed the external effect boundary; refusing blind respawn and recording unknown outcome", spawnID),
			persistErr,
		)
	case spawnPhaseUnknownOutcome:
		return spawnBeginOutcome{Entry: existing, Snapshot: snapshotFromTaskEntry(existing), Terminal: true},
			fmt.Errorf("subagent spawn %q has unknown outcome; refusing blind respawn", spawnID)
	case spawnPhaseCompensated:
		return spawnBeginOutcome{Entry: existing, Snapshot: snapshotFromTaskEntry(existing), Terminal: true},
			fmt.Errorf("subagent spawn %q was compensated", spawnID)
	default:
		return spawnBeginOutcome{Entry: existing, Snapshot: snapshotFromTaskEntry(existing), Terminal: true},
			fmt.Errorf("subagent spawn %q has invalid durable status %q", spawnID, phase)
	}
}

func (tm *taskRuntime) claimSpawnExternalEffect(ctx context.Context, entry *taskapi.Entry, spawnID string) (spawnBeginOutcome, error) {
	claimed := taskapi.CloneEntry(entry)
	setSpawnEntryPhase(claimed, spawnPhaseExternalPending, "")
	if err := tm.persistSpawnEntry(ctx, claimed); err != nil {
		var conflict *taskapi.RevisionConflictError
		if errors.As(err, &conflict) {
			return spawnBeginOutcome{Entry: entry, Snapshot: snapshotFromTaskEntry(entry), Terminal: true},
				fmt.Errorf("subagent spawn %q was claimed concurrently: %w", spawnID, err)
		}
		return spawnBeginOutcome{}, err
	}
	return spawnBeginOutcome{Entry: claimed, ShouldSpawn: true}, nil
}

func subagentSpawnRequestDigest(req taskapi.SubagentStartRequest, mode string, role session.ParticipantRole) (string, error) {
	payload := struct {
		Agent        string                  `json:"agent"`
		Prompt       string                  `json:"prompt"`
		Handle       string                  `json:"handle,omitempty"`
		Context      agent.ContextTransfer   `json:"context"`
		Mode         string                  `json:"mode"`
		ApprovalMode string                  `json:"approval_mode"`
		ParentCall   string                  `json:"parent_call"`
		Role         session.ParticipantRole `json:"role"`
	}{
		Agent: strings.TrimSpace(req.Agent), Prompt: strings.TrimSpace(req.Prompt),
		Handle: strings.TrimSpace(req.Handle), Context: agent.CloneContextTransfer(req.Context), Mode: strings.TrimSpace(mode),
		ApprovalMode: strings.TrimSpace(req.ApprovalMode), ParentCall: strings.TrimSpace(req.ParentCall), Role: role,
	}
	return hashSubagentSpawnPayload(payload)
}

func subagentSpawnTargetRequestDigest(target delegation.Target, req taskapi.SubagentStartRequest, mode string, role session.ParticipantRole) (string, error) {
	payload := struct {
		Target         delegation.Target       `json:"target"`
		Prompt         string                  `json:"prompt"`
		Handle         string                  `json:"handle,omitempty"`
		IncludeContext bool                    `json:"include_context"`
		Mode           string                  `json:"mode"`
		ApprovalMode   string                  `json:"approval_mode"`
		ParentCall     string                  `json:"parent_call"`
		Role           session.ParticipantRole `json:"role"`
	}{
		Target: delegation.NormalizeTarget(target), Prompt: strings.TrimSpace(req.Prompt),
		Handle: strings.TrimSpace(req.Handle), IncludeContext: req.IncludeContext, Mode: strings.TrimSpace(mode),
		ApprovalMode: strings.TrimSpace(req.ApprovalMode), ParentCall: strings.TrimSpace(req.ParentCall), Role: role,
	}
	return hashSubagentSpawnPayload(payload)
}

func spawnContextFromSpec(spec map[string]any) agent.ContextTransfer {
	if spec == nil {
		return agent.ContextTransfer{}
	}
	switch typed := spec["context"].(type) {
	case agent.ContextTransfer:
		return agent.CloneContextTransfer(typed)
	case map[string]any:
		raw, err := json.Marshal(typed)
		if err != nil {
			return agent.ContextTransfer{}
		}
		var transfer agent.ContextTransfer
		if err := json.Unmarshal(raw, &transfer); err != nil {
			return agent.ContextTransfer{}
		}
		return agent.CloneContextTransfer(transfer)
	default:
		return agent.ContextTransfer{}
	}
}

func hashSubagentSpawnPayload(payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode subagent spawn identity: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func newSubagentTaskFromSpawn(
	ref session.SessionRef,
	taskID string,
	spawnID string,
	requestDigest string,
	target delegation.Target,
	req taskapi.SubagentStartRequest,
	mode string,
	role session.ParticipantRole,
	handle string,
	runner subagent.Runner,
	anchor delegation.Anchor,
	result delegation.Result,
	revision uint64,
	now time.Time,
	phase spawnPhase,
) *subagentTask {
	target = delegation.NormalizeTarget(target)
	agentName := strings.TrimSpace(target.Selector)
	task := &subagentTask{
		ref:        taskapi.Ref{TaskID: taskID, SessionID: strings.TrimSpace(anchor.SessionID), TerminalID: subagentTerminalID(taskID)},
		sessionRef: session.NormalizeSessionRef(ref), anchor: delegation.CloneAnchor(anchor), runner: runner,
		agent: agentName, target: target, handle: handle, title: spawn.ToolName + " " + agentName,
		prompt: strings.TrimSpace(req.Prompt), mode: strings.TrimSpace(mode),
		approvalMode: strings.TrimSpace(req.ApprovalMode), createdAt: now, revision: revision,
		state: taskStateFromDelegation(result.State), running: result.State == delegation.StateRunning, turnSeq: 1,
		metadata: map[string]any{
			"source": firstNonEmpty(strings.TrimSpace(req.Source), "agent_spawn"), "participant_role": string(role),
			"spawn_status": string(phase), "spawn_identity": spawnID, "spawn_request_digest": requestDigest,
			"parent_call": strings.TrimSpace(req.ParentCall), "parent_tool": spawn.ToolName,
			"include_context": req.IncludeContext, "context_unsupported": req.ContextUnsupported,
		},
	}
	task.applyResult(result)
	return task
}

func delegationTargetRequiresPlacementRunner(target delegation.Target) bool {
	target = delegation.NormalizeTarget(target)
	return target.Placement.Kind == delegation.PlacementModel ||
		target.Placement.ConfigFingerprint != "" || target.Placement.Fingerprint != ""
}

func spawnSubagentTarget(
	ctx context.Context,
	runner subagent.Runner,
	spawnContext subagent.SpawnContext,
	target delegation.Target,
	prompt string,
) (delegation.Anchor, delegation.Result, error) {
	if placed, ok := runner.(subagent.PlacementRunner); ok {
		return placed.SpawnTarget(ctx, spawnContext, delegation.TargetRequest{Target: target, Prompt: prompt})
	}
	return runner.Spawn(ctx, spawnContext, delegation.Request{Agent: strings.TrimSpace(target.Placement.Agent), Prompt: prompt})
}

func validateSubagentSpawnResult(taskID string, anchor delegation.Anchor, result delegation.Result) error {
	if strings.TrimSpace(anchor.SessionID) == "" {
		return errors.New("spawned subagent anchor requires session_id")
	}
	if strings.TrimSpace(anchor.AgentID) == "" {
		return errors.New("spawned subagent anchor requires agent_id")
	}
	if anchorTaskID := strings.TrimSpace(anchor.TaskID); anchorTaskID != "" && anchorTaskID != strings.TrimSpace(taskID) {
		return fmt.Errorf("spawned subagent anchor task_id %q does not match %q", anchorTaskID, taskID)
	}
	if resultTaskID := strings.TrimSpace(result.TaskID); resultTaskID != "" && resultTaskID != strings.TrimSpace(taskID) {
		return fmt.Errorf("spawned subagent result task_id %q does not match %q", resultTaskID, taskID)
	}
	switch result.State {
	case delegation.StateRunning, delegation.StateCompleted, delegation.StateFailed, delegation.StateCancelled,
		delegation.StateInterrupted, delegation.StateUnknownOutcome, delegation.StateWaitingApproval:
		return nil
	default:
		return fmt.Errorf("spawned subagent result has invalid state %q", result.State)
	}
}

func (tm *taskRuntime) rememberRehydratedSubagent(task *subagentTask) {
	if tm == nil || task == nil {
		return
	}
	tm.mu.Lock()
	tm.subagents[task.ref.TaskID] = task
	tm.rememberTaskHandleLocked(task.sessionRef.SessionID, task.handle)
	tm.mu.Unlock()
}

func spawnPhaseOf(entry *taskapi.Entry) spawnPhase {
	if entry == nil {
		return ""
	}
	raw := taskStringValue(entry.Metadata["spawn_status"])
	if raw == "" {
		raw = taskSpecString(entry.Spec, "spawn_phase")
	}
	return normalizeSpawnPhase(raw)
}

func spawnPhaseOfTask(task *subagentTask) spawnPhase {
	if task == nil {
		return ""
	}
	return normalizeSpawnPhase(taskStringValue(task.metadata["spawn_status"]))
}

func normalizeSpawnPhase(raw string) spawnPhase {
	switch phase := spawnPhase(strings.TrimSpace(raw)); phase {
	case spawnPhaseIntent, spawnPhaseExternalPending, spawnPhasePostSpawn, spawnPhaseCommitted,
		spawnPhaseCompensating, spawnPhaseChildCancelled, spawnPhaseCompensated, spawnPhaseUnknownOutcome:
		return phase
	case spawnPhaseLegacyParticipantAttached, spawnPhaseLegacyCanonicalCommitting, spawnPhaseLegacyCanonicalCommitted:
		// Legacy pure-marker phases collapse to post_spawn for resume.
		return spawnPhasePostSpawn
	default:
		return phase
	}
}

func setSpawnEntryPhase(entry *taskapi.Entry, phase spawnPhase, reason string) {
	if entry == nil {
		return
	}
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	if entry.Spec == nil {
		entry.Spec = map[string]any{}
	}
	entry.Metadata["spawn_status"] = string(phase)
	entry.Spec["spawn_phase"] = string(phase)
	if strings.TrimSpace(reason) != "" {
		entry.Metadata["spawn_reason"] = strings.TrimSpace(reason)
	}
}

// setSpawnEntryStatus is retained for older call sites/tests.
func setSpawnEntryStatus(entry *taskapi.Entry, status string, reason string) {
	setSpawnEntryPhase(entry, normalizeSpawnPhase(status), reason)
}

func snapshotFromTaskEntry(entry *taskapi.Entry) taskapi.Snapshot {
	if entry == nil {
		return taskapi.Snapshot{}
	}
	result := session.CloneState(entry.Result)
	normalizeSubagentResultForState(&result, entry.State, entry.FailureDiagnostic)
	return taskapi.Snapshot{
		Ref:      taskapi.Ref{TaskID: entry.TaskID, SessionID: taskSpecString(entry.Spec, "session_id"), TerminalID: taskSpecString(entry.Spec, "terminal_id")},
		Handle:   firstNonEmpty(entry.Handle, taskSpecString(entry.Spec, "handle"), taskStringValue(entry.Metadata["handle"])),
		Revision: entry.Revision, Kind: entry.Kind, Title: entry.Title, State: entry.State, Running: entry.Running,
		SupportsInput: entry.SupportsInput, SupportsCancel: entry.SupportsCancel, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
		Result: result, Metadata: session.CloneState(entry.Metadata),
	}
}

func (tm *taskRuntime) markSubagentSpawnPhase(ctx context.Context, task *subagentTask, phase spawnPhase, reason string) error {
	if task == nil {
		return nil
	}
	task.mu.Lock()
	if task.metadata == nil {
		task.metadata = map[string]any{}
	}
	task.metadata["spawn_status"] = string(phase)
	if reason != "" {
		task.metadata["spawn_reason"] = strings.TrimSpace(reason)
	}
	entry := task.entrySnapshot(tm.runtime.now())
	if entry.Spec == nil {
		entry.Spec = map[string]any{}
	}
	entry.Spec["spawn_phase"] = string(phase)
	task.mu.Unlock()
	err := tm.persistSpawnEntry(ctx, entry)
	if err == nil {
		task.mu.Lock()
		task.revision = entry.Revision
		task.mu.Unlock()
	}
	return err
}

func (tm *taskRuntime) markSubagentSpawnStatus(ctx context.Context, task *subagentTask, status string, reason string) error {
	return tm.markSubagentSpawnPhase(ctx, task, normalizeSpawnPhase(status), reason)
}

func (tm *taskRuntime) advanceSubagentSpawn(
	ctx context.Context,
	activeSession session.Session,
	task *subagentTask,
	parentCall string,
	prompt string,
) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, errors.New("subagent spawn Task is required")
	}
	phase := spawnPhaseOfTask(task)
	if phase == spawnPhaseCompensating || phase == spawnPhaseChildCancelled {
		cause := errors.New(firstNonEmpty(taskStringValue(task.metadata["spawn_reason"]), "subagent spawn compensation resumed"))
		return taskapi.Snapshot{}, tm.resumeSubagentSpawnCompensation(ctx, task, cause)
	}
	if phase == spawnPhaseCommitted {
		return task.snapshot(), nil
	}
	if phase != spawnPhasePostSpawn {
		return taskapi.Snapshot{}, fmt.Errorf("cannot advance subagent spawn from phase %q", phase)
	}
	// Single post_spawn roll-forward: attach (idempotent) + canonical dialogue
	// (idempotent keys) + one committed mark. No pure marker phases.
	if err := tm.ensureSubagentParticipantAttached(ctx, activeSession, task, parentCall); err != nil {
		return taskapi.Snapshot{}, tm.compensateSubagentSpawn(ctx, task, err)
	}
	if err := tm.appendSideSubagentUserEvent(ctx, task, strings.TrimSpace(prompt)); err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.appendSideSubagentFinalEvent(ctx, task); err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.markSubagentSpawnPhase(context.WithoutCancel(ctx), task, spawnPhaseCommitted, ""); err != nil {
		return taskapi.Snapshot{}, err
	}
	return task.snapshot(), nil
}

func (tm *taskRuntime) ensureSubagentParticipantAttached(ctx context.Context, activeSession session.Session, task *subagentTask, parentCall string) error {
	current, err := tm.runtime.sessions.Session(ctx, task.sessionRef)
	if err != nil {
		return err
	}
	if binding, ok := participantBinding(current, task.anchor.AgentID); ok {
		if strings.TrimSpace(binding.DelegationID) == strings.TrimSpace(task.ref.TaskID) {
			return nil
		}
		return &session.ParticipantBindingConflictError{
			ParticipantID: task.anchor.AgentID, ExpectedDelegation: task.ref.TaskID, ActualDelegation: binding.DelegationID,
		}
	}
	err = tm.attachSubagentParticipant(ctx, activeSession, task, strings.TrimSpace(parentCall))
	if err == nil || !session.IsCommitted(err) {
		return err
	}
	reloaded, loadErr := tm.runtime.sessions.Session(context.WithoutCancel(ctx), task.sessionRef)
	if loadErr != nil {
		return errors.Join(err, loadErr)
	}
	binding, ok := participantBinding(reloaded, task.anchor.AgentID)
	if !ok || strings.TrimSpace(binding.DelegationID) != strings.TrimSpace(task.ref.TaskID) {
		return err
	}
	return nil
}

func (tm *taskRuntime) compensateSubagentSpawn(ctx context.Context, task *subagentTask, cause error) error {
	if task == nil {
		return cause
	}
	if err := tm.markSubagentSpawnPhase(context.WithoutCancel(ctx), task, spawnPhaseCompensating, subagentSpawnCompensationDiagnostic); err != nil {
		return errors.Join(cause, err)
	}
	return tm.resumeSubagentSpawnCompensation(ctx, task, cause)
}

func (tm *taskRuntime) resumeSubagentSpawnCompensation(ctx context.Context, task *subagentTask, cause error) error {
	phase := spawnPhaseOfTask(task)
	if phase == spawnPhaseCompensating {
		cancelErr := task.runner.Cancel(context.WithoutCancel(ctx), delegation.CloneAnchor(task.anchor))
		if cancelErr != nil {
			task.mu.Lock()
			task.running = false
			task.state = taskapi.StateUnknownOutcome
			normalizeSubagentResultForState(&task.result, taskapi.StateUnknownOutcome, subagentSpawnCompensationUnknownDiagnostic)
			task.notifyStreamChangeLocked()
			task.mu.Unlock()
			persistErr := tm.markSubagentSpawnPhase(context.WithoutCancel(ctx), task, spawnPhaseUnknownOutcome, subagentSpawnCompensationUnknownDiagnostic)
			return errors.Join(cause, cancelErr, persistErr)
		}
		task.mu.Lock()
		task.running = false
		task.state = taskapi.StateCancelled
		if task.result == nil {
			task.result = map[string]any{}
		}
		normalizeSubagentResultForState(&task.result, taskapi.StateCancelled, "")
		task.notifyStreamChangeLocked()
		task.mu.Unlock()
		if err := tm.markSubagentSpawnPhase(context.WithoutCancel(ctx), task, spawnPhaseChildCancelled, subagentSpawnCompensatedDiagnostic); err != nil {
			return errors.Join(cause, err)
		}
		phase = spawnPhaseChildCancelled
	}
	if phase == spawnPhaseChildCancelled {
		if err := tm.detachSubagentParticipant(context.WithoutCancel(ctx), task); err != nil {
			return errors.Join(cause, err)
		}
		if err := tm.markSubagentSpawnPhase(context.WithoutCancel(ctx), task, spawnPhaseCompensated, subagentSpawnCompensatedDiagnostic); err != nil {
			return errors.Join(cause, err)
		}
		// Producer completion may race the failed attachment after the child was
		// made discoverable. Cancellation is now the durable authority, so release
		// any queued producer and reject later completion for this Spawn attempt.
		tm.discardSubagentCompletion(task.ref.TaskID)
		return fmt.Errorf("subagent spawn %q was compensated: %w", taskStringValue(task.metadata["spawn_identity"]), cause)
	}
	return errors.Join(cause, fmt.Errorf("cannot resume compensation from phase %q", phase))
}

func (tm *taskRuntime) detachSubagentParticipant(ctx context.Context, task *subagentTask) error {
	tm.runtime.participantMu.Lock()
	defer tm.runtime.participantMu.Unlock()
	lifecycle, ok := tm.runtime.sessions.(session.ParticipantLifecycleService)
	if !ok {
		return errors.New("participant lifecycle store does not support atomic compensation")
	}
	active, err := tm.runtime.sessions.Session(ctx, task.sessionRef)
	if err != nil {
		return err
	}
	binding, ok := participantBinding(active, task.anchor.AgentID)
	if !ok {
		return nil
	}
	if strings.TrimSpace(binding.DelegationID) != strings.TrimSpace(task.ref.TaskID) {
		// This task has no attachment to remove. A different delegation now owns
		// the colliding participant ID, so compensation must leave it untouched
		// and may safely advance to terminal compensated.
		return nil
	}
	event := participantLifecycleEvent(active, binding, "detached", tm.runtime.now())
	_, _, err = lifecycle.RemoveParticipantWithEvent(ctx, session.RemoveParticipantWithEventRequest{
		SessionRef: task.sessionRef, ExpectedRevision: &active.Revision, MutationGuard: session.RuntimeMutationGuard(ctx),
		ParticipantID: binding.ID, ExpectedDelegationID: stringPointer(task.ref.TaskID), Event: event,
	})
	if err != nil {
		var conflict *session.ParticipantBindingConflictError
		if errors.As(err, &conflict) {
			reloaded, loadErr := tm.runtime.sessions.Session(context.WithoutCancel(ctx), task.sessionRef)
			if loadErr == nil {
				current, exists := participantBinding(reloaded, task.anchor.AgentID)
				if !exists || strings.TrimSpace(current.DelegationID) != strings.TrimSpace(task.ref.TaskID) {
					return nil
				}
			}
			return errors.Join(err, loadErr)
		}
	}
	return err
}

func stringPointer(value string) *string {
	value = strings.TrimSpace(value)
	return &value
}

func (tm *taskRuntime) appendSubagentSagaEvent(ctx context.Context, ref session.SessionRef, event *session.Event) error {
	req := session.AppendEventRequest{SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), Event: event}
	_, err := tm.runtime.sessions.AppendEvent(ctx, req)
	if err == nil || !session.IsCommitted(err) {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), subagentSagaRecoveryTimeout)
	defer cancel()
	req.MutationGuard = session.RuntimeMutationGuard(recoveryCtx)
	_, retryErr := tm.runtime.sessions.AppendEvent(recoveryCtx, req)
	if retryErr != nil {
		return errors.Join(err, retryErr)
	}
	return nil
}
