package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrIdleCompactionRevoked means a speculative opportunity is no longer valid.
var ErrIdleCompactionRevoked = errors.New("session: idle compaction opportunity revoked")

// IdleCompactionWindow is an opaque opportunity, not ordinary mutation authority.
// Its bearer material is deliberately excluded from JSON and observation APIs.
type IdleCompactionWindow struct {
	ref          SessionRef
	jobID, token string
}

// IdleCompactionGrant authorizes exactly one fixed checkpoint for two seconds.
type IdleCompactionGrant struct {
	window IdleCompactionWindow
	token  string
}

// IdleCompactionCoverage fixes Runtime's coverage calculation at handoff. Store
// compares this proof without owning prompt projection or summary policy.
type IdleCompactionCoverage struct {
	Revision   int    `json:"revision"`
	EventCount int    `json:"event_count"`
	EndSeq     uint64 `json:"end_seq"`
	EndID      string `json:"end_id"`
}

// IdleCompactionSource binds a completed Turn's source, activation and job.
type IdleCompactionSource struct {
	JobID        string                 `json:"job_id"`
	HostID       string                 `json:"host_id"`
	ActivationID string                 `json:"activation_id"`
	TurnID       string                 `json:"turn_id"`
	Stamp        string                 `json:"stamp"`
	ThroughSeq   uint64                 `json:"through_seq"`
	Coverage     IdleCompactionCoverage `json:"coverage"`
}

// ReleaseForIdleCompactionRequest carries the original fence claim and fixed
// transition identity. Reconciliation must reuse this exact value.
type ReleaseForIdleCompactionRequest struct {
	Release ReleaseSessionFenceRequest
	Source  IdleCompactionSource
	window  IdleCompactionWindow
}

// NewIdleCompactionRelease fixes the window identity before the first write.
func NewIdleCompactionRelease(fence SessionFence, source IdleCompactionSource) ReleaseForIdleCompactionRequest {
	return ReleaseForIdleCompactionRequest{Release: SessionFenceReleaseRequest(fence), Source: source, window: IdleCompactionWindow{ref: NormalizeSessionRef(fence.SessionRef), jobID: source.JobID, token: uuid.NewString()}}
}

// IssueIdleCompactionGrantRequest binds the one-shot grant to the complete
// checkpoint payload. It cannot authorize an arbitrary later Event.
type IssueIdleCompactionGrantRequest struct {
	Window IdleCompactionWindow
	Event  *Event
}

// AppendIdleCompactionCheckpointRequest contains a fixed checkpoint plus CAS.
type AppendIdleCompactionCheckpointRequest struct {
	Grant            IdleCompactionGrant
	Event            *Event
	ExpectedRevision uint64
}

// IdleCompactionStore is the narrow Store-owned transition and commit surface.
// Implementations validate inside their actual lock/WAL transaction.
type IdleCompactionStore interface {
	ReleaseSessionFenceForIdleCompaction(context.Context, ReleaseForIdleCompactionRequest) (IdleCompactionWindow, error)
	IssueIdleCompactionGrant(context.Context, IssueIdleCompactionGrantRequest) (IdleCompactionGrant, error)
	AppendIdleCompactionCheckpoint(context.Context, AppendIdleCompactionCheckpointRequest) (*Event, error)
	RevokeIdleCompaction(context.Context, IdleCompactionWindow) error
}

// IdleCompactionState is backend-private durable state. It must never be
// exposed through Session observations or accepted from transport payloads.
type IdleCompactionState struct {
	Epoch    uint64                              `json:"epoch,omitempty"`
	Window   *IdleCompactionWindowRecord         `json:"window,omitempty"`
	Attempts map[string]*CompactionAttemptRecord `json:"attempts,omitempty"`
}

// IdleCompactionWindowRecord stores only claim digests, never bearer tokens.
type IdleCompactionWindowRecord struct {
	Source         IdleCompactionSource `json:"source"`
	Epoch          uint64               `json:"epoch"`
	ClaimDigest    string               `json:"claim_digest"`
	ExpiresAt      time.Time            `json:"expires_at"`
	GrantDigest    string               `json:"grant_digest,omitempty"`
	GrantExpiresAt time.Time            `json:"grant_expires_at,omitempty"`
	CandidateHash  string               `json:"candidate_hash,omitempty"`
	CandidateID    string               `json:"candidate_id,omitempty"`
	Consumed       bool                 `json:"consumed,omitempty"`
	Revoked        bool                 `json:"revoked,omitempty"`
}

// Invalidate revokes checkpoint authority without revoking actual-attempt accounting.
func (s *IdleCompactionState) Invalidate() {
	s.Epoch++
	if s.Window != nil {
		s.Window.Revoked = true
	}
}

// CheckpointSourceStamp fingerprints source context, state and configuration.
// Journal-only receipts and display title/revision updates are not model input.
func CheckpointSourceStamp(loaded LoadedSession) (string, error) {
	active := CloneSession(loaded.Session)
	active.Revision = 0
	active.UpdatedAt = time.Time{}
	active.Title = ""
	events := make([]*Event, 0, len(loaded.Events))
	for _, e := range loaded.Events {
		if IsMainInvocationVisibleEvent(e) {
			events = append(events, e)
		}
	}
	return idleFingerprint(struct {
		Session Session
		State   map[string]any
		Events  []*Event
	}{active, loaded.State, events})
}

func idleFingerprint(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func idleDenied(detail string) error { return fmt.Errorf("%w: %s", ErrIdleCompactionRevoked, detail) }

// Release atomically validates a completed source and installs one 180s window.
// The backend clears the fence only after this succeeds in the same transaction.
func (s *IdleCompactionState) Release(req ReleaseForIdleCompactionRequest, active SessionFence, loaded LoadedSession, now time.Time) (IdleCompactionWindow, error) {
	if w := s.Window; w != nil && w.Source == req.Source && sessionFenceClaimMatches(w.ClaimDigest, req.window.token) {
		if w.Revoked || w.Epoch != s.Epoch {
			return IdleCompactionWindow{}, idleDenied("transition already revoked")
		}
		return req.window, nil
	}
	if !SessionFenceReleaseAuthorized(active, req.Release) {
		return IdleCompactionWindow{}, idleDenied("original fence claim required")
	}
	source := req.Source
	if source.JobID == "" || source.HostID != active.OwnerID || source.ActivationID == "" || source.TurnID == "" || source.ThroughSeq == 0 || source.Stamp == "" || req.window.jobID != source.JobID {
		return IdleCompactionWindow{}, idleDenied("incomplete source")
	}
	stamp, err := CheckpointSourceStamp(loaded)
	if err != nil {
		return IdleCompactionWindow{}, err
	}
	var through uint64
	for _, event := range loaded.Events {
		if IsMainInvocationVisibleEvent(event) {
			through = max(through, event.Seq)
		}
	}
	if stamp != source.Stamp || through != source.ThroughSeq {
		return IdleCompactionWindow{}, idleDenied("source changed")
	}
	proof := source.Coverage
	if proof.Revision <= 0 || proof.EventCount <= 0 || proof.EndSeq == 0 || proof.EndSeq > through || proof.EndID == "" {
		return IdleCompactionWindow{}, idleDenied("invalid source coverage proof")
	}
	found := false
	count := 0
	for _, event := range loaded.Events {
		if !IsMainInvocationVisibleEvent(event) {
			continue
		}
		count++
		if event.Seq == proof.EndSeq && event.ID == proof.EndID {
			found = true
		}
	}
	if !found || proof.EventCount > count {
		return IdleCompactionWindow{}, idleDenied("coverage proof does not name source events")
	}
	s.Invalidate()
	s.Window = &IdleCompactionWindowRecord{Source: source, Epoch: s.Epoch, ClaimDigest: sessionFenceClaimDigest(req.window.token), ExpiresAt: now.Add(180 * time.Second)}
	return req.window, nil
}

// ValidateWindow is used at actual attempt admission and grant issue.
func (s *IdleCompactionState) ValidateWindow(window IdleCompactionWindow, active SessionFence, loaded LoadedSession, now time.Time) error {
	w := s.Window
	if w == nil || w.Revoked || w.Consumed || w.Epoch != s.Epoch || w.Source.JobID != window.jobID || NormalizeSessionRef(loaded.Session.SessionRef) != window.ref || !sessionFenceClaimMatches(w.ClaimDigest, window.token) || !now.Before(w.ExpiresAt) || SessionFenceIsHeld(active) {
		return idleDenied("window is not current")
	}
	stamp, err := CheckpointSourceStamp(loaded)
	if err != nil {
		return err
	}
	if stamp != w.Source.Stamp {
		return idleDenied("source changed")
	}
	return nil
}

func idleCheckpointHash(event *Event) (string, error) {
	if event == nil || event.ID == "" || event.IdempotencyKey != event.ID || event.Type != EventTypeCompact || event.Tool != nil || event.Lifecycle != nil || event.Notice != nil || event.PlanPayload != nil || event.Journal != nil || event.ChildOrigin != nil || event.Invocation != nil || event.ApprovalRequestID != "" || event.Seq != 0 || event.Message == nil || len(event.Message.ToolCalls()) != 0 || strings.TrimSpace(event.Message.TextContent()) == "" || !IsMainInvocationVisibleEvent(event) {
		return "", idleDenied("only a fixed canonical checkpoint is allowed")
	}
	if event.SessionID == "" || event.ContextUsage != nil || event.Compaction != nil || event.Message.Role != "user" {
		return "", idleDenied("invalid checkpoint envelope")
	}
	if event.Protocol != nil {
		expected := &EventProtocol{Method: ProtocolMethodContextCheckpoint, Update: &ProtocolUpdate{SessionUpdate: "compact", Content: ProtocolTextContent(event.Message.TextContent())}}
		if !reflect.DeepEqual(event.Protocol, expected) {
			return "", idleDenied("unexpected checkpoint protocol payload")
		}
	}
	for _, part := range event.Message.Parts {
		if part.Text == nil {
			return "", idleDenied("checkpoint message must contain only text")
		}
	}
	data, ok := CheckpointDataFromEvent(event)
	if !ok || data.ContractVersion != 1 || data.Revision <= 0 || data.SourceEventCount <= 0 || data.SummarizedThroughSeq == 0 || data.SummarizedThroughID == "" {
		return "", idleDenied("invalid checkpoint coverage")
	}
	return idleFingerprint(event)
}

// IssueGrant mints at most one short grant. Reissuing never extends its deadline.
func (s *IdleCompactionState) IssueGrant(req IssueIdleCompactionGrantRequest, active SessionFence, loaded LoadedSession, now time.Time) (IdleCompactionGrant, error) {
	if err := s.ValidateWindow(req.Window, active, loaded, now); err != nil {
		return IdleCompactionGrant{}, err
	}
	hash, err := idleCheckpointHash(req.Event)
	if err != nil {
		return IdleCompactionGrant{}, err
	}
	data, _ := CheckpointDataFromEvent(req.Event)
	proof := s.Window.Source.Coverage
	if req.Event.SessionID != loaded.Session.SessionID || data.Revision != proof.Revision || data.SourceEventCount != proof.EventCount || data.SummarizedThroughSeq != proof.EndSeq || data.SummarizedThroughID != proof.EndID {
		return IdleCompactionGrant{}, idleDenied("checkpoint coverage differs from handoff proof")
	}
	if data.SummarizedThroughSeq > s.Window.Source.ThroughSeq {
		return IdleCompactionGrant{}, idleDenied("coverage exceeds source")
	}
	// Derive the fixed claim from the original opaque window and payload. A lost
	// issue response can be reconciled with the same request, without renewal.
	token, _ := idleFingerprint([]string{req.Window.token, hash})
	w := s.Window
	if w.GrantDigest != "" {
		if w.CandidateHash != hash || !now.Before(w.GrantExpiresAt) {
			return IdleCompactionGrant{}, idleDenied("grant already issued")
		}
	} else {
		w.GrantDigest = sessionFenceClaimDigest(token)
		w.GrantExpiresAt = now.Add(2 * time.Second)
		w.CandidateHash = hash
		w.CandidateID = req.Event.ID
	}
	return IdleCompactionGrant{window: req.Window, token: token}, nil
}

// ValidateAppend runs immediately before the backend's commit point. Idempotent
// readback of an already durable identical event precedes this authorization.
func (s *IdleCompactionState) ValidateAppend(req AppendIdleCompactionCheckpointRequest, active SessionFence, loaded LoadedSession, now time.Time) error {
	if err := s.ValidateWindow(req.Grant.window, active, loaded, now); err != nil {
		return err
	}
	w := s.Window
	hash, err := idleCheckpointHash(req.Event)
	if err != nil {
		return err
	}
	if !sessionFenceClaimMatches(w.GrantDigest, req.Grant.token) || !now.Before(w.GrantExpiresAt) || hash != w.CandidateHash || req.Event.ID != w.CandidateID {
		return idleDenied("grant does not authorize this checkpoint")
	}
	return CheckExpectedRevision(loaded.Session, &req.ExpectedRevision)
}

// IdleCheckpointReplay compares an original durable identity and full payload.
// A consumed or revoked grant never prevents this readback of a committed result.
func IdleCheckpointReplay(loaded LoadedSession, event *Event, now time.Time) (*Event, error) {
	if event == nil {
		return nil, idleDenied("checkpoint required")
	}
	for _, old := range loaded.Events {
		if old.ID != event.ID && (event.IdempotencyKey == "" || old.IdempotencyKey != event.IdempotencyKey) {
			continue
		}
		prepared, err := PrepareEventsForAppend(PrepareEventsForAppendRequest{SessionID: loaded.Session.SessionID, Events: []*Event{event}, ExistingEvents: []*Event{old}, Now: now})
		if err != nil {
			return nil, err
		}
		if len(prepared.Persisted) == 0 && len(prepared.Events) == 1 {
			return prepared.Events[0], nil
		}
		return nil, idleDenied("checkpoint identity conflict")
	}
	return nil, nil
}
