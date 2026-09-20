package runtime

import (
	"context"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (r *Runtime) persistedRunState(ctx context.Context, ref session.SessionRef, runID string) (agent.RunState, error) {
	snapshot, err := r.persistedRunJournal(ctx, ref, runID)
	if err != nil {
		return agent.RunState{}, err
	}
	if snapshot.Run == nil {
		return agent.RunState{}, session.ErrSessionNotFound
	}
	latest := *snapshot.Run
	state := agent.RunState{ActiveRunID: latest.RunID, UpdatedAt: latest.UpdatedAt}
	switch latest.Status {
	case session.ExecutionPrepared, session.ExecutionStarted, session.ExecutionCancelRequested:
		state.Status = agent.RunLifecycleStatusRunning
	case session.ExecutionWaitingApproval:
		state.Status = agent.RunLifecycleStatusWaitingApproval
		state.WaitingApproval = true
	case session.ExecutionSucceeded:
		state.Status = agent.RunLifecycleStatusCompleted
	case session.ExecutionFailed:
		state.Status = agent.RunLifecycleStatusFailed
		state.LastError = firstNonEmpty(latest.Error, latest.Reason)
	case session.ExecutionCancelled, session.ExecutionInterrupted, session.ExecutionUnknownOutcome:
		state.Status = agent.RunLifecycleStatusInterrupted
		state.LastError = firstNonEmpty(latest.Error, latest.Reason)
	}
	if state.WaitingApproval && snapshot.Pause != nil && snapshot.Pause.Status == session.PauseTokenPending {
		state.PauseTokenID = snapshot.Pause.TokenID
	}
	return state, nil
}

func (r *Runtime) persistedRunJournal(ctx context.Context, ref session.SessionRef, runID string) (session.RunJournalSnapshot, error) {
	if reader, ok := r.sessions.(session.RunJournalReader); ok {
		return reader.RunJournal(ctx, ref, runID)
	}
	// Custom SDK stores may expose only the base Service contract. Preserve its
	// journal selection semantics without imposing a new mandatory capability.
	events, err := r.sessions.Events(ctx, session.EventsRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		return session.RunJournalSnapshot{}, err
	}
	runID = strings.TrimSpace(runID)
	var latest session.ExecutionRecord
	var latestSeq uint64
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.Execution == nil {
			continue
		}
		record := session.NormalizeExecutionRecord(*event.Journal.Execution)
		if record.Kind != session.JournalKindRun || (runID != "" && record.RunID != runID) {
			continue
		}
		if runID != "" {
			if record.Revision > latest.Revision {
				latest = record
			}
		} else if event.Seq > latestSeq {
			latest = record
			latestSeq = event.Seq
		}
	}
	if latest.Revision == 0 {
		return session.RunJournalSnapshot{}, session.ErrSessionNotFound
	}
	snapshot := session.RunJournalSnapshot{Run: &latest}
	if latest.Status != session.ExecutionWaitingApproval {
		return snapshot, nil
	}
	var latestPause session.PauseToken
	var latestPauseSeq uint64
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.PauseToken == nil {
			continue
		}
		token := event.Journal.PauseToken
		if token.RunID == latest.RunID && event.Seq > latestPauseSeq {
			latestPause = session.ClonePauseToken(*token)
			latestPauseSeq = event.Seq
		}
	}
	if latestPauseSeq > 0 {
		snapshot.Pause = &latestPause
	}
	return snapshot, nil
}
