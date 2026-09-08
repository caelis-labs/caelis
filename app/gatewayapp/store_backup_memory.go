package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

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
	State             string `json:"state"`
	TargetWriter      string `json:"target_writer"`
	PreflightDigest   string `json:"preflight_digest"`
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
	return withStoreUpgradeAuthority(ctx, storeDir, func(storeDir, memoryDir string) (StoreUpgradeReport, error) {
		existing, readErr := readStoreUpgradeJournal(storeDir)
		existingPreparing := readErr == nil && existing.State == "preparing"
		if readErr == nil && !existingPreparing {
			return StoreUpgradeReport{}, errors.New("gatewayapp: Store upgrade is already prepared")
		} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return StoreUpgradeReport{}, readErr
		}
		preflight, err := readStoreUpgradePreflight(ctx, storeDir)
		if err != nil {
			return StoreUpgradeReport{}, err
		}
		if existingPreparing && existing.PreflightDigest != preflight.Digest {
			return StoreUpgradeReport{}, errors.New("gatewayapp: Store upgrade preflight changed while preparing")
		}
		journal := storeUpgradeJournal{
			Format: storeUpgradeJournalFormat, State: "preparing", StoreDir: storeDir,
			TargetWriter: currentStoreUpgradeWriter(), PreparedBy: currentStoreUpgradeBuildVersion(),
			PreflightDigest: preflight.Digest, CreatedAt: time.Now().UTC(),
		}
		if existingPreparing {
			journal = existing
		} else if err := writeStoreUpgradeJournal(storeDir, journal); err != nil {
			return StoreUpgradeReport{}, err
		}
		result, err := memoryhost.PrepareUpgrade(ctx, memoryDir)
		if err != nil {
			_, rollbackErr := memoryhost.RollbackRestore(context.WithoutCancel(ctx), memoryDir)
			if rollbackErr == nil {
				rollbackErr = clearStoreUpgradeJournal(storeDir)
			}
			return StoreUpgradeReport{}, errors.Join(err, rollbackErr)
		}
		journal.State = "prepared"
		journal.SourceGeneration = result.SourceGeneration
		journal.StorageGeneration = result.StorageGeneration
		journal.SchemaVersion = result.SchemaVersion
		if err := writeStoreUpgradeJournal(storeDir, journal); err != nil {
			return StoreUpgradeReport{}, err
		}
		return upgradeReportFromJournal(journal, result.RollbackAvailable), nil
	})
}

// RollbackStoreUpgrade restores Memory's pre-upgrade generation while the
// product Host remains stopped.
func RollbackStoreUpgrade(ctx context.Context, storeDir string) (StoreUpgradeReport, error) {
	return withStoreUpgradeAuthority(ctx, storeDir, func(storeDir, memoryDir string) (StoreUpgradeReport, error) {
		journal, err := readStoreUpgradeJournal(storeDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return StoreUpgradeReport{}, errors.New("gatewayapp: Store upgrade journal is missing")
			}
			return StoreUpgradeReport{}, err
		}
		if journal.State == "committed" {
			return StoreUpgradeReport{}, errors.New("gatewayapp: Store upgrade is already committed")
		}
		if journal.State == "committing" {
			return StoreUpgradeReport{}, errors.New("gatewayapp: Store upgrade commit outcome is unresolved; finish owner commit before admitting a writer")
		}
		if journal.State == "preparing" {
			preflight, err := readStoreUpgradePreflight(ctx, storeDir)
			if err != nil {
				return StoreUpgradeReport{}, err
			}
			if preflight.Digest != journal.PreflightDigest {
				return StoreUpgradeReport{}, errors.New("gatewayapp: Store upgrade preflight changed while recovering preparation")
			}
			prepared, err := memoryhost.PrepareUpgrade(ctx, memoryDir)
			if err != nil {
				return StoreUpgradeReport{}, err
			}
			journal.State = "prepared"
			journal.SourceGeneration = prepared.SourceGeneration
			journal.StorageGeneration = prepared.StorageGeneration
			journal.SchemaVersion = prepared.SchemaVersion
			if err := writeStoreUpgradeJournal(storeDir, journal); err != nil {
				return StoreUpgradeReport{}, err
			}
		}
		result, err := memoryhost.RollbackRestore(ctx, memoryDir)
		if err != nil {
			return StoreUpgradeReport{}, err
		}
		if err := clearStoreUpgradeJournal(storeDir); err != nil {
			return StoreUpgradeReport{}, err
		}
		journal.State = "rolled_back"
		return StoreUpgradeReport{
			SourceGeneration: result.SourceGeneration, StorageGeneration: result.StorageGeneration,
			SchemaVersion: result.SchemaVersion, RollbackAvailable: result.RollbackAvailable,
			State: journal.State, TargetWriter: journal.TargetWriter, PreflightDigest: journal.PreflightDigest,
		}, nil
	})
}

// CommitStoreUpgrade accepts Memory's migrated generation after all other
// Store authorities have passed their own checks.
func CommitStoreUpgrade(ctx context.Context, storeDir string) (StoreUpgradeReport, error) {
	return withStoreUpgradeAuthority(ctx, storeDir, func(storeDir, memoryDir string) (StoreUpgradeReport, error) {
		journal, err := readStoreUpgradeJournal(storeDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return StoreUpgradeReport{}, errors.New("gatewayapp: Store upgrade journal is missing")
			}
			return StoreUpgradeReport{}, err
		}
		if journal.State == "committed" {
			if err := clearStoreUpgradeJournal(storeDir); err != nil {
				return StoreUpgradeReport{}, err
			}
			return upgradeReportFromJournal(journal, false), nil
		}
		preflight, err := readStoreUpgradePreflight(ctx, storeDir)
		if err != nil {
			return StoreUpgradeReport{}, err
		}
		if preflight.Digest != journal.PreflightDigest {
			return StoreUpgradeReport{}, fmt.Errorf("gatewayapp: Store upgrade preflight changed since prepare")
		}
		if journal.State == "preparing" {
			prepared, err := memoryhost.PrepareUpgrade(ctx, memoryDir)
			if err != nil {
				return StoreUpgradeReport{}, err
			}
			journal.State = "prepared"
			journal.SourceGeneration = prepared.SourceGeneration
			journal.StorageGeneration = prepared.StorageGeneration
			journal.SchemaVersion = prepared.SchemaVersion
			if err := writeStoreUpgradeJournal(storeDir, journal); err != nil {
				return StoreUpgradeReport{}, err
			}
		}
		return commitStoreUpgradeGeneration(storeDir, journal, func() error {
			return memoryhost.CommitRestore(memoryDir)
		})
	})
}

// commitStoreUpgradeGeneration records the irreversible owner commit decision.
// An error may follow deletion of the owner's rollback image, so it must never
// restore a rollbackable journal state. Only an idempotent owner commit retry
// can resolve this state; successful cleanup removes the temporary journal.
func commitStoreUpgradeGeneration(storeDir string, journal storeUpgradeJournal, commit func() error) (StoreUpgradeReport, error) {
	journal.State = "committing"
	if err := writeStoreUpgradeJournal(storeDir, journal); err != nil {
		return StoreUpgradeReport{}, err
	}
	if err := commit(); err != nil {
		return StoreUpgradeReport{}, err
	}
	journal.State = "committed"
	if err := writeStoreUpgradeJournal(storeDir, journal); err != nil {
		return StoreUpgradeReport{}, err
	}
	if err := clearStoreUpgradeJournal(storeDir); err != nil {
		return StoreUpgradeReport{}, err
	}
	return upgradeReportFromJournal(journal, false), nil
}

func withStoreUpgradeAuthority(
	ctx context.Context,
	storeDir string,
	operation func(string, string) (StoreUpgradeReport, error),
) (StoreUpgradeReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	storeDir, err := normalizeStoreBackupDir(storeDir)
	if err != nil {
		return StoreUpgradeReport{}, err
	}
	authority, err := hostownership.Acquire(ctx, storeDir)
	if err != nil {
		return StoreUpgradeReport{}, err
	}
	defer authority.Close()
	return operation(storeDir, filepath.Join(storeDir, "memory", "appliance"))
}
