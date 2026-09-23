package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

var toolName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)

// ValidateProfile requires a complete profile. Execution authority is separate
// from hot configuration; no Host context, model or catalog is silently inherited.
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
	if p.Workspace.CWD != "" && (!filepath.IsAbs(p.Workspace.CWD) || strings.TrimSpace(p.Workspace.CWD) != p.Workspace.CWD) {
		return fmt.Errorf("%w: workspace cwd must be an absolute path", ErrInvalid)
	}
	if len(p.Workspace.Access) > 32 {
		return fmt.Errorf("%w: too many workspace access directories", ErrInvalid)
	}
	for _, access := range p.Workspace.Access {
		if !filepath.IsAbs(access.Path) || strings.TrimSpace(access.Path) != access.Path || (access.Mode != "read-only" && access.Mode != "read-write") {
			return fmt.Errorf("%w: workspace access requires an absolute path and read-only or read-write mode", ErrInvalid)
		}
	}
	switch p.Permissions.Mode {
	case "", "workspace-write":
	case "danger-full-access":
		if p.Execution != "workspace-write" {
			return fmt.Errorf("%w: full access requires native workspace-write execution", ErrUnsupported)
		}
	default:
		return fmt.Errorf("%w: unsupported application permission mode", ErrUnsupported)
	}
	if p.Permissions.ApprovalMode != "" && p.Permissions.ApprovalMode != "manual" {
		return fmt.Errorf("%w: application approval mode requires an available approval reviewer", ErrUnsupported)
	}
	if len(p.Instructions) > 1<<20 || len(p.Tools) > 128 {
		return fmt.Errorf("%w: profile size limit", ErrInvalid)
	}
	if p.Execution == "tools-only" && len(p.NativeTools) != 0 {
		return fmt.Errorf("%w: native tools require workspace-write execution", ErrInvalid)
	}
	knownNative := map[string]bool{"Read": true, "ViewImage": true, "Write": true, "Patch": true, "Glob": true, "Grep": true, "RunCommand": true, "Task": true}
	seenNative := make(map[string]bool, len(p.NativeTools))
	for _, name := range p.NativeTools {
		if !knownNative[name] || seenNative[name] {
			return fmt.Errorf("%w: unknown or duplicate native tool %q", ErrInvalid, name)
		}
		seenNative[name] = true
	}
	availableNative := seenNative
	if p.NativeTools == nil {
		availableNative = knownNative
	}
	seen := map[string]bool{}
	for _, def := range p.Tools {
		if !toolName.MatchString(def.Name) || seen[def.Name] {
			return fmt.Errorf("%w: duplicate or invalid tool name", ErrInvalid)
		}
		seen[def.Name] = true
		// SDK tool dispatch is exact-case. Only actually selected native
		// names and the always-present resource bridge are reserved.
		if p.Execution == "workspace-write" && (availableNative[def.Name] || def.Name == "ReadResource" || def.Name == "PublishArtifact") {
			return fmt.Errorf("%w: callback collides with a native execution tool", ErrInvalid)
		}
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

// ValidateSource verifies Control-bound prompt provenance, not background grant
// liveness. Only AdmitBackgroundSource can attach an authorized background source.
func ValidateSource(source Source) error {
	if !validID(source.OperationID) {
		return fmt.Errorf("%w: source operation required", ErrInvalid)
	}
	switch source.Kind {
	case "user", "application_summary", "external_material":
		if source.GrantID != "" || source.AuthorizedSource != "" {
			return ErrInvalid
		}
	case "authorized_background":
		if !validID(source.GrantID) || !validID(source.AuthorizedSource) {
			return ErrInvalid
		}
	default:
		return fmt.Errorf("%w: unsupported input source", ErrUnsupported)
	}
	return nil
}

// PutBinding persists a Host-created Session binding. Every field, including the
// creation digest and complete profile, participates in immutable idempotency.
func (s *Store) PutBinding(ctx context.Context, b Binding) error {
	if !validID(b.SessionID) || !validID(b.CreationDigest) || b.Archived {
		return ErrInvalid
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
		// Compare the decoded immutable value, not historical encoder key
		// order or the current tool catalog's rules for new bindings.
		var old Binding
		if err = json.Unmarshal(previous, &old); err != nil {
			return err
		}
		canonical, err := encode(old)
		if err != nil {
			return err
		}
		if !bytes.Equal(canonical, body) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(notFound(err), ErrNotFound) {
		return err
	}
	if err := ValidateProfile(b.Profile); err != nil {
		return err
	}
	config := Configuration{SessionID: b.SessionID, Revision: 1, Profile: b.Profile}
	configBody, err := encode(config)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO app_bindings(session,principal,application,connection,body) VALUES(?,?,?,?,?)`, b.SessionID, b.PrincipalID, b.ApplicationID, b.ConnectionID, body); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO app_configurations(session,revision,body) VALUES(?,?,?)`, b.SessionID, config.Revision, configBody); err != nil {
		return err
	}
	return tx.Commit()
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

// GetBinding returns the immutable creation profile and ownership in the caller's
// exact connection scope. Configuration is the authoritative desired profile.
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

// ListBindings returns creation profiles, not latest desired configurations, for
// bindings owned by this exact connection.
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

// ArchiveBinding seals a binding after the Host proves the canonical close.
// This trusted completion does not require a live lease or grant new admission;
// the caller must retain the original operation's committed close receipt.
// Read-only history and resources remain available under the original scope.
func (s *Store) ArchiveBinding(ctx context.Context, scope Scope, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
