package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

func (tm *taskRuntime) attachSubagentParticipant(ctx context.Context, activeSession session.Session, task *subagentTask, parentCall string) error {
	if tm == nil || tm.runtime == nil || tm.runtime.sessions == nil || task == nil {
		return nil
	}
	// Concurrent Spawn calls may finish their external effects together. Keep
	// only the revision-checked participant binding commit serial so each caller
	// reloads the latest Session revision without serializing child startup. The
	// reload and guarded commit intentionally share this lock; moving either side
	// out would let sibling attachments submit the same stale revision.
	tm.runtime.participantMu.Lock()
	defer tm.runtime.participantMu.Unlock()
	handle := strings.TrimSpace(task.handle)
	if handle == "" {
		var err error
		handle, err = tm.reserveTaskHandle(ctx, activeSession, task.sessionRef, taskapi.KindSubagent, task.agent)
		if err != nil {
			return err
		}
		task.handle = handle
	}
	mention := "@" + strings.TrimPrefix(handle, "@")
	role := subagentParticipantRole(task)
	lifecycle, ok := tm.runtime.sessions.(session.ParticipantLifecycleService)
	if !ok {
		return fmt.Errorf("agent-sdk/runtime: participant lifecycle store does not support atomic subagent attachment")
	}
	current, err := tm.runtime.sessions.Session(ctx, task.sessionRef)
	if err != nil {
		return err
	}
	binding := session.ParticipantBinding{
		ID:            strings.TrimSpace(task.anchor.AgentID),
		Kind:          session.ParticipantKindSubagent,
		Role:          role,
		AgentName:     strings.TrimSpace(task.agent),
		Label:         mention,
		Placement:     delegationPlacement(task),
		SessionID:     strings.TrimSpace(task.anchor.SessionID),
		Source:        firstNonEmpty(strings.TrimSpace(taskStringValue(task.metadata["source"])), "agent_spawn"),
		ParentTurnID:  strings.TrimSpace(parentCall),
		DelegationID:  strings.TrimSpace(task.ref.TaskID),
		AttachedAt:    tm.runtime.now(),
		ControllerRef: strings.TrimSpace(current.Controller.EpochID),
	}
	event := &session.Event{
		IdempotencyKey: "subagent-participant:" + task.ref.TaskID + ":attached",
		Type:           session.EventTypeParticipant,
		Visibility:     session.VisibilityMirror,
		Time:           tm.runtime.now(),
		Actor: session.ActorRef{
			Kind: session.ActorKindSystem,
			ID:   "spawn",
			Name: "spawn",
		},
		Protocol: ptrEventProtocol(session.NewParticipantProtocol(session.ProtocolParticipant{Action: "attached"})),
		Scope: &session.EventScope{
			Participant: session.ParticipantRef{
				ID:           strings.TrimSpace(task.anchor.AgentID),
				Kind:         session.ParticipantKindSubagent,
				Role:         role,
				DelegationID: strings.TrimSpace(task.ref.TaskID),
			},
		},
		Meta: map[string]any{
			"task_id":          task.ref.TaskID,
			"agent":            task.agent,
			"agent_id":         task.anchor.AgentID,
			"handle":           handle,
			"mention":          mention,
			"profile_id":       task.target.Placement.ProfileID,
			"reasoning_effort": task.target.Placement.ReasoningEffort,
			"session_id":       task.anchor.SessionID,
			"state":            string(task.state),
		},
	}
	_, _, err = lifecycle.PutParticipantWithEvent(ctx, session.PutParticipantWithEventRequest{
		SessionRef: task.sessionRef, ExpectedRevision: &current.Revision, MutationGuard: session.RuntimeMutationGuard(ctx),
		ExpectedDelegationID: stringPointer(task.ref.TaskID), Binding: binding, Event: event,
	})
	return err
}

func delegationPlacement(task *subagentTask) placement.Placement {
	if task == nil {
		return placement.Placement{}
	}
	return placement.Normalize(task.target.Placement)
}

func (tm *taskRuntime) updateSubagentParticipant(ctx context.Context, task *subagentTask, action string) error {
	if tm == nil || tm.runtime == nil || tm.runtime.sessions == nil || task == nil {
		return nil
	}
	role := subagentParticipantRole(task)
	err := tm.appendSubagentSagaEvent(ctx, task.sessionRef, &session.Event{
		IdempotencyKey: fmt.Sprintf("subagent-participant:%s:%d:%s", task.ref.TaskID, task.turnSeq, strings.TrimSpace(action)),
		Type:           session.EventTypeParticipant,
		Visibility:     session.VisibilityUIOnly,
		Time:           tm.runtime.now(),
		Actor: session.ActorRef{
			Kind: session.ActorKindSystem,
			ID:   "spawn",
			Name: "spawn",
		},
		Protocol: ptrEventProtocol(session.NewParticipantProtocol(session.ProtocolParticipant{Action: action})),
		Scope: &session.EventScope{
			Participant: session.ParticipantRef{
				ID:           strings.TrimSpace(task.anchor.AgentID),
				Kind:         session.ParticipantKindSubagent,
				Role:         role,
				DelegationID: strings.TrimSpace(task.ref.TaskID),
			},
		},
		Meta: map[string]any{
			"task_id":          task.ref.TaskID,
			"agent":            task.agent,
			"agent_id":         task.anchor.AgentID,
			"handle":           task.handle,
			"mention":          "@" + strings.TrimPrefix(task.handle, "@"),
			"profile_id":       task.target.Placement.ProfileID,
			"reasoning_effort": task.target.Placement.ReasoningEffort,
			"session_id":       task.anchor.SessionID,
			"state":            string(task.state),
			"output_preview":   taskRawStringValue(task.result["output_preview"]),
		},
	})
	return err
}

func (tm *taskRuntime) appendSideSubagentUserEvent(ctx context.Context, task *subagentTask, prompt string) error {
	if tm == nil || tm.runtime == nil || tm.runtime.sessions == nil || task == nil || !isSideSubagentTask(task) {
		return nil
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil
	}
	role := subagentParticipantRole(task)
	message := model.NewTextMessage(model.RoleUser, prompt)
	err := tm.appendSubagentSagaEvent(ctx, task.sessionRef, &session.Event{
		IdempotencyKey: fmt.Sprintf("subagent-dialogue:%s:%d:user", task.ref.TaskID, task.turnSeq),
		Type:           session.EventTypeUser,
		Visibility:     session.VisibilityCanonical,
		Time:           tm.runtime.now(),
		Actor:          session.ActorRef{Kind: session.ActorKindUser, Name: "user"},
		Scope: &session.EventScope{
			TurnID: subagentTurnID(task.ref.TaskID, task.turnSeq),
			Source: firstNonEmpty(taskStringValue(task.metadata["source"]), "subagent_sidecar"),
			Executor: session.ParticipantExecutor(session.ParticipantBinding{
				ID: task.anchor.AgentID, Kind: session.ParticipantKindSubagent, Role: role,
				AgentName: task.agent, Label: "@" + strings.TrimPrefix(strings.TrimSpace(task.handle), "@"),
			}),
			Participant: session.ParticipantRef{
				ID:           strings.TrimSpace(task.anchor.AgentID),
				Kind:         session.ParticipantKindSubagent,
				Role:         role,
				DelegationID: strings.TrimSpace(task.ref.TaskID),
			},
		},
		Message: &message,
		Text:    prompt,
		Meta: map[string]any{
			"handle":  strings.TrimSpace(task.handle),
			"mention": "@" + strings.TrimPrefix(strings.TrimSpace(task.handle), "@"),
			"agent":   strings.TrimSpace(task.agent),
		},
	})
	return err
}

type sideSubagentFinalCommitMode uint8

const (
	sideSubagentFinalCommitPublished sideSubagentFinalCommitMode = iota
	sideSubagentFinalCommitStaged
	sideSubagentFinalCommitDeferredMarker
)

func (tm *taskRuntime) appendSideSubagentFinalEvent(ctx context.Context, task *subagentTask) error {
	return tm.appendSideSubagentFinalEventMode(ctx, task, sideSubagentFinalCommitPublished)
}

func (tm *taskRuntime) appendStagedSideSubagentFinalEvent(ctx context.Context, task *subagentTask) error {
	return tm.appendSideSubagentFinalEventMode(ctx, task, sideSubagentFinalCommitStaged)
}

// appendCompletionSideSubagentFinalEvent commits the Session-owned dialogue and
// checkpoint while leaving the Task completion intent Running. The final Task
// CAS carries final_event_persisted together with the terminal lifecycle.
func (tm *taskRuntime) appendCompletionSideSubagentFinalEvent(ctx context.Context, task *subagentTask) error {
	return tm.appendSideSubagentFinalEventMode(ctx, task, sideSubagentFinalCommitDeferredMarker)
}

func (tm *taskRuntime) appendSideSubagentFinalEventMode(
	ctx context.Context,
	task *subagentTask,
	mode sideSubagentFinalCommitMode,
) error {
	if tm == nil || tm.runtime == nil || tm.runtime.sessions == nil || task == nil || !isSideSubagentTask(task) {
		return nil
	}
	switch mode {
	case sideSubagentFinalCommitPublished, sideSubagentFinalCommitStaged, sideSubagentFinalCommitDeferredMarker:
	default:
		return fmt.Errorf("agent-sdk/runtime: unsupported side subagent final commit mode %d", mode)
	}
	task.mu.Lock()
	if task.running || task.state != taskapi.StateCompleted || strings.EqualFold(taskStringValue(task.metadata["final_event_persisted"]), "true") {
		task.mu.Unlock()
		return nil
	}
	text := firstNonBlankTaskOutput(taskRawStringValue(task.result["final_message"]), taskRawStringValue(task.result["result"]))
	if !taskOutputHasNonBlankLine(text) && subagentFramesContainAssistantText(task.streamFrames) {
		text = compactFinalOutput(task.stdout, task.stderr)
	}
	if !taskOutputHasNonBlankLine(text) {
		task.mu.Unlock()
		return nil
	}
	role := subagentParticipantRole(task)
	message := model.NewTextMessage(model.RoleAssistant, text)
	event := &session.Event{
		IdempotencyKey: fmt.Sprintf("subagent-dialogue:%s:%d:assistant", task.ref.TaskID, task.turnSeq),
		Type:           session.EventTypeAssistant,
		Visibility:     session.VisibilityCanonical,
		Time:           tm.runtime.now(),
		Actor: session.ActorRef{
			Kind: session.ActorKindParticipant,
			ID:   strings.TrimSpace(task.anchor.AgentID),
			Role: string(role),
			Name: "@" + strings.TrimPrefix(strings.TrimSpace(task.handle), "@"),
		},
		Scope: &session.EventScope{
			TurnID: subagentTurnID(task.ref.TaskID, task.turnSeq),
			Source: firstNonEmpty(taskStringValue(task.metadata["source"]), "subagent_sidecar"),
			Executor: session.ParticipantExecutor(session.ParticipantBinding{
				ID: task.anchor.AgentID, Kind: session.ParticipantKindSubagent, Role: role,
				AgentName: task.agent, Label: "@" + strings.TrimPrefix(strings.TrimSpace(task.handle), "@"),
			}),
			Participant: session.ParticipantRef{
				ID:           strings.TrimSpace(task.anchor.AgentID),
				Kind:         session.ParticipantKindSubagent,
				Role:         role,
				DelegationID: strings.TrimSpace(task.ref.TaskID),
			},
		},
		Message: &message,
		Text:    text,
		Meta: map[string]any{
			"handle":  strings.TrimSpace(task.handle),
			"mention": "@" + strings.TrimPrefix(strings.TrimSpace(task.handle), "@"),
			"agent":   strings.TrimSpace(task.agent),
		},
	}
	task.mu.Unlock()

	if err := tm.appendSubagentSagaEvent(ctx, task.sessionRef, event); err != nil {
		return err
	}
	if err := tm.runtime.updateParticipantContextCheckpoint(ctx, task.sessionRef, strings.TrimSpace(task.anchor.AgentID)); err != nil {
		return err
	}
	task.mu.Lock()
	if task.metadata == nil {
		task.metadata = map[string]any{}
	}
	task.metadata["final_event_persisted"] = "true"
	if mode == sideSubagentFinalCommitDeferredMarker {
		task.mu.Unlock()
		return nil
	}
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	var persistErr error
	if mode == sideSubagentFinalCommitStaged {
		persistErr = tm.persistStagedTaskEntry(ctx, entry)
	} else {
		persistErr = tm.persistTaskEntry(ctx, entry)
	}
	if persistErr != nil {
		return persistErr
	}
	task.mu.Lock()
	task.revision = entry.Revision
	task.lease = taskapi.CloneLease(entry.Lease)
	task.mu.Unlock()
	return nil
}
