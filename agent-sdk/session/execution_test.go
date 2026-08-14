package session

import (
	"errors"
	"testing"
)

func TestToolExecutionTransitionContract(t *testing.T) {
	t.Parallel()
	key := ExecutionKey{SessionID: "s1", RunID: "r1", TurnID: "t1", StepID: "step-1", ToolCallID: "call-1"}
	prepared := ToolExecution{Schema: ToolExecutionSchemaVersion, Key: key, Status: ToolExecutionPrepared, Revision: 1}
	approved := prepared
	approved.Status = ToolExecutionApproved
	approved.Revision = 2
	started := approved
	started.Status = ToolExecutionStarted
	started.Revision = 3
	unknown := started
	unknown.Status = ToolExecutionUnknownOutcome
	unknown.Revision = 4
	for _, pair := range [][2]ToolExecution{{{}, prepared}, {prepared, approved}, {approved, started}, {started, unknown}} {
		if err := ValidateToolExecutionTransition(pair[0], pair[1]); err != nil {
			t.Fatalf("valid transition %q -> %q error = %v", pair[0].Status, pair[1].Status, err)
		}
	}
	invalid := started
	invalid.Status = ToolExecutionPrepared
	invalid.Revision++
	var transitionErr *ToolExecutionTransitionError
	if err := ValidateToolExecutionTransition(started, invalid); !errors.As(err, &transitionErr) {
		t.Fatalf("invalid transition error = %v, want *ToolExecutionTransitionError", err)
	}
}

func TestRunTurnStepExecutionTransitionContract(t *testing.T) {
	t.Parallel()
	for _, record := range []ExecutionRecord{
		{Schema: ExecutionJournalSchemaVersion, Kind: JournalKindRun, SessionID: "s1", RunID: "r1", Revision: 1, Status: ExecutionPrepared},
		{Schema: ExecutionJournalSchemaVersion, Kind: JournalKindTurn, SessionID: "s1", RunID: "r1", TurnID: "t1", Revision: 1, Status: ExecutionPrepared},
		{Schema: ExecutionJournalSchemaVersion, Kind: JournalKindStep, SessionID: "s1", RunID: "r1", TurnID: "t1", StepID: "step-1", Revision: 1, Status: ExecutionPrepared},
	} {
		prepared := NormalizeExecutionRecord(record)
		started := prepared
		started.Revision++
		started.Status = ExecutionStarted
		succeeded := started
		succeeded.Revision++
		succeeded.Status = ExecutionSucceeded
		for _, pair := range [][2]ExecutionRecord{{{}, prepared}, {prepared, started}, {started, succeeded}} {
			if err := ValidateExecutionTransition(pair[0], pair[1]); err != nil {
				t.Fatalf("%s transition %q -> %q error = %v", record.Kind, pair[0].Status, pair[1].Status, err)
			}
		}
		invalid := succeeded
		invalid.Revision++
		invalid.Status = ExecutionStarted
		var transitionErr *ExecutionTransitionError
		if err := ValidateExecutionTransition(succeeded, invalid); !errors.As(err, &transitionErr) {
			t.Fatalf("%s invalid transition error = %v, want *ExecutionTransitionError", record.Kind, err)
		}
	}
}

func TestPauseTokenTransitionContract(t *testing.T) {
	t.Parallel()
	pending := PauseToken{
		Schema: ExecutionJournalSchemaVersion, TokenID: "pause-1", SessionID: "s1", RunID: "r1", TurnID: "t1",
		Revision: 1, Status: PauseTokenPending,
	}
	resolved := pending
	resolved.Revision++
	resolved.Status = PauseTokenResolved
	if err := ValidatePauseTokenTransition(PauseToken{}, pending); err != nil {
		t.Fatalf("initial pause transition error = %v", err)
	}
	if err := ValidatePauseTokenTransition(pending, resolved); err != nil {
		t.Fatalf("resolved pause transition error = %v", err)
	}
	invalid := resolved
	invalid.Revision++
	invalid.Status = PauseTokenPending
	var transitionErr *PauseTokenTransitionError
	if err := ValidatePauseTokenTransition(resolved, invalid); !errors.As(err, &transitionErr) {
		t.Fatalf("invalid pause transition error = %v, want *PauseTokenTransitionError", err)
	}
}

func TestRecoverIncompleteToolExecutionsMarksUnknownWithoutReplay(t *testing.T) {
	t.Parallel()
	key := ExecutionKey{SessionID: "s1", RunID: "r1", TurnID: "t1", StepID: "step-1", ToolCallID: "call-1"}
	events := []*Event{
		{Journal: &ExecutionJournalEntry{Kind: JournalKindToolExecution, ToolExecution: &ToolExecution{Schema: ToolExecutionSchemaVersion, Key: key, Status: ToolExecutionPrepared, Revision: 1}}},
		{Journal: &ExecutionJournalEntry{Kind: JournalKindToolExecution, ToolExecution: &ToolExecution{Schema: ToolExecutionSchemaVersion, Key: key, Status: ToolExecutionStarted, Revision: 2}}},
	}
	recovered := RecoverIncompleteToolExecutions(events)
	if len(recovered) != 1 || recovered[0].Status != ToolExecutionUnknownOutcome || recovered[0].Revision != 3 {
		t.Fatalf("RecoverIncompleteToolExecutions() = %#v, want one UnknownOutcome revision 3", recovered)
	}
}
