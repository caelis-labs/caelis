package gatewayapp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/atomicfile"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/version"
)

const (
	storeUpgradeJournalName        = ".caelis-upgrade.json"
	storeUpgradeJournalFormat      = "caelis.store-upgrade.v1"
	storeUpgradeWriterCapability   = "caelis.store-writer.v1"
	storeUpgradeJournalTempPattern = ".caelis-upgrade-*.tmp"
)

type storeUpgradeJournal struct {
	Format            string    `json:"format"`
	State             string    `json:"state"`
	StoreDir          string    `json:"store_dir"`
	TargetWriter      string    `json:"target_writer"`
	PreparedBy        string    `json:"prepared_by,omitempty"`
	SourceGeneration  string    `json:"source_generation"`
	StorageGeneration string    `json:"storage_generation"`
	SchemaVersion     int       `json:"schema_version"`
	PreflightDigest   string    `json:"preflight_digest"`
	CreatedAt         time.Time `json:"created_at"`
}

type storeUpgradePreflight struct {
	Digest string
}

func currentStoreUpgradeWriter() string {
	return storeUpgradeWriterCapability
}

func readStoreUpgradePreflight(ctx context.Context, storeDir string) (storeUpgradePreflight, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return storeUpgradePreflight{}, err
	}
	storeDir, err := normalizeStoreBackupDir(storeDir)
	if err != nil {
		return storeUpgradePreflight{}, err
	}
	hash := sha256.New()
	add := func(name string, raw []byte) {
		_, _ = io.WriteString(hash, name)
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(raw)
		_, _ = hash.Write([]byte{0})
	}

	configPath := filepath.Join(storeDir, "config.json")
	configRaw, err := readStoreUpgradeRegularFile(configPath, "Config")
	if err != nil {
		return storeUpgradePreflight{}, err
	}
	if err := validateStoreBackupConfig(configRaw); err != nil {
		return storeUpgradePreflight{}, fmt.Errorf("store upgrade preflight Config: %w", err)
	}
	add("config", configRaw)

	controlPath := controlStoreDatabasePath(storeDir)
	if err := rejectStoreSQLiteSidecars(controlPath, "Control"); err != nil {
		return storeUpgradePreflight{}, err
	}
	if err := appserver.ValidateSQLiteOperationStoreReadOnly(ctx, controlPath); err != nil {
		return storeUpgradePreflight{}, fmt.Errorf("store upgrade preflight Control owner validation: %w", err)
	}
	controlRaw, err := readStoreUpgradeRegularFile(controlPath, "Control database")
	if err != nil {
		return storeUpgradePreflight{}, err
	}
	sqlitePath := filepath.ToSlash(controlPath)
	if filepath.VolumeName(controlPath) != "" && !strings.HasPrefix(sqlitePath, "/") {
		sqlitePath = "/" + sqlitePath
	}
	database, err := sql.Open("sqlite", (&url.URL{
		Scheme: "file", Path: sqlitePath, RawQuery: "mode=ro&immutable=1",
	}).String())
	if err != nil {
		return storeUpgradePreflight{}, fmt.Errorf("open Control database for Store upgrade preflight: %w", err)
	}
	database.SetMaxOpenConns(1)
	var integrity string
	queryErr := database.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity)
	if queryErr != nil {
		_ = database.Close()
		return storeUpgradePreflight{}, fmt.Errorf("check Control database for Store upgrade preflight: %w", queryErr)
	}
	if !strings.EqualFold(strings.TrimSpace(integrity), "ok") {
		_ = database.Close()
		return storeUpgradePreflight{}, fmt.Errorf("store upgrade preflight: Control integrity check = %q", integrity)
	}
	acpErr := validateStoreACPPreparationReadOnly(ctx, database)
	closeErr := database.Close()
	if acpErr != nil {
		return storeUpgradePreflight{}, fmt.Errorf("store upgrade preflight Control ACP owner validation: %w", acpErr)
	}
	if closeErr != nil {
		return storeUpgradePreflight{}, fmt.Errorf("close Control database after Store upgrade preflight: %w", closeErr)
	}
	add("control", controlRaw)

	sessionsDir := filepath.Join(storeDir, "sessions")
	info, err := os.Lstat(sessionsDir)
	if err != nil {
		return storeUpgradePreflight{}, fmt.Errorf("inspect Session directory for Store upgrade preflight: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return storeUpgradePreflight{}, errors.New("store upgrade preflight: Session directory is not a regular directory")
	}
	if err := sessionfile.ValidateStoreRoot(ctx, sessionsDir); err != nil {
		return storeUpgradePreflight{}, fmt.Errorf("store upgrade preflight Session owner validation: %w", err)
	}
	add("sessions/", nil)
	walkErr := filepath.WalkDir(sessionsDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("store upgrade preflight: Session path %q is not a regular file", path)
		}
		relative, err := filepath.Rel(sessionsDir, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if isSessionIndexSQLiteSidecar(relative) {
			return fmt.Errorf("store upgrade preflight: Session index SQLite sidecar %q requires owner recovery", relative)
		}
		switch relative {
		case ".sessions.index.sqlite", ".sessions.lock", ".sessions.generated-title-unicode-v1", ".sessions.workspace-key-repair-v1.json":
			// These are owner-maintained durable index/migration markers. Include
			// them in the preflight digest while excluding no active state.
		case ".sessions.transactions.pending":
			return fmt.Errorf("store upgrade preflight: transient Session path %q is present", relative)
		default:
			if strings.HasPrefix(relative, ".") || strings.HasSuffix(relative, ".lock") || strings.HasSuffix(relative, ".tmp") {
				return fmt.Errorf("store upgrade preflight: transient Session path %q is present", relative)
			}
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		add("sessions/"+relative, raw)
		return nil
	})
	if walkErr != nil {
		return storeUpgradePreflight{}, walkErr
	}
	return storeUpgradePreflight{Digest: hex.EncodeToString(hash.Sum(nil))}, nil
}

func validateStoreACPPreparationReadOnly(ctx context.Context, database *sql.DB) error {
	tableRows, err := database.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return fmt.Errorf("inspect Control tables: %w", err)
	}
	allowedTables := map[string]bool{
		"control_operation_policy": true,
		"control_operations":       true,
		"control_acp_preparations": true,
	}
	for tableRows.Next() {
		var name string
		if err := tableRows.Scan(&name); err != nil {
			tableRows.Close()
			return fmt.Errorf("read Control tables: %w", err)
		}
		if !allowedTables[name] && !strings.HasPrefix(name, "sqlite_") {
			tableRows.Close()
			return fmt.Errorf("unsupported Control table %q", name)
		}
	}
	if err := tableRows.Err(); err != nil {
		tableRows.Close()
		return fmt.Errorf("read Control tables: %w", err)
	}
	if err := tableRows.Close(); err != nil {
		return fmt.Errorf("close Control tables: %w", err)
	}
	rows, err := database.QueryContext(ctx, `PRAGMA table_info(control_acp_preparations)`)
	if err != nil {
		return fmt.Errorf("inspect ACP preparation schema: %w", err)
	}
	columns := make([]string, 0, 7)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("read ACP preparation schema: %w", err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read ACP preparation schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close ACP preparation schema: %w", err)
	}
	// Older stopped Stores may not have the optional ACP staging table. A
	// present table is owner state and must match the exact current schema;
	// preflight never creates it as a side effect.
	if len(columns) == 0 {
		return nil
	}
	want := []string{"ref", "principal_id", "operation_id", "intent_digest", "content_digest", "expires_at_ns", "record_json"}
	if len(columns) != len(want) {
		return fmt.Errorf("unsupported ACP preparation schema columns %v", columns)
	}
	for index := range want {
		if columns[index] != want[index] {
			return fmt.Errorf("unsupported ACP preparation schema columns %v", columns)
		}
	}
	rows, err = database.QueryContext(ctx, `
SELECT ref, principal_id, operation_id, intent_digest, content_digest, expires_at_ns, record_json
FROM control_acp_preparations`)
	if err != nil {
		return fmt.Errorf("read ACP preparations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ref, principalID, operationID, intentDigest, contentDigest string
		var expiresAtNS int64
		var raw []byte
		if err := rows.Scan(&ref, &principalID, &operationID, &intentDigest, &contentDigest, &expiresAtNS, &raw); err != nil {
			return fmt.Errorf("read ACP preparation: %w", err)
		}
		preparation, err := decodeACPPreparation(raw)
		if err != nil {
			return fmt.Errorf("decode ACP preparation %q: %w", ref, err)
		}
		if preparation.Ref != ref || preparation.PrincipalID != principalID ||
			preparation.OperationID != operationID || preparation.IntentDigest != intentDigest ||
			preparation.ContentDigest != contentDigest || preparation.ExpiresAt.UnixNano() != expiresAtNS {
			return fmt.Errorf("ACP preparation %q indexes do not match the stored record", ref)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read ACP preparations: %w", err)
	}
	return nil
}

func readStoreUpgradeRegularFile(path, name string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s for Store upgrade preflight: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("store upgrade preflight: %s is not a regular file", name)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s for Store upgrade preflight: %w", name, err)
	}
	return raw, nil
}

func rejectStoreSQLiteSidecars(path, name string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect %s SQLite sidecar for Store upgrade preflight: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("store upgrade preflight: %s SQLite sidecar %q is unsafe", name, filepath.Base(path+suffix))
		}
		return fmt.Errorf("store upgrade preflight: %s SQLite sidecar %q requires owner recovery", name, filepath.Base(path+suffix))
	}
	return nil
}

func readStoreUpgradeJournal(storeDir string) (storeUpgradeJournal, error) {
	raw, err := os.ReadFile(filepath.Join(storeDir, storeUpgradeJournalName))
	if err != nil {
		return storeUpgradeJournal{}, err
	}
	var journal storeUpgradeJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return storeUpgradeJournal{}, fmt.Errorf("gatewayapp: invalid Store upgrade journal: %w", err)
	}
	if journal.Format != storeUpgradeJournalFormat || filepath.Clean(journal.StoreDir) != filepath.Clean(storeDir) {
		return storeUpgradeJournal{}, errors.New("gatewayapp: Store upgrade journal does not match Store")
	}
	if journal.State != "preparing" && journal.State != "prepared" && journal.State != "committing" && journal.State != "committed" {
		return storeUpgradeJournal{}, fmt.Errorf("gatewayapp: Store upgrade journal has unknown state %q", journal.State)
	}
	if journal.TargetWriter != currentStoreUpgradeWriter() || strings.TrimSpace(journal.PreflightDigest) == "" {
		return storeUpgradeJournal{}, errors.New("gatewayapp: Store upgrade journal targets an unsupported writer")
	}
	return journal, nil
}

func writeStoreUpgradeJournal(storeDir string, journal storeUpgradeJournal) error {
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(storeDir, storeUpgradeJournalTempPattern)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(append(raw, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := atomicfile.Replace(tempPath, filepath.Join(storeDir, storeUpgradeJournalName)); err != nil {
		return err
	}
	return syncStoreRestoreDirectory(storeDir)
}

func clearStoreUpgradeJournal(storeDir string) error {
	err := os.Remove(filepath.Join(storeDir, storeUpgradeJournalName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncStoreRestoreDirectory(storeDir)
}

func rejectPendingStoreUpgrade(storeDir string) error {
	journal, err := readStoreUpgradeJournal(storeDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("gatewayapp: Store upgrade is %s for writer %s; owner-held recovery must finish before starting a Host", journal.State, journal.TargetWriter)
}

func upgradeReportFromJournal(journal storeUpgradeJournal, rollbackAvailable bool) StoreUpgradeReport {
	return StoreUpgradeReport{
		SourceGeneration: journal.SourceGeneration, StorageGeneration: journal.StorageGeneration,
		SchemaVersion: journal.SchemaVersion, RollbackAvailable: rollbackAvailable,
		State: journal.State, TargetWriter: journal.TargetWriter, PreflightDigest: journal.PreflightDigest,
	}
}

func currentStoreUpgradeBuildVersion() string {
	return version.String()
}
