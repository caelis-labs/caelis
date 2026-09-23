package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
)

// Operation is a permanent application request anchor. An empty result means
// dispatch may have happened and its outcome is unknown; it never permits retry.
type Operation struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
	// Request is immutable admitted mutation intent, used to recover its target.
	// It records dispatch intent and never replaces canonical Session history.
	Request json.RawMessage `json:"request"`
	Result  json.RawMessage `json:"result,omitempty"`
}

// BeginOperation commits intent before dispatch. Only a true fresh return grants
// one dispatch. Requerying an incomplete operation never grants dispatch again.
func (s *Store) BeginOperation(ctx context.Context, scope Scope, id string, request any) (Operation, bool, error) {
	if !validID(id) {
		return Operation{}, false, ErrInvalid
	}
	body, err := encode(request)
	if err != nil {
		return Operation{}, false, err
	}
	digest := digestBytes(body)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err = s.active(ctx, scope); err != nil {
		return Operation{}, false, err
	}
	op, err := s.operation(ctx, scope, id)
	if err == nil {
		if op.Digest != digest {
			return Operation{}, false, ErrConflict
		}
		return op, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Operation{}, false, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO app_operations(principal,application,connection,operation,digest,request) VALUES(?,?,?,?,?,?)`, scope.PrincipalID, scope.ApplicationID, scope.ConnectionID, id, digest, body)
	if err != nil {
		return Operation{}, false, err
	}
	return Operation{ID: id, Digest: digest, Request: body}, true, nil
}
func (s *Store) operation(ctx context.Context, scope Scope, id string) (Operation, error) {
	op := Operation{ID: id}
	var request, result []byte
	err := s.db.QueryRowContext(ctx, `SELECT digest,request,result FROM app_operations WHERE principal=? AND application=? AND connection=? AND operation=?`, scope.PrincipalID, scope.ApplicationID, scope.ConnectionID, id).Scan(&op.Digest, &request, &result)
	op.Request, op.Result = request, result
	if err == nil && (!json.Valid(request) || digestBytes(request) != op.Digest) {
		return Operation{}, ErrInvalid
	}
	return op, notFound(err)
}

// GetOperation observes a durable request without restoring mutation authority.
func (s *Store) GetOperation(ctx context.Context, scope Scope, id string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.connection(ctx, scope); err != nil {
		return Operation{}, err
	}
	return s.operation(ctx, scope, id)
}

// CompleteOperation records one immutable dispatch result; changed late results conflict.
func (s *Store) CompleteOperation(ctx context.Context, scope Scope, id string, result json.RawMessage) error {
	if !json.Valid(result) {
		return ErrInvalid
	}
	body, err := encode(result)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err = s.active(ctx, scope); err != nil {
		return err
	}
	op, err := s.operation(ctx, scope, id)
	if err != nil {
		return err
	}
	if len(op.Result) > 0 {
		if !bytes.Equal(op.Result, body) {
			return ErrConflict
		}
		return nil
	}
	_, err = s.db.ExecContext(ctx, `UPDATE app_operations SET result=? WHERE connection=? AND operation=? AND result IS NULL`, body, scope.ConnectionID, id)
	return err
}

// SessionID derives the one native Session address for an admitted application
// creation operation. Callers must first authenticate scope and validate the
// operation ID; deriving an address does not grant access or dispatch authority.
func SessionID(scope Scope, operationID string) string {
	return "application-" + digestBytes([]byte(scope.PrincipalID+"\x00"+scope.ApplicationID+"\x00"+scope.ConnectionID+"\x00"+operationID))
}
