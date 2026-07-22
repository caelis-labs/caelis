package file

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// legacyV1DocumentVersion and the wire types below are the complete removal
// boundary for Session document v1. Version 1 predated durable participant
// Placement: reasoning_effort alone cannot safely reconstruct an ACP endpoint.
const legacyV1DocumentVersion = 1

const (
	legacyMigrationCategoryACPParticipant = "acp_participant"
	legacyMigrationReasonMissingPlacement = "missing_sealed_placement"
	legacyMigrationReasonInvalidPlacement = "invalid_sealed_placement"
)

// MigrationReport describes legacy Session records that this Store observed
// and could not retain safely. It is process-local operational state and is
// never persisted in the Session document.
type MigrationReport struct {
	FromVersion int
	Dropped     []MigrationDrop
}

// MigrationDrop identifies one legacy record by Session and stable source
// identity. Category and Reason are fixed codes; no Placement or Agent
// configuration is copied into diagnostics.
type MigrationDrop struct {
	SessionRef session.SessionRef
	Category   string
	Identity   string
	Reason     string
}

// MigrationReport returns a detached aggregate of legacy records observed by
// this Store. Repeated reads of the same record are reported only once.
func (s *Store) MigrationReport() MigrationReport {
	if s == nil {
		return MigrationReport{}
	}
	s.legacyMigrationMu.Lock()
	defer s.legacyMigrationMu.Unlock()
	return cloneMigrationReport(s.legacyMigration)
}

func (s *Store) recordMigrationReport(report MigrationReport) {
	if s == nil || report.FromVersion == 0 {
		return
	}
	s.legacyMigrationMu.Lock()
	defer s.legacyMigrationMu.Unlock()
	if s.legacyMigration.FromVersion == 0 {
		s.legacyMigration.FromVersion = report.FromVersion
	}
	for _, drop := range report.Dropped {
		drop.SessionRef = session.NormalizeSessionRef(drop.SessionRef)
		drop.Category = strings.TrimSpace(drop.Category)
		drop.Identity = strings.TrimSpace(drop.Identity)
		drop.Reason = strings.TrimSpace(drop.Reason)
		if migrationDropExists(s.legacyMigration.Dropped, drop) {
			continue
		}
		s.legacyMigration.Dropped = append(s.legacyMigration.Dropped, drop)
	}
}

func migrationDropExists(existing []MigrationDrop, candidate MigrationDrop) bool {
	for _, drop := range existing {
		if drop.SessionRef == candidate.SessionRef &&
			drop.Category == candidate.Category &&
			drop.Identity == candidate.Identity &&
			drop.Reason == candidate.Reason {
			return true
		}
	}
	return false
}

func cloneMigrationReport(in MigrationReport) MigrationReport {
	out := in
	out.Dropped = append([]MigrationDrop(nil), in.Dropped...)
	return out
}

type legacyV1Document struct {
	Kind                      string                    `json:"kind"`
	Version                   int                       `json:"version"`
	Session                   legacyV1Session           `json:"session"`
	State                     map[string]any            `json:"state"`
	PendingApprovals          map[string]*session.Event `json:"pending_approvals"`
	AppliedTransactions       map[string]bool           `json:"applied_transactions,omitempty"`
	AppliedTransactionDigests map[string]string         `json:"applied_transaction_digests,omitempty"`
	Lease                     *session.SessionLease     `json:"lease,omitempty"`
	LeaseEpoch                uint64                    `json:"lease_epoch,omitempty"`
}

type legacyV1Session struct {
	AppName      string `json:"app_name,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	WorkspaceKey string `json:"workspace_key,omitempty"`

	Revision     uint64                       `json:"revision,omitempty"`
	CWD          string                       `json:"cwd,omitempty"`
	Title        string                       `json:"title,omitempty"`
	Metadata     map[string]any               `json:"metadata,omitempty"`
	Controller   session.ControllerBinding    `json:"controller,omitempty"`
	Participants []legacyV1ParticipantBinding `json:"participants,omitempty"`
	CreatedAt    time.Time                    `json:"created_at,omitempty"`
	UpdatedAt    time.Time                    `json:"updated_at,omitempty"`
}

type legacyV1ParticipantBinding struct {
	ID              string                  `json:"id,omitempty"`
	Kind            session.ParticipantKind `json:"kind,omitempty"`
	Role            session.ParticipantRole `json:"role,omitempty"`
	AgentName       string                  `json:"agent_name,omitempty"`
	Label           string                  `json:"label,omitempty"`
	ReasoningEffort string                  `json:"reasoning_effort,omitempty"`
	// Placement appeared in documents that were written after the live binding
	// changed but before the document version was bumped. Only a sealed value is
	// safe to retain for ACP reattachment.
	Placement            placement.Placement `json:"placement,omitempty"`
	SessionID            string              `json:"session_id,omitempty"`
	Source               string              `json:"source,omitempty"`
	ParentTurnID         string              `json:"parent_turn_id,omitempty"`
	DelegationID         string              `json:"delegation_id,omitempty"`
	AttachmentGeneration string              `json:"attachment_generation,omitempty"`
	ContextSyncSeq       uint64              `json:"context_sync_seq,omitempty"`
	AttachedAt           time.Time           `json:"attached_at,omitempty"`
	ControllerRef        string              `json:"controller_ref,omitempty"`
}

func decodeLegacyV1Document(data []byte) (persistedDocument, error) {
	doc, _, err := decodeLegacyV1DocumentWithReport(data)
	return doc, err
}

func decodeLegacyV1DocumentWithReport(data []byte) (persistedDocument, MigrationReport, error) {
	var legacy legacyV1Document
	if err := json.Unmarshal(data, &legacy); err != nil {
		return persistedDocument{}, MigrationReport{}, err
	}

	ref := session.NormalizeSessionRef(session.SessionRef{
		AppName:      legacy.Session.AppName,
		UserID:       legacy.Session.UserID,
		SessionID:    legacy.Session.SessionID,
		WorkspaceKey: legacy.Session.WorkspaceKey,
	})
	report := MigrationReport{FromVersion: legacyV1DocumentVersion}
	participants := make([]session.ParticipantBinding, 0, len(legacy.Session.Participants))
	for index, raw := range legacy.Session.Participants {
		binding := session.ParticipantBinding{
			ID:                   raw.ID,
			Kind:                 raw.Kind,
			Role:                 raw.Role,
			AgentName:            raw.AgentName,
			Label:                raw.Label,
			Placement:            raw.Placement,
			SessionID:            raw.SessionID,
			Source:               raw.Source,
			ParentTurnID:         raw.ParentTurnID,
			DelegationID:         raw.DelegationID,
			AttachmentGeneration: raw.AttachmentGeneration,
			ContextSyncSeq:       raw.ContextSyncSeq,
			AttachedAt:           raw.AttachedAt,
			ControllerRef:        raw.ControllerRef,
		}
		if binding.Kind == session.ParticipantKindACP {
			if err := placement.ValidateSealed(binding.Placement); err != nil {
				reason := legacyMigrationReasonInvalidPlacement
				normalized := placement.Normalize(binding.Placement)
				if normalized.ConfigFingerprint == "" || normalized.Fingerprint == "" {
					reason = legacyMigrationReasonMissingPlacement
				}
				report.Dropped = append(report.Dropped, MigrationDrop{
					SessionRef: ref,
					Category:   legacyMigrationCategoryACPParticipant,
					Identity:   fmt.Sprintf("participants[%d]", index),
					Reason:     reason,
				})
				continue
			}
		}
		participants = append(participants, binding)
	}

	return persistedDocument{
		Kind:    documentKind,
		Version: documentVersion,
		Session: session.Session{
			SessionRef:   ref,
			Revision:     legacy.Session.Revision,
			CWD:          legacy.Session.CWD,
			Title:        legacy.Session.Title,
			Metadata:     legacy.Session.Metadata,
			Controller:   legacy.Session.Controller,
			Participants: participants,
			CreatedAt:    legacy.Session.CreatedAt,
			UpdatedAt:    legacy.Session.UpdatedAt,
		},
		State:                     legacy.State,
		PendingApprovals:          legacy.PendingApprovals,
		AppliedTransactions:       legacy.AppliedTransactions,
		AppliedTransactionDigests: legacy.AppliedTransactionDigests,
		Lease:                     legacy.Lease,
		LeaseEpoch:                legacy.LeaseEpoch,
	}, report, nil
}
