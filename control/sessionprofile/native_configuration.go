package sessionprofile

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"reflect"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/internal/operationreceipt"
)

// The ordinary Session selector shares the existing configuration table and
// transaction receipt. This adjunct marker is not an explicit execution Profile
// and must never enter profile admission, callbacks or application assembly.
type nativeConfigurationBinding struct {
	Native bool `json:"native_configuration"`
}

// UnmarshalJSON keeps an explicit empty selector distinct from inheritance.
// A null selector is never interpreted as permission to use Host packages.
func (c *NativeConfiguration) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil || string(fields["plugins"]) == "null" {
		return ErrInvalid
	}
	type alias NativeConfiguration
	var value alias
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*c = NativeConfiguration(value)
	return nil
}

func isNativeConfigurationBinding(raw []byte) bool {
	var marker nativeConfigurationBinding
	return json.Unmarshal(raw, &marker) == nil && marker.Native
}

// NativeConfiguration reads an ordinary Session's plugin selector. The Host must
// load active from the canonical Session owner, never construct it from input.
// Reads do not create a binding, scan packages or activate execution.
func (s *Store) NativeConfiguration(ctx context.Context, scope Scope, active session.Session) (Configuration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nativeConfiguration(ctx, scope, active)
}

func (s *Store) nativeConfiguration(ctx context.Context, scope Scope, active session.Session) (Configuration, error) {
	if err := s.checkScope(ctx, scope); err != nil {
		return Configuration{}, err
	}
	if active.UserID != scope.PrincipalID || !validID(active.SessionID) {
		return Configuration{}, ErrUnauthorized
	}
	var principal string
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT principal,body FROM session_profiles WHERE session=?`, active.SessionID).Scan(&principal, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Configuration{SessionID: active.SessionID, Revision: 1, Native: &NativeConfiguration{}}, nil
	}
	if err != nil {
		return Configuration{}, err
	}
	if principal != scope.PrincipalID {
		return Configuration{}, ErrUnauthorized
	}
	if !isNativeConfigurationBinding(raw) {
		return Configuration{}, ErrUnsupported
	}
	result, err := s.configuration(ctx, active.SessionID, 0)
	if err == nil && result.Native == nil {
		return Configuration{}, ErrInvalid
	}
	return result, err
}

// UpdateNativeConfiguration commits only ordinary native plugin selection using
// the existing CAS and command receipt transaction. The caller holds native
// admission quiescent through commit; active is canonical ownership evidence.
// admit validates package contributions and retires an idle prior activation.
func (s *Store) UpdateNativeConfiguration(ctx context.Context, scope Scope, active session.Session, request UpdateConfigurationRequest, admit func(context.Context, []Plugin) error) (Configuration, error) {
	if !validID(request.OperationID) || request.ExpectedConfigurationRevision == 0 || request.ExpectedConfigurationRevision >= math.MaxInt64 {
		return Configuration{}, ErrInvalid
	}
	if !operationreceipt.HasTransaction(ctx) {
		return Configuration{}, ErrUnsupported
	}
	// Ownership and the initial CAS precede package validation and Host
	// retirement, exactly as they precede explicit-profile validation.
	s.mu.Lock()
	current, err := s.nativeConfiguration(ctx, scope, active)
	s.mu.Unlock()
	if err != nil {
		return Configuration{}, err
	}
	if current.Revision != request.ExpectedConfigurationRevision {
		return Configuration{}, ErrConfigurationStale
	}
	if !reflect.DeepEqual(request.Patch, ConfigurationPatch{Plugins: request.Patch.Plugins}) {
		return Configuration{}, ErrUnsupported
	}
	if request.Patch.Plugins != nil {
		if *request.Patch.Plugins == nil {
			return Configuration{}, ErrInvalid
		}
		if err := validatePlugins(*request.Patch.Plugins); err != nil {
			return Configuration{}, err
		}
		if admit != nil {
			if err := admit(ctx, *request.Patch.Plugins); err != nil {
				return Configuration{}, err
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	latest, err := s.nativeConfiguration(ctx, scope, active)
	if err != nil {
		return Configuration{}, err
	}
	if latest.Revision != request.ExpectedConfigurationRevision {
		return Configuration{}, ErrConfigurationStale
	}
	result := latest
	if request.Patch.Plugins != nil {
		result.Native = &NativeConfiguration{Plugins: request.Patch.Plugins}
	}
	before, err := encode(latest.Native)
	if err != nil {
		return Configuration{}, err
	}
	after, err := encode(result.Native)
	if err != nil {
		return Configuration{}, err
	}
	if !bytes.Equal(before, after) {
		result.Revision++
	}
	body, err := encode(result)
	if err != nil {
		return Configuration{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Configuration{}, err
	}
	defer func() { _ = tx.Rollback() }()
	marker, _ := encode(nativeConfigurationBinding{Native: true})
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO session_profiles(session,principal,body) VALUES(?,?,?)`, active.SessionID, scope.PrincipalID, marker); err != nil {
		return Configuration{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO session_configurations(session,revision,body) VALUES(?,?,?)`, active.SessionID, result.Revision, body); err != nil {
		return Configuration{}, err
	}
	if err = operationreceipt.CompleteTransaction(ctx, tx, body); err != nil {
		return Configuration{}, err
	}
	if err = tx.Commit(); err != nil {
		return Configuration{}, err
	}
	var committed Configuration
	err = json.Unmarshal(body, &committed)
	return committed, err
}
