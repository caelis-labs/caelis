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
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/internal/version"
)

const (
	// StoreBackupFormat identifies the Host-private recovery archive encoding.
	StoreBackupFormat = "caelis.store-backup.v1"
	// StoreBackupSourceFloor identifies the pre-backup Store layout accepted by
	// this archive. It is a layout capability, not a Caelis reader version.
	StoreBackupSourceFloor = "caelis.store-layout.v0"
	// StoreBackupLastWriter identifies the last writer of the source layout.
	// It is recorded separately from the reader capability because the first
	// release carrying this reader contract has not been assigned yet.
	StoreBackupLastWriter = "v0.51.2"
	// StoreBackupReaderCapability is the internal reader capability required to
	// consume a recovery snapshot. Future capabilities are rejected before restore.
	StoreBackupReaderCapability = "caelis.store-backup.v1"
	storeBackupManifestName     = "manifest.json"
	storeBackupComponentRoot    = "components/"
	storeBackupArchiveMaxBytes  = int64(1 << 30)
	storeBackupEntryMaxBytes    = int64(512 << 20)
)

var (
	// ErrStoreBackupTooLarge means an internal recovery snapshot exceeds the
	// shared writer/reader size budget; no truncated snapshot is accepted.
	ErrStoreBackupTooLarge = errors.New("gatewayapp: Store recovery snapshot exceeds size limit")
	ErrStoreBackupFormat   = errors.New("gatewayapp: unsupported Store backup format")
	ErrStoreBackupMemory   = errors.New("gatewayapp: embedded Memory owner backup is unavailable")
	ErrStoreRestoreMemory  = errors.New("gatewayapp: embedded Memory owner restore is unavailable")
)

// storeBackupLimits applies the same byte budgets to every capture path.
type storeBackupLimits struct {
	archive int64
	entry   int64
}

type storeBackupLimitWriter struct {
	writer    io.Writer
	remaining int64
	exceeded  bool
}

func (w *storeBackupLimitWriter) Write(data []byte) (int, error) {
	if w.exceeded || int64(len(data)) > w.remaining {
		w.exceeded = true
		return 0, ErrStoreBackupTooLarge
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}

func readStoreBackupLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrStoreBackupTooLarge
	}
	return data, nil
}

func copyStoreBackupLimited(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	if src == nil {
		return 0, errors.New("gatewayapp: Store backup archive is required")
	}
	if dst == nil {
		return 0, errors.New("gatewayapp: Store backup output is required")
	}
	if limit < 0 {
		return 0, ErrStoreBackupTooLarge
	}
	written, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return written, err
	}
	if written > limit {
		return written, ErrStoreBackupTooLarge
	}
	return written, nil
}

func materializeStoreBackupArchive(src io.Reader, limit int64) (*os.File, int64, error) {
	if src == nil {
		return nil, 0, errors.New("gatewayapp: Store restore archive is required")
	}
	file, err := os.CreateTemp("", ".caelis-recovery-*.zip")
	if err != nil {
		return nil, 0, err
	}
	cleanup := func() {
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
	}
	size, err := copyStoreBackupLimited(file, src, limit)
	if err != nil {
		cleanup()
		return nil, 0, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, 0, err
	}
	return file, size, nil
}

// MemoryBackup writes one consistent Memory-owned snapshot into output. The
// callback is deliberately narrow so Caelis cannot inspect Memory storage.
type MemoryBackup func(context.Context, io.Writer) error

// MemoryRestore installs one authenticated Memory-owned snapshot. The callback
// owns credentials, schema, generation and rollback semantics.
type MemoryRestore func(context.Context, io.Reader) error

// StoreBackupOptions configures one cross-component archive.
type StoreBackupOptions struct {
	StoreDir     string
	MemoryBackup MemoryBackup
	Now          func() time.Time
}

// StoreBackupManifest is the archive's secret-free consistency contract.
// Components are independently durable snapshots under one Host quiesce
// boundary; the manifest never claims a cross-database transaction.
type StoreBackupManifest struct {
	Format                  string                 `json:"format"`
	SourceFloor             string                 `json:"source_floor"`
	LastWriter              string                 `json:"last_writer"`
	MinimumReaderCapability string                 `json:"minimum_reader_capability"`
	CreatedAt               time.Time              `json:"created_at"`
	CaelisVersion           string                 `json:"caelis_version,omitempty"`
	GoOS                    string                 `json:"goos"`
	GoArch                  string                 `json:"goarch"`
	QuiesceBoundary         string                 `json:"quiesce_boundary"`
	Atomicity               string                 `json:"atomicity"`
	SecretsExcluded         []string               `json:"secrets_excluded"`
	Components              []StoreBackupComponent `json:"components"`
}

// StoreBackupComponent identifies one archive member and its integrity
// evidence. Relative paths are fixed product paths.
type StoreBackupComponent struct {
	Name        string `json:"name"`
	ArchivePath string `json:"archive_path"`
	SourcePath  string `json:"source_path"`
	Kind        string `json:"kind"`
	Consistency string `json:"consistency"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Required    bool   `json:"required"`
}

// StoreRestoreOptions describes an offline restore. Restore refuses to omit
// the Memory component.
type StoreRestoreOptions struct {
	StoreDir      string
	Backup        io.Reader
	MemoryRestore MemoryRestore
	// MemoryCommit accepts the owner generation only after all independent
	// Caelis components have been installed. It is required for a durable
	// restore; leaving the owner pending would make the next Host unreadable.
	MemoryCommit MemoryCommit
	// MemoryRollback is used when the owner installed a pending generation but
	// a later cross-component step failed. The owner keeps this callback
	// private so Caelis never touches its database or rollback image.
	MemoryRollback MemoryRollback
}

// MemoryCommit accepts a pending Memory generation after the other Store
// authorities have completed their restore.
type MemoryCommit func(context.Context) error

// MemoryRollback restores the Memory generation that preceded a pending
// restore. It is invoked only while Host admission is closed.
type MemoryRollback func(context.Context) error

// StoreRestoreReport records the component-level restore result.
type StoreRestoreReport struct {
	Format              string   `json:"format"`
	RestoredComponents  []string `json:"restored_components"`
	QuiesceBoundary     string   `json:"quiesce_boundary"`
	RolledBackOnFailure bool     `json:"rolled_back_on_failure"`
	RecoveredPrevious   bool     `json:"recovered_previous_restore"`
}

// WriteStoreBackup writes one complete archive. It excludes credentials,
// runtime tokens, provider OAuth/API-key stores, lock files, logs and lossy
// spool cache. This Host-private primitive requires an already quiesced Store.
// Internal callers share a 1GiB compressed ZIP budget and a 512MiB uncompressed
// entry budget, including the Memory owner snapshot and encoded manifest.
// Exceeding either limit fails with ErrStoreBackupTooLarge and publishes no
// archive bytes. Callers own destination cleanup after publication errors and
// after verified recovery completion.
func WriteStoreBackup(ctx context.Context, options StoreBackupOptions, output io.Writer) (StoreBackupManifest, error) {
	return writeStoreBackupLimited(ctx, options, output, storeBackupLimits{
		archive: storeBackupArchiveMaxBytes, entry: storeBackupEntryMaxBytes,
	})
}

func writeStoreBackupLimited(ctx context.Context, options StoreBackupOptions, output io.Writer, limits storeBackupLimits) (StoreBackupManifest, error) {
	if output == nil {
		return StoreBackupManifest{}, errors.New("gatewayapp: Store backup output is required")
	}
	storeDir, err := normalizeStoreBackupDir(options.StoreDir)
	if err != nil {
		return StoreBackupManifest{}, err
	}
	if options.MemoryBackup == nil {
		return StoreBackupManifest{}, ErrStoreBackupMemory
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	manifest := StoreBackupManifest{
		Format:                  StoreBackupFormat,
		SourceFloor:             StoreBackupSourceFloor,
		LastWriter:              StoreBackupLastWriter,
		MinimumReaderCapability: StoreBackupReaderCapability,
		CreatedAt:               now().UTC(),
		CaelisVersion:           version.String(),
		GoOS:                    runtime.GOOS,
		GoArch:                  runtime.GOARCH,
		QuiesceBoundary:         "host-admission-closed-and-all-producers-drained",
		Atomicity:               "independent-component-snapshots-under-one-quiesce-boundary",
		SecretsExcluded: []string{
			"providers/**", "memory/**/management.token", "memory/**/steward-worker.token",
			"runtime/**", "control/auth.token", "control/acp-ingress.token",
			"control/cursor.key", "control/spool/**", "*.lock", "*.log",
		},
	}
	// Build the archive completely before publishing it. A failed owner
	// snapshot must never leave a caller's destination looking like a usable
	// backup prefix.
	archiveFile, err := os.CreateTemp("", ".caelis-recovery-*.zip")
	if err != nil {
		return StoreBackupManifest{}, err
	}
	defer func() {
		_ = archiveFile.Close()
		_ = os.Remove(archiveFile.Name())
	}()
	archiveLimit := &storeBackupLimitWriter{writer: archiveFile, remaining: limits.archive}
	archive := zip.NewWriter(archiveLimit)
	fail := func(cause error) (StoreBackupManifest, error) {
		_ = archive.Close()
		return StoreBackupManifest{}, cause
	}
	sources := []storeBackupSource{
		{name: "config", path: filepath.Join(storeDir, "config.json"), kind: "config", required: true},
		{name: "control", path: controlStoreDatabasePath(storeDir), kind: "sqlite", required: true},
		{name: "sessions", path: filepath.Join(storeDir, "sessions"), kind: "session-canonical", required: true},
	}
	for _, source := range sources {
		components, err := addStoreBackupSource(ctx, archive, source, limits.entry)
		if err != nil {
			return fail(fmt.Errorf("backup %s: %w", source.name, err))
		}
		manifest.Components = append(manifest.Components, components...)
	}
	memoryEntry, err := archive.Create(storeBackupComponentRoot + "memory/memory.db")
	if err != nil {
		return fail(fmt.Errorf("create Memory backup entry: %w", err))
	}
	memoryHash := sha256.New()
	memoryLimit := &storeBackupLimitWriter{
		writer: io.MultiWriter(memoryEntry, memoryHash), remaining: limits.entry,
	}
	if err := options.MemoryBackup(ctx, memoryLimit); err != nil {
		return fail(fmt.Errorf("memory owner backup: %w", err))
	}
	if memoryLimit.exceeded {
		return fail(ErrStoreBackupTooLarge)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	manifest.Components = append(manifest.Components, StoreBackupComponent{
		Name: "memory", ArchivePath: storeBackupComponentRoot + "memory/memory.db",
		SourcePath: "Memory owner snapshot", Kind: "memory-owner-snapshot",
		Consistency: "memory-owner-consistent-snapshot", Size: limits.entry - memoryLimit.remaining,
		SHA256: hex.EncodeToString(memoryHash.Sum(nil)), Required: true,
	})
	sort.Slice(manifest.Components, func(i, j int) bool {
		return manifest.Components[i].ArchivePath < manifest.Components[j].ArchivePath
	})
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fail(fmt.Errorf("encode Store backup manifest: %w", err))
	}
	if int64(len(raw))+1 > limits.entry {
		return fail(ErrStoreBackupTooLarge)
	}
	entry, err := archive.Create(storeBackupManifestName)
	if err != nil {
		return fail(fmt.Errorf("create Store backup manifest: %w", err))
	}
	if _, err := entry.Write(append(raw, '\n')); err != nil {
		return fail(fmt.Errorf("write Store backup manifest: %w", err))
	}
	if err := archive.Close(); err != nil {
		return StoreBackupManifest{}, fmt.Errorf("close Store backup archive: %w", err)
	}
	if archiveLimit.exceeded {
		return StoreBackupManifest{}, ErrStoreBackupTooLarge
	}
	if _, err := archiveFile.Seek(0, io.SeekStart); err != nil {
		return StoreBackupManifest{}, err
	}
	if _, err := io.Copy(output, archiveFile); err != nil {
		return StoreBackupManifest{}, fmt.Errorf("publish Store backup archive: %w", err)
	}
	return manifest, nil
}

type storeBackupSource struct {
	name     string
	path     string
	kind     string
	required bool
}

func addStoreBackupSource(ctx context.Context, archive *zip.Writer, source storeBackupSource, entryLimit int64) ([]StoreBackupComponent, error) {
	info, err := os.Lstat(source.path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrStoreBackupFormat
	}
	if source.kind != "session-canonical" && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("component %s is not a regular file", source.name)
	}
	if source.kind == "session-canonical" && !info.IsDir() {
		return nil, fmt.Errorf("Session store is not a directory")
	}
	if info.Mode().IsRegular() {
		component, err := addStoreBackupFile(ctx, archive, source.name, source.path, source.kind, source.required, entryLimit)
		if err != nil {
			return nil, err
		}
		return []StoreBackupComponent{component}, nil
	}
	var components []StoreBackupComponent
	err = filepath.WalkDir(source.path, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return ErrStoreBackupFormat
		}
		relative, err := filepath.Rel(source.path, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if source.kind == "session-canonical" && isSessionIndexSQLiteSidecar(relative) {
			return fmt.Errorf("Session index SQLite sidecar %q prevents a consistent backup; recover the Session owner first", relative)
		}
		if (strings.HasPrefix(relative, ".") && relative != ".sessions.index.sqlite") || strings.HasSuffix(relative, ".lock") || strings.HasSuffix(relative, ".tmp") {
			return nil
		}
		component, err := addStoreBackupFile(ctx, archive, source.name+"/"+relative, path, source.kind, source.required, entryLimit)
		if err != nil {
			return err
		}
		components = append(components, component)
		return nil
	})
	if err == nil && len(components) == 0 && source.kind == "session-canonical" {
		// An empty canonical Session directory is still a real required
		// component. A directory entry lets restore replace a non-empty target
		// with the exact empty source state.
		const archivePath = storeBackupComponentRoot + "sessions/"
		if _, err := archive.Create(archivePath); err != nil {
			return nil, err
		}
		components = append(components, StoreBackupComponent{
			Name: "sessions", ArchivePath: archivePath, SourcePath: "sessions",
			Kind: source.kind, Consistency: "host-quiesced-directory", Size: 0,
			SHA256: sha256Bytes(nil), Required: source.required,
		})
	}
	return components, err
}

func isSessionIndexSQLiteSidecar(relative string) bool {
	return strings.HasPrefix(filepath.ToSlash(strings.TrimSpace(relative)), ".sessions.index.sqlite-")
}

func addStoreBackupFile(ctx context.Context, archive *zip.Writer, name, path, kind string, required bool, entryLimit int64) (StoreBackupComponent, error) {
	if err := ctx.Err(); err != nil {
		return StoreBackupComponent{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return StoreBackupComponent{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return StoreBackupComponent{}, err
	}
	if info.Size() > entryLimit {
		return StoreBackupComponent{}, ErrStoreBackupTooLarge
	}
	var source io.Reader = file
	if kind == "config" {
		raw, err := readStoreBackupLimited(file, entryLimit)
		if err != nil {
			return StoreBackupComponent{}, err
		}
		if err := validateStoreBackupConfig(raw); err != nil {
			return StoreBackupComponent{}, err
		}
		source = bytes.NewReader(raw)
	}
	sum := sha256.New()
	archivePath := storeBackupComponentRoot + strings.ReplaceAll(name, string(filepath.Separator), "/")
	entry, err := archive.Create(archivePath)
	if err != nil {
		return StoreBackupComponent{}, err
	}
	limited := &storeBackupLimitWriter{writer: io.MultiWriter(entry, sum), remaining: entryLimit}
	if _, err := io.Copy(limited, source); err != nil {
		return StoreBackupComponent{}, err
	}
	if limited.exceeded {
		return StoreBackupComponent{}, ErrStoreBackupTooLarge
	}
	return StoreBackupComponent{
		Name: name, ArchivePath: archivePath, SourcePath: name,
		Kind: kind, Consistency: "host-quiesced-file", Size: entryLimit - limited.remaining,
		SHA256: hex.EncodeToString(sum.Sum(nil)), Required: required,
	}, nil
}

func validateStoreBackupConfig(raw []byte) error {
	var document configstore.AppConfig
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("gatewayapp: decode Store AppConfig: %w", err)
	}
	if document.SchemaVersion != configstore.SchemaVersionV2 {
		return fmt.Errorf("gatewayapp: unsupported Store AppConfig schema version %d", document.SchemaVersion)
	}
	if err := configstore.Validate(configstore.Normalize(document)); err != nil {
		return fmt.Errorf("gatewayapp: validate Store AppConfig: %w", err)
	}
	return nil
}

// ReadStoreBackupManifest validates only the archive envelope and manifest.
// Callers must pass the complete archive size; a ZIP larger than 1GiB is
// rejected with ErrStoreBackupTooLarge before entries are opened. Manifest
// and component payloads are capped at 512MiB uncompressed.
func ReadStoreBackupManifest(input io.ReaderAt, size int64) (StoreBackupManifest, error) {
	if input == nil || size <= 0 {
		return StoreBackupManifest{}, errors.New("gatewayapp: Store backup archive is required")
	}
	if size > storeBackupArchiveMaxBytes {
		return StoreBackupManifest{}, ErrStoreBackupTooLarge
	}
	reader, err := zip.NewReader(input, size)
	if err != nil {
		return StoreBackupManifest{}, fmt.Errorf("read Store backup archive: %w", err)
	}
	entry := findZipEntry(reader, storeBackupManifestName)
	if entry == nil {
		return StoreBackupManifest{}, errors.New("gatewayapp: Store backup manifest is missing")
	}
	raw, err := readZipEntry(entry)
	if err != nil {
		return StoreBackupManifest{}, err
	}
	var manifest StoreBackupManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return StoreBackupManifest{}, fmt.Errorf("decode Store backup manifest: %w", err)
	}
	if err := validateStoreBackupManifest(manifest); err != nil {
		return StoreBackupManifest{}, err
	}
	return manifest, nil
}

func validateStoreBackupManifest(manifest StoreBackupManifest) error {
	if manifest.Format != StoreBackupFormat ||
		manifest.SourceFloor != StoreBackupSourceFloor ||
		manifest.LastWriter != StoreBackupLastWriter ||
		manifest.MinimumReaderCapability != StoreBackupReaderCapability {
		return ErrStoreBackupFormat
	}
	if manifest.Atomicity != "independent-component-snapshots-under-one-quiesce-boundary" ||
		manifest.QuiesceBoundary == "" {
		return fmt.Errorf("%w: invalid consistency contract", ErrStoreBackupFormat)
	}
	if len(manifest.Components) == 0 {
		return fmt.Errorf("%w: no components", ErrStoreBackupFormat)
	}
	seen := map[string]struct{}{}
	kinds := map[string]int{}
	for _, component := range manifest.Components {
		if strings.TrimSpace(component.ArchivePath) == "" ||
			strings.Contains(component.ArchivePath, "..") ||
			!strings.HasPrefix(component.ArchivePath, storeBackupComponentRoot) {
			return fmt.Errorf("%w: invalid component path", ErrStoreBackupFormat)
		}
		if _, ok := seen[component.ArchivePath]; ok {
			return fmt.Errorf("%w: duplicate component path", ErrStoreBackupFormat)
		}
		seen[component.ArchivePath] = struct{}{}
		if len(component.SHA256) != sha256.Size*2 || component.Size < 0 {
			return fmt.Errorf("%w: invalid component digest", ErrStoreBackupFormat)
		}
		switch component.Kind {
		case "config", "sqlite", "session-canonical", "memory-owner-snapshot":
		default:
			return fmt.Errorf("%w: unsupported component kind %q", ErrStoreBackupFormat, component.Kind)
		}
		if !validStoreBackupArchivePath(component) {
			return fmt.Errorf("%w: archive path %q does not match component kind %q", ErrStoreBackupFormat, component.ArchivePath, component.Kind)
		}
		kinds[component.Kind]++
	}
	for _, required := range []string{"config", "sqlite", "memory-owner-snapshot"} {
		if kinds[required] != 1 {
			return fmt.Errorf("%w: required component %s must occur exactly once", ErrStoreBackupFormat, required)
		}
	}
	if kinds["session-canonical"] == 0 {
		return fmt.Errorf("%w: required component session-canonical is missing", ErrStoreBackupFormat)
	}
	for _, component := range manifest.Components {
		if (component.Kind == "config" || component.Kind == "sqlite" || component.Kind == "session-canonical" || component.Kind == "memory-owner-snapshot") && !component.Required {
			return fmt.Errorf("%w: required component %s is not marked required", ErrStoreBackupFormat, component.Name)
		}
	}
	return nil
}

func validStoreBackupArchivePath(component StoreBackupComponent) bool {
	path := component.ArchivePath
	switch component.Kind {
	case "config":
		return path == storeBackupComponentRoot+"config"
	case "sqlite":
		return path == storeBackupComponentRoot+"control"
	case "memory-owner-snapshot":
		return path == storeBackupComponentRoot+"memory/memory.db"
	case "session-canonical":
		return path == storeBackupComponentRoot+"sessions/" || strings.HasPrefix(path, storeBackupComponentRoot+"sessions/")
	default:
		return false
	}
}

func normalizeStoreBackupDir(storeDir string) (string, error) {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return "", errors.New("gatewayapp: Store directory is required")
	}
	absolute, err := filepath.Abs(storeDir)
	if err != nil {
		return "", fmt.Errorf("gatewayapp: resolve Store directory: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func findZipEntry(reader *zip.Reader, name string) *zip.File {
	for _, entry := range reader.File {
		if entry.Name == name {
			return entry
		}
	}
	return nil
}

func readZipEntry(entry *zip.File) ([]byte, error) {
	if entry == nil {
		return nil, errors.New("gatewayapp: archive entry is missing")
	}
	if entry.UncompressedSize64 > uint64(storeBackupEntryMaxBytes) {
		return nil, ErrStoreBackupTooLarge
	}
	reader, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return readStoreBackupLimited(reader, storeBackupEntryMaxBytes)
}
