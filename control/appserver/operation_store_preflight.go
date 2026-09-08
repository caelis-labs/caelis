package appserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sqliteOperationStoreSchemaVersion remains zero because the current ledger
// schema predates a user_version marker. A future writer must reserve and
// advance this marker together with a read-only validator update.
const sqliteOperationStoreSchemaVersion = 0

// ValidateSQLiteOperationStoreReadOnly validates the Control operation
// ledger without opening its owner or running schema initialization. It is
// intended for a stopped-Host backup/upgrade preflight and never writes,
// migrates, or repairs the database.
func ValidateSQLiteOperationStoreReadOnly(ctx context.Context, path string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("controlclient: operation store database path is required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("controlclient: resolve operation store database: %w", err)
	}
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("controlclient: inspect operation store database: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("controlclient: operation store database is not a secure regular file")
	}
	db, err := sql.Open("sqlite", filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("controlclient: open operation store database for read-only validation: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("controlclient: check operation store database: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(integrity), "ok") {
		return fmt.Errorf("controlclient: operation store integrity check = %q", integrity)
	}
	var userVersion int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&userVersion); err != nil {
		return fmt.Errorf("controlclient: read operation store schema version: %w", err)
	}
	if userVersion != sqliteOperationStoreSchemaVersion {
		return fmt.Errorf("controlclient: unsupported operation store schema version %d", userVersion)
	}
	if err := validateSQLiteTableColumns(ctx, db, "control_operation_policy", []string{
		"id", "version", "terminal_retention_ns",
	}); err != nil {
		return err
	}
	if err := validateSQLiteTableColumns(ctx, db, "control_operations", []string{
		"principal_id", "operation_id", "action", "session_id", "outcome",
		"created_at_ns", "updated_at_ns", "retain_until_ns", "record_json",
	}); err != nil {
		return err
	}
	if err := validateSQLiteOperationPolicyRows(ctx, db); err != nil {
		return err
	}
	if err := validateSQLiteOperationRows(ctx, db); err != nil {
		return err
	}
	return nil
}

func validateSQLiteTableColumns(ctx context.Context, db *sql.DB, table string, want []string) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return fmt.Errorf("controlclient: inspect %s schema: %w", table, err)
	}
	defer rows.Close()
	got := make([]string, 0, len(want))
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("controlclient: read %s schema: %w", table, err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("controlclient: read %s schema: %w", table, err)
	}
	if len(got) != len(want) {
		return fmt.Errorf("controlclient: unsupported %s schema columns %v", table, got)
	}
	for index := range want {
		if got[index] != want[index] {
			return fmt.Errorf("controlclient: unsupported %s schema columns %v", table, got)
		}
	}
	return nil
}

func validateSQLiteOperationPolicyRows(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT id, version, terminal_retention_ns FROM control_operation_policy`)
	if err != nil {
		return fmt.Errorf("controlclient: read operation policy: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, version int
		var retention int64
		if err := rows.Scan(&id, &version, &retention); err != nil {
			return fmt.Errorf("controlclient: read operation policy: %w", err)
		}
		count++
		if id != operationPolicyRowID || version != operationRetentionPolicyVersion || retention <= 0 {
			return fmt.Errorf("controlclient: invalid operation policy row")
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("controlclient: read operation policy: %w", err)
	}
	if count > 1 {
		return errors.New("controlclient: operation policy contains duplicate singleton rows")
	}
	return nil
}

func validateSQLiteOperationRows(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
SELECT principal_id, operation_id, action, session_id, outcome,
       created_at_ns, updated_at_ns, retain_until_ns, record_json
FROM control_operations`)
	if err != nil {
		return fmt.Errorf("controlclient: read operation records: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var principalID, operationID, action, sessionID, outcome string
		var createdAtNS, updatedAtNS, retainUntilNS int64
		var raw []byte
		if err := rows.Scan(&principalID, &operationID, &action, &sessionID, &outcome,
			&createdAtNS, &updatedAtNS, &retainUntilNS, &raw); err != nil {
			return fmt.Errorf("controlclient: read operation record: %w", err)
		}
		var record OperationRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return fmt.Errorf("controlclient: decode operation record during preflight: %w", err)
		}
		if err := validateOperationRecord(record); err != nil {
			return fmt.Errorf("controlclient: validate operation record during preflight: %w", err)
		}
		if strings.TrimSpace(record.Intent.PrincipalID) != strings.TrimSpace(principalID) ||
			strings.TrimSpace(record.Intent.OperationID) != strings.TrimSpace(operationID) ||
			string(record.Intent.Action) != action || strings.TrimSpace(record.Intent.SessionID) != strings.TrimSpace(sessionID) ||
			record.Intent.CreatedAt.UnixNano() != createdAtNS || record.UpdatedAt.UnixNano() != updatedAtNS ||
			operationRecordOutcome(record) != outcome || operationRecordRetentionNS(record) != retainUntilNS {
			return errors.New("controlclient: operation row does not match its durable record")
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("controlclient: read operation records: %w", err)
	}
	return nil
}

func operationRecordOutcome(record OperationRecord) string {
	if record.Result == nil {
		return ""
	}
	return string(record.Result.Outcome)
}

func operationRecordRetentionNS(record OperationRecord) int64 {
	if record.RetainUntil.IsZero() {
		return 0
	}
	return record.RetainUntil.UnixNano()
}
