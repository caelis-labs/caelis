package file

import (
	"database/sql"
	"os"
)

// durabilityOps owns the persistence barriers used by the file store.
// Its zero value is the production implementation; tests may replace a
// specific operation to exercise failure boundaries or avoid measuring host
// storage latency when durability is outside the test's contract.
type durabilityOps struct {
	syncFile        func(*os.File) error
	syncDirectory   func(string) error
	configureSQLite func(*sql.DB) error
}

func (ops durabilityOps) SyncFile(file *os.File) error {
	if ops.syncFile != nil {
		return ops.syncFile(file)
	}
	return file.Sync()
}

func (ops durabilityOps) SyncDirectory(path string) error {
	if ops.syncDirectory != nil {
		return ops.syncDirectory(path)
	}
	return syncDir(path)
}

func (ops durabilityOps) ConfigureSQLite(db *sql.DB) error {
	if ops.configureSQLite == nil {
		return nil
	}
	return ops.configureSQLite(db)
}
