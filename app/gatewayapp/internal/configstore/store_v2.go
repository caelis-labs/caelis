package configstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
)

// Load reads the current AppConfig. A pre-v2 wire document is converted once
// when at least one record can be mapped safely; otherwise its bytes remain
// untouched and an empty current document is returned.
func (s *Store) Load() (AppConfig, error) {
	if s == nil {
		return AppConfig{SchemaVersion: SchemaVersionV2}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.path) == "" {
		return AppConfig{SchemaVersion: SchemaVersionV2}, nil
	}
	data, err := os.ReadFile(s.path)
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
		migrated, err := decodeLegacyAppConfig(data)
		if err != nil {
			return AppConfig{}, err
		}
		migrated.Report.FromSchema = version
		s.migration = cloneMigrationReport(migrated.Report)
		if !migrated.HasSafeContent {
			s.migration.SourcePreserved = true
			return migrated.Document, nil
		}
		backupPath, fromSchema, backedUp, backupErr := s.backupLegacyDestinationUnlocked()
		s.migration.FromSchema = fromSchema
		if backedUp {
			s.migration.BackupPath = backupPath
		}
		if backupErr != nil {
			s.migration.SourcePreserved = true
			return AppConfig{}, backupErr
		}
		credentialTxn, err := applyLegacyCredentialWrites(filepath.Dir(s.path), migrated.CredentialWrites)
		if err != nil {
			s.migration.SourcePreserved = true
			return AppConfig{}, err
		}
		if err := s.saveUnlocked(migrated.Document, true); err != nil {
			persistErr := fmt.Errorf("gatewayapp: persist migrated app config: %w", err)
			if WriteCommitted(err) {
				return migrated.Document, persistErr
			}
			return AppConfig{}, errors.Join(persistErr, credentialTxn.rollback())
		}
		return migrated.Document, nil
	case SchemaVersionV2:
		var doc AppConfig
		if err := json.Unmarshal(data, &doc); err != nil {
			return AppConfig{}, fmt.Errorf("gatewayapp: decode app config: %w", err)
		}
		// Reject conflicting identities before normalization: normalization is
		// intentionally lossy and must not hide duplicate current-schema records.
		if err := validateCurrentRecordIdentities(doc); err != nil {
			return AppConfig{}, err
		}
		doc = Normalize(doc)
		if err := Validate(doc); err != nil {
			return AppConfig{}, err
		}
		return doc, nil
	default:
		return AppConfig{}, fmt.Errorf("gatewayapp: unsupported AppConfig schema version %d", version)
	}
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
	if source.Environment != "" {
		return store.PutEnvironment(context.Background(), ref, source.Environment)
	}
	return store.Put(context.Background(), ref, source.APIKey)
}

// Save validates and atomically persists one current AppConfig document.
func (s *Store) Save(doc AppConfig) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.path) == "" {
		return nil
	}
	return s.saveUnlocked(doc, false)
}

func (s *Store) saveUnlocked(doc AppConfig, migratingLegacy bool) error {
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
	dir := filepath.Dir(s.path)
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
	backupPath, fromSchema, backedUp, err := s.backupLegacyDestinationUnlocked()
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
	err = AtomicWriteFile(s.path, data, 0o600, s.writeOps)
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

func (s *Store) backupLegacyDestinationUnlocked() (string, int, bool, error) {
	data, err := os.ReadFile(s.path)
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
	backupPath := s.path + ".v1.bak"
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
