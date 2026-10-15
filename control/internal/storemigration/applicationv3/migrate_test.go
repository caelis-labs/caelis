package applicationv3_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/internal/storemigration/applicationv3"
	_ "modernc.org/sqlite"
)

var importedAt = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func raw(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, e := db.Exec(q, args...); e != nil {
		t.Fatal(e)
	}
}

// Frozen released schema; never construct fixtures through the current Store.
// The importer must remain removable without bringing back a live legacy owner.
const legacySchema = `
CREATE TABLE app_schema(singleton INTEGER PRIMARY KEY,version INTEGER NOT NULL);
INSERT INTO app_schema VALUES(1,2);
CREATE TABLE app_connections(principal TEXT,application TEXT,connection TEXT PRIMARY KEY,operation TEXT,digest TEXT,credential_hash TEXT,name TEXT,expires INTEGER,revoked INTEGER);
CREATE TABLE app_workers(session TEXT PRIMARY KEY,principal TEXT,application TEXT,connection TEXT);
CREATE TABLE app_bindings(session TEXT PRIMARY KEY,principal TEXT,application TEXT,connection TEXT,body BLOB);
CREATE TABLE app_configurations(session TEXT,revision INTEGER,body BLOB,PRIMARY KEY(session,revision));
CREATE TABLE app_calls(principal TEXT,application TEXT,connection TEXT,session TEXT,call TEXT,digest TEXT,body BLOB,state TEXT,result BLOB,PRIMARY KEY(connection,session,call));
CREATE TABLE app_resources(id TEXT PRIMARY KEY,principal TEXT,application TEXT,connection TEXT,session TEXT,operation TEXT,digest TEXT,body BLOB,data BLOB,UNIQUE(connection,session,operation));
CREATE TABLE app_background_grants(id TEXT PRIMARY KEY,principal TEXT,application TEXT,connection TEXT,session TEXT,operation TEXT,source TEXT,authorization_operation TEXT,revoked INTEGER);
CREATE TABLE app_operations(principal TEXT,application TEXT,connection TEXT,operation TEXT,digest TEXT,request BLOB,result BLOB,PRIMARY KEY(connection,operation));
`

func legacy(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	exec(t, db, legacySchema)
	for _, c := range []string{"c1", "c2"} {
		exec(t, db, `INSERT INTO app_connections VALUES('owner','app',?,'register','registration','credential-'||?,'Bot',1,1)`, c, c)
	}
	p := map[string]any{"version": "v1", "model": "model", "tools_version": "tools-v1", "instructions": "private instructions", "execution": "workspace-write", "inherit": map[string]bool{}}
	b := map[string]any{"principal_id": "owner", "application_id": "app", "connection_id": "c1", "session_id": "session", "creation_digest": "creation", "archived": false, "profile": p}
	exec(t, db, `INSERT INTO app_bindings VALUES('session','owner','app','c1',?)`, raw(t, b))
	cfg := map[string]any{"session_id": "session", "revision": 1, "profile": p}
	exec(t, db, `INSERT INTO app_configurations VALUES('session',1,?)`, raw(t, cfg))
	for i, state := range []string{"pending", "claimed", "completed", "unknown"} {
		id := "call-" + state
		call := map[string]any{"id": id, "principal_id": "owner", "application_id": "app", "connection_id": "c1", "session_id": "session", "turn_id": "turn", "item_id": id, "call_id": "provider-reused", "tools_version": "tools-v1", "configuration_revision": 1, "source": map[string]string{"kind": "user", "operation_id": "prompt"}, "name": "tool", "arguments": map[string]any{"number": i}, "state": "pending"}
		body := raw(t, call)
		var result any
		if state == "completed" {
			result = []byte(`{"outcome":"succeeded","content":{"value":9007199254740993}}`)
		}
		exec(t, db, `INSERT INTO app_calls VALUES('owner','app','c1','session',?,?,?, ?,?)`, id, hash(body), body, state, result)
	}
	data := []byte{0, 255, 42}
	resource := map[string]any{"id": "resource-original", "session_id": "session", "name": "artifact", "media_type": "application/octet-stream", "size": len(data), "sha256": hash(data), "expires_at": importedAt.Add(-time.Hour)}
	body := raw(t, resource)
	exec(t, db, `INSERT INTO app_resources VALUES('resource-original','owner','app','c1','session','same-id',?,?,?)`, hash(body), body, data)
	exec(t, db, `INSERT INTO app_background_grants VALUES('grant-original','owner','app','c1','session','grant-op','daily','user-approval',1)`)
	for _, c := range []string{"c1", "c2"} {
		request := raw(t, map[string]any{"operation_id": "same-id", "session_id": "session", "input": c, "source_kind": "user"})
		result := raw(t, map[string]any{"operation_id": "same-id", "session_id": "session", "outcome": "accepted", "target": map[string]string{"turn_id": "turn-" + c}})
		exec(t, db, `INSERT INTO app_operations VALUES('owner','app',?,'same-id',?,?,?)`, c, hash(request), request, result)
	}
	request := []byte(`{"operation_id":"unknown","session_id":"session","input":"effect may have happened"}`)
	exec(t, db, `INSERT INTO app_operations VALUES('owner','app','c1','unknown',?,?,NULL)`, hash(request), request)
	request = []byte(`{"kind":"background_grant_create","session_id":"session","request":{"operation_id":"grant-op","source":"daily","authorization_operation_id":"user-approval"}}`)
	exec(t, db, `INSERT INTO app_operations VALUES('owner','app','c1','grant-op',?,?,?)`, hash(request), request, []byte(`{"operation_id":"grant-op","session_id":"session","outcome":"committed"}`))
	request = []byte(`{"kind":"configuration_update","session_id":"session","request":{"operation_id":"config","expected_configuration_revision":1,"patch":{}}}`)
	exec(t, db, `INSERT INTO app_operations VALUES('owner','app','c1','config',?,?,?)`, hash(request), request, raw(t, cfg))
	exec(t, db, `INSERT INTO app_workers VALUES('worker-original','owner','app','c1')`)
	return db, path
}

func TestMigrationPreservesIdentityReceiptsAndUnknownEffects(t *testing.T) {
	db, path := legacy(t)
	if err := applicationv3.Run(t.Context(), db, importedAt); err != nil {
		t.Fatal(err)
	}
	// All legacy operational tables are gone; only the refusal marker survives.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name GLOB 'app_*' AND name!='app_schema'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("legacy owners: %d %v", n, err)
	}
	var body []byte
	if err := db.QueryRow(`SELECT body FROM session_profiles WHERE session='session' AND principal='owner'`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "connection_id") || strings.Contains(string(body), "application_id") {
		t.Fatalf("profile retains obsolete authority: %s", body)
	}
	var state string
	for before, after := range map[string]string{"pending": "cancelled", "claimed": "unknown", "completed": "completed", "unknown": "unknown"} {
		if err := db.QueryRow(`SELECT state FROM client_tool_calls WHERE call=?`, "call-"+before).Scan(&state); err != nil || state != after {
			t.Fatalf("callback %s: %s %v", before, state, err)
		}
	}
	if err := db.QueryRow(`SELECT result FROM client_tool_calls WHERE call='call-completed'`).Scan(&body); err != nil || !strings.Contains(string(body), "9007199254740993") {
		t.Fatalf("exact callback result lost: %s %v", body, err)
	}
	if err := db.QueryRow(`SELECT data FROM session_resources WHERE id='resource-original'`).Scan(&body); err != nil || string(body) != string([]byte{0, 255, 42}) {
		t.Fatalf("bytes changed: %v %v", body, err)
	}
	if err := db.QueryRow(`SELECT revoked FROM session_background_grants WHERE id='grant-original'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("revocation lost: %d %v", n, err)
	}
	// Open the real common ledger, then exercise retained observation/retry.
	store, err := appserver.NewSQLiteOperationStoreWithConfig(path, appserver.OperationRetentionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, c := range []string{"c1", "c2"} {
		id := applicationv3.OperationID("owner", "app", c, "same-id")
		r, found, err := store.Read(t.Context(), "owner", id)
		if err != nil || !found || !r.RetainForever || r.Result == nil || string(r.Result.Outcome) != "accepted" {
			t.Fatalf("receipt %s: %+v %v %v", c, r, found, err)
		}
		if _, fresh, err := store.Begin(t.Context(), r.Intent); err != nil || fresh {
			t.Fatalf("migration granted dispatch: %v %v", fresh, err)
		}
	}
	unknown, found, err := store.Read(t.Context(), "owner", applicationv3.OperationID("owner", "app", "c1", "unknown"))
	if err != nil || !found || unknown.Result != nil || string(unknown.Intent.Action) != "retired.application" {
		t.Fatalf("unknown result invented: %+v %v %v", unknown, found, err)
	}
	grant, found, err := store.Read(t.Context(), "owner", applicationv3.OperationID("owner", "app", "c1", "grant-op"))
	if err != nil || !found || grant.Result == nil || grant.Result.BackgroundGrant == nil || grant.Result.BackgroundGrant.Revoked || grant.Result.BackgroundGrant.ID != "grant-original" {
		t.Fatalf("creation receipt changed by later revoke: %+v %v", grant, err)
	}
	cfg, found, err := store.Read(t.Context(), "owner", applicationv3.OperationID("owner", "app", "c1", "config"))
	if err != nil || !found || cfg.Result == nil || cfg.Result.Configuration == nil || cfg.Result.Configuration.Revision != 1 {
		t.Fatalf("config receipt lost: %+v %v", cfg, err)
	}
	resource, found, err := store.Read(t.Context(), "owner", applicationv3.ResourceOperationID("owner", "app", "c1", "session", "same-id"))
	if err != nil || !found || resource.Result == nil || resource.Result.SessionResource == nil || resource.Result.SessionResource.ID != "resource-original" {
		t.Fatalf("upload receipt lost: %+v %v", resource, err)
	}
	if err := applicationv3.Run(t.Context(), db, importedAt.Add(time.Hour)); err != nil {
		t.Fatalf("second open: %v", err)
	}
	var archiveCount int
	if err := db.QueryRow(`SELECT count(*) FROM retired_application_archive`).Scan(&archiveCount); err != nil || archiveCount == 0 {
		t.Fatalf("original evidence missing: %d %v", archiveCount, err)
	}
}

func TestMigrationRollbackOnCorruptionAndFutureVersion(t *testing.T) {
	for name, corrupt := range map[string]string{
		"resource bytes":            `UPDATE app_resources SET data=x'00'`,
		"callback null result":      `UPDATE app_calls SET result='null' WHERE state='completed'`,
		"callback address mismatch": `INSERT INTO app_calls SELECT principal,application,connection,session,'alias',digest,body,state,result FROM app_calls WHERE call='call-claimed'`,
		"callback provenance":       `UPDATE app_calls SET digest='corrupt' WHERE state='claimed'`,
		"unknown future":            `UPDATE app_schema SET version=99`,
		"missing schema":            `DROP TABLE app_operations`,
	} {
		t.Run(name, func(t *testing.T) {
			db, _ := legacy(t)
			exec(t, db, corrupt)
			if err := applicationv3.Run(t.Context(), db, importedAt); err == nil {
				t.Fatal("corrupt migration succeeded")
			}
			var state string
			if err := db.QueryRow(`SELECT state FROM app_calls WHERE call='call-claimed'`).Scan(&state); err != nil || state != "claimed" {
				t.Fatalf("partial call recovery escaped rollback: %q %v", state, err)
			}
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='session_profiles' OR name='retired_application_archive'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("partial target schema escaped rollback: %d %v", n, err)
			}
		})
	}
}

func TestMigrationPinsV1CatalogWithoutRewritingCreationProfile(t *testing.T) {
	db, _ := legacy(t)
	exec(t, db, `DROP TABLE app_configurations; DROP TABLE app_workers; UPDATE app_schema SET version=1`)
	// A v1 operation set has no configuration-update receipt.
	exec(t, db, `DELETE FROM app_operations WHERE operation='config'`)
	if err := applicationv3.Run(t.Context(), db, importedAt); err != nil {
		t.Fatal(err)
	}
	var binding, config string
	if err := db.QueryRow(`SELECT CAST(body AS TEXT) FROM session_profiles`).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT CAST(body AS TEXT) FROM session_configurations`).Scan(&config); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(binding, "native_tools") || !strings.Contains(config, `"native_tools":["RunCommand","Task"]`) {
		t.Fatalf("v1 catalog changed: %s / %s", binding, config)
	}
}

func TestMigrationCancellationAndFreshStore(t *testing.T) {
	db, _ := legacy(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := applicationv3.Run(ctx, db, importedAt); err == nil {
		t.Fatal("cancelled migration succeeded")
	}
	fresh, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "fresh.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer fresh.Close()
	if e = applicationv3.Run(t.Context(), fresh, importedAt); e != nil {
		t.Fatal(e)
	}
	var version int
	if e = fresh.QueryRow(`SELECT version FROM app_schema`).Scan(&version); e != nil || version != 3 {
		t.Fatalf("new Store version: %d %v", version, e)
	}
}

func TestMigrationMergesOnlyMatchingHistoricalCommandProof(t *testing.T) {
	for _, mode := range []string{"matching", "missing-permanent-result", "conflicting-result", "conflicting-intent", "unowned-scope"} {
		t.Run(mode, func(t *testing.T) {
			db, path := legacy(t)
			// Initialize the released common ledger's physical schema independently.
			ledger, e := appserver.NewSQLiteOperationStoreWithConfig(path, appserver.OperationRetentionConfig{})
			if e != nil {
				t.Fatal(e)
			}
			if e = ledger.Initialize(t.Context()); e != nil {
				t.Fatal(e)
			}
			if e = ledger.Close(); e != nil {
				t.Fatal(e)
			}
			var request, result []byte
			if e = db.QueryRow(`SELECT request,result FROM app_operations WHERE connection='c1' AND operation='same-id'`).Scan(&request, &result); e != nil {
				t.Fatal(e)
			}
			wrapped := raw(t, struct {
				ApplicationID string
				ConnectionID  string
				Request       json.RawMessage
			}{"app", "c1", request})
			scopedPrincipal := "application-scope:" + hash(raw(t, []string{"owner", "app", "c1"}))
			commandDigest := hash(wrapped)
			if mode == "conflicting-intent" {
				commandDigest = "another-command"
			}
			if mode == "unowned-scope" {
				scopedPrincipal = "application-scope:unproven"
			}
			if mode == "conflicting-result" {
				result = []byte(`{"operation_id":"same-id","session_id":"session","outcome":"committed"}`)
			}
			if mode == "missing-permanent-result" {
				exec(t, db, `UPDATE app_operations SET result=NULL WHERE connection='c1' AND operation='same-id'`)
			}
			var receipt appserver.CommandResult
			if e = json.Unmarshal(result, &receipt); e != nil {
				t.Fatal(e)
			}
			created := importedAt.Add(-time.Hour)
			record := appserver.OperationRecord{Version: 1, Intent: appserver.OperationIntent{PrincipalID: scopedPrincipal, OperationID: "same-id", Action: appserver.ActionPrompt, SessionID: "session", Target: "session", Digest: commandDigest, CreatedAt: created}, Result: &receipt, UpdatedAt: created, TerminalRetentionNanoseconds: int64(30 * 24 * time.Hour)}
			// Accepted receipts are indeterminate and have no retention deadline.
			if receipt.Outcome == appserver.OutcomeCommitted {
				record.RetainUntil = created.Add(30 * 24 * time.Hour)
			}
			deadline := int64(0)
			if !record.RetainUntil.IsZero() {
				deadline = record.RetainUntil.UnixNano()
			}
			exec(t, db, `INSERT INTO control_operations VALUES(?,?,?,?,?,?,?,?,?)`, scopedPrincipal, "same-id", "turn.prompt", "session", string(receipt.Outcome), created.UnixNano(), created.UnixNano(), deadline, raw(t, record))
			err := applicationv3.Run(t.Context(), db, importedAt)
			wantError := strings.HasPrefix(mode, "conflicting") || mode == "unowned-scope"
			if wantError {
				if err == nil {
					t.Fatal("ambiguous historical evidence accepted")
				}
				var n int
				if e = db.QueryRow(`SELECT count(*) FROM app_operations`).Scan(&n); e != nil || n != 5 {
					t.Fatalf("legacy operations changed on failure: %d %v", n, e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var n int
			if e = db.QueryRow(`SELECT count(*) FROM control_operations WHERE principal_id LIKE 'application-scope:%'`).Scan(&n); e != nil || n != 0 {
				t.Fatalf("second command authority remains: %d %v", n, e)
			}
			id := applicationv3.OperationID("owner", "app", "c1", "same-id")
			if e = db.QueryRow(`SELECT record_json FROM control_operations WHERE principal_id='owner' AND operation_id=?`, id).Scan(&result); e != nil {
				t.Fatal(e)
			}
			var got appserver.OperationRecord
			if e = json.Unmarshal(result, &got); e != nil {
				t.Fatal(e)
			}
			if got.Result == nil || got.Result.Target.TurnID != "turn-c1" || !got.RetainForever {
				t.Fatalf("original proof lost: %+v", got)
			}
		})
	}
}

func TestMigrationKeepsUnknownArchiveTargetAndCreationAddress(t *testing.T) {
	db, path := legacy(t)
	for id, request := range map[string][]byte{
		"archive-unknown": []byte(`{"Action":"archive","Request":{"operation_id":"archive-unknown","session_id":"session"}}`),
		"create-unknown":  []byte(`{"operation_id":"create-unknown","profile":{"version":"v1"}}`),
		"worker-unknown":  []byte(`{"kind":"worker.create","request":{"operation_id":"worker-unknown","cwd":"/fixture"}}`),
	} {
		exec(t, db, `INSERT INTO app_operations VALUES('owner','app','c1',?,?,?,NULL)`, id, hash(request), request)
	}
	if e := applicationv3.Run(t.Context(), db, importedAt); e != nil {
		t.Fatal(e)
	}
	ledger, e := appserver.NewSQLiteOperationStoreWithConfig(path, appserver.OperationRetentionConfig{})
	if e != nil {
		t.Fatal(e)
	}
	defer ledger.Close()
	for id, prefix := range map[string]string{"archive-unknown": "session", "create-unknown": "application-", "worker-unknown": "worker-"} {
		got, found, e := ledger.Read(t.Context(), "owner", applicationv3.OperationID("owner", "app", "c1", id))
		if e != nil || !found || got.Result != nil || !strings.HasPrefix(got.Intent.SessionID, prefix) {
			t.Fatalf("lost unknown original target: %+v %v", got, e)
		}
	}
}

func TestMigrationRejectsTwoAddressesForOneNativeCallback(t *testing.T) {
	db, _ := legacy(t)
	var body []byte
	if e := db.QueryRow(`SELECT body FROM app_calls WHERE call='call-claimed'`).Scan(&body); e != nil {
		t.Fatal(e)
	}
	var call map[string]json.RawMessage
	if e := json.Unmarshal(body, &call); e != nil {
		t.Fatal(e)
	}
	call["id"] = json.RawMessage(`"second-address"`)
	body = raw(t, call)
	exec(t, db, `INSERT INTO app_calls VALUES('owner','app','c1','session','second-address',?,?,'pending',NULL)`, hash(body), body)
	if e := applicationv3.Run(t.Context(), db, importedAt); e == nil {
		t.Fatal("two callback addresses accepted for the same native item")
	}
	var state string
	if e := db.QueryRow(`SELECT state FROM app_calls WHERE call='call-claimed'`).Scan(&state); e != nil || state != "claimed" {
		t.Fatalf("partial migration escaped rollback: %s %v", state, e)
	}
}
