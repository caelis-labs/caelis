package bot

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (s *WorkStore) observeWork(ctx context.Context, work Work) (Work, error) {
	if s.Sessions == nil || work.Execution.TurnID == "" {
		return work, nil
	}
	// Control and Runtime Run IDs have separate allocators. Their shared TurnID
	// selects the native journal; a Run record itself has no TurnID. Journal
	// visibility must be included explicitly, independently of model history.
	events, err := s.Sessions.Events(ctx, session.EventsRequest{SessionRef: session.SessionRef{SessionID: work.SessionID}, IncludeTransient: true})
	if errors.Is(err, session.ErrSessionNotFound) {
		work.Status = "unknown"
		return work, nil
	}
	if err != nil {
		return work, err
	}
	var latest *session.ExecutionRecord
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.Kind != session.JournalKindTurn || event.Journal.Execution == nil || event.Journal.Execution.TurnID != work.Execution.TurnID {
			continue
		}
		record := event.Journal.Execution
		if latest == nil || record.Revision > latest.Revision {
			latest = record
		}
	}
	if latest == nil {
		work.Status = "unknown"
		return work, nil
	}
	work.Status = string(latest.Status)
	switch latest.Status {
	case session.ExecutionSucceeded, session.ExecutionFailed, session.ExecutionCancelled, session.ExecutionInterrupted, session.ExecutionUnknownOutcome:
	default:
		if work.Execution.InstanceID != s.InstanceID {
			work.Status = "unknown"
		}
	}
	if work.Status == string(session.ExecutionSucceeded) || work.Status == string(session.ExecutionFailed) {
		work.Result, err = s.workResult(ctx, work)
	}
	return work, err
}

func (s *WorkStore) workResult(ctx context.Context, work Work) (string, error) {
	events, err := s.Sessions.Events(ctx, session.EventsRequest{SessionRef: session.SessionRef{SessionID: work.SessionID}})
	if err != nil {
		return "", err
	}
	var text strings.Builder
	for _, event := range events {
		if event == nil || event.Scope == nil || event.Scope.TurnID != work.Execution.TurnID || event.ChildOrigin != nil || event.Type != session.EventTypeAssistant || event.Message == nil {
			continue
		}
		content := event.Message.TextContent()
		if text.Len()+len(content) > 64*1024 {
			break
		}
		text.WriteString(content)
		text.WriteString("\n")
	}
	return strings.TrimSpace(text.String()), nil
}
