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
	"sort"
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
	Format               string                                   `json:"format"`
	State                string                                   `json:"state"`
	StoreDir             string                                   `json:"store_dir"`
	StageDir             string                                   `json:"stage_dir"`
	RollbackDir          string                                   `json:"rollback_dir"`
	Applied              []string                                 `json:"applied"`
	Components           map[string]storeRestoreComponentEvidence `json:"components,omitempty"`
	RollbackCompleted    []string                                 `json:"rollback_completed,omitempty"`
	Pending              string                                   `json:"pending,omitempty"`
	PendingTargetExisted bool                                     `json:"pending_target_existed,omitempty"`
	MemoryRestorePending bool                                     `json:"memory_restore_pending,omitempty"`
	MemoryCommitPending  bool                                     `json:"memory_commit_pending,omitempty"`
	CreatedAt            time.Time                                `json:"created_at"`
}

// storeRestoreComponentEvidence records enough durable content evidence to
// recognize a completed rollback after the component rename succeeded but the
// journal update did not. It deliberately contains digests only; component
// contents remain owned by their existing Store authorities.
type storeRestoreComponentEvidence struct {
	TargetExisted   bool   `json:"target_existed"`
	OriginalDigest  string `json:"original_digest,omitempty"`
	InstalledDigest string `json:"installed_digest,omitempty"`
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
	if err := rejectPendingStoreUpgrade(storeDir); err != nil {
		return StoreRestoreReport{}, fmt.Errorf("gatewayapp: cannot restore while Store upgrade is pending: %w", err)
	}
	if err := validateStoreRestoreTargets(storeDir); err != nil {
		return StoreRestoreReport{}, err
	}
	ownership, err := hostownership.Acquire(ctx, storeDir)
	if err != nil {
		return StoreRestoreReport{}, fmt.Errorf("gatewayapp: Store is owned by another Host: %w", err)
	}
	defer ownership.Close()
	if err := rejectStoreRestoreControlSidecars(storeDir); err != nil {
		return StoreRestoreReport{}, err
	}
	recovered, err := recoverStoreRestore(ctx, storeDir, options)
	if err != nil {
		return StoreRestoreReport{}, err
	}
	archiveFile, archiveSize, err := materializeStoreBackupArchive(options.Backup, storeBackupArchiveMaxBytes)
	if err != nil {
		return StoreRestoreReport{}, fmt.Errorf("read Store restore archive: %w", err)
	}
	defer func() {
		name := archiveFile.Name()
		_ = archiveFile.Close()
		_ = os.Remove(name)
	}()
	reader, err := zip.NewReader(archiveFile, archiveSize)
	if err != nil {
		return StoreRestoreReport{}, fmt.Errorf("open Store restore archive: %w", err)
	}
	manifest, memoryBytes, stageDir, err := stageStoreRestore(ctx, reader, storeDir)
	if err != nil {
		return StoreRestoreReport{}, err
	}
	defer func() {
		if err := os.RemoveAll(stageDir); err == nil {
			_ = storeRestoreSyncDirectory(filepath.Dir(stageDir))
		}
	}()
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
		Components: make(map[string]storeRestoreComponentEvidence),
	}
	if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
		os.RemoveAll(rollbackDir)
		return StoreRestoreReport{}, err
	}
	if err := storeRestoreSyncDirectory(filepath.Dir(rollbackDir)); err != nil {
		return rollbackStoreRestoreWithError(storeDir, fmt.Errorf("sync Store restore recovery directories: %w", err), StoreRestoreReport{RecoveredPrevious: recovered})
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
		evidence := storeRestoreComponentEvidence{}
		if _, err := os.Lstat(target); err == nil {
			journal.PendingTargetExisted = true
			evidence.TargetExisted = true
			evidence.OriginalDigest, err = storeRestorePathDigest(target)
			if err != nil {
				return rollbackStoreRestoreWithError(storeDir, fmt.Errorf("capture original %s evidence: %w", relative, err), report)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return rollbackStoreRestoreWithError(storeDir, err, report)
		}
		installedDigest, err := storeRestorePathDigest(stagedPath)
		if err != nil {
			return rollbackStoreRestoreWithError(storeDir, fmt.Errorf("capture staged %s evidence: %w", relative, err), report)
		}
		evidence.InstalledDigest = installedDigest
		if journal.Components == nil {
			journal.Components = make(map[string]storeRestoreComponentEvidence)
		}
		journal.Components[relative] = evidence
		if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
			return rollbackStoreRestoreWithError(storeDir, err, report)
		}
		if err := os.MkdirAll(filepath.Dir(rollbackTarget), 0o700); err != nil {
			return rollbackStoreRestoreWithError(storeDir, err, report)
		}
		if err := syncStoreRestoreDirectories(filepath.Dir(rollbackTarget), journal.RollbackDir); err != nil {
			return rollbackStoreRestoreWithError(storeDir, fmt.Errorf("sync rollback destination for %s: %w", relative, err), report)
		}
		if journal.PendingTargetExisted {
			if err := storeRestoreRename(target, rollbackTarget); err != nil {
				return rollbackStoreRestoreWithError(storeDir, fmt.Errorf("move existing %s to rollback: %w", relative, err), report)
			}
			if err := syncStoreRestoreDirectories(filepath.Dir(target), filepath.Dir(rollbackTarget)); err != nil {
				return rollbackStoreRestoreWithError(storeDir, fmt.Errorf("sync moved %s to rollback: %w", relative, err), report)
			}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return rollbackStoreRestoreWithError(storeDir, err, report)
		}
		if err := storeRestoreRename(stagedPath, target); err != nil {
			return rollbackStoreRestoreWithError(storeDir, fmt.Errorf("install restored %s: %w", relative, err), report)
		}
		if err := syncStoreRestoreDirectories(filepath.Dir(stagedPath), filepath.Dir(target)); err != nil {
			return rollbackStoreRestoreWithError(storeDir, fmt.Errorf("sync installed %s: %w", relative, err), report)
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
	if err := refuseUnsyncedStoreRestoreCommit(storeDir, journal.Applied); err != nil {
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
	if err := storeRestoreSyncDirectory(filepath.Dir(rollbackDir)); err != nil {
		return StoreRestoreReport{}, fmt.Errorf("sync Store restore rollback cleanup: %w", err)
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
	if err := validateStagedStoreRestore(ctx, stageDir); err != nil {
		return cleanup(err)
	}
	if err := syncStoreRestoreTree(stageDir); err != nil {
		return cleanup(fmt.Errorf("sync staged Store restore: %w", err))
	}
	if err := storeRestoreSyncDirectory(filepath.Dir(stageDir)); err != nil {
		return cleanup(fmt.Errorf("sync staged Store restore parent: %w", err))
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

// storeRestorePathDigest is a read-only, deterministic digest for one Store
// component. It includes relative names and entry kinds so a directory with
// different children cannot be mistaken for the original component after a
// crash. Symlinks and other special entries are refused by the Store
// authority instead of being followed during recovery.
func storeRestorePathDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
		return "", fmt.Errorf("path is not a regular file or directory")
	}
	hash := sha256.New()
	addEntry := func(relative string, entryType string, mode os.FileMode, raw []byte) {
		_, _ = io.WriteString(hash, entryType)
		_, _ = hash.Write([]byte{0})
		_, _ = io.WriteString(hash, relative)
		_, _ = hash.Write([]byte{0})
		_, _ = io.WriteString(hash, fmt.Sprintf("%o", mode.Perm()))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(raw)
		_, _ = hash.Write([]byte{0})
	}
	if info.Mode().IsRegular() {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		addEntry(filepath.Base(path), "file", info.Mode(), raw)
		return hex.EncodeToString(hash.Sum(nil)), nil
	}
	err = filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entryInfo.Mode().IsRegular() && !entryInfo.IsDir()) {
			return fmt.Errorf("component entry %q is not a regular file or directory", current)
		}
		relative, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		if relative == "." {
			addEntry(".", "directory", entryInfo.Mode(), nil)
			return nil
		}
		if entryInfo.IsDir() {
			addEntry(filepath.ToSlash(relative), "directory", entryInfo.Mode(), nil)
			return nil
		}
		raw, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		addEntry(filepath.ToSlash(relative), "file", entryInfo.Mode(), raw)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
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
		if err := os.RemoveAll(journal.StageDir); err != nil {
			return false, err
		}
		if err := storeRestoreSyncDirectory(filepath.Dir(journal.StageDir)); err != nil {
			return false, err
		}
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
		if err := syncStoreRestoreDirectories(filepath.Dir(journal.StageDir), filepath.Dir(journal.RollbackDir)); err != nil {
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
		if err := refuseUnsyncedStoreRestoreCommit(storeDir, journal.Applied); err != nil {
			return false, err
		}
		if err := options.MemoryCommit(ctx); err == nil {
			journal.MemoryCommitPending = false
			if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
				return false, err
			}
			if err := os.RemoveAll(journal.RollbackDir); err != nil {
				return false, err
			}
			if err := os.RemoveAll(journal.StageDir); err != nil {
				return false, err
			}
			if err := syncStoreRestoreDirectories(filepath.Dir(journal.RollbackDir), filepath.Dir(journal.StageDir)); err != nil {
				return false, err
			}
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
	if err := rejectStoreRestoreControlSidecars(storeDir); err != nil {
		return err
	}
	journal, err := readStoreRestoreJournal(storeDir)
	if err != nil {
		return err
	}
	relatives := append([]string(nil), journal.Applied...)
	if journal.Pending != "" {
		if !containsStoreRestorePath(relatives, journal.Pending) {
			relatives = append(relatives, journal.Pending)
		}
	}
	componentRelatives := make([]string, 0, len(journal.Components))
	for relative := range journal.Components {
		componentRelatives = append(componentRelatives, relative)
	}
	sort.Strings(componentRelatives)
	for _, relative := range componentRelatives {
		if !containsStoreRestorePath(relatives, relative) {
			relatives = append(relatives, relative)
		}
	}
	for index := len(relatives) - 1; index >= 0; index-- {
		relative := relatives[index]
		evidence, hasEvidence := journal.Components[relative]
		if containsStoreRestorePath(journal.RollbackCompleted, relative) {
			if err := verifyStoreRestoreRollbackCompletion(storeDir, relative, evidence, hasEvidence); err != nil {
				return err
			}
			continue
		}
		target := filepath.Join(storeDir, relative)
		rollbackTarget := filepath.Join(journal.RollbackDir, relative)
		_, rollbackErr := os.Lstat(rollbackTarget)
		if rollbackErr == nil {
			if hasEvidence && !evidence.TargetExisted {
				return fmt.Errorf("rollback evidence for existing %s contradicts the original target state", relative)
			}
			if !hasEvidence {
				originalDigest, err := storeRestorePathDigest(rollbackTarget)
				if err != nil {
					return fmt.Errorf("capture rollback evidence for %s: %w", relative, err)
				}
				evidence = storeRestoreComponentEvidence{TargetExisted: true, OriginalDigest: originalDigest}
				if journal.Components == nil {
					journal.Components = make(map[string]storeRestoreComponentEvidence)
				}
				journal.Components[relative] = evidence
				if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
					return err
				}
			}
			rollbackDigest, err := storeRestorePathDigest(rollbackTarget)
			if err != nil {
				return fmt.Errorf("inspect rollback evidence for %s: %w", relative, err)
			}
			if evidence.OriginalDigest == "" {
				evidence.OriginalDigest = rollbackDigest
				journal.Components[relative] = evidence
				if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
					return err
				}
			} else if rollbackDigest != evidence.OriginalDigest {
				return fmt.Errorf("rollback evidence for %s does not match the recorded original", relative)
			}
			if _, err := os.Lstat(target); err == nil {
				if err := os.RemoveAll(target); err != nil {
					return err
				}
				if err := storeRestoreSyncDirectory(filepath.Dir(target)); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			if err := storeRestoreSyncDirectory(filepath.Dir(target)); err != nil {
				return err
			}
			if err := storeRestoreRename(rollbackTarget, target); err != nil {
				return err
			}
			if err := syncStoreRestoreDirectories(filepath.Dir(rollbackTarget), filepath.Dir(target)); err != nil {
				return err
			}
			if err := verifyStoreRestorePathDigest(target, evidence.OriginalDigest, relative); err != nil {
				return err
			}
			journal.RollbackCompleted = append(journal.RollbackCompleted, relative)
			if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(rollbackErr, os.ErrNotExist) {
			return rollbackErr
		}
		if !hasEvidence {
			return fmt.Errorf("rollback evidence for %s is missing; refusing to remove an uncertain target", relative)
		}
		if evidence.TargetExisted {
			if err := verifyStoreRestorePathDigest(target, evidence.OriginalDigest, relative); err != nil {
				return err
			}
		} else {
			info, err := os.Lstat(target)
			switch {
			case errors.Is(err, os.ErrNotExist):
				// The new component was already removed before the crash.
			case err != nil:
				return err
			default:
				if evidence.InstalledDigest == "" {
					return fmt.Errorf("rollback evidence for new %s has no installed digest; refusing to remove an uncertain target", relative)
				}
				if info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("rollback target %s is a symlink", relative)
				}
				installedDigest, err := storeRestorePathDigest(target)
				if err != nil {
					return fmt.Errorf("inspect installed %s during rollback: %w", relative, err)
				}
				if installedDigest != evidence.InstalledDigest {
					return fmt.Errorf("installed %s changed before rollback; refusing to remove it", relative)
				}
				if err := os.RemoveAll(target); err != nil {
					return err
				}
				if err := storeRestoreSyncDirectory(filepath.Dir(target)); err != nil {
					return err
				}
			}
		}
		journal.RollbackCompleted = append(journal.RollbackCompleted, relative)
		if err := writeStoreRestoreJournal(storeDir, journal); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(journal.StageDir); err != nil {
		return err
	}
	if err := os.RemoveAll(journal.RollbackDir); err != nil {
		return err
	}
	if err := syncStoreRestoreDirectories(filepath.Dir(journal.StageDir), filepath.Dir(journal.RollbackDir)); err != nil {
		return err
	}
	return clearStoreRestoreJournal(storeDir)
}

func containsStoreRestorePath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

func verifyStoreRestorePathDigest(target, wantDigest, relative string) error {
	if strings.TrimSpace(wantDigest) == "" {
		return fmt.Errorf("rollback evidence for %s has no original digest", relative)
	}
	got, err := storeRestorePathDigest(target)
	if err != nil {
		return fmt.Errorf("verify restored %s: %w", relative, err)
	}
	if got != wantDigest {
		return fmt.Errorf("restored %s does not match the recorded original", relative)
	}
	return nil
}

func verifyStoreRestoreRollbackCompletion(storeDir, relative string, evidence storeRestoreComponentEvidence, hasEvidence bool) error {
	if !hasEvidence {
		return fmt.Errorf("rollback completion for %s has no component evidence", relative)
	}
	target := filepath.Join(storeDir, relative)
	if evidence.TargetExisted {
		return verifyStoreRestorePathDigest(target, evidence.OriginalDigest, relative)
	}
	if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("rollback completion for new %s has a surviving target", relative)
}

func writeStoreRestoreJournal(storeDir string, journal storeRestoreJournal) error {
	storeRestoreHooks.RLock()
	writeJournal := storeRestoreHooks.writeJournal
	storeRestoreHooks.RUnlock()
	if writeJournal != nil {
		if err := writeJournal(journal); err != nil {
			return err
		}
	}
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
	return storeRestoreSyncDirectory(storeDir)
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
	for relative, evidence := range journal.Components {
		if !validStoreRestoreRelativePath(relative) {
			return storeRestoreJournal{}, errors.New("gatewayapp: Store restore journal contains an invalid component path")
		}
		for name, digest := range map[string]string{
			"original": evidence.OriginalDigest, "installed": evidence.InstalledDigest,
		} {
			if digest != "" && (len(digest) != sha256.Size*2 || !isLowerHexDigest(digest)) {
				return storeRestoreJournal{}, fmt.Errorf("gatewayapp: Store restore journal contains an invalid %s digest", name)
			}
		}
	}
	for _, relative := range journal.RollbackCompleted {
		if !validStoreRestoreRelativePath(relative) {
			return storeRestoreJournal{}, errors.New("gatewayapp: Store restore journal contains an invalid completed path")
		}
	}
	if journal.Pending != "" && !validStoreRestoreRelativePath(journal.Pending) {
		return storeRestoreJournal{}, errors.New("gatewayapp: Store restore journal contains an invalid pending path")
	}
	return journal, nil
}

func isLowerHexDigest(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
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
	if err != nil {
		return err
	}
	return storeRestoreSyncDirectory(storeDir)
}
