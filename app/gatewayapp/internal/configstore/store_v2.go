package configstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/control/memorybinding"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
)

// Load reads the current AppConfig. A pre-v2 wire document is converted once
// when at least one record can be mapped safely; otherwise its bytes remain
// untouched and an empty current document is returned.
func (s *Store) Load() (document AppConfig, returnErr error) {
	return s.LoadContext(context.Background())
}

// LoadContext reads one complete AppConfig snapshot. Current-schema documents
// use the atomic replacement boundary directly and never wait for the writer
// lock. Legacy documents still enter the write transaction because loading
// them may migrate credentials and replace config.json.
func (s *Store) LoadContext(ctx context.Context) (AppConfig, error) {
	if s == nil {
		return AppConfig{SchemaVersion: SchemaVersionV2}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return AppConfig{}, err
	}
	path, err := s.snapshotPath(ctx)
	if err != nil {
		return AppConfig{}, err
	}
	if strings.TrimSpace(path) == "" {
		return AppConfig{SchemaVersion: SchemaVersionV2}, nil
	}
	if _, err := os.Stat(filepath.Dir(path)); os.IsNotExist(err) {
		return AppConfig{SchemaVersion: SchemaVersionV2}, nil
	} else if err != nil {
		return AppConfig{}, err
	}
	data, err := readAppConfigSnapshot(ctx, path)
	if os.IsNotExist(err) {
		return AppConfig{SchemaVersion: SchemaVersionV2}, nil
	}
	if err != nil {
		return AppConfig{}, err
	}
	version, err := appConfigSchemaVersion(data)
	if err != nil {
		return AppConfig{}, err
	}
	switch version {
	case 0, 1:
		return s.loadLegacyContext(ctx, path)
	case SchemaVersionV2:
		return decodeCurrentAppConfig(data)
	default:
		return AppConfig{}, fmt.Errorf("gatewayapp: unsupported AppConfig schema version %d", version)
	}
}

func (s *Store) snapshotPath(ctx context.Context) (string, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	s.pathMu.RLock()
	defer s.pathMu.RUnlock()
	return s.path, nil
}

func (s *Store) loadLegacyContext(ctx context.Context, path string) (document AppConfig, returnErr error) {
	if err := s.gate.LockContext(ctx); err != nil {
		return AppConfig{}, err
	}
	currentPath, err := s.snapshotPath(ctx)
	if err != nil {
		s.gate.Unlock()
		return AppConfig{}, err
	}
	if currentPath != path {
		s.gate.Unlock()
		return s.LoadContext(ctx)
	}
	defer s.gate.Unlock()
	s.migrationMu.Lock()
	defer s.migrationMu.Unlock()

	lock, err := acquireFileLock(ctx, path+".lock")
	if err != nil {
		return AppConfig{}, fmt.Errorf("gatewayapp: lock legacy app config migration: %w", err)
	}
	committed := false
	defer func() {
		closeErr := lock.Close()
		if committed {
			closeErr = writeCommittedError(closeErr)
		}
		returnErr = errors.Join(returnErr, closeErr)
	}()
	if err := ctx.Err(); err != nil {
		return AppConfig{}, err
	}
	document, committed, returnErr = s.loadLegacyLocked(path)
	return document, returnErr
}

func (s *Store) loadLegacyLocked(path string) (document AppConfig, committed bool, returnErr error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AppConfig{}, false, err
	}
	version, err := appConfigSchemaVersion(data)
	if err != nil {
		return AppConfig{}, false, err
	}
	if version == SchemaVersionV2 {
		doc, decodeErr := decodeCurrentAppConfig(data)
		return doc, false, decodeErr
	}
	if version != 0 && version != 1 {
		return AppConfig{}, false, fmt.Errorf("gatewayapp: unsupported AppConfig schema version %d", version)
	}
	migrated, err := decodeLegacyAppConfig(data)
	if err != nil {
		return AppConfig{}, false, err
	}
	migrated.Report.FromSchema = version
	s.migration = cloneMigrationReport(migrated.Report)
	if !migrated.HasSafeContent {
		s.migration.SourcePreserved = true
		return migrated.Document, false, nil
	}
	backupPath, fromSchema, backedUp, backupErr := s.backupLegacyDestinationUnlocked(path)
	s.migration.FromSchema = fromSchema
	if backedUp {
		s.migration.BackupPath = backupPath
	}
	if backupErr != nil {
		s.migration.SourcePreserved = true
		return AppConfig{}, false, backupErr
	}
	credentialTxn, err := applyLegacyCredentialWrites(filepath.Dir(path), migrated.CredentialWrites)
	if err != nil {
		s.migration.SourcePreserved = true
		return AppConfig{}, false, err
	}
	migrated.Document.ConfigurationRevision = 1
	if err := s.saveUnlocked(path, migrated.Document, true); err != nil {
		persistErr := fmt.Errorf("gatewayapp: persist migrated app config: %w", err)
		if WriteCommitted(err) {
			return migrated.Document, true, persistErr
		}
		return AppConfig{}, false, errors.Join(persistErr, credentialTxn.rollback())
	}
	return migrated.Document, true, nil
}

func decodeCurrentAppConfig(data []byte) (AppConfig, error) {
	if err := validateCurrentMemoryWire(data); err != nil {
		return AppConfig{}, wrapInvalidMemoryConfiguration(err)
	}
	var doc AppConfig
	if err := json.Unmarshal(data, &doc); err != nil {
		return AppConfig{}, fmt.Errorf("gatewayapp: decode app config: %w", err)
	}
	if err := validateCurrentRecordIdentities(doc); err != nil {
		return AppConfig{}, err
	}
	doc = Normalize(doc)
	if err := Validate(doc); err != nil {
		return AppConfig{}, err
	}
	return doc, nil
}

func validateCurrentMemoryWire(data []byte) error {
	var top struct {
		Memory json.RawMessage `json:"memory"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return fmt.Errorf("gatewayapp: decode app config: %w", err)
	}
	if len(top.Memory) == 0 || string(top.Memory) == "null" {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(top.Memory))
	decoder.DisallowUnknownFields()
	var current memorybinding.Configuration
	if err := decoder.Decode(&current); err != nil {
		return fmt.Errorf("decode current Memory configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode current Memory configuration: trailing data")
	}
	return nil
}

type legacyCredentialPrevious struct {
	Ref     string
	Source  credentialstore.Source
	Existed bool
}

type legacyCredentialTransaction struct {
	Store    *credentialstore.Store
	Previous []legacyCredentialPrevious
}

func applyLegacyCredentialWrites(root string, writes []legacyCredentialWrite) (*legacyCredentialTransaction, error) {
	store, err := credentialstore.New(root)
	if err != nil {
		return nil, err
	}
	txn := &legacyCredentialTransaction{Store: store}
	for _, write := range writes {
		previous, lookupErr := store.LookupSource(context.Background(), write.Ref)
		switch {
		case lookupErr == nil:
			if previous != write.Source {
				return nil, errors.Join(
					fmt.Errorf("gatewayapp: migrate provider credential %q: existing source conflicts with legacy config", write.Ref),
					txn.rollback(),
				)
			}
			continue
		case errors.Is(lookupErr, os.ErrNotExist):
			txn.Previous = append(txn.Previous, legacyCredentialPrevious{Ref: write.Ref})
		default:
			return nil, errors.Join(lookupErr, txn.rollback())
		}
		if err := putLegacyCredentialSource(store, write.Ref, write.Source); err != nil {
			return nil, errors.Join(err, txn.rollback())
		}
	}
	return txn, nil
}

func (t *legacyCredentialTransaction) rollback() error {
	if t == nil || t.Store == nil {
		return nil
	}
	var errs []error
	for index := len(t.Previous) - 1; index >= 0; index-- {
		previous := t.Previous[index]
		if previous.Existed {
			errs = append(errs, putLegacyCredentialSource(t.Store, previous.Ref, previous.Source))
		} else {
			errs = append(errs, t.Store.Delete(context.Background(), previous.Ref))
		}
	}
	return errors.Join(errs...)
}

func putLegacyCredentialSource(store *credentialstore.Store, ref string, source credentialstore.Source) error {
	return store.Put(context.Background(), ref, source.APIKey)
}

// Save validates and atomically persists one current AppConfig document.
func (s *Store) Save(doc AppConfig) error {
	_, err := s.save(context.Background(), nil, doc)
	return err
}

func (s *Store) save(ctx context.Context, expected *uint64, doc AppConfig) (saved AppConfig, returnErr error) {
	if s == nil {
		return doc, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return AppConfig{}, err
	}
	if err := s.gate.LockContext(ctx); err != nil {
		return AppConfig{}, err
	}
	defer s.gate.Unlock()
	path, err := s.snapshotPath(ctx)
	if err != nil {
		return AppConfig{}, err
	}
	if strings.TrimSpace(path) == "" {
		return doc, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return AppConfig{}, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return AppConfig{}, err
	}
	lock, err := acquireFileLock(ctx, path+".lock")
	if err != nil {
		return AppConfig{}, fmt.Errorf("gatewayapp: lock app config: %w", err)
	}
	committed := false
	defer func() {
		closeErr := lock.Close()
		if committed {
			closeErr = writeCommittedError(closeErr)
		}
		returnErr = errors.Join(returnErr, closeErr)
	}()
	if err := ctx.Err(); err != nil {
		return AppConfig{}, err
	}
	current, err := s.currentRevisionUnlocked(path)
	if err != nil {
		return AppConfig{}, err
	}
	if expected != nil && current != *expected {
		return AppConfig{}, &ConfigurationRevisionConflict{Expected: *expected, Actual: current}
	}
	if current == math.MaxUint64 {
		return AppConfig{}, errors.New("gatewayapp: configuration revision exhausted")
	}
	doc.ConfigurationRevision = current + 1
	s.migrationMu.Lock()
	err = s.saveUnlocked(path, doc, false)
	s.migrationMu.Unlock()
	if err != nil && !WriteCommitted(err) {
		return AppConfig{}, err
	}
	committed = true
	return doc, err
}

func (s *Store) currentRevisionUnlocked(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("gatewayapp: read app config revision: %w", err)
	}
	version, err := appConfigSchemaVersion(data)
	if err != nil {
		return 0, err
	}
	switch version {
	case 0, 1:
		return 0, nil
	case SchemaVersionV2:
		var header struct {
			ConfigurationRevision uint64 `json:"configuration_revision"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			return 0, fmt.Errorf("gatewayapp: decode app config revision: %w", err)
		}
		return header.ConfigurationRevision, nil
	default:
		return 0, fmt.Errorf("gatewayapp: unsupported AppConfig schema version %d", version)
	}
}

func (s *Store) saveUnlocked(path string, doc AppConfig, migratingLegacy bool) error {
	// In-memory callers may construct a fresh document without spelling the
	// current version. Any explicit version remains subject to validation.
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = SchemaVersionV2
	}
	if doc.SchemaVersion != SchemaVersionV2 {
		return fmt.Errorf("gatewayapp: unsupported AppConfig schema version %d", doc.SchemaVersion)
	}
	if err := validateCurrentRecordIdentities(doc); err != nil {
		return err
	}
	doc = Normalize(doc)
	if err := Validate(doc); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("gatewayapp: encode app config: %w", err)
	}
	backupPath, fromSchema, backedUp, err := s.backupLegacyDestinationUnlocked(path)
	if backupPath != "" {
		s.migration.FromSchema = fromSchema
	}
	if backedUp {
		s.migration.BackupPath = backupPath
	}
	if err != nil {
		s.migration.SourcePreserved = backupPath != ""
		return err
	}
	err = AtomicWriteFile(path, data, 0o600, s.writeOps)
	if backedUp {
		if err == nil || WriteCommitted(err) {
			s.migration.Migrated = migratingLegacy
			s.migration.ExplicitReplacement = !migratingLegacy
			s.migration.SourcePreserved = false
		} else {
			s.migration.SourcePreserved = true
		}
	}
	return err
}

type legacyBackupWriteError struct {
	err error
}

func (e *legacyBackupWriteError) Error() string {
	if e == nil || e.err == nil {
		return "gatewayapp: backup legacy app config"
	}
	return "gatewayapp: backup legacy app config: " + e.err.Error()
}

// Is preserves sentinel inspection without exposing a backup's commit marker
// through errors.As. WriteCommitted must describe config.json, not its backup.
func (e *legacyBackupWriteError) Is(target error) bool {
	return e != nil && errors.Is(e.err, target)
}

func (s *Store) backupLegacyDestinationUnlocked(path string) (string, int, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, fmt.Errorf("gatewayapp: read app config before save: %w", err)
	}
	version, err := appConfigSchemaVersion(data)
	if err != nil {
		return "", 0, false, fmt.Errorf("gatewayapp: inspect app config before save: %w", err)
	}
	if version == SchemaVersionV2 {
		return "", 0, false, nil
	}
	if version != 0 && version != 1 {
		return "", version, false, fmt.Errorf("gatewayapp: unsupported AppConfig schema version %d", version)
	}
	backupPath := path + ".v1.bak"
	info, statErr := os.Lstat(backupPath)
	switch {
	case statErr == nil:
		if !info.Mode().IsRegular() {
			return backupPath, version, false, &legacyBackupWriteError{err: fmt.Errorf("existing backup is not a regular file")}
		}
		existing, readErr := os.ReadFile(backupPath)
		if readErr != nil {
			return backupPath, version, false, &legacyBackupWriteError{err: readErr}
		}
		if !bytes.Equal(existing, data) {
			return backupPath, version, false, &legacyBackupWriteError{err: fmt.Errorf("existing backup conflicts with the legacy source")}
		}
		if chmodErr := os.Chmod(backupPath, 0o600); chmodErr != nil {
			return backupPath, version, true, &legacyBackupWriteError{err: chmodErr}
		}
		return backupPath, version, true, nil
	case !os.IsNotExist(statErr):
		return backupPath, version, false, &legacyBackupWriteError{err: statErr}
	}
	err = AtomicWriteFile(backupPath, data, 0o600, s.backupWriteOps)
	backedUp := err == nil || WriteCommitted(err)
	if err != nil {
		return backupPath, version, backedUp, &legacyBackupWriteError{err: err}
	}
	return backupPath, version, true, nil
}

func appConfigSchemaVersion(data []byte) (int, error) {
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return 0, fmt.Errorf("gatewayapp: decode app config schema version: %w", err)
	}
	return header.SchemaVersion, nil
}
