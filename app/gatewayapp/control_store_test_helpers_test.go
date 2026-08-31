package gatewayapp

import (
	"context"
	"testing"

	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func reopenControlOperationStore(t *testing.T, storeDir string) *appserver.SQLiteOperationStore {
	t.Helper()
	store, err := appserver.NewSQLiteOperationStoreWithConfig(controlStoreDatabasePath(storeDir), appserver.OperationRetentionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Initialize(context.Background()); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
