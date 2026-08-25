package runtime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func (r *Runtime) startRunTurnJournal(ctx context.Context, ref session.SessionRef, runID, turnID string) error {
	r.executionMu.Lock()
	defer r.executionMu.Unlock()

	now := r.now()
	records := []session.ExecutionRecord{
		newExecutionRecord(session.JournalKindRun, ref, runID, "", "", now),
		newExecutionRecord(session.JournalKindTurn, ref, runID, turnID, "", now),
	}
	events := make([]*session.Event, 0, 4)
	for _, prepared := range records {
		if err := session.ValidateExecutionTransition(session.ExecutionRecord{}, prepared); err != nil {
			return err
		}
		events = append(events, executionJournalEvent(prepared))
		started := prepared
		started.Revision++
		started.Status = session.ExecutionStarted
		started.UpdatedAt = r.now()
		if err := session.ValidateExecutionTransition(prepared, started); err != nil {
			return err
		}
		events = append(events, executionJournalEvent(started))
	}
	return r.appendExecutionEvents(ctx, ref, events)
}

func newExecutionRecord(kind session.JournalKind, ref session.SessionRef, runID, turnID, stepID string, now time.Time) session.ExecutionRecord {
	ref = session.NormalizeSessionRef(ref)
	return session.NormalizeExecutionRecord(session.ExecutionRecord{
		Schema: session.ExecutionJournalSchemaVersion,
		Kind:   kind, SessionID: ref.SessionID, RunID: strings.TrimSpace(runID), TurnID: strings.TrimSpace(turnID), StepID: strings.TrimSpace(stepID),
		Revision: 1, Status: session.ExecutionPrepared, CreatedAt: now, UpdatedAt: now,
	})
}

func executionJournalEvent(record session.ExecutionRecord) *session.Event {
	record = session.NormalizeExecutionRecord(record)
	return &session.Event{
		IdempotencyKey: fmt.Sprintf("%s-execution:%s:%d", record.Kind, record.Identity, record.Revision),
		Type:           session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Time: record.UpdatedAt,
		Actor:     session.ActorRef{Kind: session.ActorKindSystem, ID: "runtime", Name: "runtime"},
		Lifecycle: &session.EventLifecycle{Status: string(record.Status), Reason: record.Reason},
		Journal:   &session.ExecutionJournalEntry{Schema: session.ExecutionJournalSchemaVersion, Kind: record.Kind, Execution: &record},
	}
}

func (r *Runtime) transitionRunTurnJournal(ctx context.Context, ref session.SessionRef, runID, turnID string, status session.ExecutionStatus, reason string) error {
	r.executionMu.Lock()
	defer r.executionMu.Unlock()

	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		return err
	}
	latest := latestRunTurnRecords(events, runID, turnID)
	updates := make([]*session.Event, 0, len(latest))
	for _, previous := range latest {
		if executionTerminal(previous.Status) || previous.Status == status {
			continue
		}
		next := previous
		next.Revision++
		next.Status = status
		next.Reason = strings.TrimSpace(reason)
		if status == session.ExecutionFailed {
			next.Error = strings.TrimSpace(reason)
		}
		next.UpdatedAt = r.now()
		if err := session.ValidateExecutionTransition(previous, next); err != nil {
			return err
		}
		updates = append(updates, executionJournalEvent(next))
	}
	if executionTerminal(status) {
		return r.appendExecutionEventsAndClearActivePlan(ctx, ref, updates,
			fmt.Sprintf("turn-terminal-plan:%s:%s:%s", strings.TrimSpace(runID), strings.TrimSpace(turnID), status))
	}
	if len(updates) == 0 {
		return nil
	}
	return r.appendExecutionEvents(ctx, ref, updates)
}

func (r *Runtime) appendExecutionEventsAndClearActivePlan(
	ctx context.Context,
	ref session.SessionRef,
	events []*session.Event,
	transactionID string,
) error {
	state, err := r.sessions.SnapshotState(ctx, ref)
	if err != nil {
		return err
	}
	if _, hasPlan := state["plan"]; !hasPlan {
		return r.appendExecutionEvents(ctx, ref, events)
	}
	compound, ok := r.sessions.(session.EventBatchStateService)
	if !ok {
		return fmt.Errorf("agent-sdk/runtime: terminal Turn plan cleanup requires atomic event/state commit")
	}
	_, err = compound.AppendEventsAndUpdateState(ctx, session.AppendEventsAndUpdateStateRequest{
		SessionRef:     ref,
		MutationGuard:  session.RuntimeMutationGuard(ctx),
		TransactionID:  strings.TrimSpace(transactionID),
		MutationDigest: "clear-active-plan-v1",
		Events:         events,
		UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
			delete(state, "plan")
			return state, nil
		},
	})
	return err
}

func executionEventsContainTerminalRunOrTurn(events []*session.Event) bool {
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.Execution == nil {
			continue
		}
		record := session.NormalizeExecutionRecord(*event.Journal.Execution)
		if (record.Kind == session.JournalKindRun || record.Kind == session.JournalKindTurn) && executionTerminal(record.Status) {
			return true
		}
	}
	return false
}

func executionRecoveryPlanTransactionID(events []*session.Event) string {
	keys := make([]string, 0, len(events))
	for _, event := range events {
		if event != nil {
			keys = append(keys, strings.TrimSpace(event.IdempotencyKey))
		}
	}
	sort.Strings(keys)
	digest := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return fmt.Sprintf("execution-recovery-plan:%x", digest[:12])
}

func latestRunTurnRecords(events []*session.Event, runID, turnID string) []session.ExecutionRecord {
	runID = strings.TrimSpace(runID)
	turnID = strings.TrimSpace(turnID)
	latest := map[session.JournalKind]session.ExecutionRecord{}
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.Execution == nil {
			continue
		}
		record := session.NormalizeExecutionRecord(*event.Journal.Execution)
		if record.RunID != runID || (record.Kind == session.JournalKindTurn && record.TurnID != turnID) {
			continue
		}
		if record.Kind != session.JournalKindRun && record.Kind != session.JournalKindTurn {
			continue
		}
		if prior, ok := latest[record.Kind]; !ok || record.Revision > prior.Revision {
			latest[record.Kind] = record
		}
	}
	out := make([]session.ExecutionRecord, 0, 2)
	for _, kind := range []session.JournalKind{session.JournalKindTurn, session.JournalKindRun} {
		if record, ok := latest[kind]; ok {
			out = append(out, record)
		}
	}
	return out
}

func executionTerminal(status session.ExecutionStatus) bool {
	switch status {
	case session.ExecutionSucceeded, session.ExecutionFailed, session.ExecutionCancelled, session.ExecutionInterrupted, session.ExecutionUnknownOutcome:
		return true
	default:
		return false
	}
}

func (r *Runtime) recoverIncompleteExecutionJournal(ctx context.Context, ref session.SessionRef, recoveryTools ...tool.Tool) error {
	if err := r.recoverIncompleteToolExecutions(ctx, ref, recoveryTools...); err != nil {
		return err
	}
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		return err
	}
	latest := map[string]session.ExecutionRecord{}
	latestPause := map[string]session.PauseToken{}
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.Execution == nil {
			continue
		}
		record := session.NormalizeExecutionRecord(*event.Journal.Execution)
		if prior, ok := latest[record.Identity]; !ok || record.Revision > prior.Revision {
			latest[record.Identity] = record
		}
	}
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.PauseToken == nil {
			continue
		}
		token := session.ClonePauseToken(*event.Journal.PauseToken)
		if prior, ok := latestPause[token.TokenID]; !ok || token.Revision > prior.Revision {
			latestPause[token.TokenID] = token
		}
	}
	updates := make([]*session.Event, 0)
	for _, previous := range latest {
		if previous.Status != session.ExecutionStarted && previous.Status != session.ExecutionWaitingApproval && previous.Status != session.ExecutionCancelRequested {
			continue
		}
		next := previous
		next.Revision++
		next.RecoveredFrom = previous.Status
		next.Reason = "runtime recovered without a durable terminal record"
		next.UpdatedAt = r.now()
		if previous.Kind == session.JournalKindStep {
			next.Status = session.ExecutionUnknownOutcome
		} else {
			next.Status = session.ExecutionInterrupted
		}
		if err := session.ValidateExecutionTransition(previous, next); err != nil {
			return err
		}
		updates = append(updates, executionJournalEvent(next))
	}
	for _, token := range latestPause {
		if token.Status != session.PauseTokenPending {
			continue
		}
		token.Revision++
		token.Status = session.PauseTokenCancelled
		token.Reason = "runtime recovered without a live approval waiter"
		token.UpdatedAt = r.now()
		updates = append(updates, pauseTokenEvent(token))
	}
	if len(updates) == 0 {
		return nil
	}
	sort.SliceStable(updates, func(i, j int) bool {
		return strings.TrimSpace(updates[i].IdempotencyKey) < strings.TrimSpace(updates[j].IdempotencyKey)
	})
	if executionEventsContainTerminalRunOrTurn(updates) {
		return r.appendExecutionEventsAndClearActivePlan(ctx, ref, updates, executionRecoveryPlanTransactionID(updates))
	}
	return r.appendExecutionEvents(ctx, ref, updates)
}

func (r *Runtime) appendExecutionEvents(ctx context.Context, ref session.SessionRef, events []*session.Event) error {
	if len(events) == 0 {
		return nil
	}
	if batch, ok := r.sessions.(session.EventBatchService); ok {
		_, err := batch.AppendEvents(ctx, session.AppendEventsRequest{SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), Events: events})
		return err
	}
	// Legacy/narrow service wrappers may expose only single-event append. Each
	// record remains independently recoverable; production memory/file services
	// implement EventBatchService and commit these transitions atomically.
	for _, event := range events {
		if _, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), Event: event}); err != nil {
			return err
		}
	}
	return nil
}
