package gatewayapp

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// controlHostIdentity persists one Store identity independent of individual
// product authorities. The instance identity changes on each Host lifetime.
type controlHostIdentity struct {
	storeID    string
	instanceID string
}

func openControlHostIdentity(ctx context.Context, path string) (controlHostIdentity, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return controlHostIdentity{}, err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL;
CREATE TABLE IF NOT EXISTS control_host_identity (key TEXT PRIMARY KEY, id TEXT NOT NULL);`); err != nil {
		return controlHostIdentity{}, err
	}
	instance := uuid.NewString()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return controlHostIdentity{}, err
	}
	candidate := "control-store-" + hex.EncodeToString(nonce[:])
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return controlHostIdentity{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM control_host_identity WHERE key='store'`).Scan(&stored); err == nil {
		if stored == "" {
			return controlHostIdentity{}, fmt.Errorf("gatewayapp: durable Host Store identity is empty")
		}
		if err := tx.Commit(); err != nil {
			return controlHostIdentity{}, err
		}
		return controlHostIdentity{storeID: stored, instanceID: instance}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return controlHostIdentity{}, err
	}
	// Once, copy only the pre-existing Store identity. Historical product rows
	// are never read for execution, changed, deleted, or migrated.
	var legacyTable string
	err = tx.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='bot_authority'`).Scan(&legacyTable)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return controlHostIdentity{}, err
	}
	if err == nil {
		var previous string
		err = tx.QueryRowContext(ctx, `SELECT json_extract(body,'$.id') FROM bot_authority WHERE kind='identity' AND id='host'`).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return controlHostIdentity{}, err
		}
		if err == nil && previous != "" {
			candidate = previous
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_host_identity(key,id) VALUES('store',?) ON CONFLICT(key) DO NOTHING`, candidate); err != nil {
		return controlHostIdentity{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM control_host_identity WHERE key='store'`).Scan(&stored); err != nil {
		return controlHostIdentity{}, err
	}
	if stored == "" {
		return controlHostIdentity{}, fmt.Errorf("gatewayapp: durable Host Store identity is empty")
	}
	if err := tx.Commit(); err != nil {
		return controlHostIdentity{}, err
	}
	return controlHostIdentity{storeID: stored, instanceID: instance}, nil
}

// StoreID is the durable Host Store identity presented to Control clients.
func (s *Stack) StoreID() string {
	if s == nil {
		return ""
	}
	return s.identity.storeID
}

// InstanceID identifies the current Host lifetime, not a Session or Store.
func (s *Stack) InstanceID() string {
	if s == nil {
		return ""
	}
	return s.identity.instanceID
}
