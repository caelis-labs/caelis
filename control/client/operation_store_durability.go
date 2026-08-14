package controlclient

import "os"

// operationStoreDurability owns the persistence barriers used by the file
// operation ledger. Its zero value is the production implementation; logical
// tests may replace individual operations without bypassing filesystem,
// locking, rename, or retention behavior.
type operationStoreDurability struct {
	syncFile      func(*os.File) error
	syncDirectory func(string) error
}

func (durability operationStoreDurability) SyncFile(file *os.File) error {
	if durability.syncFile != nil {
		return durability.syncFile(file)
	}
	return file.Sync()
}

func (durability operationStoreDurability) SyncDirectory(path string) error {
	if durability.syncDirectory != nil {
		return durability.syncDirectory(path)
	}
	return syncOperationStoreDirectory(path)
}
