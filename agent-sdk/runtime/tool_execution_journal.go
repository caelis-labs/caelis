package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type journaledTool struct {
	base       tool.Tool
	sessions   session.Service
	sessionRef session.SessionRef
	runID      string
	turnID     string
	now        func() time.Time
}

func (t journaledTool) Definition() tool.Definition { return tool.CloneDefinition(t.base.Definition()) }

func (t journaledTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	def := t.base.Definition()
	now := t.now
	if now == nil {
		now = time.Now
	}
	createdAt := now()
	var mu sync.Mutex
	record := session.NormalizeToolExecution(session.ToolExecution{
		Schema:      session.ToolExecutionSchemaVersion,
		Key:         session.ExecutionKey{SessionID: t.sessionRef.SessionID, RunID: t.runID, TurnID: t.turnID, StepID: strings.TrimSpace(call.ID), ToolCallID: strings.TrimSpace(call.ID)},
		Revision:    1,
		ToolName:    def.Name,
		EffectClass: string(tool.EffectClassOf(def)),
		Status:      session.ToolExecutionPrepared,
		Input:       append(json.RawMessage(nil), call.Input...),
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	})
	step := session.NormalizeExecutionRecord(session.ExecutionRecord{
		Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindStep,
		SessionID: t.sessionRef.SessionID, RunID: t.runID, TurnID: t.turnID, StepID: strings.TrimSpace(call.ID),
		Revision: 1, Status: session.ExecutionPrepared, CreatedAt: createdAt, UpdatedAt: createdAt,
	})
	if err := t.appendEntry(ctx, session.ToolExecution{}, record, session.ExecutionRecord{}, step); err != nil {
		return tool.Result{}, err
	}
	for _, status := range []session.ToolExecutionStatus{session.ToolExecutionApproved, session.ToolExecutionStarted} {
		previous := record
		record.Revision++
		record.Status = status
		record.UpdatedAt = now()
		previousStep := session.ExecutionRecord{}
		nextStep := session.ExecutionRecord{}
		if status == session.ToolExecutionStarted {
			previousStep = step
			step.Revision++
			step.Status = session.ExecutionStarted
			step.UpdatedAt = record.UpdatedAt
			nextStep = step
		}
		if err := t.appendEntry(ctx, previous, record, previousStep, nextStep); err != nil {
			return tool.Result{}, err
		}
	}

	requestCancellation := func() error {
		mu.Lock()
		defer mu.Unlock()
		if record.Status != session.ToolExecutionStarted {
			return nil
		}
		previous := record
		record.Revision++
		record.Status = session.ToolExecutionCancelRequested
		if ctx.Err() != nil {
			record.Reason = ctx.Err().Error()
		} else {
			record.Reason = "cancellation requested"
		}
		record.UpdatedAt = now()
		previousStep := step
		step.Revision++
		step.Status = session.ExecutionCancelRequested
		step.Reason = record.Reason
		step.UpdatedAt = record.UpdatedAt
		return t.appendEntry(context.WithoutCancel(ctx), previous, record, previousStep, step)
	}
	callFinished := make(chan struct{})
	cancelWatcherFinished := make(chan struct{})
	var cancelJournalErr error
	go func() {
		defer close(cancelWatcherFinished)
		select {
		case <-ctx.Done():
			cancelJournalErr = requestCancellation()
		case <-callFinished:
			// If the context was already cancelled when the tool returned (or both
			// channels were ready), still record cancel_requested for durable state.
			if ctx.Err() != nil {
				cancelJournalErr = requestCancellation()
			}
		}
	}()
	result, callErr := t.base.Call(ctx, call)
	close(callFinished)
	<-cancelWatcherFinished
	if cancelJournalErr != nil {
		return result, errors.Join(callErr, cancelJournalErr)
	}

	mu.Lock()
	defer mu.Unlock()
	record.Revision++
	record.UpdatedAt = now()
	resultRaw, marshalErr := json.Marshal(result)
	if marshalErr == nil {
		record.Result = resultRaw
	}
	switch {
	case ctx.Err() != nil && (callErr != nil || result.IsError):
		record.Status = session.ToolExecutionCancelled
		record.Error = firstNonEmpty(errorText(callErr), ctx.Err().Error())
	case callErr != nil || result.IsError:
		record.Status = session.ToolExecutionFailed
		record.Error = errorText(callErr)
	case marshalErr != nil:
		record.Status = session.ToolExecutionFailed
		record.Error = marshalErr.Error()
	default:
		record.Status = session.ToolExecutionSucceeded
	}
	step.Revision++
	step.UpdatedAt = record.UpdatedAt
	step.Error = record.Error
	step.Reason = record.Reason
	switch record.Status {
	case session.ToolExecutionSucceeded:
		step.Status = session.ExecutionSucceeded
	case session.ToolExecutionCancelled:
		step.Status = session.ExecutionCancelled
	default:
		step.Status = session.ExecutionFailed
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	terminalJournal := session.ExecutionJournalEntry{
		Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindToolExecution,
		Execution: &step, ToolExecution: &record,
	}
	journalRaw, journalErr := json.Marshal(terminalJournal)
	if journalErr != nil {
		return result, errors.Join(callErr, journalErr)
	}
	var journalValue map[string]any
	if err := json.Unmarshal(journalRaw, &journalValue); err != nil {
		return result, errors.Join(callErr, err)
	}
	result.Metadata[tool.MetadataExecutionJournal] = journalValue
	return result, callErr
}

func (t journaledTool) appendEntry(
	ctx context.Context,
	previousTool session.ToolExecution,
	nextTool session.ToolExecution,
	previousExecution session.ExecutionRecord,
	nextExecution session.ExecutionRecord,
) error {
	if t.sessions == nil {
		return fmt.Errorf("agent-sdk/runtime: session journal is unavailable")
	}
	if err := session.ValidateToolExecutionTransition(previousTool, nextTool); err != nil {
		return err
	}
	if nextExecution.Status != "" {
		if err := session.ValidateExecutionTransition(previousExecution, nextExecution); err != nil {
			return err
		}
	}
	nextTool = session.NormalizeToolExecution(nextTool)
	if nextExecution.Status != "" {
		nextExecution = session.NormalizeExecutionRecord(nextExecution)
	}
	journal := &session.ExecutionJournalEntry{
		Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindToolExecution,
		ToolExecution: &nextTool,
	}
	if nextExecution.Status != "" {
		journal.Execution = &nextExecution
	}
	_, err := t.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef:    t.sessionRef,
		MutationGuard: session.RuntimeMutationGuard(ctx),
		Event: &session.Event{
			IdempotencyKey: "tool-execution:" + nextTool.Identity + ":" + fmt.Sprint(nextTool.Revision),
			Type:           session.EventTypeLifecycle,
			Visibility:     session.VisibilityJournal,
			Time:           nextTool.UpdatedAt,
			Actor:          session.ActorRef{Kind: session.ActorKindTool, ID: nextTool.Key.ToolCallID, Name: nextTool.ToolName},
			Lifecycle:      &session.EventLifecycle{Status: string(nextTool.Status), Reason: nextTool.Reason},
			Journal:        journal,
		},
	})
	return err
}

func (r *Runtime) wrapToolsForExecutionJournal(ref session.SessionRef, runID string, turnID string, tools []tool.Tool) []tool.Tool {
	out := make([]tool.Tool, 0, len(tools))
	for _, item := range tools {
		if item == nil {
			continue
		}
		out = append(out, journaledTool{base: item, sessions: r.sessions, sessionRef: session.NormalizeSessionRef(ref), runID: strings.TrimSpace(runID), turnID: strings.TrimSpace(turnID), now: r.now})
	}
	return out
}

func (r *Runtime) recoverIncompleteToolExecutions(ctx context.Context, ref session.SessionRef, recoveryTools ...tool.Tool) error {
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		return err
	}
	for _, record := range session.RecoverIncompleteToolExecutions(events) {
		record.UpdatedAt = r.now()
		previous := record
		previous.Revision--
		previous.Status = record.RecoveredFrom
		previous.RecoveredFrom = ""
		previousStep := session.NormalizeExecutionRecord(session.ExecutionRecord{
			Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindStep,
			SessionID: record.Key.SessionID, RunID: record.Key.RunID, TurnID: record.Key.TurnID, StepID: record.Key.StepID,
			Revision: 2, Status: session.ExecutionStarted, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		})
		if record.RecoveredFrom == session.ToolExecutionCancelRequested {
			previousStep.Revision = 3
			previousStep.Status = session.ExecutionCancelRequested
		}
		nextStep := previousStep
		nextStep.Revision++
		result, recoveryReason := reconcileToolExecution(ctx, record, recoveryTools)
		payload := recoveredToolResultPayload(record, result, recoveryReason)
		switch result.Status {
		case tool.RecoverySucceeded:
			record.Status = session.ToolExecutionSucceeded
			nextStep.Status = session.ExecutionSucceeded
		case tool.RecoveryFailed:
			record.Status = session.ToolExecutionFailed
			record.Error = firstNonEmpty(result.Reason, recoveryReason)
			nextStep.Status = session.ExecutionFailed
			nextStep.Error = record.Error
		default:
			record.Status = session.ToolExecutionUnknownOutcome
			nextStep.Status = session.ExecutionUnknownOutcome
		}
		nextStep.RecoveredFrom = previousStep.Status
		record.Reason = firstNonEmpty(result.Reason, recoveryReason, record.Reason)
		nextStep.Reason = record.Reason
		nextStep.UpdatedAt = record.UpdatedAt
		if result.Status == tool.RecoverySucceeded || result.Status == tool.RecoveryFailed {
			record.Result, _ = json.Marshal(result.Result)
		}
		if err := session.ValidateToolExecutionTransition(previous, record); err != nil {
			return err
		}
		if err := session.ValidateExecutionTransition(previousStep, nextStep); err != nil {
			return err
		}
		if err := r.appendRecoveredToolResult(ctx, ref, record, nextStep, payload); err != nil {
			return err
		}
	}
	return nil
}

func reconcileToolExecution(ctx context.Context, record session.ToolExecution, tools []tool.Tool) (tool.RecoveryResult, string) {
	for _, candidate := range tools {
		if candidate == nil || !strings.EqualFold(candidate.Definition().Name, record.ToolName) {
			continue
		}
		recoverer, ok := candidate.(tool.Recoverer)
		if !ok {
			break
		}
		result, err := recoverer.Recover(ctx, tool.RecoveryRequest{
			ExecutionIdentity: record.Identity,
			Call:              tool.Call{ID: record.Key.ToolCallID, Name: record.ToolName, Input: append(json.RawMessage(nil), record.Input...)},
		})
		if err != nil {
			return tool.RecoveryResult{Status: tool.RecoveryUnknown}, "recovery failed: " + err.Error()
		}
		return result, strings.TrimSpace(result.Reason)
	}
	return tool.RecoveryResult{Status: tool.RecoveryUnknown}, "no configured recoverer could prove the side-effect outcome"
}

func recoveredToolResultPayload(record session.ToolExecution, recovery tool.RecoveryResult, reason string) map[string]any {
	if recovery.Status == tool.RecoverySucceeded || recovery.Status == tool.RecoveryFailed {
		payload := recoveryResultPayload(recovery.Result)
		if payload == nil {
			payload = map[string]any{}
		}
		payload["status"] = string(recovery.Status)
		payload["effect_class"] = record.EffectClass
		payload["execution_identity"] = record.Identity
		if reason = strings.TrimSpace(reason); reason != "" {
			payload["reason"] = reason
		}
		return payload
	}
	return map[string]any{
		"status":             "unknown_outcome",
		"effect_class":       record.EffectClass,
		"execution_identity": record.Identity,
		"reason":             strings.TrimSpace(reason),
		"instruction":        "Do not retry this tool call blindly; reconcile its side effects before any new execution.",
	}
}

func recoveryResultPayload(result tool.Result) map[string]any {
	for _, part := range result.Content {
		if part.JSON != nil {
			var decoded map[string]any
			if json.Unmarshal(part.JSON.Value, &decoded) == nil {
				return session.CloneState(decoded)
			}
		}
		if part.Text != nil && strings.TrimSpace(part.Text.Text) != "" {
			return map[string]any{"result": part.Text.Text}
		}
	}
	return nil
}

func (r *Runtime) appendRecoveredToolResult(
	ctx context.Context,
	ref session.SessionRef,
	record session.ToolExecution,
	step session.ExecutionRecord,
	payload map[string]any,
) error {
	message := model.Message{
		Role: model.RoleTool,
		Parts: []model.Part{model.NewToolResultJSONPart(
			record.Key.ToolCallID,
			record.ToolName,
			payload,
			record.Status != session.ToolExecutionSucceeded,
		)},
	}
	event := &session.Event{
		IdempotencyKey: "tool-recovery-result:" + record.Identity,
		Type:           session.EventTypeToolResult,
		Visibility:     session.VisibilityCanonical,
		Time:           record.UpdatedAt,
		Actor:          session.ActorRef{Kind: session.ActorKindTool, ID: record.Key.ToolCallID, Name: record.ToolName},
		Message:        &message,
		Tool: &session.EventTool{
			ID: record.Key.ToolCallID, Name: record.ToolName, Status: recoveredToolStatus(record.Status),
			Input: rawObject(record.Input), Output: session.CloneState(payload),
		},
		Journal: &session.ExecutionJournalEntry{
			Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindToolExecution,
			Execution: &step, ToolExecution: &record,
		},
	}
	_, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), Event: event})
	return err
}

func rawObject(raw json.RawMessage) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func recoveredToolStatus(status session.ToolExecutionStatus) string {
	return string(status)
}
