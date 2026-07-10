package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
		previous := record
		record.Revision++
		record.Status = session.ToolExecutionCancelRequested
		record.Reason = ctx.Err().Error()
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
		}
	}()
	result, callErr := t.base.Call(ctx, call)
	close(callFinished)
	<-cancelWatcherFinished
	if ctx.Err() != nil && record.Status == session.ToolExecutionStarted {
		cancelJournalErr = requestCancellation()
	}
	if cancelJournalErr != nil {
		return result, errors.Join(callErr, cancelJournalErr)
	}
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

func (t journaledTool) append(ctx context.Context, previous session.ToolExecution, next session.ToolExecution) error {
	return t.appendEntry(ctx, previous, next, session.ExecutionRecord{}, session.ExecutionRecord{})
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
		SessionRef: t.sessionRef,
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

func (r *Runtime) recoverIncompleteToolExecutions(ctx context.Context, ref session.SessionRef) error {
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
		nextStep.Status = session.ExecutionUnknownOutcome
		nextStep.RecoveredFrom = previousStep.Status
		nextStep.Reason = record.Reason
		nextStep.UpdatedAt = record.UpdatedAt
		writer := journaledTool{sessions: r.sessions, sessionRef: ref, now: r.now}
		if err := writer.appendEntry(ctx, previous, record, previousStep, nextStep); err != nil {
			return err
		}
	}
	return nil
}
