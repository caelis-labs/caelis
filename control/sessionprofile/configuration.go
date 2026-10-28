package sessionprofile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/caelis-labs/caelis/control/internal/operationreceipt"
	"math"
)

// configuration reads one durable revision. Caller holds s.mu and has verified
// exact binding ownership; zero selects the latest revision.
func (s *Store) configuration(ctx context.Context, session string, revision uint64) (Configuration, error) {
	var raw []byte
	var err error
	if revision == 0 {
		err = s.db.QueryRowContext(ctx, `SELECT body FROM session_configurations WHERE session=? ORDER BY revision DESC LIMIT 1`, session).Scan(&raw)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT body FROM session_configurations WHERE session=? AND revision=?`, session, revision).Scan(&raw)
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
	if patch.MCPServers != nil {
		if *patch.MCPServers == nil {
			return Profile{}, ErrInvalid
		}
		p.MCPServers = *patch.MCPServers
	}
	if patch.SkillDirs != nil {
		if *patch.SkillDirs == nil {
			return Profile{}, ErrInvalid
		}
		p.SkillDirs = *patch.SkillDirs
	}
	if patch.SkillRoots != nil {
		if *patch.SkillRoots == nil {
			return Profile{}, ErrInvalid
		}
		p.SkillRoots = *patch.SkillRoots
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
	if !operationreceipt.HasTransaction(ctx) {
		return Configuration{}, ErrUnsupported
	}
	var err error
	// Both reads and the final CAS share the admission mutex. Validation may
	// perform external work; it cannot hold this mutex across that boundary.
	s.mu.Lock()
	if err = s.checkScope(ctx, scope); err != nil {
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
	if err = s.checkScope(ctx, scope); err != nil {
		return Configuration{}, err
	}
	binding, err = s.binding(ctx, scope, session)
	if err != nil {
		return Configuration{}, err
	}
	if binding.Archived {
		return Configuration{}, ErrRevoked
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
		if _, err = tx.ExecContext(ctx, `INSERT INTO session_configurations(session,revision,body) VALUES(?,?,?)`, session, result.Revision, resultBody); err != nil {
			return Configuration{}, err
		}
	}
	err = operationreceipt.CompleteTransaction(ctx, tx, resultBody)
	if err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM session_configurations WHERE session=?`, session)
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
	validTurn := validID(request.TurnID) && request.JobID == ""
	validJob := validID(request.JobID) && request.TurnID == ""
	if request.Revision == 0 || !validID(request.RequestID) || (!validTurn && !validJob) || !validID(request.Model) || !validID(request.ToolsVersion) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkScope(ctx, scope); err != nil {
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
	_, err = s.db.ExecContext(ctx, `UPDATE session_configurations SET body=? WHERE session=? AND revision=?`, body, session, request.Revision)
	return err
}
