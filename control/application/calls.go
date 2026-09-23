package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const maxCallBytes = 1 << 20

func callID(c CallContext) string {
	return stableID("app-call-", c.PrincipalID, c.ApplicationID, c.ConnectionID, c.SessionID, c.TurnID, c.ItemID)
}

// Invoke persists an intent before waking consumers, then waits for a terminal
// result using notifications and the lease deadline. It never executes an
// application callback itself or retries an uncertain external effect.
func (s *Store) Invoke(ctx context.Context, c CallContext, name string, args json.RawMessage) (result CallResult, invokeErr error) {
	id, err := s.enqueue(ctx, c, name, args)
	if err != nil {
		return CallResult{}, err
	}
	defer func() {
		if ctx.Err() != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			invokeErr = errors.Join(ctx.Err(), s.CancelCall(cleanup, c.Scope, c.SessionID, id))
		}
	}()
	for {
		s.mu.Lock()
		call, changed, remaining, err := s.observeCall(ctx, c.Scope, c.SessionID, id)
		s.mu.Unlock()
		if err != nil {
			return CallResult{}, err
		}
		if result, done, err := terminalResult(call); done {
			return result, err
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return CallResult{Outcome: "unknown", Content: json.RawMessage(`null`)}, ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
			// Observe rechecks the live lease; a renewal may have moved its deadline.
		}
	}
}
func (s *Store) enqueue(ctx context.Context, c CallContext, name string, args json.RawMessage) (string, error) {
	if !validID(c.SessionID) || !validID(c.TurnID) || !validID(c.ItemID) || !validID(c.CallID) || !validID(c.ToolsVersion) || len(args) > maxCallBytes || !json.Valid(args) {
		return "", ErrInvalid
	}
	if err := ValidateSource(c.Source); err != nil {
		return "", err
	}
	raw, err := encode(args)
	if err != nil {
		return "", err
	}
	call := Call{ID: callID(c), CallContext: c, Name: name, Arguments: raw, State: "pending"}
	body, err := encode(call)
	if err != nil {
		return "", err
	}
	digest := digestBytes(body)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err = s.active(ctx, c.Scope); err != nil {
		return "", err
	}
	binding, err := s.binding(ctx, c.Scope, c.SessionID)
	if err != nil {
		return "", err
	}
	if binding.Archived {
		return "", ErrRevoked
	}
	// Revision zero is the pre-revision baseline callback representation.
	// Existing in-flight calls still bind to creation revision 1 after upgrade.
	revision := c.ConfigurationRevision
	if revision == 0 {
		revision = 1
	}
	configuration, err := s.configuration(ctx, c.SessionID, revision)
	if err != nil {
		return "", err
	}
	if c.ToolsVersion != configuration.Profile.ToolsVersion {
		return "", ErrConflict
	}
	found := false
	for _, def := range configuration.Profile.Tools {
		if def.Name != name {
			continue
		}
		found = true
		schema, err := resolveSchema(def.InputSchema)
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		var value any
		if err = json.Unmarshal(raw, &value); err != nil {
			return "", ErrInvalid
		}
		if err = schema.Validate(value); err != nil {
			return "", fmt.Errorf("%w: tool arguments: %w", ErrInvalid, err)
		}
		break
	}
	if !found {
		return "", ErrUnauthorized
	}
	var oldDigest string
	err = s.db.QueryRowContext(ctx, `SELECT digest FROM app_calls WHERE connection=? AND session=? AND call=?`, c.ConnectionID, c.SessionID, call.ID).Scan(&oldDigest)
	if err == nil {
		if oldDigest != digest {
			return "", ErrConflict
		}
		return call.ID, nil
	}
	if !errors.Is(notFound(err), ErrNotFound) {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO app_calls(principal,application,connection,session,call,digest,body,state) VALUES(?,?,?,?,?,?,?,'pending')`, c.PrincipalID, c.ApplicationID, c.ConnectionID, c.SessionID, call.ID, digest, body)
	if err != nil {
		return "", err
	}
	s.signal()
	return call.ID, nil
}
func terminalResult(call Call) (CallResult, bool, error) {
	switch call.State {
	case "completed":
		if call.Result == nil {
			return CallResult{}, true, ErrInvalid
		}
		return *call.Result, true, nil
	case "unknown":
		return CallResult{Outcome: "unknown", Content: json.RawMessage(`null`)}, true, nil
	case "cancelled":
		return CallResult{}, true, ErrCancelled
	default:
		return CallResult{}, false, nil
	}
}
func (s *Store) terminalize(ctx context.Context, scope Scope) error {
	result, err := s.db.ExecContext(ctx, `UPDATE app_calls SET state=CASE state WHEN 'pending' THEN 'cancelled' ELSE 'unknown' END WHERE connection=? AND state IN ('pending','claimed')`, scope.ConnectionID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if n > 0 {
		s.signal()
	}
	return err
}
func (s *Store) observeCall(ctx context.Context, scope Scope, session, id string) (Call, <-chan struct{}, time.Duration, error) {
	c, err := s.connection(ctx, scope)
	if err != nil {
		return Call{}, nil, 0, err
	}
	if c.Revoked || !s.now().Before(c.ExpiresAt) {
		if err = s.terminalize(ctx, scope); err != nil {
			return Call{}, nil, 0, err
		}
	}
	call, err := s.call(ctx, scope, session, id)
	return call, s.changed, c.ExpiresAt.Sub(s.now()), err
}
func (s *Store) call(ctx context.Context, scope Scope, session, id string) (Call, error) {
	if _, err := s.binding(ctx, scope, session); err != nil {
		return Call{}, err
	}
	var raw, result []byte
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT body,state,result FROM app_calls WHERE principal=? AND application=? AND connection=? AND session=? AND call=?`, scope.PrincipalID, scope.ApplicationID, scope.ConnectionID, session, id).Scan(&raw, &state, &result)
	if err != nil {
		return Call{}, notFound(err)
	}
	var call Call
	if err = json.Unmarshal(raw, &call); err != nil {
		return Call{}, err
	}
	call.State = state
	if len(result) > 0 {
		call.Result = new(CallResult)
		if err = json.Unmarshal(result, call.Result); err != nil {
			return Call{}, err
		}
	}
	return call, nil
}

// GetCall reads a receipt by Call.ID, requiring exact scope and Session ownership.
func (s *Store) GetCall(ctx context.Context, scope Scope, session, id string) (Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, _, _, err := s.observeCall(ctx, scope, session, id)
	return call, err
}

// ListCalls lists durable receipts, including terminal and unclaimed intents.
func (s *Store) ListCalls(ctx context.Context, scope Scope, session string) ([]Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls(ctx, scope, session)
}
func (s *Store) listCalls(ctx context.Context, scope Scope, session string) ([]Call, error) {
	c, err := s.connection(ctx, scope)
	if err != nil {
		return nil, err
	}
	if _, err = s.binding(ctx, scope, session); err != nil {
		return nil, err
	}
	if c.Revoked || !s.now().Before(c.ExpiresAt) {
		if err = s.terminalize(ctx, scope); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT body,state,result FROM app_calls WHERE principal=? AND application=? AND connection=? AND session=? ORDER BY rowid`, scope.PrincipalID, scope.ApplicationID, scope.ConnectionID, session)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	calls := []Call{}
	for rows.Next() {
		var raw, result []byte
		var state string
		var call Call
		if err = rows.Scan(&raw, &state, &result); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &call); err != nil {
			return nil, err
		}
		call.State = state
		if len(result) > 0 {
			call.Result = new(CallResult)
			if err = json.Unmarshal(result, call.Result); err != nil {
				return nil, err
			}
		}
		calls = append(calls, call)
	}
	return calls, rows.Err()
}

// WaitCalls returns available unclaimed receipts, or waits without polling until
// a store notification, lease expiry, shutdown, or caller cancellation occurs.
func (s *Store) WaitCalls(ctx context.Context, scope Scope, session string) ([]Call, error) {
	for {
		s.mu.Lock()
		c, err := s.active(ctx, scope)
		var calls []Call
		if err == nil {
			calls, err = s.listCalls(ctx, scope, session)
		}
		changed := s.changed
		remaining := c.ExpiresAt.Sub(s.now())
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		pending := []Call{}
		for _, call := range calls {
			if call.State == "pending" {
				pending = append(pending, call)
			}
		}
		if len(pending) > 0 {
			return pending, nil
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// ClaimCall durably grants one consumer permission to dispatch this effect. A
// second claim, including the same consumer's requery, never grants it again.
func (s *Store) ClaimCall(ctx context.Context, scope Scope, session, id string) (Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.active(ctx, scope); err != nil {
		return Call{}, err
	}
	binding, err := s.binding(ctx, scope, session)
	if err != nil {
		return Call{}, err
	}
	if binding.Archived {
		return Call{}, ErrRevoked
	}
	call, err := s.call(ctx, scope, session, id)
	if err != nil {
		return Call{}, err
	}
	if call.State != "pending" {
		return Call{}, ErrAlreadyClaimed
	}
	res, err := s.db.ExecContext(ctx, `UPDATE app_calls SET state='claimed' WHERE connection=? AND session=? AND call=? AND state='pending'`, scope.ConnectionID, session, id)
	if err != nil {
		return Call{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Call{}, err
	}
	if n != 1 {
		return Call{}, ErrAlreadyClaimed
	}
	call.State = "claimed"
	s.signal()
	return call, nil
}

// CompleteCall accepts one immutable result only while the claimed effect still
// belongs to an active lease. It cannot revive a cancelled or unknown call.
func (s *Store) CompleteCall(ctx context.Context, scope Scope, session, id string, result CallResult) error {
	if result.Outcome != "succeeded" && result.Outcome != "failed" && result.Outcome != "unknown" {
		return ErrInvalid
	}
	if !json.Valid(result.Content) || len(result.Content) > maxCallBytes {
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
	call, err := s.call(ctx, scope, session, id)
	if err != nil {
		return err
	}
	if call.State == "completed" {
		old, _ := encode(call.Result)
		if bytes.Equal(old, body) {
			return nil
		}
		return ErrConflict
	}
	if call.State != "claimed" {
		return ErrAlreadyClaimed
	}
	res, err := s.db.ExecContext(ctx, `UPDATE app_calls SET state='completed',result=? WHERE connection=? AND session=? AND call=? AND state='claimed'`, body, scope.ConnectionID, session, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAlreadyClaimed
	}
	s.signal()
	return nil
}

// CancelCall withdraws execution without granting any new authority. It remains
// available after expiry or revocation for trusted Runtime cancellation cleanup.
func (s *Store) CancelCall(ctx context.Context, scope Scope, session, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.call(ctx, scope, session, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE app_calls SET state=CASE state WHEN 'pending' THEN 'cancelled' ELSE 'unknown' END WHERE connection=? AND session=? AND call=? AND state IN ('pending','claimed')`, scope.ConnectionID, session, id)
	if err == nil {
		s.signal()
	}
	return err
}
