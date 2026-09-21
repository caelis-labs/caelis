package gatewayapp

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// These names describe the embedded Memory schema-2 appliance layout. The
// Memory package intentionally keeps its storage owner internals private; the
// Caelis diagnostic contract records the layout without opening those files.
const (
	embeddedMemoryDatabaseFilename             = "memory.db"
	embeddedMemoryOwnerLockFilename            = "memoryd.lock"
	embeddedMemoryManagementCredentialFilename = "management.token"
	embeddedMemoryStewardCredentialFilename    = "steward-worker.token"
	embeddedMemoryCurrentSchemaVersion         = 2
)

// StoreDiagnostics is a read-only snapshot of the local durable Store
// layout. It deliberately reports file state and bounded SQLite header
// information only; it never opens a database, runs a migration, or reads
// credentials and receipt content. It may perform a non-blocking OS lock
// probe and releases that probe immediately.
//
// A present file is not evidence that the corresponding service is healthy.
// Callers must use the explicit state fields and treat unknown or unavailable
// values as requiring operator inspection.
type StoreDiagnostics struct {
	ReadOnly             bool                     `json:"read_only"`
	StoreDir             string                   `json:"store_dir,omitempty"`
	ConfigPath           string                   `json:"config_path,omitempty"`
	ConfigState          string                   `json:"config_state,omitempty"`
	ControlDatabasePath  string                   `json:"control_database_path,omitempty"`
	ControlDatabaseState string                   `json:"control_database_state,omitempty"`
	SessionsPath         string                   `json:"sessions_path,omitempty"`
	SessionsState        string                   `json:"sessions_state,omitempty"`
	Memory               MemoryStorageDiagnostics `json:"memory"`
	RecoveryAdvice       []string                 `json:"recovery_advice,omitempty"`
	Warnings             []string                 `json:"warnings,omitempty"`
}

// MemoryStorageDiagnostics describes the embedded Memory files without
// asserting that a database is usable. OwnerLockState is a best-effort
// non-blocking probe on platforms that support it; it is never inferred from
// the lock file's existence alone.
type MemoryStorageDiagnostics struct {
	DataDir                   string `json:"data_dir,omitempty"`
	DataDirState              string `json:"data_dir_state,omitempty"`
	DatabasePath              string `json:"database_path,omitempty"`
	DatabaseState             string `json:"database_state,omitempty"`
	DatabaseFormat            string `json:"database_format,omitempty"`
	ExpectedSchemaVersion     int    `json:"expected_schema_version,omitempty"`
	SchemaState               string `json:"schema_state,omitempty"`
	OwnerLockPath             string `json:"owner_lock_path,omitempty"`
	OwnerLockFileState        string `json:"owner_lock_file_state,omitempty"`
	OwnerLockState            string `json:"owner_lock_state,omitempty"`
	WALPath                   string `json:"wal_path,omitempty"`
	WALState                  string `json:"wal_state,omitempty"`
	SHMPath                   string `json:"shm_path,omitempty"`
	SHMState                  string `json:"shm_state,omitempty"`
	RollbackPath              string `json:"rollback_path,omitempty"`
	RollbackState             string `json:"rollback_state,omitempty"`
	ManagementCredentialPath  string `json:"management_credential_path,omitempty"`
	ManagementCredentialState string `json:"management_credential_state,omitempty"`
	StewardCredentialPath     string `json:"steward_credential_path,omitempty"`
	StewardCredentialState    string `json:"steward_credential_state,omitempty"`
}

const (
	diagnosticStateMissing    = "missing"
	diagnosticStatePresent    = "present"
	diagnosticStateInvalid    = "invalid"
	diagnosticStateUnreadable = "unreadable"
	diagnosticStateUnknown    = "unknown"

	diagnosticLockAbsent  = "absent"
	diagnosticLockHeld    = "held"
	diagnosticLockFree    = "free"
	diagnosticLockUnknown = "unknown"
)

// InspectStoreDiagnostics returns a bounded, side-effect-free view of one
// local Store. It is safe to call after Host startup failed, including when
// another process may own embedded Memory. The function does not create the
// Store root or any missing path, and its lock probe never waits for or
// bypasses an existing owner.
func InspectStoreDiagnostics(storeDir string) (StoreDiagnostics, error) {
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return StoreDiagnostics{}, errors.New("gatewayapp: Store directory is required for diagnostics")
	}
	absolute, err := filepath.Abs(storeDir)
	if err != nil {
		return StoreDiagnostics{}, fmt.Errorf("gatewayapp: resolve Store directory for diagnostics: %w", err)
	}
	storeDir = filepath.Clean(absolute)
	result := StoreDiagnostics{
		ReadOnly:            true,
		StoreDir:            storeDir,
		ConfigPath:          filepath.Join(storeDir, "config.json"),
		ControlDatabasePath: controlStoreDatabasePath(storeDir),
		SessionsPath:        filepath.Join(storeDir, "sessions"),
		Memory: MemoryStorageDiagnostics{
			DataDir:                  filepath.Join(storeDir, "memory", "appliance"),
			DatabasePath:             filepath.Join(storeDir, "memory", "appliance", embeddedMemoryDatabaseFilename),
			ExpectedSchemaVersion:    embeddedMemoryCurrentSchemaVersion,
			OwnerLockPath:            filepath.Join(storeDir, "memory", "appliance", embeddedMemoryOwnerLockFilename),
			WALPath:                  filepath.Join(storeDir, "memory", "appliance", embeddedMemoryDatabaseFilename+"-wal"),
			SHMPath:                  filepath.Join(storeDir, "memory", "appliance", embeddedMemoryDatabaseFilename+"-shm"),
			RollbackPath:             filepath.Join(storeDir, "memory", "appliance", "memory.db.rollback"),
			ManagementCredentialPath: filepath.Join(storeDir, "memory", "appliance", embeddedMemoryManagementCredentialFilename),
			StewardCredentialPath:    filepath.Join(storeDir, "memory", "appliance", embeddedMemoryStewardCredentialFilename),
		},
	}

	result.ConfigState = inspectPath(result.ConfigPath, false)
	result.ControlDatabaseState = inspectPath(result.ControlDatabasePath, false)
	result.SessionsState = inspectPath(result.SessionsPath, true)
	result.Memory.DataDirState = inspectPath(result.Memory.DataDir, true)
	result.Memory.DatabaseState = inspectPath(result.Memory.DatabasePath, false)
	result.Memory.OwnerLockFileState = inspectPath(result.Memory.OwnerLockPath, false)
	result.Memory.WALState = inspectPath(result.Memory.WALPath, false)
	result.Memory.SHMState = inspectPath(result.Memory.SHMPath, false)
	result.Memory.RollbackState = inspectPath(result.Memory.RollbackPath, false)
	result.Memory.ManagementCredentialState = inspectPath(result.Memory.ManagementCredentialPath, false)
	result.Memory.StewardCredentialState = inspectPath(result.Memory.StewardCredentialPath, false)

	switch result.Memory.OwnerLockFileState {
	case diagnosticStatePresent:
		result.Memory.OwnerLockState = probeOwnerLock(result.Memory.OwnerLockPath)
	case diagnosticStateMissing:
		result.Memory.OwnerLockState = diagnosticLockAbsent
	default:
		result.Memory.OwnerLockState = diagnosticLockUnknown
	}

	if result.Memory.DatabaseState == diagnosticStatePresent {
		result.Memory.DatabaseFormat, result.Memory.SchemaState = inspectSQLiteHeader(result.Memory.DatabasePath)
	} else {
		result.Memory.DatabaseFormat = diagnosticStateUnknown
		result.Memory.SchemaState = diagnosticStateUnknown
	}
	addRecoveryAdvice(&result)
	return result, nil
}

func inspectPath(path string, directory bool) string {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return diagnosticStateMissing
	}
	if err != nil {
		return diagnosticStateUnreadable
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return diagnosticStateInvalid
	}
	if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return diagnosticStateInvalid
	}
	return diagnosticStatePresent
}

func inspectSQLiteHeader(path string) (string, string) {
	file, err := os.Open(path)
	if err != nil {
		return diagnosticStateUnknown, diagnosticStateUnreadable
	}
	defer file.Close()
	var header [16]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return diagnosticStateUnknown, diagnosticStateUnreadable
	}
	if string(header[:]) != "SQLite format 3\x00" {
		return diagnosticStateInvalid, diagnosticStateInvalid
	}
	// The Memory schema is represented by its metadata table rather than the
	// SQLite user_version header. Reading that table would bypass the appliance
	// owner boundary, so report the safe format fact and leave the schema
	// observation explicitly unknown until the owner can inspect it.
	return "sqlite3", diagnosticStateUnknown
}

func addRecoveryAdvice(result *StoreDiagnostics) {
	if result == nil {
		return
	}
	var advice []string
	if result.Memory.OwnerLockState == diagnosticLockHeld {
		advice = append(advice, "stop the owning Caelis Host before inspecting or restoring embedded Memory")
	} else if result.Memory.OwnerLockState == diagnosticLockUnknown || result.Memory.OwnerLockFileState == diagnosticStatePresent {
		advice = append(advice, "do not remove memoryd.lock; verify the owning process before recovery")
	}
	if result.Memory.DatabaseState == diagnosticStateMissing || result.Memory.DatabaseState == diagnosticStateInvalid || result.Memory.DatabaseState == diagnosticStateUnreadable {
		advice = append(advice, "restore embedded Memory from a verified backup or perform an explicit rebuild; startup will not overwrite it")
	}
	if result.Memory.DatabaseFormat == diagnosticStateInvalid {
		advice = append(advice, "the embedded Memory database header is not SQLite; preserve the files and inspect the private service log")
	}
	if result.Memory.SchemaState == diagnosticStateUnknown && result.Memory.DatabaseState == diagnosticStatePresent {
		advice = append(advice, "schema version is unknown without an owner-held Memory inspection; do not run an ad hoc migration")
	}
	if result.Memory.RollbackState == diagnosticStatePresent {
		advice = append(advice, "a Memory rollback image is present; keep it until the restored generation is explicitly verified and committed")
	}
	if result.ControlDatabaseState != diagnosticStatePresent {
		advice = append(advice, "inspect the Control store before recovery; its absence or unreadable state is not repaired by diagnostics")
	}
	if result.SessionsState != diagnosticStatePresent {
		advice = append(advice, "inspect the canonical Session store before recovery; diagnostics do not rebuild or replace Session files")
	}
	result.RecoveryAdvice = uniqueSortedStrings(advice)
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
