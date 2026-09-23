package gatewayapp

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestControlHostIdentityFreshStoreIsDurableWithoutHistoricalTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	first, err := openControlHostIdentity(t.Context(), path)
	if err != nil || first.storeID == "" || first.instanceID == "" {
		t.Fatalf("new Host identity = %+v, %v", first, err)
	}
	second, err := openControlHostIdentity(t.Context(), path)
	if err != nil || second.storeID != first.storeID || second.instanceID == first.instanceID {
		t.Fatalf("reopened Host identity = %+v, %v", second, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='bot_authority'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("historical product table created: %d, %v", count, err)
	}
}

func TestControlHostIdentityAdoptsExistingStoreIDWithoutChangingHistoricalRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE bot_authority (kind TEXT NOT NULL, id TEXT NOT NULL, body TEXT NOT NULL, PRIMARY KEY(kind,id));
INSERT INTO bot_authority(kind,id,body) VALUES('identity','host','{"id":"control-store-existing"}');
INSERT INTO bot_authority(kind,id,body) VALUES('old-data','historical','{"untouched":true}');`)
	if err != nil {
		t.Fatal(err)
	}
	first, err := openControlHostIdentity(context.Background(), path)
	if err != nil || first.storeID != "control-store-existing" || first.instanceID == "" {
		t.Fatalf("first identity = %+v, %v", first, err)
	}
	second, err := openControlHostIdentity(context.Background(), path)
	if err != nil || second.storeID != first.storeID || second.instanceID == first.instanceID {
		t.Fatalf("restarted identity = %+v, %v", second, err)
	}
	var body string
	if err := db.QueryRow(`SELECT body FROM bot_authority WHERE kind='old-data' AND id='historical'`).Scan(&body); err != nil || body != `{"untouched":true}` {
		t.Fatalf("historical data = %q, %v", body, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
