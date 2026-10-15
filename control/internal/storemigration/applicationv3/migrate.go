// Package applicationv3 is the one-time importer of Application Store v1/v2.
// It is only called before opening the current Store, under exclusive Host
// ownership. Current dispatch and read paths must not import this package.
package applicationv3

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/control/internal/operationreceipt"
	"github.com/caelis-labs/caelis/control/internal/sessionstate"
)

// OperationID maps the old connection namespace into the common principal
// namespace. This spelling is also the Bot state migration contract. It is an
// observation address only; an imported intent never grants a fresh dispatch.
func OperationID(principal, sessionprofile, connection, operation string) string {
	return stableID("retired-app-op-", principal, sessionprofile, connection, operation)
}

// ResourceOperationID separates uploads, which had their own old namespace.
func ResourceOperationID(principal, sessionprofile, connection, session, operation string) string {
	return stableID("retired-app-resource-op-", principal, sessionprofile, connection, session, operation)
}

func stableID(prefix string, parts ...string) string {
	b, _ := json.Marshal(parts)
	return prefix + digest(b)
}
func digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

type row map[string]any

func (r row) text(k string) string {
	switch v := r[k].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	return ""
}
func (r row) bytes(k string) []byte {
	switch v := r[k].(type) {
	case []byte:
		return v
	case string:
		return []byte(v)
	}
	return nil
}
func (r row) number(k string) int64 { v, _ := r[k].(int64); return v }
func (r row) operationID() string {
	return OperationID(r.text("principal"), r.text("application"), r.text("connection"), r.text("operation"))
}
func (r row) scopeKey() string {
	return stableID("application-scope:", r.text("principal"), r.text("application"), r.text("connection"))
}

// Run atomically imports the old format, removes its live tables, and raises
// the old app_schema marker so a released old Host refuses to open this Store.
// It preserves source rows in an inert audit archive, never consulted at runtime.
// Unknown versions and any ambiguous/corrupt row fail before committing changes.
func Run(ctx context.Context, db *sql.DB, now time.Time) error {
	if now.IsZero() {
		return errors.New("application migration requires an explicit timestamp")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS app_schema(singleton INTEGER PRIMARY KEY CHECK(singleton=1),version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var version int
	err = tx.QueryRowContext(ctx, `SELECT version FROM app_schema WHERE singleton=1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name GLOB 'app_*' AND name!='app_schema'`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("unversioned Application tables")
		}
		if err = sessionstate.EnsureSchema(ctx, tx); err != nil {
			return err
		}
		if err = operationreceipt.EnsureSchema(ctx, tx); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO app_schema VALUES(1,?)`, sessionstate.Version); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if version == sessionstate.Version {
		return tx.Commit()
	}
	if version != 1 && version != 2 {
		return fmt.Errorf("unsupported Session Store schema %d", version)
	}
	if err = sessionstate.EnsureSchema(ctx, tx); err != nil {
		return err
	}
	if err = operationreceipt.EnsureSchema(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE retired_application_archive (
  version INTEGER NOT NULL, source_table TEXT NOT NULL, source_key TEXT NOT NULL,
  record BLOB NOT NULL, PRIMARY KEY(version,source_table,source_key)
 )`); err != nil {
		return err
	}
	m := importer{ctx: ctx, tx: tx, now: now.UTC(), version: version, scopes: map[string]row{}, commandDigests: map[string]string{}}
	tables := []string{"app_connections", "app_workers", "app_bindings", "app_configurations", "app_calls", "app_resources", "app_background_grants", "app_operations"}
	for _, table := range tables {
		exists, err := tableExists(ctx, tx, table)
		if err != nil {
			return err
		}
		if !exists {
			if table == "app_configurations" && version == 1 || table == "app_background_grants" || table == "app_workers" {
				continue
			}
			return fmt.Errorf("incomplete Application schema: missing %s", table)
		}
		if err := eachRow(ctx, tx, table, func(r row) error { return m.importRow(table, r) }); err != nil {
			return fmt.Errorf("migrate %s: %w", table, err)
		}
	}
	if err := m.importScopedControlOperations(); err != nil {
		return err
	}
	// Drop dependent tables before profiles. No old runtime owner remains.
	for _, table := range []string{"app_operations", "app_background_grants", "app_resources", "app_calls", "app_configurations", "app_bindings", "app_workers", "app_connections"} {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+table); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE app_schema SET version=? WHERE singleton=1`, sessionstate.Version); err != nil {
		return err
	}
	return tx.Commit()
}

func tableExists(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n)
	return n == 1, err
}

func eachRow(ctx context.Context, tx *sql.Tx, table string, visit func(row) error) error {
	// table is always a hardcoded identifier owned by this importer.
	rows, err := tx.QueryContext(ctx, `SELECT rowid AS migration_rowid,* FROM `+table)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		r := row{}
		for i, k := range columns {
			r[k] = values[i]
		}
		if err := visit(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

type importer struct {
	ctx            context.Context
	tx             *sql.Tx
	now            time.Time
	version        int
	scopes         map[string]row
	commandDigests map[string]string
}

func (m *importer) archive(table string, r row) error {
	// Resource data is already copied byte-for-byte into the sole resource owner.
	// Its immutable original descriptor/digest remains in the archive.
	copy := row{}
	for k, v := range r {
		if k != "data" {
			copy[k] = v
		}
	}
	b, err := json.Marshal(copy)
	if err != nil {
		return err
	}
	_, err = m.tx.ExecContext(m.ctx, `INSERT INTO retired_application_archive VALUES(?,?,?,?)`, m.version, table, fmt.Sprint(r["migration_rowid"]), b)
	return err
}
func object(raw []byte) (map[string]json.RawMessage, error) {
	var v map[string]json.RawMessage
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	if v == nil {
		return nil, errors.New("expected JSON object")
	}
	return v, nil
}
func stringField(v map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(v[k], &s)
	return s
}
func set(v map[string]json.RawMessage, k string, value any) { v[k], _ = json.Marshal(value) }
func normalizedScope(raw []byte, r row) (map[string]json.RawMessage, error) {
	v, err := object(raw)
	if err != nil {
		return nil, err
	}
	if stringField(v, "principal_id") != r.text("principal") || stringField(v, "application_id") != r.text("application") || stringField(v, "connection_id") != r.text("connection") {
		return nil, errors.New("scope does not match indexed owner")
	}
	delete(v, "application_id")
	delete(v, "connection_id")
	return v, nil
}
func (m *importer) importRow(table string, r row) error {
	if err := m.archive(table, r); err != nil {
		return err
	}
	if r.text("principal") != "" && r.text("connection") != "" {
		m.scopes[r.scopeKey()] = r
	}
	switch table {
	case "app_connections", "app_workers":
		return nil // Native Session owner survives; credentials and extra worker grants retire.
	case "app_bindings":
		return m.profile(r)
	case "app_configurations":
		return m.configuration(r)
	case "app_calls":
		return m.call(r)
	case "app_resources":
		return m.resource(r)
	case "app_background_grants":
		return m.grant(r)
	case "app_operations":
		return m.operation(r)
	default:
		return errors.New("unexpected migration table")
	}
}
func (m *importer) profile(r row) error {
	v, err := normalizedScope(r.bytes("body"), r)
	if err != nil {
		return err
	}
	if stringField(v, "session_id") != r.text("session") || r.text("principal") == "" {
		return errors.New("profile Session identity mismatch")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err = m.tx.ExecContext(m.ctx, `INSERT INTO session_profiles VALUES(?,?,?)`, r.text("session"), r.text("principal"), b); err != nil {
		return err
	}
	if m.version != 1 {
		return nil
	}
	p, err := object(v["profile"])
	if err != nil {
		return err
	}
	if stringField(p, "execution") == "workspace-write" {
		if _, present := p["native_tools"]; !present {
			set(p, "native_tools", []string{"RunCommand", "Task"})
		}
	}
	seed, _ := json.Marshal(map[string]any{"session_id": r.text("session"), "revision": 1, "profile": p})
	_, err = m.tx.ExecContext(m.ctx, `INSERT INTO session_configurations VALUES(?,1,?)`, r.text("session"), seed)
	return err
}
func (m *importer) configuration(r row) error {
	// Version 1 has no revision authority; it is seeded solely from the immutable binding.
	if m.version == 1 {
		return nil
	}
	v, err := object(r.bytes("body"))
	if err != nil {
		return err
	}
	var revision int64
	if err = json.Unmarshal(v["revision"], &revision); err != nil {
		return err
	}
	if revision <= 0 || revision != r.number("revision") || stringField(v, "session_id") != r.text("session") {
		return errors.New("configuration revision identity mismatch")
	}
	if err = m.ownedSession(r); err != nil {
		return err
	}
	_, err = m.tx.ExecContext(m.ctx, `INSERT INTO session_configurations VALUES(?,?,?)`, r.text("session"), revision, r.bytes("body"))
	return err
}
func (m *importer) ownedSession(r row) error {
	var owner string
	if err := m.tx.QueryRowContext(m.ctx, `SELECT principal FROM session_profiles WHERE session=?`, r.text("session")).Scan(&owner); err != nil {
		return err
	}
	if r.text("principal") != "" && owner != r.text("principal") {
		return errors.New("session owner mismatch")
	}
	return nil
}
func (m *importer) call(r row) error {
	if err := m.ownedSession(r); err != nil {
		return err
	}
	if digest(r.bytes("body")) != r.text("digest") {
		return errors.New("callback digest mismatch")
	}
	v, err := normalizedScope(r.bytes("body"), r)
	if err != nil {
		return err
	}
	if stringField(v, "session_id") != r.text("session") || stringField(v, "id") != r.text("call") || stringField(v, "turn_id") == "" || stringField(v, "item_id") == "" {
		return errors.New("callback native identity mismatch")
	}
	if _, present := v["configuration_revision"]; !present || string(v["configuration_revision"]) == "0" {
		set(v, "configuration_revision", 1)
	}
	state := r.text("state")
	switch state {
	case "pending":
		state = "cancelled"
	case "claimed":
		state = "unknown"
	case "completed":
		result, err := object(r.bytes("result"))
		if err != nil || result["content"] == nil {
			return errors.New("completed callback lacks a structured result")
		}
		switch stringField(result, "outcome") {
		case "succeeded", "failed", "unknown":
		default:
			return errors.New("invalid callback outcome")
		}
	case "cancelled", "unknown":
	default:
		return errors.New("unknown callback state")
	}
	// The immutable body records original admission; current terminal state/result
	// remain separate columns, exactly as in the native callback owner.
	b, _ := json.Marshal(v)
	_, err = m.tx.ExecContext(m.ctx, `INSERT INTO client_tool_calls VALUES(?,?,?,?,?,?,?,?,?)`, r.text("principal"), r.text("session"), r.text("call"), stringField(v, "turn_id"), stringField(v, "item_id"), digest(b), b, state, r["result"])
	return err
}
func (m *importer) resource(r row) error {
	if err := m.ownedSession(r); err != nil {
		return err
	}
	v, err := object(r.bytes("body"))
	if err != nil {
		return err
	}
	var size int64
	if err = json.Unmarshal(v["size"], &size); err != nil {
		return err
	}
	if size < 0 || size > 8<<20 {
		return errors.New("resource byte limit")
	}
	if expiry, ok := v["expires_at"]; ok {
		var deadline time.Time
		if err = json.Unmarshal(expiry, &deadline); err != nil || deadline.IsZero() {
			return errors.New("invalid resource expiry")
		}
	}
	if stringField(v, "id") != r.text("id") || stringField(v, "session_id") != r.text("session") || digest(r.bytes("body")) != r.text("digest") || size != int64(len(r.bytes("data"))) || stringField(v, "sha256") != digest(r.bytes("data")) {
		return errors.New("resource identity or bytes mismatch")
	}
	if _, err = m.tx.ExecContext(m.ctx, `INSERT INTO session_resources VALUES(?,?,?,?,?,?,?)`, r.text("id"), r.text("principal"), r.text("session"), r.text("operation"), r.text("digest"), r.bytes("body"), r.bytes("data")); err != nil {
		return err
	}
	id := ResourceOperationID(r.text("principal"), r.text("application"), r.text("connection"), r.text("session"), r.text("operation"))
	result, _ := json.Marshal(map[string]any{"operation_id": id, "session_id": r.text("session"), "outcome": "committed", "session_resource": json.RawMessage(r.bytes("body"))})
	return m.receipt(r.text("principal"), id, r.text("session"), r.text("digest"), result)
}
func (m *importer) grant(r row) error {
	if err := m.ownedSession(r); err != nil {
		return err
	}
	if r.text("id") == "" || r.text("operation") == "" || r.text("source") == "" || r.text("authorization_operation") == "" || r.number("revoked") < 0 || r.number("revoked") > 1 {
		return errors.New("invalid background grant")
	}
	_, err := m.tx.ExecContext(m.ctx, `INSERT INTO session_background_grants VALUES(?,?,?,?,?,?,?)`, r.text("id"), r.text("principal"), r.text("session"), r.operationID(), r.text("source"), r.text("authorization_operation"), r.number("revoked"))
	return err
}
