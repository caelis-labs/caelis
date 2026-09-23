package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	_ "modernc.org/sqlite"
)

// LeaseDuration bounds the authority of a connected application tool host.
const LeaseDuration = 10 * time.Minute

// Store errors distinguish ownership, admission, idempotency, and lifecycle failures.
var (
	ErrUnauthorized   = errorcode.New(errorcode.PermissionDenied, "application scope is unauthorized")
	ErrLeaseExpired   = errorcode.New(errorcode.FailedPrecondition, "application lease expired")
	ErrRevoked        = errorcode.New(errorcode.FailedPrecondition, "application connection revoked")
	ErrConflict       = errorcode.New(errorcode.Conflict, "application operation conflicts with durable intent")
	ErrNotFound       = errorcode.New(errorcode.NotFound, "application record not found")
	ErrInvalid        = errorcode.New(errorcode.InvalidArgument, "invalid application request")
	ErrAlreadyClaimed = errorcode.New(errorcode.Conflict, "application call already claimed or terminal")
	ErrClosed         = errorcode.New(errorcode.Unavailable, "application store closed")
	ErrUnsupported    = errorcode.New(errorcode.Unsupported, "unsupported application capability")
	ErrCancelled      = errorcode.New(errorcode.Cancelled, "application call cancelled")
)

// Store owns additive app_* tables in the Host database. One live Host owns the
// database; it must drain producers before reopening the Store. It stores no
// Session transcript and never dispatches or retries external effects itself.
type Store struct {
	mu      sync.Mutex
	db      *sql.DB
	changed chan struct{}
	closed  bool
	now     func() time.Time
}

// Open adds only application-owned tables, rejecting unknown schema versions
// transactionally. Calls abandoned by a prior Host are terminalized, never retried.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: database path required", ErrInvalid)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*Store, error) { _ = db.Close(); return nil, err }
	if _, err = db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fail(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS app_schema (singleton INTEGER PRIMARY KEY CHECK(singleton=1), version INTEGER NOT NULL)`); err != nil {
		return fail(err)
	}
	var version int
	err = tx.QueryRow(`SELECT version FROM app_schema WHERE singleton=1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		// An unversioned existing app table is not safe to reinterpret.
		var count int
		if err = tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name GLOB 'app_*' AND name!='app_schema'`).Scan(&count); err != nil {
			return fail(err)
		}
		if count != 0 {
			return fail(fmt.Errorf("%w: unversioned application tables", ErrInvalid))
		}
		_, err = tx.Exec(`INSERT INTO app_schema(singleton,version) VALUES(1,1)`)
	} else if err == nil && version != 1 {
		return fail(fmt.Errorf("%w: unsupported application schema %d", ErrInvalid, version))
	}
	if err != nil {
		return fail(err)
	}
	_, err = tx.Exec(`
 CREATE TABLE IF NOT EXISTS app_connections (
  principal TEXT NOT NULL, application TEXT NOT NULL, connection TEXT PRIMARY KEY,
  operation TEXT NOT NULL, digest TEXT NOT NULL, credential_hash TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL, expires INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0,
  UNIQUE(principal,operation)
 );
 CREATE TABLE IF NOT EXISTS app_bindings (
  session TEXT PRIMARY KEY, principal TEXT NOT NULL, application TEXT NOT NULL,
  connection TEXT NOT NULL, body BLOB NOT NULL
 );
 CREATE TABLE IF NOT EXISTS app_operations (
  principal TEXT NOT NULL, application TEXT NOT NULL, connection TEXT NOT NULL,
  operation TEXT NOT NULL, digest TEXT NOT NULL, request BLOB NOT NULL, result BLOB,
  PRIMARY KEY(connection,operation)
 );
 CREATE TABLE IF NOT EXISTS app_calls (
  principal TEXT NOT NULL, application TEXT NOT NULL, connection TEXT NOT NULL,
  session TEXT NOT NULL, call TEXT NOT NULL, digest TEXT NOT NULL, body BLOB NOT NULL,
  state TEXT NOT NULL, result BLOB, PRIMARY KEY(connection,session,call)
 );
 CREATE TABLE IF NOT EXISTS app_resources (
  id TEXT PRIMARY KEY, principal TEXT NOT NULL, application TEXT NOT NULL,
  connection TEXT NOT NULL, session TEXT NOT NULL, operation TEXT NOT NULL,
  digest TEXT NOT NULL, body BLOB NOT NULL, data BLOB NOT NULL,
  UNIQUE(connection,session,operation)
 );`)
	if err != nil {
		return fail(err)
	}
	// Version 1 requires complete mutation intent. Reject an incompatible
	// unshipped shape before recovery changes any persisted call state.
	if _, err = tx.Exec(`SELECT request FROM app_operations LIMIT 0`); err != nil {
		return fail(fmt.Errorf("%w: incompatible application operation schema: %w", ErrInvalid, err))
	}
	if _, err = tx.Exec(`UPDATE app_calls SET state=CASE state WHEN 'pending' THEN 'cancelled' ELSE 'unknown' END WHERE state IN ('pending','claimed')`); err != nil {
		return fail(err)
	}

	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	return &Store{db: db, changed: make(chan struct{}), now: time.Now}, nil
}

// Close terminalizes outstanding intents and releases waiting callers.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	_, err := s.db.Exec(`UPDATE app_calls SET state=CASE state WHEN 'pending' THEN 'cancelled' ELSE 'unknown' END WHERE state IN ('pending','claimed')`)
	s.closed = true
	s.signal()
	return errors.Join(err, s.db.Close())
}

func (s *Store) signal()             { close(s.changed); s.changed = make(chan struct{}) }
func digestBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func encode(value any) ([]byte, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return b, nil
}
func stableID(prefix string, parts ...string) string {
	b, _ := json.Marshal(parts)
	return prefix + digestBytes(b)
}
func validID(v string) bool {
	return v != "" && len(v) <= 512 && strings.TrimSpace(v) == v && !strings.ContainsRune(v, 0)
}
func validScope(v Scope) bool {
	return validID(v.PrincipalID) && validID(v.ApplicationID) && validID(v.ConnectionID)
}
func credentialHash(v string) (string, error) {
	if len(v) != len("app-client-")+64 || !strings.HasPrefix(v, "app-client-") {
		return "", ErrUnauthorized
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(v, "app-client-")); err != nil {
		return "", ErrUnauthorized
	}
	return digestBytes([]byte(v)), nil
}
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *Store) connection(ctx context.Context, scope Scope) (Connection, error) {
	if s.closed {
		return Connection{}, ErrClosed
	}
	if !validScope(scope) {
		return Connection{}, ErrUnauthorized
	}
	c := Connection{Scope: scope}
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT name,expires,revoked FROM app_connections WHERE principal=? AND application=? AND connection=?`, scope.PrincipalID, scope.ApplicationID, scope.ConnectionID).Scan(&c.Name, &expires, &c.Revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, ErrUnauthorized
	}
	c.ExpiresAt = time.Unix(0, expires).UTC()
	return c, err
}
func (s *Store) active(ctx context.Context, scope Scope) (Connection, error) {
	c, err := s.connection(ctx, scope)
	if err != nil {
		return c, err
	}
	if c.Revoked {
		return c, ErrRevoked
	}
	if !s.now().Before(c.ExpiresAt) {
		return c, ErrLeaseExpired
	}
	return c, nil
}

// Register is enrollment for a trusted authenticated Host principal. Applications
// cannot grant themselves a principal. Equal enrollment retries retain the same
// identities and original lease; changed requests conflict, including credentials.
func (s *Store) Register(ctx context.Context, principal string, r Registration) (Connection, error) {
	hash, err := credentialHash(r.Credential)
	if err != nil {
		return Connection{}, err
	}
	if !validID(principal) || !validID(r.OperationID) || !validID(r.Name) {
		return Connection{}, ErrInvalid
	}
	payload, _ := encode([]string{principal, r.OperationID, r.Name, hash})
	digest := digestBytes(payload)
	scope := Scope{PrincipalID: principal, ApplicationID: stableID("app-", principal, r.OperationID), ConnectionID: stableID("connection-", principal, r.OperationID)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Connection{}, ErrClosed
	}
	var previous string
	err = s.db.QueryRowContext(ctx, `SELECT digest FROM app_connections WHERE principal=? AND operation=?`, principal, r.OperationID).Scan(&previous)
	if err == nil {
		if previous != digest {
			return Connection{}, ErrConflict
		}
		return s.connection(ctx, scope)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Connection{}, err
	}
	var used int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM app_connections WHERE credential_hash=?`, hash).Scan(&used); err != nil {
		return Connection{}, err
	}
	if used != 0 {
		return Connection{}, ErrConflict
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO app_connections(principal,application,connection,operation,digest,credential_hash,name,expires) VALUES(?,?,?,?,?,?,?,?)`, principal, scope.ApplicationID, scope.ConnectionID, r.OperationID, digest, hash, r.Name, s.now().Add(LeaseDuration).UnixNano())
	if err != nil {
		return Connection{}, err
	}
	s.signal()
	return s.connection(ctx, scope)
}

// Authenticate proves credential ownership, even when the lease no longer allows
// mutation. Every mutation separately checks current revocation and expiry.
func (s *Store) Authenticate(ctx context.Context, credential string) (Scope, error) {
	hash, err := credentialHash(credential)
	if err != nil {
		return Scope{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Scope{}, ErrClosed
	}
	var scope Scope
	err = s.db.QueryRowContext(ctx, `SELECT principal,application,connection FROM app_connections WHERE credential_hash=?`, hash).Scan(&scope.PrincipalID, &scope.ApplicationID, &scope.ConnectionID)
	if errors.Is(err, sql.ErrNoRows) {
		return Scope{}, ErrUnauthorized
	}
	return scope, err
}

// Connection returns the owned lease, including expired or revoked state.
func (s *Store) Connection(ctx context.Context, scope Scope) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connection(ctx, scope)
}

// Renew explicitly reauthorizes the same connection, including after expiry.
// Expired callback intents are terminalized atomically with the new deadline;
// their receipts never regain dispatch authority. Revocation remains permanent.
func (s *Store) Renew(ctx context.Context, scope Scope) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.connection(ctx, scope)
	if err != nil {
		return Connection{}, err
	}
	if c.Revoked {
		return Connection{}, ErrRevoked
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Connection{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if !now.Before(c.ExpiresAt) {
		if _, err = tx.ExecContext(ctx, `UPDATE app_calls SET state=CASE state WHEN 'pending' THEN 'cancelled' ELSE 'unknown' END WHERE connection=? AND state IN ('pending','claimed')`, scope.ConnectionID); err != nil {
			return Connection{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE app_connections SET expires=? WHERE connection=?`, now.Add(LeaseDuration).UnixNano(), scope.ConnectionID); err != nil {
		return Connection{}, err
	}
	if err = tx.Commit(); err != nil {
		return Connection{}, err
	}
	s.signal()
	return s.connection(ctx, scope)
}

// Revoke permanently withdraws a connection, including after lease expiry, and
// terminalizes its calls. Claimed effects become unknown; unclaimed intents are
// cancelled. Late results cannot revive them.
func (s *Store) Revoke(ctx context.Context, scope Scope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.connection(ctx, scope)
	if err != nil {
		return err
	}
	if c.Revoked {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `UPDATE app_connections SET revoked=1 WHERE connection=?`, scope.ConnectionID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE app_calls SET state=CASE state WHEN 'pending' THEN 'cancelled' ELSE 'unknown' END WHERE connection=? AND state IN ('pending','claimed')`, scope.ConnectionID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.signal()
	return nil
}

// CheckActive validates current mutation authority without granting a durable lease snapshot.
func (s *Store) CheckActive(ctx context.Context, scope Scope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.active(ctx, scope)
	return err
}
