package applicationv3

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// A retired action is deliberately not a current dispatch/recovery action.
// Migration observes old receipts; it cannot reconstruct missing external facts.
const retiredAction = "retired.application"

func (m *importer) operation(r row) error {
	if r.text("operation") == "" || digest(r.bytes("request")) != r.text("digest") {
		return errors.New("operation identity or request digest mismatch")
	}
	request, err := object(r.bytes("request"))
	if err != nil {
		return err
	}
	session := stringField(request, "session_id")
	commandRequest := r.bytes("request")
	kind := stringField(request, "kind")
	nestedRequest := request["request"]
	if nestedRequest == nil {
		nestedRequest = request["Request"]
	}
	if session == "" && nestedRequest != nil {
		nested, e := object(nestedRequest)
		if e != nil {
			return e
		}
		session = stringField(nested, "session_id")
		commandRequest = nestedRequest
	}
	if session == "" && request["intent"] != nil {
		nested, e := object(request["intent"])
		if e != nil {
			return e
		}
		session = stringField(nested, "session_id")
	}
	result := r.bytes("result")
	id := r.operationID()
	if kind == "control_command" {
		nested, e := object(request["intent"])
		if e != nil {
			return e
		}
		if stringField(nested, "operation_id") != r.text("operation") || stringField(nested, "principal_id") != r.scopeKey() {
			return errors.New("scoped command intent identity mismatch")
		}
		m.commandDigests[id] = stringField(nested, "digest")
	} else {
		wrapped, e := json.Marshal(struct {
			ApplicationID string
			ConnectionID  string
			Request       json.RawMessage
		}{r.text("application"), r.text("connection"), commandRequest})
		if e != nil {
			return e
		}
		m.commandDigests[id] = digest(wrapped)
	}
	if session == "" && kind == "worker.create" {
		session = stableID("worker-", r.text("principal"), r.text("application"), r.text("connection"), r.text("operation"))
	}
	if session == "" && request["profile"] != nil {
		session = "application-" + digest([]byte(r.text("principal")+"\x00"+r.text("application")+"\x00"+r.text("connection")+"\x00"+r.text("operation")))
	}
	if len(result) > 0 {
		v, e := object(result)
		if e != nil {
			return e
		}
		if kind == "configuration_update" {
			if stringField(v, "session_id") != session || v["revision"] == nil {
				return errors.New("configuration receipt identity mismatch")
			}
			v = map[string]json.RawMessage{"configuration": result}
			set(v, "outcome", "committed")
			set(v, "session_id", session)
		} else if stringField(v, "operation_id") != r.text("operation") {
			return errors.New("receipt operation identity mismatch")
		}
		if session == "" {
			session = stringField(v, "session_id")
		}
		if stringField(v, "session_id") != "" && stringField(v, "session_id") != session {
			return errors.New("receipt Session identity mismatch")
		}
		set(v, "operation_id", id)
		if kind == "background_grant_create" {
			// The old receipt did not carry grant state. The immutable creation fields
			// prove the original unrevoked grant, independently of today's revocation.
			var grantID, source, authorization string
			err = m.tx.QueryRowContext(m.ctx, `SELECT id,source,authorization_operation FROM session_background_grants WHERE principal=? AND operation=? AND session=?`, r.text("principal"), id, session).Scan(&grantID, &source, &authorization)
			if err != nil {
				return err
			}
			set(v, "background_grant", map[string]any{"id": grantID, "principal_id": r.text("principal"), "session_id": session, "source": source, "authorization_operation_id": authorization, "revoked": false})
		}
		result, err = json.Marshal(v)
		if err != nil {
			return err
		}
	}
	return m.receipt(r.text("principal"), id, session, r.text("digest"), result)
}

func validOutcome(s string) bool {
	switch s {
	case "accepted", "committed", "rejected", "conflicted", "unknown":
		return true
	}
	return false
}

func (m *importer) receipt(principal, id, session, intentDigest string, result []byte) error {
	if principal == "" || id == "" || intentDigest == "" {
		return errors.New("invalid migrated operation identity")
	}
	outcome := ""
	if len(result) > 0 {
		v, err := object(result)
		if err != nil {
			return err
		}
		outcome = stringField(v, "outcome")
		if stringField(v, "operation_id") != id || !validOutcome(outcome) {
			return errors.New("invalid migrated receipt")
		}
	}
	// The timestamp records import, not an invented original execution time.
	// Original timestamps (where present) and request bytes are kept in the archive.
	record := map[string]any{"version": 2, "retain_forever": true, "intent": map[string]any{
		"principal_id": principal, "operation_id": id, "action": retiredAction, "session_id": session, "digest": intentDigest, "created_at": m.now,
	}, "updated_at": m.now}
	if len(result) > 0 {
		record["result"] = json.RawMessage(result)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	var previous []byte
	err = m.tx.QueryRowContext(m.ctx, `SELECT record_json FROM control_operations WHERE principal_id=? AND operation_id=?`, principal, id).Scan(&previous)
	if err == nil {
		// A historical scoped command could also have an ordinary ledger copy.
		// Merge only exact receipts or unknown+known; ambiguity aborts the transaction.
		old, e := object(previous)
		if e != nil {
			return e
		}
		intent, e := object(old["intent"])
		if e != nil {
			return e
		}
		if stringField(intent, "action") != retiredAction || stringField(intent, "session_id") != session {
			return errors.New("migrated operation collision")
		}
		oldResult := old["result"]
		if len(oldResult) > 0 && len(result) > 0 {
			a, e := object(oldResult)
			if e != nil {
				return e
			}
			b, e := object(result)
			if e != nil {
				return e
			}
			canonicalA, _ := json.Marshal(a)
			canonicalB, _ := json.Marshal(b)
			if !bytes.Equal(canonicalA, canonicalB) {
				return errors.New("conflicting historical receipts")
			}
		}
		if len(oldResult) > 0 || len(result) == 0 {
			return nil
		}
		// Keep the first permanent intent and attach the only proven receipt.
		old["result"] = result
		raw, err = json.Marshal(old)
		if err != nil {
			return err
		}
		_, err = m.tx.ExecContext(m.ctx, `UPDATE control_operations SET outcome=?,record_json=? WHERE principal_id=? AND operation_id=?`, outcome, raw, principal, id)
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = m.tx.ExecContext(m.ctx, `INSERT INTO control_operations VALUES(?,?,?,?,?,?,?,?,?)`, principal, id, retiredAction, session, outcome, m.now.UnixNano(), m.now.UnixNano(), 0, raw)
	return err
}

func (m *importer) importScopedControlOperations() error {
	// Collect first: inserting/deleting rows while traversing the same table is
	// not a stable SQLite cursor contract. No runtime receipt is ever reexecuted.
	var candidates []row
	if err := eachRow(m.ctx, m.tx, "control_operations", func(r row) error {
		if strings.HasPrefix(r.text("principal_id"), "application-scope:") {
			candidates = append(candidates, r)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, r := range candidates {
		scope, ok := m.scopes[r.text("principal_id")]
		if !ok {
			return errors.New("scoped ledger has no provable original principal")
		}
		if err := m.archive("control_operations", r); err != nil {
			return err
		}
		v, err := object(r.bytes("record_json"))
		if err != nil {
			return err
		}
		intent, err := object(v["intent"])
		if err != nil {
			return err
		}
		if stringField(intent, "principal_id") != r.text("principal_id") || stringField(intent, "operation_id") != r.text("operation_id") || stringField(intent, "action") != r.text("action") || stringField(intent, "session_id") != r.text("session_id") {
			return errors.New("scoped operation index mismatch")
		}
		id := OperationID(scope.text("principal"), scope.text("application"), scope.text("connection"), r.text("operation_id"))
		if expected, exists := m.commandDigests[id]; exists && expected != stringField(intent, "digest") {
			return errors.New("historical command intent mismatch")
		}
		result := v["result"]
		if len(result) > 0 && string(result) != "null" {
			out, e := object(result)
			if e != nil {
				return e
			}
			if stringField(out, "operation_id") != r.text("operation_id") || stringField(out, "outcome") != r.text("outcome") {
				return errors.New("scoped receipt index mismatch")
			}
			set(out, "operation_id", id)
			result, err = json.Marshal(out)
			if err != nil {
				return err
			}
		} else {
			result = nil
			if r.text("outcome") != "" {
				return errors.New("scoped outcome without receipt")
			}
		}
		session := r.text("session_id")
		if session == "" && len(result) > 0 {
			v, e := object(result)
			if e != nil {
				return e
			}
			session = stringField(v, "session_id")
		}
		if session == "" && r.text("action") == "worker.create" {
			session = stableID("worker-", scope.text("principal"), scope.text("application"), scope.text("connection"), r.text("operation_id"))
		}
		if err = m.receipt(scope.text("principal"), id, session, stringField(intent, "digest"), result); err != nil {
			return fmt.Errorf("merge historical command: %w", err)
		}
		if _, err = m.tx.ExecContext(m.ctx, `DELETE FROM control_operations WHERE principal_id=? AND operation_id=?`, r.text("principal_id"), r.text("operation_id")); err != nil {
			return err
		}
	}
	return nil
}
