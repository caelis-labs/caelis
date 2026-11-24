package sessionprofile

import (
	"bytes"
	"context"
	"encoding/json"
)

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
	return decodeStoredCall(raw, state, result)
}

func decodeStoredCall(raw []byte, state string, result []byte) (Call, error) {
	var call Call
	if err := json.Unmarshal(raw, &call); err != nil {
		return Call{}, err
	}
	call.State = state
	if len(result) > 0 {
		// v0.83.1 stored turn_complete on completed receipts. Accept and discard
		// it here until those Stores age out of the supported upgrade window.
		var value struct {
			callResultJSON
			TurnComplete bool `json:"turn_complete,omitempty"`
		}
		if err := decodeCallResultJSON(result, &value); err != nil {
			return Call{}, err
		}
		decoded := CallResult(value.callResultJSON)
		call.Result = &decoded
	}
	return call, nil
}

// GetCall reads a receipt by Call.ID, requiring exact scope and Session ownership.
func (s *Store) GetCall(ctx context.Context, scope Scope, session, id string) (Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.call(ctx, scope, session, id)
}

// ListCalls lists durable receipts, in their original insertion order.
func (s *Store) ListCalls(ctx context.Context, scope Scope, session string) ([]Call, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.binding(ctx, scope, session); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT body,state,result FROM client_tool_calls WHERE principal=? AND session=? ORDER BY rowid`, scope.PrincipalID, session)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	calls := []Call{}
	for rows.Next() {
		var raw, result []byte
		var state string
		if err = rows.Scan(&raw, &state, &result); err != nil {
			return nil, err
		}
		call, err := decodeStoredCall(raw, state, result)
		if err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	return calls, rows.Err()
}

type callResultJSON CallResult

// UnmarshalJSON preserves exact numbers in historical typed receipts.
func (r *CallResult) UnmarshalJSON(data []byte) error {
	var value callResultJSON
	if err := decodeCallResultJSON(data, &value); err != nil {
		return err
	}
	*r = CallResult(value)
	return nil
}

func decodeCallResultJSON(data []byte, value any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return ErrInvalid
	}
	for _, key := range []string{"result_format", "structuredContent"} {
		if bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
			return ErrInvalid
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return ErrInvalid
	}
	return nil
}
