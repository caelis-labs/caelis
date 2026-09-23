package application

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// migrateConfigurations upgrades schema v1 atomically with creation-profile
// seeding. Each version retains its catalog even after the desired profile moves.
func migrateConfigurations(tx *sql.Tx, version int) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS app_configurations (
		session TEXT NOT NULL, revision INTEGER NOT NULL, body BLOB NOT NULL,
		PRIMARY KEY(session,revision), FOREIGN KEY(session) REFERENCES app_bindings(session)
	)`); err != nil {
		return err
	}
	if version == 2 {
		return nil
	}
	rows, err := tx.Query(`SELECT session,body FROM app_bindings`)
	if err != nil {
		return err
	}
	var seeds []Configuration
	for rows.Next() {
		var session string
		var raw []byte
		if err = rows.Scan(&session, &raw); err != nil {
			break
		}
		var binding Binding
		if err = json.Unmarshal(raw, &binding); err != nil {
			break
		}
		if binding.SessionID != session {
			err = fmt.Errorf("%w: binding session mismatch", ErrInvalid)
			break
		}
		profile := binding.Profile
		if profile.Execution == "workspace-write" && profile.NativeTools == nil {
			// Schema 1 exposed only RunCommand/Task plus resource transfer.
			// Pin that native catalog as desired configuration without changing
			// the immutable creation binding or broadening old callback names.
			profile.NativeTools = []string{"RunCommand", "Task"}
		}
		seeds = append(seeds, Configuration{SessionID: session, Revision: 1, Profile: profile})
	}
	if err == nil {
		err = rows.Err()
	}
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	for _, seed := range seeds {
		body, err := encode(seed)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO app_configurations(session,revision,body) VALUES(?,?,?)`, seed.SessionID, seed.Revision, body); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`UPDATE app_schema SET version=2 WHERE singleton=1`)
	return err
}

// configuration reads one durable revision. Caller holds s.mu and has verified
// exact binding ownership; zero selects the latest revision.
func (s *Store) configuration(ctx context.Context, session string, revision uint64) (Configuration, error) {
	var raw []byte
	var err error
	if revision == 0 {
		err = s.db.QueryRowContext(ctx, `SELECT body FROM app_configurations WHERE session=? ORDER BY revision DESC LIMIT 1`, session).Scan(&raw)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT body FROM app_configurations WHERE session=? AND revision=?`, session, revision).Scan(&raw)
	}
	if err != nil {
		return Configuration{}, notFound(err)
	}
	var result Configuration
	if err = json.Unmarshal(raw, &result); err != nil {
		return Configuration{}, err
	}
	if result.SessionID != session || result.Revision == 0 || (revision != 0 && result.Revision != revision) {
		return Configuration{}, ErrInvalid
	}
	return result, nil
}

// Configuration returns the latest desired snapshot in the exact connection
// scope, including after lease expiry or revocation for read-only recovery.
func (s *Store) Configuration(ctx context.Context, scope Scope, session string) (Configuration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.binding(ctx, scope, session); err != nil {
		return Configuration{}, err
	}
	return s.configuration(ctx, session, 0)
}

// ConfigurationRevision returns a historical, immutable catalog snapshot under
// the same ownership check. LastRequest is recorded separately from the catalog.
func (s *Store) ConfigurationRevision(ctx context.Context, scope Scope, session string, revision uint64) (Configuration, error) {
	if revision == 0 {
		return Configuration{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.binding(ctx, scope, session); err != nil {
		return Configuration{}, err
	}
	return s.configuration(ctx, session, revision)
}

type configurationIntent struct {
	Kind      string                     `json:"kind"`
	SessionID string                     `json:"session_id"`
	Request   UpdateConfigurationRequest `json:"request"`
}

func configurationResult(op Operation) (Configuration, error) {
	var intent configurationIntent
	if err := json.Unmarshal(op.Request, &intent); err != nil || intent.Kind != "configuration_update" || intent.SessionID == "" || intent.Request.OperationID != op.ID {
		return Configuration{}, ErrConflict
	}
	var result Configuration
	if len(op.Result) == 0 || json.Unmarshal(op.Result, &result) != nil || result.SessionID != intent.SessionID || result.Revision == 0 {
		return Configuration{}, ErrInvalid
	}
	return result, nil
}

// ConfigurationOperation returns the exact committed result (including a no-op),
// never a later mutable latest configuration or a fresh update grant.
func (s *Store) ConfigurationOperation(ctx context.Context, scope Scope, operationID string) (Configuration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.connection(ctx, scope); err != nil {
		return Configuration{}, err
	}
	op, err := s.operation(ctx, scope, operationID)
	if err != nil {
		return Configuration{}, err
	}
	return configurationResult(op)
}

func applyConfigurationPatch(p Profile, patch ConfigurationPatch) (Profile, error) {
	if patch.Instructions != nil {
		p.Instructions = *patch.Instructions
	}
	if patch.Model != nil {
		p.Model = *patch.Model
	}
	if patch.ReasoningEffort != nil {
		p.ReasoningEffort = *patch.ReasoningEffort
	}
	if patch.ServiceTier != nil {
		p.ServiceTier = *patch.ServiceTier
	}
	if patch.ToolsVersion != nil {
		p.ToolsVersion = *patch.ToolsVersion
	}
	if patch.Tools != nil {
		if *patch.Tools == nil {
			return Profile{}, ErrInvalid
		}
		p.Tools = *patch.Tools
	}
	if patch.NativeTools != nil {
		if *patch.NativeTools == nil {
			return Profile{}, ErrInvalid
		}
		p.NativeTools = *patch.NativeTools
	}
	if err := ValidateProfile(p); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// UpdateConfiguration validates the candidate outside the Store lock, then
// atomically checks CAS and commits the new snapshot plus its complete operation
// request/result. A no-op also commits a durable receipt without a new revision.
func (s *Store) UpdateConfiguration(ctx context.Context, scope Scope, session string, request UpdateConfigurationRequest, validate func(context.Context, Profile) error) (Configuration, error) {
	if !validID(request.OperationID) || !validID(session) || request.ExpectedConfigurationRevision == 0 || request.ExpectedConfigurationRevision >= math.MaxInt64 {
		return Configuration{}, ErrInvalid
	}
	intent := configurationIntent{Kind: "configuration_update", SessionID: session, Request: request}
	requestBody, err := encode(intent)
	if err != nil {
		return Configuration{}, err
	}
	digest := digestBytes(requestBody)
	// Both reads and the final CAS share the admission mutex. Validation may
	// perform external work; it cannot hold this mutex across that boundary.
	s.mu.Lock()
	if _, err = s.active(ctx, scope); err != nil {
		s.mu.Unlock()
		return Configuration{}, err
	}
	binding, err := s.binding(ctx, scope, session)
	if err != nil {
		s.mu.Unlock()
		return Configuration{}, err
	}
	if binding.Archived {
		s.mu.Unlock()
		return Configuration{}, ErrRevoked
	}
	op, err := s.operation(ctx, scope, request.OperationID)
	if err == nil {
		s.mu.Unlock()
		if op.Digest != digest {
			return Configuration{}, ErrConflict
		}
		return configurationResult(op)
	}
	if !errors.Is(err, ErrNotFound) {
		s.mu.Unlock()
		return Configuration{}, err
	}
	current, err := s.configuration(ctx, session, 0)
	s.mu.Unlock()
	if err != nil {
		return Configuration{}, err
	}
	if current.Revision != request.ExpectedConfigurationRevision {
		return Configuration{}, ErrConfigurationStale
	}
	candidate, err := applyConfigurationPatch(current.Profile, request.Patch)
	if err != nil {
		return Configuration{}, err
	}
	if validate != nil {
		if err = validate(ctx, candidate); err != nil {
			return Configuration{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err = s.active(ctx, scope); err != nil {
		return Configuration{}, err
	}
	binding, err = s.binding(ctx, scope, session)
	if err != nil {
		return Configuration{}, err
	}
	if binding.Archived {
		return Configuration{}, ErrRevoked
	}
	op, err = s.operation(ctx, scope, request.OperationID)
	if err == nil {
		if op.Digest != digest {
			return Configuration{}, ErrConflict
		}
		return configurationResult(op)
	}
	if !errors.Is(err, ErrNotFound) {
		return Configuration{}, err
	}
	latest, err := s.configuration(ctx, session, 0)
	if err != nil {
		return Configuration{}, err
	}
	if latest.Revision != request.ExpectedConfigurationRevision {
		return Configuration{}, ErrConfigurationStale
	}
	// LastRequest may advance during validation. Never overwrite that admission.
	result := latest
	newProfile, err := encode(candidate)
	if err != nil {
		return Configuration{}, err
	}
	oldProfile, err := encode(latest.Profile)
	if err != nil {
		return Configuration{}, err
	}
	if !bytes.Equal(newProfile, oldProfile) {
		if err = s.validateCatalogVersion(ctx, session, candidate); err != nil {
			return Configuration{}, err
		}
		result.Revision++
		result.Profile = candidate
	}
	resultBody, err := encode(result)
	if err != nil {
		return Configuration{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Configuration{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if result.Revision != latest.Revision {
		if _, err = tx.ExecContext(ctx, `INSERT INTO app_configurations(session,revision,body) VALUES(?,?,?)`, session, result.Revision, resultBody); err != nil {
			return Configuration{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO app_operations(principal,application,connection,operation,digest,request,result) VALUES(?,?,?,?,?,?,?)`, scope.PrincipalID, scope.ApplicationID, scope.ConnectionID, request.OperationID, digest, requestBody, resultBody); err != nil {
		return Configuration{}, err
	}
	if err = tx.Commit(); err != nil {
		return Configuration{}, err
	}
	// The returned value must equal the durable receipt, including JSON's
	// normalization of empty callback lists to an omitted empty catalog.
	var committed Configuration
	if err = json.Unmarshal(resultBody, &committed); err != nil {
		return Configuration{}, err
	}
	return committed, nil
}

func (s *Store) validateCatalogVersion(ctx context.Context, session string, candidate Profile) error {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM app_configurations WHERE session=?`, session)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	catalog, err := encode(candidate.Tools)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		var previous Configuration
		if err = json.Unmarshal(raw, &previous); err != nil {
			return err
		}
		if previous.Profile.ToolsVersion == candidate.ToolsVersion {
			prior, err := encode(previous.Profile.Tools)
			if err != nil {
				return err
			}
			if (len(previous.Profile.Tools) != 0 || len(candidate.Tools) != 0) && !bytes.Equal(catalog, prior) {
				return fmt.Errorf("%w: tools_version already names a different catalog", ErrConflict)
			}
		}
	}
	return rows.Err()
}

// AdmitRequest fences a request against latest desired revision under the same
// mutex used for configuration CAS, and records its revision and canonical IDs.
// A stale snapshot is never silently admitted under a newer profile.
func (s *Store) AdmitRequest(ctx context.Context, scope Scope, session string, request RequestConfiguration) error {
	if request.Revision == 0 || !validID(request.RequestID) || !validID(request.TurnID) || !validID(request.Model) || !validID(request.ToolsVersion) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.active(ctx, scope); err != nil {
		return err
	}
	binding, err := s.binding(ctx, scope, session)
	if err != nil {
		return err
	}
	if binding.Archived {
		return ErrRevoked
	}
	config, err := s.configuration(ctx, session, 0)
	if err != nil {
		return err
	}
	if config.Revision != request.Revision {
		return ErrConfigurationStale
	}
	if request.ToolsVersion != config.Profile.ToolsVersion || request.ServiceTier != config.Profile.ServiceTier || (config.Profile.ReasoningEffort != "" && request.ReasoningEffort != config.Profile.ReasoningEffort) {
		return ErrConflict
	}
	config.LastRequest = &request
	body, err := encode(config)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE app_configurations SET body=? WHERE session=? AND revision=?`, body, session, request.Revision)
	return err
}
