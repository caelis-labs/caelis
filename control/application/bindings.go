package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/jsonschema-go/jsonschema"
)

var toolName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)

// ValidateProfile requires a complete immutable profile. No Host settings are
// inherited and no execution mode, model, or version is silently defaulted.
func ValidateProfile(p Profile) error {
	if !validID(p.Version) || !validID(p.Model) || !validID(p.ToolsVersion) {
		return fmt.Errorf("%w: explicit profile version, model and tools_version required", ErrInvalid)
	}
	if p.Execution != "tools-only" && p.Execution != "workspace-write" {
		return fmt.Errorf("%w: execution must be tools-only or workspace-write", ErrInvalid)
	}
	if p.Inherit != (Inheritance{}) {
		return fmt.Errorf("%w: configuration inheritance is unavailable", ErrUnsupported)
	}
	if len(p.Instructions) > 1<<20 || len(p.Tools) > 128 {
		return fmt.Errorf("%w: profile size limit", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, def := range p.Tools {
		if !toolName.MatchString(def.Name) || seen[def.Name] {
			return fmt.Errorf("%w: duplicate or invalid tool name", ErrInvalid)
		}
		seen[def.Name] = true
		if len(def.Description) > 65536 {
			return ErrInvalid
		}
		if _, err := resolveSchema(def.InputSchema); err != nil {
			return fmt.Errorf("%w: tool %s: %w", ErrInvalid, def.Name, err)
		}
	}
	return nil
}
func resolveSchema(value map[string]any) (*jsonschema.Resolved, error) {
	if value == nil || value["type"] != "object" {
		return nil, fmt.Errorf("schema must explicitly have object type")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw) > 256<<10 {
		return nil, fmt.Errorf("schema exceeds size limit")
	}
	var schema jsonschema.Schema
	if err = json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	// Resolve's nil loader rejects remote references without making network requests.
	return schema.Resolve(nil)
}

// ValidateSource permits explicit input provenance only; background grants are
// deliberately absent from version 1. Source is supplied by trusted Control.
func ValidateSource(source Source) error {
	if !validID(source.OperationID) {
		return fmt.Errorf("%w: source operation required", ErrInvalid)
	}
	switch source.Kind {
	case "user", "application_summary", "external_material":
		return nil
	default:
		return fmt.Errorf("%w: unsupported input source", ErrUnsupported)
	}
}

// PutBinding persists a Host-created Session binding. Every field, including the
// creation digest and complete profile, participates in immutable idempotency.
func (s *Store) PutBinding(ctx context.Context, b Binding) error {
	if !validID(b.SessionID) || !validID(b.CreationDigest) || b.Archived {
		return ErrInvalid
	}
	if err := ValidateProfile(b.Profile); err != nil {
		return err
	}
	body, err := encode(b)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err = s.active(ctx, b.Scope); err != nil {
		return err
	}
	var previous []byte
	err = s.db.QueryRowContext(ctx, `SELECT body FROM app_bindings WHERE session=?`, b.SessionID).Scan(&previous)
	if err == nil {
		if !bytes.Equal(previous, body) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(notFound(err), ErrNotFound) {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO app_bindings(session,principal,application,connection,body) VALUES(?,?,?,?,?)`, b.SessionID, b.PrincipalID, b.ApplicationID, b.ConnectionID, body)
	return err
}
func (s *Store) binding(ctx context.Context, scope Scope, id string) (Binding, error) {
	if _, err := s.connection(ctx, scope); err != nil {
		return Binding{}, err
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT body FROM app_bindings WHERE session=? AND principal=? AND application=? AND connection=?`, id, scope.PrincipalID, scope.ApplicationID, scope.ConnectionID).Scan(&raw)
	if err != nil {
		return Binding{}, notFound(err)
	}
	var b Binding
	err = json.Unmarshal(raw, &b)
	return b, err
}

// GetBinding returns only a binding in the caller's exact connection scope.
func (s *Store) GetBinding(ctx context.Context, scope Scope, id string) (Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.binding(ctx, scope, id)
}

// BindingForSession is a trusted Host lookup for authorizers and Runtime assembly.
// A public adapter must use GetBinding after authenticating its scope.
func (s *Store) BindingForSession(ctx context.Context, id string) (Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Binding{}, ErrClosed
	}
	var raw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT body FROM app_bindings WHERE session=?`, id).Scan(&raw); err != nil {
		return Binding{}, notFound(err)
	}
	var b Binding
	err := json.Unmarshal(raw, &b)
	return b, err
}

// ListBindings returns only bindings owned by this exact connection.
func (s *Store) ListBindings(ctx context.Context, scope Scope) ([]Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.connection(ctx, scope); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM app_bindings WHERE principal=? AND application=? AND connection=? ORDER BY session`, scope.PrincipalID, scope.ApplicationID, scope.ConnectionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Binding{}
	for rows.Next() {
		var raw []byte
		var b Binding
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ArchiveBinding seals a binding after the Host closes the canonical Session.
// Read-only history and resources remain available under the original scope.
func (s *Store) ArchiveBinding(ctx context.Context, scope Scope, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.active(ctx, scope); err != nil {
		return err
	}
	b, err := s.binding(ctx, scope, id)
	if err != nil {
		return err
	}
	if b.Archived {
		return nil
	}
	b.Archived = true
	body, err := encode(b)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `UPDATE app_bindings SET body=? WHERE session=? AND connection=?`, body, id, scope.ConnectionID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE app_calls SET state=CASE state WHEN 'pending' THEN 'cancelled' ELSE 'unknown' END WHERE connection=? AND session=? AND state IN ('pending','claimed')`, scope.ConnectionID, id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.signal()
	return nil
}
