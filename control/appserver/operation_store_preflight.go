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
	if err := validateSQLiteOperationStoreSchema(ctx, db); err != nil {
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

type sqliteColumnContract struct {
	name              string
	affinity          string
	notNull           bool
	primaryKey        int
	defaultExpression string
}

var (
	operationPolicyColumns = []sqliteColumnContract{
		{name: "id", affinity: "INTEGER", primaryKey: 1},
		{name: "version", affinity: "INTEGER", notNull: true},
		{name: "terminal_retention_ns", affinity: "INTEGER", notNull: true},
	}
	operationColumns = []sqliteColumnContract{
		{name: "principal_id", affinity: "TEXT", notNull: true, primaryKey: 1},
		{name: "operation_id", affinity: "TEXT", notNull: true, primaryKey: 2},
		{name: "action", affinity: "TEXT", notNull: true},
		{name: "session_id", affinity: "TEXT", notNull: true},
		{name: "outcome", affinity: "TEXT", notNull: true},
		{name: "created_at_ns", affinity: "INTEGER", notNull: true},
		{name: "updated_at_ns", affinity: "INTEGER", notNull: true},
		{name: "retain_until_ns", affinity: "INTEGER", notNull: true},
		{name: "record_json", affinity: "BLOB", notNull: true},
	}
)

type sqliteIndexContract struct {
	columns []string
	where   string
}

var operationIndexes = []sqliteIndexContract{
	{columns: []string{"updated_at_ns"}},
	{columns: []string{"action", "updated_at_ns"}},
	{columns: []string{"session_id", "updated_at_ns"}, where: "session_id <> ''"},
	{columns: []string{"retain_until_ns"}, where: "retain_until_ns > 0"},
}

func validateSQLiteOperationStoreSchema(ctx context.Context, db *sql.DB) error {
	if err := validateSQLiteTableColumns(ctx, db, "control_operation_policy", operationPolicyColumns); err != nil {
		return err
	}
	if err := validateSQLiteTableColumns(ctx, db, "control_operations", operationColumns); err != nil {
		return err
	}
	return validateSQLiteIndexes(ctx, db, "control_operations", operationIndexes)
}

func validateSQLiteTableColumns(ctx context.Context, db *sql.DB, table string, want []sqliteColumnContract) error {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return fmt.Errorf("controlclient: inspect %s schema: %w", table, err)
	}
	defer rows.Close()
	got := make([]sqliteColumnContract, 0, len(want))
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("controlclient: read %s schema: %w", table, err)
		}
		got = append(got, sqliteColumnContract{
			name: name, affinity: sqliteColumnAffinity(columnType), notNull: notNull != 0,
			primaryKey: primaryKey, defaultExpression: sqliteColumnDefaultExpression(defaultValue),
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("controlclient: read %s schema: %w", table, err)
	}
	if len(got) != len(want) {
		return fmt.Errorf("controlclient: unsupported %s schema columns %v", table, sqliteColumnNames(got))
	}
	for index := range want {
		expected, actual := want[index], got[index]
		if actual.name != expected.name {
			return fmt.Errorf("controlclient: unsupported %s schema columns %v", table, sqliteColumnNames(got))
		}
		if actual.affinity != expected.affinity {
			return fmt.Errorf("controlclient: unsupported %s schema: column %q has %s affinity, want %s", table, actual.name, actual.affinity, expected.affinity)
		}
		if expected.notNull && !actual.notNull {
			return fmt.Errorf("controlclient: unsupported %s schema: column %q must be NOT NULL", table, actual.name)
		}
		if actual.primaryKey != expected.primaryKey {
			return fmt.Errorf("controlclient: unsupported %s schema: column %q has primary key position %d, want %d", table, actual.name, actual.primaryKey, expected.primaryKey)
		}
		if actual.defaultExpression != expected.defaultExpression {
			return fmt.Errorf("controlclient: unsupported %s schema: column %q has an unsupported default expression", table, actual.name)
		}
	}
	return nil
}

func sqliteColumnDefaultExpression(value sql.NullString) string {
	if !value.Valid || strings.EqualFold(strings.TrimSpace(value.String), "NULL") {
		return ""
	}
	return strings.TrimSpace(value.String)
}

func sqliteColumnNames(columns []sqliteColumnContract) []string {
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, column.name)
	}
	return names
}

// sqliteColumnAffinity follows SQLite's declared-type affinity rules so an
// equivalent declaration such as BIGINT remains accepted while a TEXT/number
// substitution that changes owner behavior is rejected.
func sqliteColumnAffinity(declaredType string) string {
	declaredType = strings.ToUpper(strings.TrimSpace(declaredType))
	switch {
	case strings.Contains(declaredType, "INT"):
		return "INTEGER"
	case strings.Contains(declaredType, "CHAR"), strings.Contains(declaredType, "CLOB"), strings.Contains(declaredType, "TEXT"):
		return "TEXT"
	case strings.Contains(declaredType, "BLOB"), declaredType == "":
		return "BLOB"
	case strings.Contains(declaredType, "REAL"), strings.Contains(declaredType, "FLOA"), strings.Contains(declaredType, "DOUBLE"):
		return "REAL"
	default:
		return "NUMERIC"
	}
}

func validateSQLiteIndexes(ctx context.Context, db *sql.DB, table string, want []sqliteIndexContract) error {
	rows, err := db.QueryContext(ctx, `PRAGMA index_list('`+strings.ReplaceAll(table, `'`, `''`)+`')`)
	if err != nil {
		return fmt.Errorf("controlclient: inspect %s indexes: %w", table, err)
	}
	type sqliteIndexDescriptor struct {
		name    string
		unique  int
		origin  string
		partial int
	}
	indexes := make([]sqliteIndexDescriptor, 0)
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return fmt.Errorf("controlclient: read %s indexes: %w", table, err)
		}
		indexes = append(indexes, sqliteIndexDescriptor{name: name, unique: unique, origin: origin, partial: partial})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("controlclient: read %s indexes: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("controlclient: close %s indexes: %w", table, err)
	}

	matched := make([]bool, len(want))
	for _, descriptor := range indexes {
		if descriptor.origin == "pk" {
			continue
		}
		columns, err := readSQLiteIndexColumns(ctx, db, descriptor.name)
		if err != nil {
			return fmt.Errorf("controlclient: inspect %s index %q: %w", table, descriptor.name, err)
		}
		var indexSQL string
		if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, descriptor.name).Scan(&indexSQL); err != nil {
			return fmt.Errorf("controlclient: read %s index %q definition: %w", table, descriptor.name, err)
		}
		for wantIndex, expected := range want {
			if matched[wantIndex] || descriptor.unique != 0 || !sameSQLiteIndexColumns(columns, expected.columns) {
				continue
			}
			if expected.where == "" {
				if descriptor.partial != 0 {
					continue
				}
			} else if descriptor.partial == 0 || !sqliteIndexPredicateMatches(indexSQL, expected.where) {
				continue
			}
			matched[wantIndex] = true
			break
		}
	}
	for index, expected := range want {
		if !matched[index] {
			return fmt.Errorf("controlclient: unsupported %s schema: required index on %s is missing", table, strings.Join(expected.columns, ", "))
		}
	}
	return nil
}

func readSQLiteIndexColumns(ctx context.Context, db *sql.DB, indexName string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA index_info('`+strings.ReplaceAll(indexName, `'`, `''`)+`')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := []string{}
	for rows.Next() {
		var sequence, columnID int
		var name sql.NullString
		if err := rows.Scan(&sequence, &columnID, &name); err != nil {
			return nil, err
		}
		if !name.Valid || strings.TrimSpace(name.String) == "" {
			return nil, errors.New("index contains an expression column")
		}
		columns = append(columns, name.String)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func sameSQLiteIndexColumns(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if !strings.EqualFold(actual[index], expected[index]) {
			return false
		}
	}
	return true
}

func sqliteIndexPredicateMatches(indexSQL, expected string) bool {
	normalize := func(value string) string {
		value = strings.ToUpper(value)
		value = strings.NewReplacer(`"`, "", "`", "", "[", "", "]", "").Replace(value)
		value = strings.Map(func(r rune) rune {
			switch r {
			case ' ', '\t', '\r', '\n', '(', ')', ';':
				return -1
			default:
				return r
			}
		}, value)
		return strings.ReplaceAll(value, "!=", "<>")
	}
	normalizedSQL := normalize(indexSQL)
	where := strings.LastIndex(normalizedSQL, "WHERE")
	if where < 0 {
		return false
	}
	return strings.TrimSpace(normalizedSQL[where+len("WHERE"):]) == normalize(expected)
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
