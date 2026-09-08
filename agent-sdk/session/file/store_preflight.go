package file

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const eventLogSuffix = ".events.jsonl"

// ValidateStoreRoot performs the Session owner's read-only validation needed
// before an offline Store upgrade or backup. It checks document versions and
// durable values, every complete event-log record, and every recovery
// transaction. It never opens a Store, rebuilds an index, migrates a file, or
// writes a recovery marker; callers remain responsible for deciding whether a
// valid recovery transaction is admissible under their quiesce contract.
func ValidateStoreRoot(ctx context.Context, rootDir string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		return errors.New("agent-sdk/session/file: Session root is required")
	}
	rootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return fmt.Errorf("agent-sdk/session/file: resolve Session root: %w", err)
	}
	rootDir = filepath.Clean(rootDir)
	info, err := os.Lstat(rootDir)
	if err != nil {
		return fmt.Errorf("agent-sdk/session/file: inspect Session root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("agent-sdk/session/file: Session root is not a regular directory")
	}
	err = filepath.WalkDir(rootDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("agent-sdk/session/file: Session path %q is a symlink", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("agent-sdk/session/file: Session path %q is not a regular file", path)
		}
		relative, err := filepath.Rel(rootDir, path)
		if err != nil {
			return err
		}
		switch {
		case strings.HasSuffix(entry.Name(), transactionSuffix):
			return validateStoreTransactionFile(path)
		case strings.HasSuffix(entry.Name(), eventLogSuffix):
			return validateStoreEventLogFile(ctx, path)
		case currentDocumentFileName(entry.Name()):
			return validateStoreDocumentFile(path)
		case filepath.ToSlash(relative) == indexFilename,
			filepath.ToSlash(relative) == lockFilename,
			filepath.ToSlash(relative) == generatedTitleMigrationMarkerFilename,
			filepath.ToSlash(relative) == workspaceKeyRepairMarkerFilename,
			filepath.ToSlash(relative) == transactionRecoveryMarkerFilename:
			// The gateway Store preflight applies the quiesce policy for these
			// owner markers after this schema/content pass.
			return nil
		default:
			return nil
		}
	})
	if err != nil {
		return fmt.Errorf("agent-sdk/session/file: validate Session root: %w", err)
	}
	return nil
}

func validateStoreDocumentFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := rejectUnsupportedLegacyDocument(data, path); err != nil {
		return err
	}
	doc, _, err := decodePersistedDocumentWithReport(data)
	if err != nil {
		return fmt.Errorf("decode Session document %s: %w", path, err)
	}
	if err := session.ValidateMetadata(doc.Session.Metadata); err != nil {
		return fmt.Errorf("validate Session document %s metadata: %w", path, err)
	}
	if err := session.ValidateState(doc.State); err != nil {
		return fmt.Errorf("validate Session document %s state: %w", path, err)
	}
	for requestID, event := range doc.PendingApprovals {
		if event == nil {
			return fmt.Errorf("validate Session document %s pending approval %q: event is nil", path, requestID)
		}
		if err := session.ValidateDurableCoreEvent(event); err != nil {
			return fmt.Errorf("validate Session document %s pending approval %q: %w", path, requestID, err)
		}
	}
	return nil
}

func validateStoreEventLogFile(ctx context.Context, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	lineNo := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := reader.ReadString('\n')
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			return nil
		}
		lineNo++
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if errors.Is(readErr, io.EOF) {
				return fmt.Errorf("Session event log %s line %d is not newline terminated", path, lineNo)
			}
			if err := rejectUnsupportedLegacyEventLogLine([]byte(trimmed), path, lineNo); err != nil {
				return err
			}
			migrated, err := session.MigrateEventJSON(json.RawMessage(trimmed))
			if err != nil {
				return fmt.Errorf("decode Session event log %s line %d: %w", path, lineNo, err)
			}
			var event session.Event
			if err := json.Unmarshal(migrated, &event); err != nil {
				return fmt.Errorf("decode Session event log %s line %d: %w", path, lineNo, err)
			}
			if err := session.ValidateDurableCoreEvent(&event); err != nil {
				return fmt.Errorf("validate Session event log %s line %d: %w", path, lineNo, err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}

func validateStoreTransactionFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	record, _, err := decodePersistedTransactionWithReport(data)
	if err != nil {
		return fmt.Errorf("decode Session transaction %s: %w", path, err)
	}
	if record.Kind != transactionKind || record.Version != transactionVersion {
		return fmt.Errorf("unsupported Session transaction %q version %d", record.Kind, record.Version)
	}
	if err := validateStoreDocument(record.Document, path); err != nil {
		return err
	}
	for index, event := range record.Events {
		if err := session.ValidateDurableCoreEvent(event); err != nil {
			return fmt.Errorf("validate Session transaction %s event %d: %w", path, index, err)
		}
	}
	return nil
}

func validateStoreDocument(doc persistedDocument, path string) error {
	if err := session.ValidateMetadata(doc.Session.Metadata); err != nil {
		return fmt.Errorf("validate Session transaction %s metadata: %w", path, err)
	}
	if err := session.ValidateState(doc.State); err != nil {
		return fmt.Errorf("validate Session transaction %s state: %w", path, err)
	}
	for requestID, event := range doc.PendingApprovals {
		if event == nil {
			return fmt.Errorf("validate Session transaction %s pending approval %q: event is nil", path, requestID)
		}
		if err := session.ValidateDurableCoreEvent(event); err != nil {
			return fmt.Errorf("validate Session transaction %s pending approval %q: %w", path, requestID, err)
		}
	}
	return nil
}
