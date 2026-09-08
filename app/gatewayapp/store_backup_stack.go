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
	if _, err := database.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("checkpoint Control database for Store backup: %w", err)
	}
	if err := database.Close(); err != nil {
		return fmt.Errorf("close Control database after Store backup checkpoint: %w", err)
	}
	closed = true
	return nil
}
