package sessionprofile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const maxCallBytes = 1 << 20

const pendingCallsQuery = `SELECT body,state,result FROM client_tool_calls WHERE principal=?  AND session=? AND state='pending' ORDER BY rowid`

func callID(c CallContext) string {
	return stableID("client-call-", c.PrincipalID, c.SessionID, c.TurnID, c.ItemID)
}

// Invoke persists an intent before waking consumers, then waits for a terminal
// result using notifications and originating Turn cancellation. It never executes an
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
		call, changed, err := s.observeCall(ctx, c.Scope, c.SessionID, id)
		s.mu.Unlock()
		if err != nil {
			return CallResult{}, err
		}
		if result, done, err := terminalResult(call); done {
			return result, err
		}
		if err := waitCallChange(ctx, changed); err != nil {
			return CallResult{Outcome: "unknown", Content: json.RawMessage(`null`)}, err
		}
	}
}
func (s *Store) enqueue(ctx context.Context, c CallContext, name string, args json.RawMessage) (string, error) {
	if !validID(c.SessionID) || !validID(c.TurnID) || !validID(c.ItemID) || !validID(c.CallID) || !validID(c.ToolsVersion) || c.ConfigurationRevision == 0 || len(args) > maxCallBytes || !json.Valid(args) {
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
	if err = s.checkScope(ctx, c.Scope); err != nil {
		return "", err
	}
	binding, err := s.binding(ctx, c.Scope, c.SessionID)
	if err != nil {
		return "", err
	}
	if binding.Archived {
		return "", ErrRevoked
	}
	configuration, err := s.configuration(ctx, c.SessionID, c.ConfigurationRevision)
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
	// Native tuple is canonical; a migrated opaque callback address must survive.
	var oldID, oldDigest string
	var oldBody []byte
	err = s.db.QueryRowContext(ctx, `SELECT call,digest,body FROM client_tool_calls WHERE session=? AND turn=? AND item=?`, c.SessionID, c.TurnID, c.ItemID).Scan(&oldID, &oldDigest, &oldBody)
	if err == nil {
		var previous Call
		if digestBytes(oldBody) != oldDigest || json.Unmarshal(oldBody, &previous) != nil || previous.ID != oldID {
			return "", ErrInvalid
		}
		call.ID = oldID
		canonical, err := encode(previous)
		if err != nil {
			return "", err
		}
		candidate, err := encode(call)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(canonical, candidate) {
			return "", ErrConflict
		}
		return oldID, nil
	}
	if !errors.Is(notFound(err), ErrNotFound) {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO client_tool_calls(principal,session,call,turn,item,digest,body,state) VALUES(?,?,?,?,?,?,?,'pending')`, c.PrincipalID, c.SessionID, call.ID, c.TurnID, c.ItemID, digest, body)
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
func (s *Store) observeCall(ctx context.Context, scope Scope, session, id string) (Call, <-chan struct{}, error) {
	call, err := s.call(ctx, scope, session, id)
	return call, s.changed, err
}
func (s *Store) call(ctx context.Context, scope Scope, session, id string) (Call, error) {
	if _, err := s.binding(ctx, scope, session); err != nil {
		return Call{}, err
	}
	var raw, result []byte
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT body,state,result FROM client_tool_calls WHERE principal=?  AND session=? AND call=?`, scope.PrincipalID, session, id).Scan(&raw, &state, &result)
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
	call, _, err := s.observeCall(ctx, scope, session, id)
	return call, err
}

// ListCalls lists durable receipts, including terminal and unclaimed intents.
func (s *Store) ListCalls(ctx context.Context, scope Scope, session string) ([]Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls(ctx, scope, session)
}
func (s *Store) listCalls(ctx context.Context, scope Scope, session string) ([]Call, error) {
	return s.queryCalls(ctx, scope, session, `SELECT body,state,result FROM client_tool_calls WHERE principal=?  AND session=? ORDER BY rowid`)
}

// pendingCalls is called under s.mu. SQL excludes historical payloads before
// decoding; client_tool_calls_pending also avoids scanning completed history when no
// candidate exists. Ownership checks are shared with history reads.
func (s *Store) pendingCalls(ctx context.Context, scope Scope, session string) ([]Call, error) {
	b, err := s.binding(ctx, scope, session)
	if err != nil {
		return nil, err
	}
	if b.Archived {
		return nil, ErrRevoked
	}
	return s.queryCalls(ctx, scope, session, pendingCallsQuery)
}

func (s *Store) queryCalls(ctx context.Context, scope Scope, session, query string) ([]Call, error) {
	if _, err := s.binding(ctx, scope, session); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, query, scope.PrincipalID, session)
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
// a store notification, shutdown, or caller cancellation occurs.
func (s *Store) WaitCalls(ctx context.Context, scope Scope, session string) ([]Call, error) {
	for {
		s.mu.Lock()
		err := s.checkScope(ctx, scope)
		var calls []Call
		if err == nil {
			calls, err = s.pendingCalls(ctx, scope, session)
		}
		// Read candidates and subscribe under the same lock as enqueue/signal so
		// an insertion between the empty scan and select cannot be lost.
		changed := s.changed
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if len(calls) > 0 {
			return calls, nil
		}
		if err := waitCallChange(ctx, changed); err != nil {
			return nil, err
		}
	}
}

// ClaimCall durably grants one consumer permission to dispatch this effect. A
// second claim, including the same consumer's requery, never grants it again.
func (s *Store) ClaimCall(ctx context.Context, scope Scope, session, id string) (Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkScope(ctx, scope); err != nil {
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
	res, err := s.db.ExecContext(ctx, `UPDATE client_tool_calls SET state='claimed' WHERE principal=? AND session=? AND call=? AND state='pending'`, scope.PrincipalID, session, id)
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
	if result.TurnComplete && result.Outcome != "succeeded" {
		return ErrInvalid
	}
	if !json.Valid(result.Content) || len(result.Content) > maxCallBytes {
		return ErrInvalid
	}
	body, err := encode(result)
	if err != nil {
		return err
	}
	if result.ResultFormat != "" && len(body) > maxCallBytes {
		return fmt.Errorf("%w: callback result exceeds byte limit", ErrInvalid)
	}
	// Validate precisely the immutable bytes that will be stored, not aliases
	// of the embedding caller's maps or RawMessage slices.
	var snapshot CallResult
	if err = json.Unmarshal(body, &snapshot); err != nil {
		return ErrInvalid
	}
	result = snapshot
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.checkScope(ctx, scope); err != nil {
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
	if err = s.validateCallResult(ctx, call, result); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE client_tool_calls SET state='completed',result=? WHERE principal=? AND session=? AND call=? AND state='claimed'`, body, scope.PrincipalID, session, id)
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
// available after profile closure for trusted Runtime cancellation cleanup.
func (s *Store) CancelCall(ctx context.Context, scope Scope, session, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.call(ctx, scope, session, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE client_tool_calls SET state=CASE state WHEN 'pending' THEN 'cancelled' ELSE 'unknown' END WHERE principal=? AND session=? AND call=? AND state IN ('pending','claimed')`, scope.PrincipalID, session, id)
	if err == nil {
		s.signal()
	}
	return err
}

// Call lifetime follows the originating Turn and Host, with no client lease.
func waitCallChange(ctx context.Context, changed <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-changed:
		return nil
	}
}
