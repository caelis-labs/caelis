package sessionprofile

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
	"github.com/caelis-labs/caelis/control/internal/sessionstate"
	"github.com/caelis-labs/caelis/control/internal/storemigration/applicationv3"
	_ "modernc.org/sqlite"
)

// Store errors distinguish ownership, admission, idempotency, and lifecycle failures.
var (
	ErrUnauthorized       = errorcode.New(errorcode.PermissionDenied, "Session profile scope is unauthorized")
	ErrResourceExpired    = errorcode.New(errorcode.FailedPrecondition, "Session profile resource expired")
	ErrRevoked            = errorcode.New(errorcode.FailedPrecondition, "Session profile archived")
	ErrConflict           = errorcode.New(errorcode.Conflict, "Session profile operation conflicts with durable intent")
	ErrNotFound           = errorcode.New(errorcode.NotFound, "Session profile record not found")
	ErrInvalid            = errorcode.New(errorcode.InvalidArgument, "invalid Session profile request")
	ErrAlreadyClaimed     = errorcode.New(errorcode.Conflict, "Session profile call already claimed or terminal")
	ErrClosed             = errorcode.New(errorcode.Unavailable, "Session profile store closed")
	ErrUnsupported        = errorcode.New(errorcode.Unsupported, "unsupported Session profile capability")
	ErrCancelled          = errorcode.New(errorcode.Cancelled, "Session profile call cancelled")
	ErrConfigurationStale = errorcode.New(errorcode.Conflict, "Session profile configuration revision is stale")
)

// Store owns explicit Session configuration, callback receipts and resources.
// Native Sessions own execution/history; the common operation ledger owns every
// command admission and receipt. There are no client credentials or leases here.
// The caller must hold exclusive Host ownership before Open and drain producers
// before Close; SQLite connection count is not a multi-Host writer protocol.
type Store struct {
	mu      sync.Mutex
	db      *sql.DB
	changed chan struct{}
	closed  bool
	now     func() time.Time
}

// Open performs the bounded old-format import before opening the current Store.
// Remove the importer when the minimum supported on-disk format becomes v3.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: database path required", ErrInvalid)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*Store, error) { _ = db.Close(); return nil, e }
	if _, err = db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		return fail(err)
	}
	if err = applicationv3.Run(context.Background(), db, time.Now()); err != nil {
		return fail(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = sessionstate.EnsureSchema(context.Background(), tx); err != nil {
		return fail(err)
	}
	if _, err = tx.Exec(`UPDATE client_tool_calls SET state=CASE state WHEN 'pending' THEN 'cancelled' ELSE 'unknown' END WHERE state IN ('pending','claimed')`); err != nil {
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	return &Store{db: db, changed: make(chan struct{}), now: time.Now}, nil
}

// Close terminalizes abandoned callback intents without replaying effects.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	_, err := s.db.Exec(`UPDATE client_tool_calls SET state=CASE state WHEN 'pending' THEN 'cancelled' ELSE 'unknown' END WHERE state IN ('pending','claimed')`)
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
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func (s *Store) checkScope(ctx context.Context, scope Scope) error {
	if s.closed {
		return ErrClosed
	}
	if !validID(scope.PrincipalID) {
		return ErrUnauthorized
	}
	return ctx.Err()
}

// CheckActive checks Store availability for the authenticated native owner.
func (s *Store) CheckActive(ctx context.Context, scope Scope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkScope(ctx, scope)
}
