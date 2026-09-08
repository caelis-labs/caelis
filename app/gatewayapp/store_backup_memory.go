package gatewayapp

import (
	"context"
	"io"
	"path/filepath"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/memoryhost"
	"github.com/caelis-labs/caelis/internal/hostownership"
)

// StoreUpgradeReport records Memory's owner-controlled generation transition
// without exposing its credentials, database path or schema internals.
type StoreUpgradeReport struct {
	SourceGeneration  string `json:"source_generation"`
	StorageGeneration string `json:"storage_generation"`
	SchemaVersion     int    `json:"schema_version"`
	RollbackAvailable bool   `json:"rollback_available"`
}

// RestoreStoreWithEmbeddedMemory restores a complete Store using the
// embedded Memory owner's offline lifecycle. Memory remains the only caller
// that opens, authenticates, migrates or rolls back its SQLite authority.
func RestoreStoreWithEmbeddedMemory(ctx context.Context, storeDir string, backup io.Reader) (StoreRestoreReport, error) {
	storeDir, err := normalizeStoreBackupDir(storeDir)
	if err != nil {
		return StoreRestoreReport{}, err
	}
	memoryDir := filepath.Join(storeDir, "memory", "appliance")
	return RestoreStore(ctx, StoreRestoreOptions{
		StoreDir: storeDir,
		Backup:   backup,
		MemoryRestore: func(ctx context.Context, snapshot io.Reader) error {
			_, err := memoryhost.Restore(ctx, memoryDir, snapshot)
			return err
		},
		MemoryCommit: func(context.Context) error {
			return memoryhost.CommitRestore(memoryDir)
		},
		MemoryRollback: func(ctx context.Context) error {
			_, err := memoryhost.RollbackRestore(ctx, memoryDir)
			return err
		},
	})
}

// PrepareStoreUpgrade closes the product ownership window and asks Memory to
// capture its exact stopped generation before a new writer is allowed to
// migrate the Store.
func PrepareStoreUpgrade(ctx context.Context, storeDir string) (StoreUpgradeReport, error) {
	return withStoreUpgradeAuthority(ctx, storeDir, func(memoryDir string) (StoreUpgradeReport, error) {
		result, err := memoryhost.PrepareUpgrade(ctx, memoryDir)
		return StoreUpgradeReport{
			SourceGeneration: result.SourceGeneration, StorageGeneration: result.StorageGeneration,
			SchemaVersion: result.SchemaVersion, RollbackAvailable: result.RollbackAvailable,
		}, err
	})
}

// RollbackStoreUpgrade restores Memory's pre-upgrade generation while the
// product Host remains stopped.
func RollbackStoreUpgrade(ctx context.Context, storeDir string) (StoreUpgradeReport, error) {
	return withStoreUpgradeAuthority(ctx, storeDir, func(memoryDir string) (StoreUpgradeReport, error) {
		result, err := memoryhost.RollbackRestore(ctx, memoryDir)
		return StoreUpgradeReport{
			SourceGeneration: result.SourceGeneration, StorageGeneration: result.StorageGeneration,
			SchemaVersion: result.SchemaVersion, RollbackAvailable: result.RollbackAvailable,
		}, err
	})
}

// CommitStoreUpgrade accepts Memory's migrated generation after all other
// Store authorities have passed their own checks.
func CommitStoreUpgrade(ctx context.Context, storeDir string) error {
	_, err := withStoreUpgradeAuthority(ctx, storeDir, func(memoryDir string) (StoreUpgradeReport, error) {
		return StoreUpgradeReport{}, memoryhost.CommitRestore(memoryDir)
	})
	return err
}

func withStoreUpgradeAuthority(
	ctx context.Context,
	storeDir string,
	operation func(string) (StoreUpgradeReport, error),
) (StoreUpgradeReport, error) {
	storeDir, err := normalizeStoreBackupDir(storeDir)
	if err != nil {
		return StoreUpgradeReport{}, err
	}
	authority, err := hostownership.Acquire(ctx, storeDir)
	if err != nil {
		return StoreUpgradeReport{}, err
	}
	defer authority.Close()
	return operation(filepath.Join(storeDir, "memory", "appliance"))
}
