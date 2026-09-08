package gatewayapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
)

// WriteStoreBackup quiesces this Host, checkpoints the Host-owned Control
// SQLite WAL, and delegates the Memory component to its owner. Quiesce is
// permanent for the Stack, so callers should close the Stack after the
// archive has been published.
func (s *Stack) WriteStoreBackup(ctx context.Context, output io.Writer) (StoreBackupManifest, error) {
	if s == nil {
		return StoreBackupManifest{}, errors.New("gatewayapp: Stack is required for Store backup")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.Quiesce(ctx); err != nil {
		return StoreBackupManifest{}, fmt.Errorf("quiesce Host for Store backup: %w", err)
	}
	if s.memoryRuntime == nil {
		return StoreBackupManifest{}, ErrStoreBackupMemory
	}
	storeDir := s.composition.authorities.storeDir
	if err := checkpointStoreControlDatabase(ctx, storeDir); err != nil {
		return StoreBackupManifest{}, err
	}
	return WriteStoreBackup(ctx, StoreBackupOptions{
		StoreDir:     storeDir,
		MemoryBackup: s.memoryRuntime.Backup,
	}, output)
}

func checkpointStoreControlDatabase(ctx context.Context, storeDir string) error {
	path := controlStoreDatabasePath(storeDir)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("inspect Control database before Store backup: %w", err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open Control database for Store backup: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = database.Close()
		}
	}()
	var busy, logFrames, checkpointedFrames int
	if err := database.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return fmt.Errorf("checkpoint Control database for Store backup: %w", err)
	}
	// wal_checkpoint reports a busy reader as a result row rather than a SQL
	// execution error. A non-empty WAL would make copying the main database an
	// incomplete snapshot, so refuse the backup until the reader is gone and
	// SQLite has fully checkpointed and truncated the WAL.
	// SQLite reports (-1, -1) when the database is not in WAL mode; there is
	// then no sidecar to include in the snapshot. A positive log count means
	// frames remain outside the main database and is never safe to copy.
	if busy != 0 || logFrames > 0 {
		return fmt.Errorf(
			"checkpoint Control database for Store backup incomplete: busy=%d log=%d checkpointed=%d",
			busy,
			logFrames,
			checkpointedFrames,
		)
	}
	if err := database.Close(); err != nil {
		return fmt.Errorf("close Control database after Store backup checkpoint: %w", err)
	}
	closed = true
	return nil
}
