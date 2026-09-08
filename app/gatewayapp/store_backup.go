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
	// StoreBackupFormat is the stable archive format identifier.
	StoreBackupFormat = "caelis.store-backup.v1"
	// StoreBackupSourceFloor identifies the pre-backup Store layout accepted by
	// this archive. It is a layout capability, not a Caelis reader version.
	StoreBackupSourceFloor = "caelis.store-layout.v0"
	// StoreBackupLastWriter identifies the last writer of the source layout.
	// It is recorded separately from the reader capability because the first
	// release carrying this reader contract has not been assigned yet.
	StoreBackupLastWriter = "v0.51.2"
	// StoreBackupReaderCapability is the minimum reader capability required to
	// consume this archive. Future capabilities are rejected before restore.
	StoreBackupReaderCapability = "caelis.store-backup.v1"
	storeBackupManifestName     = "manifest.json"
	storeBackupComponentRoot    = "components/"
)

var (
	ErrStoreBackupFormat  = errors.New("gatewayapp: unsupported Store backup format")
	ErrStoreBackupMemory  = errors.New("gatewayapp: embedded Memory owner backup is unavailable")
	ErrStoreRestoreMemory = errors.New("gatewayapp: embedded Memory owner restore is unavailable")
)

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
// spool cache.
func WriteStoreBackup(ctx context.Context, options StoreBackupOptions, output io.Writer) (StoreBackupManifest, error) {
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
	var archiveBytes bytes.Buffer
	archive := zip.NewWriter(&archiveBytes)
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
		components, err := addStoreBackupSource(ctx, archive, source)
		if err != nil {
			return fail(fmt.Errorf("backup %s: %w", source.name, err))
		}
		manifest.Components = append(manifest.Components, components...)
	}
	memoryEntry, err := archive.Create(storeBackupComponentRoot + "memory/memory.db")
	if err != nil {
		return fail(fmt.Errorf("create Memory backup entry: %w", err))
	}
	var memory bytes.Buffer
	if err := options.MemoryBackup(ctx, &memory); err != nil {
		return fail(fmt.Errorf("memory owner backup: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	memoryBytes := memory.Bytes()
	if _, err := io.Copy(memoryEntry, bytes.NewReader(memoryBytes)); err != nil {
		return fail(fmt.Errorf("write Memory backup entry: %w", err))
	}
	memoryHash := sha256.Sum256(memoryBytes)
	manifest.Components = append(manifest.Components, StoreBackupComponent{
		Name: "memory", ArchivePath: storeBackupComponentRoot + "memory/memory.db",
		SourcePath: "Memory owner snapshot", Kind: "memory-owner-snapshot",
		Consistency: "memory-owner-consistent-snapshot", Size: int64(len(memoryBytes)),
		SHA256: hex.EncodeToString(memoryHash[:]), Required: true,
	})
	sort.Slice(manifest.Components, func(i, j int) bool {
		return manifest.Components[i].ArchivePath < manifest.Components[j].ArchivePath
	})
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fail(fmt.Errorf("encode Store backup manifest: %w", err))
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
	if _, err := io.Copy(output, bytes.NewReader(archiveBytes.Bytes())); err != nil {
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

func addStoreBackupSource(ctx context.Context, archive *zip.Writer, source storeBackupSource) ([]StoreBackupComponent, error) {
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
		component, err := addStoreBackupFile(ctx, archive, source.name, source.path, source.kind, source.required)
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
		component, err := addStoreBackupFile(ctx, archive, source.name+"/"+relative, path, source.kind, source.required)
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

func addStoreBackupFile(ctx context.Context, archive *zip.Writer, name, path, kind string, required bool) (StoreBackupComponent, error) {
	if err := ctx.Err(); err != nil {
		return StoreBackupComponent{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return StoreBackupComponent{}, err
	}
	if kind == "config" {
		if err := validateStoreBackupConfig(raw); err != nil {
			return StoreBackupComponent{}, err
		}
	}
	sum := sha256.Sum256(raw)
	archivePath := storeBackupComponentRoot + strings.ReplaceAll(name, string(filepath.Separator), "/")
	entry, err := archive.Create(archivePath)
	if err != nil {
		return StoreBackupComponent{}, err
	}
	if _, err := entry.Write(raw); err != nil {
		return StoreBackupComponent{}, err
	}
	return StoreBackupComponent{
		Name: name, ArchivePath: archivePath, SourcePath: name,
		Kind: kind, Consistency: "host-quiesced-file", Size: int64(len(raw)),
		SHA256: hex.EncodeToString(sum[:]), Required: required,
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
func ReadStoreBackupManifest(input io.ReaderAt, size int64) (StoreBackupManifest, error) {
	if input == nil || size <= 0 {
		return StoreBackupManifest{}, errors.New("gatewayapp: Store backup archive is required")
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
	reader, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, 512<<20))
}
