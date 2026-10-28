package session

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// CompactionReceiptTicket is an opaque accounting-only capability. Foreground
// activity revokes new dispatch and checkpoint rights, not settlement rights.
type CompactionReceiptTicket struct {
	ref                 SessionRef
	invocationID, token string
}

// BeginCompactionAttemptRequest reserves accounting before an actual dispatch.
// Invocation must contain only stable identity and StartedAt, not usage/outcome.
type BeginCompactionAttemptRequest struct {
	Window     IdleCompactionWindow
	Invocation model.Invocation
}

// CompactionAttemptResult is the only terminal data a receipt ticket can write.
type CompactionAttemptResult struct {
	Usage      model.Usage
	Outcome    string
	FinishedAt time.Time
}

// CompactionReceiptStore writes Store-constructed journal facts only. No method
// accepts arbitrary events, model messages, tool calls, or state updates.
type CompactionReceiptStore interface {
	BeginCompactionAttempt(context.Context, BeginCompactionAttemptRequest) (CompactionReceiptTicket, error)
	// CancelCompactionReservation is legal only after the start hook rejected,
	// proving that this exact attempt was never dispatched. It writes no receipt.
	CancelCompactionReservation(context.Context, BeginCompactionAttemptRequest) error
	StageCompactionReceipt(context.Context, CompactionReceiptTicket, CompactionAttemptResult) error
	FinalizeCompactionReceipt(context.Context, CompactionReceiptTicket) (*Event, error)
}

// CompactionAttemptRecord is bounded backend-private pending accounting state.
type CompactionAttemptRecord struct {
	Invocation  model.Invocation         `json:"invocation"`
	TurnID      string                   `json:"turn_id"`
	JobID       string                   `json:"job_id"`
	HostID      string                   `json:"host_id"`
	ClaimDigest string                   `json:"claim_digest"`
	Result      *CompactionAttemptResult `json:"result,omitempty"`
}

// BeginAttempt reserves one of eight per-Session pending slots before dispatch.
// There is no global ticket pool. Duplicate identities never authorize a replay.
func (s *IdleCompactionState) BeginAttempt(req BeginCompactionAttemptRequest, active SessionFence, loaded LoadedSession, now time.Time) (CompactionReceiptTicket, error) {
	if err := s.ValidateWindow(req.Window, active, loaded, now); err != nil {
		return CompactionReceiptTicket{}, err
	}
	in := req.Invocation
	if in.ID == "" || in.Provider == "" || in.Model == "" || in.StartedAt.IsZero() || in.Usage.IsReported() || in.Outcome != "" || !in.FinishedAt.IsZero() {
		return CompactionReceiptTicket{}, idleDenied("invalid attempt identity")
	}
	for _, event := range loaded.Events {
		if IsModelInvocationReceipt(event) && event.ID == in.ID {
			return CompactionReceiptTicket{}, idleDenied("attempt already finalized")
		}
	}
	token, _ := idleFingerprint(struct {
		Claim      string
		Invocation model.Invocation
	}{req.Window.token, in})
	ticket := CompactionReceiptTicket{ref: req.Window.ref, invocationID: in.ID, token: token}
	if old := s.Attempts[in.ID]; old != nil {
		oldHash, _ := idleFingerprint(old.Invocation)
		newHash, _ := idleFingerprint(in)
		if oldHash != newHash || !sessionFenceClaimMatches(old.ClaimDigest, token) {
			return CompactionReceiptTicket{}, idleDenied("attempt identity conflict")
		}
		return ticket, nil
	}
	if len(s.Attempts) >= 8 {
		return CompactionReceiptTicket{}, idleDenied("pending accounting capacity exhausted")
	}
	if s.Attempts == nil {
		s.Attempts = map[string]*CompactionAttemptRecord{}
	}
	s.Attempts[in.ID] = &CompactionAttemptRecord{Invocation: in, TurnID: s.Window.Source.TurnID, JobID: s.Window.Source.JobID, HostID: s.Window.Source.HostID, ClaimDigest: sessionFenceClaimDigest(token)}
	return ticket, nil
}

// StageReceipt seals one terminal payload even after the window was revoked.
func (s *IdleCompactionState) StageReceipt(ticket CompactionReceiptTicket, result CompactionAttemptResult) error {
	record := s.Attempts[ticket.invocationID]
	if record == nil || !sessionFenceClaimMatches(record.ClaimDigest, ticket.token) {
		return idleDenied("unknown accounting claim")
	}
	switch result.Outcome {
	case "completed", "failed", "cancelled", "unknown":
	default:
		return idleDenied("invalid attempt outcome")
	}
	if result.FinishedAt.IsZero() {
		return idleDenied("invalid attempt completion time")
	}
	if record.Result != nil {
		oldHash, _ := idleFingerprint(record.Result)
		newHash, _ := idleFingerprint(result)
		if oldHash != newHash {
			return idleDenied("sealed accounting payload conflict")
		}
		return nil
	}
	record.Result = &result
	return nil
}

// ReceiptEvent constructs a fixed journal entry, never a model-visible event.
func (s *IdleCompactionState) ReceiptEvent(ticket CompactionReceiptTicket, loaded LoadedSession) (*Event, error) {
	if ticket.token == "" || ticket.ref != NormalizeSessionRef(loaded.Session.SessionRef) {
		return nil, idleDenied("invalid receipt session")
	}
	if record := s.Attempts[ticket.invocationID]; record != nil {
		if !sessionFenceClaimMatches(record.ClaimDigest, ticket.token) || record.Result == nil {
			return nil, idleDenied("receipt is not sealed")
		}
		in := record.Invocation
		in.Usage = record.Result.Usage
		in.Outcome = record.Result.Outcome
		in.FinishedAt = record.Result.FinishedAt
		return idleAttemptReceipt(in, record, ticket.ref), nil
	}
	for _, event := range loaded.Events {
		if IsModelInvocationReceipt(event) && event.ID == ticket.invocationID {
			return CloneEvent(event), nil
		}
	}
	return nil, fmt.Errorf("session: pending compaction receipt %q not found", ticket.invocationID)
}

// SessionRef returns the bound identity without exposing bearer material.
func (w IdleCompactionWindow) SessionRef() SessionRef { return w.ref }

// SessionRef returns the bound identity without exposing bearer material.
func (g IdleCompactionGrant) SessionRef() SessionRef { return g.window.ref }

// SessionRef returns the bound identity without exposing bearer material.
func (t CompactionReceiptTicket) SessionRef() SessionRef { return t.ref }

// CloneIdleCompactionState detaches mutable pending records for a transaction.
func CloneIdleCompactionState(state IdleCompactionState) IdleCompactionState {
	out := state
	if state.Window != nil {
		w := *state.Window
		out.Window = &w
	}
	if state.Attempts != nil {
		out.Attempts = make(map[string]*CompactionAttemptRecord, len(state.Attempts))
		for id, p := range state.Attempts {
			r := *p
			if p.Result != nil {
				v := *p.Result
				r.Result = &v
			}
			out.Attempts[id] = &r
		}
	}
	return out
}

// ForgetReceipt drops pending state only in the transaction that appended it.
func (s *IdleCompactionState) ForgetReceipt(ticket CompactionReceiptTicket) {
	delete(s.Attempts, ticket.invocationID)
}

// RevokeWindow affects only this exact opportunity, never a later activation.
func (s *IdleCompactionState) RevokeWindow(window IdleCompactionWindow) {
	if w := s.Window; w != nil && w.Source.JobID == window.jobID && sessionFenceClaimMatches(w.ClaimDigest, window.token) {
		s.Invalidate()
	}
}

// CancelReservation removes only the original, unsealed pre-dispatch reservation.
// The caller must have proved that its invocation-start hook never returned nil.
func (s *IdleCompactionState) CancelReservation(req BeginCompactionAttemptRequest) error {
	record := s.Attempts[req.Invocation.ID]
	if record == nil {
		return nil
	}
	token, _ := idleFingerprint(struct {
		Claim      string
		Invocation model.Invocation
	}{req.Window.token, req.Invocation})
	if !sessionFenceClaimMatches(record.ClaimDigest, token) || record.Result != nil {
		return idleDenied("reservation is not cancellable")
	}
	delete(s.Attempts, req.Invocation.ID)
	return nil
}

// PriorHostCompactionReceiptRecovery is a separately authorized product-Host
// capability. Ordinary Stores and receipt tickets cannot declare an old owner
// dead. Recovery journals sealed results, or explicit unknown outcomes after a
// prior Host stopped before staging; it never replays provider requests.
type PriorHostCompactionReceiptRecovery interface {
	RecoverCompactionReceipts(context.Context, SessionRef, string) error
}

// RecoverReceipts builds fixed receipts under proven exclusive Host ownership.
// Unsealed records from the current live Host remain pending.
func (s *IdleCompactionState) RecoverReceipts(currentHost string, ref SessionRef, now time.Time) []*Event {
	var events []*Event
	if s.Window != nil && !s.Window.Revoked && s.Window.Source.HostID != currentHost {
		s.Invalidate()
	}
	ids := make([]string, 0, len(s.Attempts))
	for id := range s.Attempts {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		record := s.Attempts[id]
		if record.Result == nil && record.HostID == currentHost {
			continue
		}
		in := record.Invocation
		if record.Result != nil {
			in.Usage = record.Result.Usage
			in.Outcome = record.Result.Outcome
			in.FinishedAt = record.Result.FinishedAt
		} else {
			in.Outcome = "unknown"
			in.FinishedAt = now
			if in.FinishedAt.Before(in.StartedAt) {
				in.FinishedAt = in.StartedAt
			}
		}
		event := idleAttemptReceipt(in, record, ref)
		events = append(events, event)
		delete(s.Attempts, id)
	}
	return events
}

func idleAttemptReceipt(in model.Invocation, record *CompactionAttemptRecord, ref SessionRef) *Event {
	event := NewModelInvocationReceipt(in, "compaction")
	event.SessionID = ref.SessionID
	event.Time = in.FinishedAt
	sdk := event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)
	sdk["job_id"] = record.JobID
	sdk["source_turn_id"] = record.TurnID
	return event
}
