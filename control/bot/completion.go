package bot

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Completion is one durable notification for an exact work execution. Read and
// Acknowledged do not authorize dispatch. ReportState is pending, claimed,
// admitted, or suppressed; a claimed record is never automatically replayed.
type Completion struct {
	ID              string    `json:"id"`
	PrincipalID     string    `json:"principal_id"`
	BotID           string    `json:"bot_id"`
	WorkID          string    `json:"work_id"`
	Execution       Execution `json:"execution"`
	Status          string    `json:"status"`
	Summary         string    `json:"summary"`
	ReportState     string    `json:"report_state"`
	ReportExecution Execution `json:"report_execution"`
	Acknowledged    bool      `json:"acknowledged"`
}

// ObserveCompletion reconstructs an outbox record from a terminal native
// journal and canonical assistant messages. This is safe to repeat on restart.
func (s *WorkStore) ObserveCompletion(ctx context.Context, work Work) error {
	work, err := s.observeWork(ctx, work)
	if err != nil {
		return err
	}
	switch session.ExecutionStatus(work.Status) {
	case session.ExecutionSucceeded, session.ExecutionFailed, session.ExecutionCancelled, session.ExecutionInterrupted, session.ExecutionUnknownOutcome:
	default:
		return nil
	}
	if work.Execution.RunID == "" {
		return nil
	}
	summary, err := s.workResult(ctx, work)
	if err != nil {
		return err
	}
	notice := Completion{ID: opaqueID("bot-completion-", work.ID, work.Execution.RunID, work.Execution.TurnID), PrincipalID: work.PrincipalID, BotID: work.BotID, WorkID: work.ID, Execution: work.Execution, Status: work.Status, Summary: summary, ReportState: "pending"}
	if work.Status == string(session.ExecutionCancelled) || work.Status == string(session.ExecutionInterrupted) {
		notice.ReportState = "suppressed"
	}
	_, err = s.db.Put(ctx, "completion", notice.ID, notice)
	return err
}

// Completions returns durable notifications independently of the main chat.
func (s *WorkStore) Completions(ctx context.Context, principalID, botID string) ([]Completion, error) {
	rows, err := s.db.List(ctx, "completion")
	if err != nil {
		return nil, err
	}
	out := []Completion{}
	for _, row := range rows {
		var item Completion
		if err := json.Unmarshal(row, &item); err != nil {
			return nil, err
		}
		if item.PrincipalID == principalID && item.BotID == botID {
			out = append(out, item)
		}
	}
	return out, nil
}

// ClaimCompletion fences automatic reporting before starting an LLM turn.
// A crash after the claim leaves an observable unknown dispatch, never a retry.
func (s *WorkStore) ClaimCompletion(ctx context.Context, item Completion) (bool, error) {
	if item.ReportState != "pending" {
		return false, nil
	}
	next := item
	next.ReportState = "claimed"
	return s.db.compare(ctx, "completion", item.ID, item, next)
}

// AdmitCompletion stores the native report target after successful admission.
func (s *WorkStore) AdmitCompletion(ctx context.Context, id string, target Execution) error {
	var item Completion
	if err := s.db.Get(ctx, "completion", id, &item); err != nil {
		return err
	}
	if item.ReportState != "claimed" {
		return errors.New("bot: completion is not claimed")
	}
	next := item
	next.ReportState = "admitted"
	next.ReportExecution = target
	changed, err := s.db.compare(ctx, "completion", id, item, next)
	if err == nil && !changed {
		return errorcode.New(errorcode.Conflict, "bot: completion changed")
	}
	return err
}

// AcknowledgeCompletion marks presentation consumption. It never resumes a
// work execution, triggers a report, or changes a report claim.
func (s *WorkStore) AcknowledgeCompletion(ctx context.Context, principalID, botID, id string) error {
	for range 8 {
		var item Completion
		if err := s.db.Get(ctx, "completion", id, &item); err != nil {
			return err
		}
		if item.PrincipalID != principalID || item.BotID != botID {
			return errorcode.New(errorcode.PermissionDenied, "bot: completion is not owned")
		}
		if item.Acknowledged {
			return nil
		}
		next := item
		next.Acknowledged = true
		if next.ReportState == "pending" {
			next.ReportState = "suppressed"
		}
		changed, err := s.db.compare(ctx, "completion", id, item, next)
		if err != nil || changed {
			return err
		}
	}
	return errorcode.New(errorcode.Conflict, "bot: completion changed concurrently")
}

// PauseReports persists the explicit-stop boundary independently of unread
// notifications. A later admitted user request can reopen report delivery.
func (s *WorkStore) PauseReports(ctx context.Context, principalID, botID string, paused bool) error {
	key := opaqueID("report-control-", principalID, botID)
	value := struct {
		Paused bool `json:"paused"`
	}{paused}
	fresh, err := s.db.Put(ctx, "report_control", key, value)
	if err != nil || fresh {
		return err
	}
	return s.db.Replace(ctx, "report_control", key, value)
}

// ReportsPaused reads the persistent stop boundary without changing it.
func (s *WorkStore) ReportsPaused(ctx context.Context, principalID, botID string) (bool, error) {
	var value struct {
		Paused bool `json:"paused"`
	}
	err := s.db.Get(ctx, "report_control", opaqueID("report-control-", principalID, botID), &value)
	if errorcode.CodeOf(err) == errorcode.NotFound {
		return false, nil
	}
	return value.Paused, err
}
