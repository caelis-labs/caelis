package gatewayapp

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckpointStoreControlDatabaseRejectsBusyReaderAndCompletesAfterRelease(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	storeDir := t.TempDir()
	controlDir := filepath.Dir(controlStoreDatabasePath(storeDir))
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		t.Fatal(err)
	}

	database, err := sql.Open("sqlite", controlStoreDatabasePath(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	var journalMode string
	if err := database.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&journalMode); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	if _, err := database.ExecContext(ctx, `CREATE TABLE records (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO records (value) VALUES ('before-reader')`); err != nil {
		t.Fatal(err)
	}

	reader, err := sql.Open("sqlite", controlStoreDatabasePath(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	reader.SetMaxOpenConns(1)
	defer reader.Close()
	readTx, err := reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := readTx.QueryRowContext(ctx, `SELECT value FROM records WHERE id = 1`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "before-reader" {
		t.Fatalf("reader value = %q", value)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO records (value) VALUES ('after-reader')`); err != nil {
		t.Fatal(err)
	}

	if err := checkpointStoreControlDatabase(ctx, storeDir); err == nil {
		t.Fatal("checkpointStoreControlDatabase() succeeded while a reader held an older WAL snapshot")
	} else {
		t.Logf("busy checkpoint status: %v", err)
		if !strings.Contains(err.Error(), "busy=1") || !strings.Contains(err.Error(), "log=") || !strings.Contains(err.Error(), "checkpointed=") {
			t.Fatalf("checkpointStoreControlDatabase() error = %v, want SQLite checkpoint status", err)
		}
	}
	if err := readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := checkpointStoreControlDatabase(ctx, storeDir); err != nil {
		t.Fatalf("checkpointStoreControlDatabase() after reader release: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	copyPath := filepath.Join(storeDir, "control-copy.sqlite")
	raw, err := os.ReadFile(controlStoreDatabasePath(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	copyDB, err := sql.Open("sqlite", copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var count int
	if err := copyDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM records WHERE value = 'after-reader'`).Scan(&count); err != nil {
		t.Fatalf("query copied control database: %v", err)
	}
	if count != 1 {
		t.Fatalf("copied control database contains %d committed post-reader records, want 1", count)
	}
}
