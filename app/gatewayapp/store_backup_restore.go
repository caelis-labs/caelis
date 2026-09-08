package gatewayapp

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/atomicfile"
	"github.com/caelis-labs/caelis/internal/hostownership"
)

const (
	storeRestoreJournalName    = ".caelis-restore.json"
	storeRestoreStagePrefix    = ".caelis-restore-stage-"
	storeRestoreRollbackPrefix = ".caelis-restore-rollback-"
)

type storeRestoreJournal struct {
	Format               string    `json:"format"`
	State                string    `json:"state"`
	StoreDir             string    `json:"store_dir"`
	StageDir             string    `json:"stage_dir"`
	RollbackDir          string    `json:"rollback_dir"`
	Applied              []string  `json:"applied"`
	Pending              string    `json:"pending,omitempty"`
	PendingTargetExisted bool      `json:"pending_target_existed,omitempty"`
	MemoryRestorePending bool      `json:"memory_restore_pending,omitempty"`
	MemoryCommitPending  bool      `json:"memory_commit_pending,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}

// RestoreStore installs all non-secret components and delegates the Memory
// component to its owner. An interrupted component swap is recovered before
// a new archive is accepted.
func RestoreStore(ctx context.Context, options StoreRestoreOptions) (StoreRestoreReport, error) {
	storeDir, err := normalizeStoreBackupDir(options.StoreDir)
	if err != nil {
		return StoreRestoreReport{}, err
	}
	if options.Backup == nil {
		return StoreRestoreReport{}, errors.New("gatewayapp: Store restore archive is required")
	}
	if options.MemoryRestore == nil || options.MemoryCommit == nil || options.MemoryRollback == nil {
		return StoreRestoreReport{}, ErrStoreRestoreMemory
	}
	if err := validateStoreRestoreTargets(storeDir); err != nil {
		return StoreRestoreReport{}, err
	}
	ownership, err := hostownership.Acquire(ctx, storeDir)
	if err != nil {
		return StoreRestoreReport{}, fmt.Errorf("gatewayapp: Store is owned by another Host: %w", err)
	}
	defer ownership.Close()
	recovered, err := recoverStoreRestore(ctx, storeDir, options)
	if err != nil {
		return StoreRestoreReport{}, err
	}
	rawArchive, err := io.ReadAll(io.LimitReader(options.Backup, 1<<30))
	if err != nil {
		return StoreRestoreReport{}, fmt.Errorf("read Store restore archive: %w", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(rawArchive), int64(len(rawArchive)))
	if err != nil {
		return StoreRestoreReport{}, fmt.Errorf("open Store restore archive: %w", err)
	}
	manifest, memoryBytes, stageDir, err := stageStoreRestore(ctx, reader, storeDir)
	if err != nil {
		return StoreRestoreReport{}, err
	}
	defer os.RemoveAll(stageDir)
	if err := writeStoreRestoreJournal(storeDir, storeRestoreJournal{
		Format: StoreBackupFormat, State: "staged", StoreDir: storeDir,
		StageDir: stageDir, CreatedAt: time.Now().UTC(),
	}); err != nil {
		return StoreRestoreReport{}, err
	}
	rollbackDir, err := os.MkdirTemp(filepath.Dir(storeDir), storeRestoreRollbackPrefix)
	if err != nil {
		return StoreRestoreReport{}, err
	}
	journal := storeRestoreJournal{
		Format: StoreBackupFormat, State: "applying", StoreDir: storeDir,
		StageDir: stageDir, RollbackDir: rollbackDir, CreatedAt: time.Now().UTC(),
	}
	if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
		os.RemoveAll(rollbackDir)
		return StoreRestoreReport{}, err
	}
	report := StoreRestoreReport{
		Format: manifest.Format, QuiesceBoundary: manifest.QuiesceBoundary,
		RecoveredPrevious: recovered,
	}
	for _, relative := range []string{
		"config.json",
		filepath.Join("control", controlStoreDatabaseFile),
		"sessions",
	} {
		if err := ctx.Err(); err != nil {
			return rollbackStoreRestoreWithError(storeDir, err, report)
		}
		stagedPath := filepath.Join(stageDir, relative)
		if _, err := os.Lstat(stagedPath); err != nil {
			if errors.Is(err, os.ErrNotExist) && relative == "sessions" {
				continue
			}
			return rollbackStoreRestoreWithError(storeDir, fmt.Errorf("staged component %s is missing", relative), report)
		}
		target := filepath.Join(storeDir, relative)
		rollbackTarget := filepath.Join(rollbackDir, relative)
		journal.Pending = relative
		journal.PendingTargetExisted = false
		if _, err := os.Lstat(target); err == nil {
			journal.PendingTargetExisted = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return rollbackStoreRestoreWithError(storeDir, err, report)
		}
		if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
			return rollbackStoreRestoreWithError(storeDir, err, report)
		}
		if err := os.MkdirAll(filepath.Dir(rollbackTarget), 0o700); err != nil {
			return rollbackStoreRestoreWithError(storeDir, err, report)
		}
		if journal.PendingTargetExisted {
			if err := os.Rename(target, rollbackTarget); err != nil {
				return rollbackStoreRestoreWithError(storeDir, fmt.Errorf("move existing %s to rollback: %w", relative, err), report)
			}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return rollbackStoreRestoreWithError(storeDir, err, report)
		}
		if err := os.Rename(stagedPath, target); err != nil {
			return rollbackStoreRestoreWithError(storeDir, fmt.Errorf("install restored %s: %w", relative, err), report)
		}
		journal.Applied = append(journal.Applied, relative)
		journal.Pending = ""
		journal.PendingTargetExisted = false
		if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
			return rollbackStoreRestoreWithError(storeDir, err, report)
		}
		report.RestoredComponents = append(report.RestoredComponents, relative)
	}
	journal.MemoryRestorePending = true
	if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
		return rollbackStoreRestoreWithError(storeDir, err, report)
	}
	if err := options.MemoryRestore(ctx, bytes.NewReader(memoryBytes)); err != nil {
		if rollbackErr := options.MemoryRollback(ctx); rollbackErr != nil {
			return report, errors.Join(fmt.Errorf("restore Memory owner snapshot: %w", err), rollbackErr)
		}
		return rollbackStoreRestoreWithError(storeDir, fmt.Errorf("restore Memory owner snapshot: %w", err), report)
	}
	journal.MemoryRestorePending = false
	journal.MemoryCommitPending = true
	if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
		return report, err
	}
	if err := options.MemoryCommit(ctx); err != nil {
		if rollbackErr := options.MemoryRollback(ctx); rollbackErr != nil {
			return report, errors.Join(fmt.Errorf("commit Memory owner restore: %w", err), rollbackErr)
		}
		return rollbackStoreRestoreWithError(storeDir, fmt.Errorf("commit Memory owner restore: %w", err), report)
	}
	journal.MemoryCommitPending = false
	journal.State = "committed"
	if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
		return report, err
	}
	if err := os.RemoveAll(rollbackDir); err != nil {
		return StoreRestoreReport{}, fmt.Errorf("remove Store restore rollback: %w", err)
	}
	if err := clearStoreRestoreJournal(storeDir); err != nil {
		return StoreRestoreReport{}, err
	}
	return report, nil
}

func stageStoreRestore(ctx context.Context, reader *zip.Reader, storeDir string) (StoreBackupManifest, []byte, string, error) {
	entry := findZipEntry(reader, storeBackupManifestName)
	if entry == nil {
		return StoreBackupManifest{}, nil, "", errors.New("gatewayapp: Store restore manifest is missing")
	}
	rawManifest, err := readZipEntry(entry)
	if err != nil {
		return StoreBackupManifest{}, nil, "", err
	}
	var manifest StoreBackupManifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		return StoreBackupManifest{}, nil, "", fmt.Errorf("decode Store restore manifest: %w", err)
	}
	if err := validateStoreBackupManifest(manifest); err != nil {
		return StoreBackupManifest{}, nil, "", err
	}
	stageDir, err := os.MkdirTemp(filepath.Dir(storeDir), storeRestoreStagePrefix)
	if err != nil {
		return StoreBackupManifest{}, nil, "", fmt.Errorf("create Store restore staging directory: %w", err)
	}
	cleanup := func(cause error) (StoreBackupManifest, []byte, string, error) {
		_ = os.RemoveAll(stageDir)
		return StoreBackupManifest{}, nil, "", cause
	}
	var memoryBytes []byte
	found := map[string]bool{}
	for _, component := range manifest.Components {
		if err := ctx.Err(); err != nil {
			return cleanup(err)
		}
		entry := findZipEntry(reader, component.ArchivePath)
		if entry == nil {
			return cleanup(fmt.Errorf("%w: missing %s", ErrStoreBackupFormat, component.ArchivePath))
		}
		raw, err := readZipEntry(entry)
		if err != nil {
			return cleanup(err)
		}
		if int64(len(raw)) != component.Size || sha256Bytes(raw) != component.SHA256 {
			return cleanup(fmt.Errorf("%w: integrity mismatch for %s", ErrStoreBackupFormat, component.Name))
		}
		if component.Kind == "config" {
			if err := validateStoreBackupConfig(raw); err != nil {
				return cleanup(fmt.Errorf("%w: invalid config component: %w", ErrStoreBackupFormat, err))
			}
		}
		if component.Kind == "memory-owner-snapshot" {
			if len(memoryBytes) != 0 {
				return cleanup(fmt.Errorf("%w: duplicate Memory snapshot", ErrStoreBackupFormat))
			}
			memoryBytes = raw
			found["memory"] = true
			continue
		}
		relative, err := restoreComponentRelativePath(component)
		if err != nil {
			return cleanup(err)
		}
		found[component.Kind] = true
		path := filepath.Join(stageDir, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return cleanup(err)
		}
		if strings.HasSuffix(component.ArchivePath, "/") {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return cleanup(err)
			}
		} else if err := os.WriteFile(path, raw, 0o600); err != nil {
			return cleanup(err)
		}
	}
	for _, required := range []string{"config", "sqlite", "session-canonical", "memory"} {
		if !found[required] {
			return cleanup(fmt.Errorf("%w: required component %s is missing", ErrStoreBackupFormat, required))
		}
	}
	return manifest, memoryBytes, stageDir, nil
}

func restoreComponentRelativePath(component StoreBackupComponent) (string, error) {
	name := strings.TrimPrefix(component.ArchivePath, storeBackupComponentRoot)
	switch {
	case component.Kind == "config" && name == "config":
		return "config.json", nil
	case component.Kind == "sqlite" && name == "control":
		return filepath.Join("control", controlStoreDatabaseFile), nil
	case component.Kind == "session-canonical" && strings.HasPrefix(name, "sessions/"):
		if name == "sessions/" {
			return "sessions", nil
		}
		relative := strings.TrimPrefix(name, "sessions/")
		if !validStoreSessionRelativePath(relative) {
			return "", fmt.Errorf("%w: invalid Session path", ErrStoreBackupFormat)
		}
		return filepath.Join("sessions", filepath.FromSlash(relative)), nil
	default:
		return "", fmt.Errorf("%w: unsupported component %q", ErrStoreBackupFormat, component.Name)
	}
}

func sha256Bytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func rollbackStoreRestoreWithError(storeDir string, cause error, report StoreRestoreReport) (StoreRestoreReport, error) {
	rollbackErr := rollbackStoreRestore(storeDir)
	report.RolledBackOnFailure = rollbackErr == nil
	return report, errors.Join(cause, rollbackErr)
}

func recoverStoreRestore(ctx context.Context, storeDir string, options StoreRestoreOptions) (bool, error) {
	journal, err := readStoreRestoreJournal(storeDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if journal.State == "staged" {
		_ = os.RemoveAll(journal.StageDir)
		if err := clearStoreRestoreJournal(storeDir); err != nil {
			return false, err
		}
		return true, nil
	}
	if journal.State == "committed" {
		if err := os.RemoveAll(journal.StageDir); err != nil {
			return false, err
		}
		if err := os.RemoveAll(journal.RollbackDir); err != nil {
			return false, err
		}
		if err := clearStoreRestoreJournal(storeDir); err != nil {
			return false, err
		}
		return true, nil
	}
	if journal.State != "applying" {
		return false, fmt.Errorf("gatewayapp: restore journal has unknown state %q", journal.State)
	}
	if journal.MemoryCommitPending {
		if options.MemoryCommit == nil || options.MemoryRollback == nil {
			return false, ErrStoreRestoreMemory
		}
		if err := options.MemoryCommit(ctx); err == nil {
			journal.MemoryCommitPending = false
			if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
				return false, err
			}
			if err := os.RemoveAll(journal.RollbackDir); err != nil {
				return false, err
			}
			_ = os.RemoveAll(journal.StageDir)
			if err := clearStoreRestoreJournal(storeDir); err != nil {
				return false, err
			}
			return true, nil
		} else if rollbackErr := options.MemoryRollback(ctx); rollbackErr != nil {
			return false, errors.Join(err, rollbackErr)
		}
	}
	if journal.MemoryRestorePending {
		if options.MemoryRollback == nil {
			return false, ErrStoreRestoreMemory
		}
		if err := options.MemoryRollback(ctx); err != nil {
			return false, err
		}
	}
	if err := rollbackStoreRestore(storeDir); err != nil {
		return false, fmt.Errorf("gatewayapp: interrupted Store restore cannot be recovered: %w", err)
	}
	return true, nil
}

func rollbackStoreRestore(storeDir string) error {
	if err := validateStoreRestoreTargets(storeDir); err != nil {
		return err
	}
	journal, err := readStoreRestoreJournal(storeDir)
	if err != nil {
		return err
	}
	relatives := append([]string(nil), journal.Applied...)
	if journal.Pending != "" {
		alreadyApplied := false
		for _, relative := range relatives {
			if relative == journal.Pending {
				alreadyApplied = true
				break
			}
		}
		if !alreadyApplied {
			relatives = append(relatives, journal.Pending)
		}
	}
	for index := len(relatives) - 1; index >= 0; index-- {
		relative := relatives[index]
		target := filepath.Join(storeDir, relative)
		rollbackTarget := filepath.Join(journal.RollbackDir, relative)
		_, rollbackErr := os.Lstat(rollbackTarget)
		if rollbackErr == nil {
			if _, err := os.Lstat(target); err == nil {
				if err := os.RemoveAll(target); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			if err := os.Rename(rollbackTarget, target); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(rollbackErr, os.ErrNotExist) {
			return rollbackErr
		}
		// If the target existed before the pending move, the absence of a
		// rollback copy means the rename never happened; preserve that target.
		// A newly-created target has no prior copy and must be removed.
		if relative == journal.Pending && journal.PendingTargetExisted {
			continue
		}
		if _, err := os.Lstat(target); err == nil {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	_ = os.RemoveAll(journal.StageDir)
	_ = os.RemoveAll(journal.RollbackDir)
	return clearStoreRestoreJournal(storeDir)
}

func writeStoreRestoreJournal(storeDir string, journal storeRestoreJournal) error {
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(storeDir, ".caelis-restore-journal-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(append(raw, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := atomicfile.Replace(tempPath, filepath.Join(storeDir, storeRestoreJournalName)); err != nil {
		return err
	}
	return syncStoreRestoreDirectory(storeDir)
}

func validateStoreRestoreTargets(storeDir string) error {
	if err := validateStoreRestoreTarget(storeDir, true, "Store"); err != nil {
		return err
	}
	for _, target := range []struct {
		path string
		dir  bool
		name string
	}{
		{path: filepath.Join(storeDir, "config.json"), name: "config"},
		{path: filepath.Join(storeDir, "control"), dir: true, name: "Control directory"},
		{path: controlStoreDatabasePath(storeDir), name: "Control database"},
		{path: filepath.Join(storeDir, "sessions"), dir: true, name: "Session directory"},
	} {
		if err := validateStoreRestoreTarget(target.path, target.dir, target.name); err != nil {
			return err
		}
	}
	return nil
}

func validateStoreRestoreTarget(path string, wantDir bool, name string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("gatewayapp: inspect restore %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("gatewayapp: restore %s must not be a symlink", name)
	}
	if wantDir && !info.IsDir() {
		return fmt.Errorf("gatewayapp: restore %s must be a directory", name)
	}
	if !wantDir && !info.Mode().IsRegular() {
		return fmt.Errorf("gatewayapp: restore %s must be a regular file", name)
	}
	return nil
}

func readStoreRestoreJournal(storeDir string) (storeRestoreJournal, error) {
	raw, err := os.ReadFile(filepath.Join(storeDir, storeRestoreJournalName))
	if err != nil {
		return storeRestoreJournal{}, err
	}
	var journal storeRestoreJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return storeRestoreJournal{}, fmt.Errorf("gatewayapp: invalid Store restore journal: %w", err)
	}
	if journal.Format != StoreBackupFormat || filepath.Clean(journal.StoreDir) != filepath.Clean(storeDir) {
		return storeRestoreJournal{}, errors.New("gatewayapp: Store restore journal does not match Store")
	}
	if err := validateStoreRestoreJournalPath(storeDir, journal.StageDir, storeRestoreStagePrefix); err != nil {
		return storeRestoreJournal{}, err
	}
	if err := validateStoreRestoreJournalPath(storeDir, journal.RollbackDir, storeRestoreRollbackPrefix); err != nil {
		return storeRestoreJournal{}, err
	}
	switch journal.State {
	case "staged":
		if journal.StageDir == "" {
			return storeRestoreJournal{}, errors.New("gatewayapp: staged Store restore journal is missing a staging directory")
		}
	case "applying":
		if journal.StageDir == "" || journal.RollbackDir == "" {
			return storeRestoreJournal{}, errors.New("gatewayapp: applying Store restore journal is missing recovery directories")
		}
	case "committed":
		// Finalization may have already removed either directory before the
		// committed journal became observable; absent cleanup paths are valid.
	default:
		return storeRestoreJournal{}, fmt.Errorf("gatewayapp: restore journal has unknown state %q", journal.State)
	}
	for _, relative := range journal.Applied {
		if !validStoreRestoreRelativePath(relative) {
			return storeRestoreJournal{}, errors.New("gatewayapp: Store restore journal contains an invalid applied path")
		}
	}
	if journal.Pending != "" && !validStoreRestoreRelativePath(journal.Pending) {
		return storeRestoreJournal{}, errors.New("gatewayapp: Store restore journal contains an invalid pending path")
	}
	return journal, nil
}

func validateStoreRestoreJournalPath(storeDir, path, prefix string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return errors.New("gatewayapp: invalid Store restore journal recovery path")
	}
	absolute = filepath.Clean(absolute)
	storeParent := filepath.Dir(filepath.Clean(storeDir))
	if filepath.Dir(absolute) != storeParent || !strings.HasPrefix(filepath.Base(absolute), prefix) {
		return errors.New("gatewayapp: Store restore journal recovery path is outside its Store")
	}
	info, err := os.Lstat(absolute)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("gatewayapp: Store restore journal recovery path is not a directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gatewayapp: inspect Store restore journal recovery path: %w", err)
	}
	return nil
}

func validStoreRestoreRelativePath(relative string) bool {
	if strings.TrimSpace(relative) == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return false
	}
	switch relative {
	case "config.json", filepath.Join("control", controlStoreDatabaseFile), "sessions":
		return true
	default:
		if !strings.HasPrefix(relative, "sessions"+string(filepath.Separator)) {
			return false
		}
		return validStoreSessionRelativePath(strings.TrimPrefix(relative, "sessions"+string(filepath.Separator)))
	}
}

func validStoreSessionRelativePath(relative string) bool {
	if strings.TrimSpace(relative) == "" || filepath.IsAbs(filepath.FromSlash(relative)) || strings.Contains(relative, "..") {
		return false
	}
	base := filepath.Base(filepath.FromSlash(relative))
	if base == ".sessions.index.sqlite" {
		return true
	}
	return !strings.HasPrefix(base, ".") && !strings.HasSuffix(base, ".lock") && !strings.HasSuffix(base, ".tmp")
}

func clearStoreRestoreJournal(storeDir string) error {
	err := os.Remove(filepath.Join(storeDir, storeRestoreJournalName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
