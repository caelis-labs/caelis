package file

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// newLogicalTestStore is only for high-cardinality tests whose contract is
// logical persistence, recovery, or concurrency rather than crash durability.
// Tests whose assertions cover commit classification, recovery-marker or WAL
// durability, or sync failures must use NewStore or inject a failing durability
// operation directly.
func newLogicalTestStore(t testing.TB, cfg Config) *Store {
	t.Helper()
	store := NewStore(cfg)
	store.durability = durabilityOps{
		syncFile:      func(*os.File) error { return nil },
		syncDirectory: func(string) error { return nil },
		configureSQLite: func(db *sql.DB) error {
			_, err := db.Exec(`PRAGMA synchronous = OFF`)
			return err
		},
	}
	return store
}

func TestWriteDocumentDurabilityBoundaries(t *testing.T) {
	t.Run("temporary file sync failure is not committed", func(t *testing.T) {
		fault := errors.New("sync temporary document")
		store := NewStore(Config{RootDir: t.TempDir()})
		doc, path := durabilityTestDocument(store, "document-sync-failure")
		store.durability = durabilityOps{
			syncFile: func(*os.File) error { return fault },
		}

		err := store.writeDocumentInternal(doc, false, false)
		if !errors.Is(err, fault) {
			t.Fatalf("writeDocumentInternal() error = %v, want %v", err, fault)
		}
		if documentWriteCommitted(err) {
			t.Fatalf("writeDocumentInternal() error = %v, want uncommitted failure", err)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("Stat(document) error = %v, want document absent", statErr)
		}
	})

	t.Run("directory sync failure follows committed rename", func(t *testing.T) {
		fault := errors.New("sync document directory")
		store := NewStore(Config{RootDir: t.TempDir()})
		doc, path := durabilityTestDocument(store, "document-directory-failure")
		store.durability = durabilityOps{
			syncFile:      func(*os.File) error { return nil },
			syncDirectory: func(string) error { return fault },
		}

		err := store.writeDocumentInternal(doc, false, false)
		if !errors.Is(err, fault) {
			t.Fatalf("writeDocumentInternal() error = %v, want %v", err, fault)
		}
		if !documentWriteCommitted(err) {
			t.Fatalf("writeDocumentInternal() error = %v, want committed failure", err)
		}
		persisted, readErr := store.readDocumentAt(path)
		if readErr != nil {
			t.Fatalf("readDocumentAt() error = %v", readErr)
		}
		if persisted.Session.SessionID != doc.Session.SessionID {
			t.Fatalf("persisted SessionID = %q, want %q", persisted.Session.SessionID, doc.Session.SessionID)
		}
	})
}

func TestAppendEventLogDurabilityRollback(t *testing.T) {
	t.Run("append sync failure resyncs the truncated log", func(t *testing.T) {
		fault := errors.New("sync appended event")
		store := NewStore(Config{RootDir: t.TempDir()})
		documentPath, before := writeDurabilityTestEventLog(t, store.rootDir)
		syncCalls := 0
		store.durability = durabilityOps{
			syncFile: func(*os.File) error {
				syncCalls++
				if syncCalls == 1 {
					return fault
				}
				return nil
			},
			syncDirectory: func(string) error { return nil },
		}

		if _, err := store.appendEventLogTransaction(documentPath, []*session.Event{durabilityTestEvent("event-2", 2)}); !errors.Is(err, fault) {
			t.Fatalf("appendEventLogTransaction() error = %v, want %v", err, fault)
		}
		if syncCalls != 2 {
			t.Fatalf("file sync calls = %d, want append barrier plus rollback barrier", syncCalls)
		}
		assertDurabilityTestLogUnchanged(t, documentPath, before)
	})

	t.Run("directory sync failure reports rollback failure", func(t *testing.T) {
		fault := errors.New("sync event log directory")
		rollbackFault := errors.New("sync rolled back event log directory")
		store := NewStore(Config{RootDir: t.TempDir()})
		documentPath, before := writeDurabilityTestEventLog(t, store.rootDir)
		directorySyncCalls := 0
		store.durability = durabilityOps{
			syncFile: func(*os.File) error { return nil },
			syncDirectory: func(string) error {
				directorySyncCalls++
				if directorySyncCalls == 1 {
					return fault
				}
				return rollbackFault
			},
		}

		_, err := store.appendEventLogTransaction(documentPath, []*session.Event{durabilityTestEvent("event-2", 2)})
		if !errors.Is(err, fault) || !errors.Is(err, rollbackFault) {
			t.Fatalf("appendEventLogTransaction() error = %v, want %v and %v", err, fault, rollbackFault)
		}
		if directorySyncCalls != 2 {
			t.Fatalf("directory sync calls = %d, want append barrier plus rollback barrier", directorySyncCalls)
		}
		assertDurabilityTestLogUnchanged(t, documentPath, before)
	})
}

func TestWriteTransactionDurabilityBoundaries(t *testing.T) {
	t.Run("temporary file sync failure is not committed", func(t *testing.T) {
		fault := errors.New("sync temporary transaction")
		store := NewStore(Config{RootDir: t.TempDir()})
		path := filepath.Join(store.rootDir, "session.txn.json")
		store.durability = durabilityOps{
			syncFile: func(file *os.File) error {
				if filepath.Base(file.Name()) == transactionRecoveryMarkerFilename {
					return nil
				}
				return fault
			},
			syncDirectory: func(string) error { return nil },
		}

		err := store.writeTransaction(path, persistedTransaction{})
		if !errors.Is(err, fault) {
			t.Fatalf("writeTransaction() error = %v, want %v", err, fault)
		}
		if documentWriteCommitted(err) {
			t.Fatalf("writeTransaction() error = %v, want uncommitted failure", err)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("Stat(transaction) error = %v, want transaction absent", statErr)
		}
	})

	t.Run("directory sync failure follows committed rename", func(t *testing.T) {
		fault := errors.New("sync transaction directory")
		store := NewStore(Config{RootDir: t.TempDir()})
		path := filepath.Join(store.rootDir, "session.txn.json")
		directorySyncCalls := 0
		store.durability = durabilityOps{
			syncFile: func(*os.File) error { return nil },
			syncDirectory: func(string) error {
				directorySyncCalls++
				if directorySyncCalls == 2 {
					return fault
				}
				return nil
			},
		}

		err := store.writeTransaction(path, persistedTransaction{})
		if !errors.Is(err, fault) {
			t.Fatalf("writeTransaction() error = %v, want %v", err, fault)
		}
		if !documentWriteCommitted(err) {
			t.Fatalf("writeTransaction() error = %v, want committed failure", err)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("Stat(transaction) error = %v, want committed transaction", statErr)
		}
	})
}

func TestOpenSessionIndexPropagatesDurabilityConfigurationFailure(t *testing.T) {
	fault := errors.New("configure SQLite durability")
	store := NewStore(Config{RootDir: t.TempDir()})
	store.durability.configureSQLite = func(*sql.DB) error { return fault }

	db, err := store.openSessionIndex()
	if db != nil {
		db.Close()
		t.Fatal("openSessionIndex() database is non-nil after configuration failure")
	}
	if !errors.Is(err, fault) {
		t.Fatalf("openSessionIndex() error = %v, want %v", err, fault)
	}
}

func durabilityTestDocument(store *Store, sessionID string) (persistedDocument, string) {
	at := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	doc := persistedDocument{Session: session.Session{
		SessionRef: session.SessionRef{
			AppName:      "caelis",
			UserID:       "user-1",
			SessionID:    sessionID,
			WorkspaceKey: "workspace-1",
		},
		CreatedAt: at,
		UpdatedAt: at,
	}}
	path := store.newDocumentPath(doc.Session)
	store.pathCache[pathCacheKey(doc.Session.SessionID, doc.Session.WorkspaceKey)] = path
	return doc, path
}

func durabilityTestEvent(id string, seq uint64) *session.Event {
	return &session.Event{
		ID:         id,
		Seq:        seq,
		SessionID:  "session-1",
		Type:       session.EventTypeCustom,
		Visibility: session.VisibilityCanonical,
		Text:       id,
	}
}

func writeDurabilityTestEventLog(t testing.TB, root string) (string, []byte) {
	t.Helper()
	documentPath := filepath.Join(root, "rollout-session-1.json")
	logPath := eventLogPath(documentPath)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(event log directory) error = %v", err)
	}
	before := []byte(`{"id":"event-1","seq":1}` + "\n")
	if err := os.WriteFile(logPath, before, 0o600); err != nil {
		t.Fatalf("WriteFile(event log) error = %v", err)
	}
	return documentPath, before
}

func assertDurabilityTestLogUnchanged(t testing.TB, documentPath string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(eventLogPath(documentPath))
	if err != nil {
		t.Fatalf("ReadFile(event log) error = %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("event log = %q, want unchanged %q", got, want)
	}
}
