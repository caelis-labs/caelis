package sessionprofile

import (
	"context"
	"fmt"
	"github.com/caelis-labs/caelis/control/internal/operationreceipt"
	"strings"
)

// BackgroundGrant records an application's attestation of prior user authorization.
// Source names the application-owned authorized schedule or trigger; neither a
// summary nor the model can create or infer this authorization. Revocation only
// prevents future prompt admissions, not already accepted work.
type BackgroundGrant struct {
	ID string `json:"id"`
	Scope
	SessionID                string `json:"session_id"`
	Source                   string `json:"source"`
	AuthorizationOperationID string `json:"authorization_operation_id"`
	Revoked                  bool   `json:"revoked"`
}

// BackgroundGrantRequest creates a grant for an existing application Session.
// OperationID is a durable mutation key, not the authorization evidence itself.
type BackgroundGrantRequest struct {
	OperationID              string `json:"operation_id"`
	Source                   string `json:"source"`
	AuthorizationOperationID string `json:"authorization_operation_id"`
}

func backgroundGrantID(scope Scope, operation string) string {
	return stableID("background-grant-", scope.PrincipalID, operation)
}

// CreateBackgroundGrant persists one immutable authorization attestation. Its
// intent and grant commit atomically in the permanent application operation
// namespace; a changed request or an ID used by another mutation conflicts.
func (s *Store) CreateBackgroundGrant(ctx context.Context, scope Scope, sessionID string, req BackgroundGrantRequest) (BackgroundGrant, error) {
	if !validID(sessionID) || !validID(req.OperationID) || !validID(req.AuthorizationOperationID) ||
		!validID(req.Source) || strings.TrimSpace(req.Source) == "" {
		return BackgroundGrant{}, ErrInvalid
	}
	if !operationreceipt.HasTransaction(ctx) {
		return BackgroundGrant{}, ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkScope(ctx, scope); err != nil {
		return BackgroundGrant{}, err
	}
	binding, err := s.binding(ctx, scope, sessionID)
	if err != nil {
		return BackgroundGrant{}, err
	}
	if binding.Archived {
		return BackgroundGrant{}, ErrRevoked
	}
	grant := BackgroundGrant{ID: backgroundGrantID(scope, req.OperationID), Scope: scope,
		SessionID: sessionID, Source: req.Source, AuthorizationOperationID: req.AuthorizationOperationID}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BackgroundGrant{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO session_background_grants(id,principal,session,operation,source,authorization_operation) VALUES(?,?,?,?,?,?)`,
		grant.ID, scope.PrincipalID, sessionID, req.OperationID, req.Source, req.AuthorizationOperationID); err != nil {
		return BackgroundGrant{}, err
	}
	if operationreceipt.HasTransaction(ctx) {
		body, err := encode(grant)
		if err != nil {
			return BackgroundGrant{}, err
		}
		if err := operationreceipt.CompleteTransaction(ctx, tx, body); err != nil {
			return BackgroundGrant{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return BackgroundGrant{}, err
	}
	return grant, nil
}

func scanBackgroundGrant(row interface{ Scan(...any) error }) (BackgroundGrant, error) {
	var g BackgroundGrant
	err := row.Scan(&g.ID, &g.PrincipalID, &g.SessionID, &g.Source, &g.AuthorizationOperationID, &g.Revoked)
	return g, notFound(err)
}

func (s *Store) backgroundGrant(ctx context.Context, scope Scope, sessionID, id string) (BackgroundGrant, error) {
	return scanBackgroundGrant(s.db.QueryRowContext(ctx, `SELECT id,principal,session,source,authorization_operation,revoked
		FROM session_background_grants WHERE id=? AND principal=?  AND session=?`,
		id, scope.PrincipalID, sessionID))
}

// GetBackgroundGrant returns only the grant bound to this exact connection and Session.
func (s *Store) GetBackgroundGrant(ctx context.Context, scope Scope, sessionID, id string) (BackgroundGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.binding(ctx, scope, sessionID); err != nil {
		return BackgroundGrant{}, err
	}
	return s.backgroundGrant(ctx, scope, sessionID, id)
}

// ListBackgroundGrants returns durable grants, including revoked ones.
func (s *Store) ListBackgroundGrants(ctx context.Context, scope Scope, sessionID string) ([]BackgroundGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.binding(ctx, scope, sessionID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,principal,session,source,authorization_operation,revoked
		FROM session_background_grants WHERE principal=?  AND session=? ORDER BY id`,
		scope.PrincipalID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := []BackgroundGrant{}
	for rows.Next() {
		g, err := scanBackgroundGrant(rows)
		if err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// RevokeBackgroundGrant permanently closes new prompt admissions without
// cancelling already admitted Turns or revoking the application connection.
func (s *Store) RevokeBackgroundGrant(ctx context.Context, scope Scope, sessionID, id string) (BackgroundGrant, error) {
	if !operationreceipt.HasTransaction(ctx) {
		return BackgroundGrant{}, ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkScope(ctx, scope); err != nil {
		return BackgroundGrant{}, err
	}
	if _, err := s.binding(ctx, scope, sessionID); err != nil {
		return BackgroundGrant{}, err
	}
	g, err := s.backgroundGrant(ctx, scope, sessionID, id)
	if err != nil {
		return g, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BackgroundGrant{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `UPDATE session_background_grants SET revoked=1 WHERE id=? AND principal=? AND session=?`, id, scope.PrincipalID, sessionID); err != nil {
		return BackgroundGrant{}, err
	}
	g.Revoked = true
	body, err := encode(g)
	if err != nil {
		return BackgroundGrant{}, err
	}
	if err = operationreceipt.CompleteTransaction(ctx, tx, body); err != nil {
		return BackgroundGrant{}, err
	}
	return g, tx.Commit()
}

// AdmitBackgroundSource verifies live authority at actual prompt admission,
// under the same lock as grant revocation. A previously admitted Turn is not
// rechecked or cancelled when the grant is later revoked.
func (s *Store) AdmitBackgroundSource(ctx context.Context, scope Scope, sessionID, grantID, operationID string) (Source, error) {
	if !validID(grantID) || !validID(operationID) {
		return Source{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkScope(ctx, scope); err != nil {
		return Source{}, err
	}
	binding, err := s.binding(ctx, scope, sessionID)
	if err != nil {
		return Source{}, err
	}
	g, err := s.backgroundGrant(ctx, scope, sessionID, grantID)
	if err != nil {
		return Source{}, err
	}
	if g.Revoked {
		return Source{}, fmt.Errorf("%w: background grant revoked", ErrRevoked)
	}
	if binding.Archived {
		return Source{}, ErrRevoked
	}
	return Source{Kind: "authorized_background", OperationID: operationID, GrantID: g.ID, AuthorizedSource: g.Source}, nil
}
