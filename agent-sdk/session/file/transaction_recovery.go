package file

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// The root lock covers the scan, WAL application, index publication and marker
// removal. Only failures in the contents of a single rollout may be isolated.
func (s *Store) recoverTransactions(ctx context.Context) error {
	if s.transactionRecoveryScan != nil {
		s.transactionRecoveryScan()
	}
	retained := map[string]string{}
	err := filepath.WalkDir(s.rootDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), transactionSuffix) {
			return nil
		}
		key := rolloutPathKey(s.rootDir, strings.TrimSuffix(path, transactionSuffix))
		if key == "" {
			return nil
		}
		err = s.recoverTransaction(ctx, path, key)
		if errors.Is(err, session.ErrUnreadableSession) {
			retained[path] = key
			return nil
		}
		if err != nil {
			return fmt.Errorf("agent-sdk/session/file: recover committed transaction %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.indexRetainedTransactions(retained); err != nil {
		return err
	}
	return s.clearTransactionRecoveryMarker(ctx)
}

func (s *Store) recoverTransaction(ctx context.Context, path, pathKey string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	record, err := decodePersistedTransaction(data)
	if err != nil {
		return fmt.Errorf("%w: %w", session.ErrUnreadableSession, err)
	}
	if sanitizeSessionID(record.Document.Session.SessionID) != pathKey {
		return fmt.Errorf("%w: transaction names a different Session than its rollout", session.ErrUnreadableSession)
	}
	documentPath := strings.TrimSuffix(path, transactionSuffix)
	if err := s.validateRecoveryIdentity(documentPath, record.Document.Session); err != nil {
		return err
	}
	// Recovery uses the physical suffix. A prewarmed index belongs to the
	// caller's subsequent append and cannot authorize this committed batch.
	err = s.applyTransaction(ctx, path, record, nil)
	if errors.Is(err, session.ErrEventConflict) {
		return fmt.Errorf("%w: %w", session.ErrUnreadableSession, err)
	}
	return err
}

func (s *Store) validateRecoveryIdentity(documentPath string, sess session.Session) error {
	db, err := s.openSessionIndex()
	if err != nil {
		return err
	}
	defer db.Close()
	rel, err := filepath.Rel(s.rootDir, documentPath)
	if err != nil {
		return err
	}
	rows, err := db.Query(`SELECT session_id, app_name, user_id, created_at_ns, path FROM sessions WHERE session_id = ? OR path IN (?, ?)`, sess.SessionID, rel, documentPath)
	if err != nil {
		return err
	}
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, app, user, path string
		var createdAt int64
		if err := rows.Scan(&id, &app, &user, &createdAt, &path); err != nil {
			return err
		}
		if id != sess.SessionID || app != sess.AppName || user != sess.UserID || createdAt != timeToUnixNano(sess.CreatedAt) || (path != rel && path != documentPath) {
			return fmt.Errorf("%w: transaction does not match indexed Session identity", session.ErrUnreadableSession)
		}
		indexed = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !indexed && (sess.CreatedAt.IsZero() || s.newDocumentPath(sess) != documentPath) {
		return fmt.Errorf("%w: transaction path does not match Session identity", session.ErrUnreadableSession)
	}
	return nil
}

// A creation WAL may be the only bytes on disk for its Session. Index the
// physical addresses of retained WALs before clearing the shared marker so an
// already-running process cannot recreate one under a different timestamp.
func (s *Store) indexRetainedTransactions(retained map[string]string) error {
	db, err := s.openSessionIndex()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM unreadable_transactions`); err != nil {
		return err
	}
	for path, key := range retained {
		rel, err := filepath.Rel(s.rootDir, path)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO unreadable_transactions(path_key, path)
			SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM sessions WHERE path IN (?, ?))`,
			key, rel, strings.TrimSuffix(rel, transactionSuffix), strings.TrimSuffix(path, transactionSuffix)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// unindexedTransactionError runs only after an exact Session lookup misses.
func (s *Store) unindexedTransactionError(db *sql.DB, id string) error {
	rows, err := db.Query(`SELECT path FROM unreadable_transactions WHERE path_key = ?`, sanitizeSessionID(id))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rel string
		if err := rows.Scan(&rel); err != nil {
			return err
		}
		path := filepath.Join(s.rootDir, rel)
		if !filepath.IsLocal(rel) || !strings.HasSuffix(rel, transactionSuffix) || rolloutPathKey(s.rootDir, strings.TrimSuffix(path, transactionSuffix)) != sanitizeSessionID(id) {
			return errors.New("agent-sdk/session/file: invalid transaction path in Session index")
		}
		if err := retainedTransactionError(path); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return session.ErrSessionNotFound
}

func retainedTransactionError(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := decodePersistedTransaction(data); err != nil {
		return fmt.Errorf("%w: committed transaction %s: %w", session.ErrUnreadableSession, path, err)
	}
	return fmt.Errorf("%w: committed transaction %s conflicts with stored Session", session.ErrUnreadableSession, path)
}
