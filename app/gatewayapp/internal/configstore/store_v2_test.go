package configstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestStoreLoadKeepsMigratedCredentialAfterCommittedWriteFault(t *testing.T) {
	for name, writeOps := range map[string]func(string, error) AtomicWriteOps{
		"destination chmod": func(path string, fault error) AtomicWriteOps {
			return AtomicWriteOps{Chmod: func(candidate string, mode os.FileMode) error {
				if candidate == path {
					return fault
				}
				return os.Chmod(candidate, mode)
			}}
		},
		"directory fsync": func(_ string, fault error) AtomicWriteOps {
			return AtomicWriteOps{FsyncDir: func(string) error { return fault }}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			store := New(root)
			legacy := migrationFixture()
			legacy.Models.Configs[0].CredentialRef = ""
			legacy.Models.Configs[0].Token = "committed-secret"
			legacy.Models.Configs[0].PersistToken = true
			original, err := json.MarshalIndent(legacy, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "config.json")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			fault := errors.New(name + " failed")
			store.writeOps = writeOps(path, fault)

			doc, loadErr := store.Load()
			if !errors.Is(loadErr, fault) || !WriteCommitted(loadErr) {
				t.Fatalf("Load() error = %v, want committed %v", loadErr, fault)
			}
			if doc.SchemaVersion != SchemaVersionV2 || len(doc.Models.ProviderEndpoints) != 1 {
				t.Fatalf("Load() document = %#v, want migrated config", doc)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("committed-secret")) || !bytes.Contains(raw, []byte(`"schema_version": 2`)) {
				t.Fatalf("committed config = %s", raw)
			}
			backup, err := os.ReadFile(path + ".v1.bak")
			if err != nil || !bytes.Equal(backup, original) {
				t.Fatalf("legacy backup = %q, %v; want original bytes", backup, err)
			}
			credentials, err := credentialstore.New(root)
			if err != nil {
				t.Fatal(err)
			}
			ref := doc.Models.ProviderEndpoints[0].CredentialRef
			if got, err := credentials.Get(context.Background(), ref); err != nil || got != "committed-secret" {
				t.Fatalf("credential after committed config write = %q, %v", got, err)
			}
			if _, err := New(root).Load(); err != nil {
				t.Fatalf("reload committed config: %v", err)
			}
		})
	}
}

func TestStoreLoadRollsBackMigratedCredentialBeforeWriteCommit(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	legacy := migrationFixture()
	legacy.Models.Configs[0].CredentialRef = ""
	legacy.Models.Configs[0].Token = "uncommitted-secret"
	legacy.Models.Configs[0].PersistToken = true
	original, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := decodeLegacyAppConfig(original)
	if err != nil || len(migration.CredentialWrites) != 1 {
		t.Fatalf("decodeLegacyAppConfig() writes/error = %#v/%v", migration.CredentialWrites, err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	fault := errors.New("rename failed")
	store.writeOps = AtomicWriteOps{Rename: func(string, string) error { return fault }}

	if _, err := store.Load(); !errors.Is(err, fault) || WriteCommitted(err) {
		t.Fatalf("Load() error = %v, want uncommitted %v", err, fault)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("uncommitted migration changed config:\n%s", after)
	}
	credentials, err := credentialstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.LookupSource(context.Background(), migration.CredentialWrites[0].Ref); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back credential lookup error = %v", err)
	}
}

func TestStoreLoadBackupFaultDoesNotCommitConfigOrCredential(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	legacy := migrationFixture()
	legacy.Models.Configs[0].CredentialRef = ""
	legacy.Models.Configs[0].Token = "backup-fault-secret"
	legacy.Models.Configs[0].PersistToken = true
	original, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := decodeLegacyAppConfig(original)
	if err != nil || len(migration.CredentialWrites) != 1 {
		t.Fatalf("decodeLegacyAppConfig() writes/error = %#v/%v", migration.CredentialWrites, err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	fault := errors.New("backup directory fsync failed")
	store.backupWriteOps = AtomicWriteOps{FsyncDir: func(string) error { return fault }}

	if _, err := store.Load(); !errors.Is(err, fault) || WriteCommitted(err) {
		t.Fatalf("Load() error = %v, want ordinary backup error containing %v", err, fault)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("config after backup fault = %q, %v; want original bytes", after, err)
	}
	backup, err := os.ReadFile(path + ".v1.bak")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("committed backup = %q, %v; want original bytes", backup, err)
	}
	credentials, err := credentialstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.LookupSource(context.Background(), migration.CredentialWrites[0].Ref); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back credential lookup error = %v", err)
	}
	report := store.MigrationReport()
	if !report.SourcePreserved || report.Migrated || report.ExplicitReplacement {
		t.Fatalf("MigrationReport() = %#v, want preserved legacy source", report)
	}
}

func TestStoreSaveRefusesConflictingLegacyBackup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	original := []byte(`{"models":{"configs":[{"provider":"","model":""}]}}`)
	previousBackup := []byte(`{"older":"legacy source"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".v1.bak", previousBackup, 0o600); err != nil {
		t.Fatal(err)
	}

	store := New(root)
	err := store.Save(AppConfig{})
	if err == nil || !strings.Contains(err.Error(), "existing backup conflicts") || WriteCommitted(err) {
		t.Fatalf("Save() error = %v, want ordinary backup conflict", err)
	}
	if current, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(current, original) {
		t.Fatalf("config after backup conflict = %q, %v", current, readErr)
	}
	if backup, readErr := os.ReadFile(path + ".v1.bak"); readErr != nil || !bytes.Equal(backup, previousBackup) {
		t.Fatalf("backup after conflict = %q, %v", backup, readErr)
	}
	report := store.MigrationReport()
	if report.FromSchema != 0 || !report.SourcePreserved || report.BackupPath != "" {
		t.Fatalf("MigrationReport() = %#v, want preserved schema-0 source without a claimed backup", report)
	}
}

func TestStoreSaveRejectsMalformedOrFutureDestinationWithoutRewrite(t *testing.T) {
	for name, original := range map[string][]byte{
		"malformed": []byte(`{"models":`),
		"future":    []byte(`{"schema_version":99}`),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.json")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}

			err := New(root).Save(AppConfig{})
			if err == nil || WriteCommitted(err) {
				t.Fatalf("Save() error = %v, want ordinary source rejection", err)
			}
			if current, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(current, original) {
				t.Fatalf("config after rejected Save = %q, %v", current, readErr)
			}
			if _, statErr := os.Stat(path + ".v1.bak"); !os.IsNotExist(statErr) {
				t.Fatalf("backup after rejected destination stat error = %v, want not exist", statErr)
			}
		})
	}
}

func TestStoreLoadMigratesLegacyProfileIDWireField(t *testing.T) {
	root := t.TempDir()
	legacyRaw, err := json.MarshalIndent(migrationFixture(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(legacyRaw, []byte(`"profile_id"`)) {
		t.Fatalf("fixture does not exercise legacy profile_id: %s", legacyRaw)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, legacyRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	doc, err := New(root).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Models.Configs[0].ProviderEndpointID; got != normalizedFixtureModel().ProviderEndpointID {
		t.Fatalf("ProviderEndpointID = %q", got)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(persisted, []byte(`"provider_endpoint_id"`)) {
		t.Fatalf("migrated provider wire shape = %s", persisted)
	}
	var wire struct {
		Models struct {
			Configs []map[string]json.RawMessage `json:"configs"`
		} `json:"models"`
	}
	if err := json.Unmarshal(persisted, &wire); err != nil {
		t.Fatal(err)
	}
	if _, exists := wire.Models.Configs[0]["profile_id"]; exists {
		t.Fatalf("migrated model retained legacy profile_id: %s", persisted)
	}
}

func TestStoreLoadSkipsBadLegacyBindingAndDiscovery(t *testing.T) {
	root := t.TempDir()
	legacy := migrationFixture()
	legacy.Delegation.Bindings[0].AgentID = "missing"
	legacy.AgentRoster.Discoveries[0].ConfigOptions = append(
		legacy.AgentRoster.Discoveries[0].ConfigOptions,
		legacy.AgentRoster.Discoveries[0].ConfigOptions[0],
	)
	original, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	store := New(root)
	doc, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(doc.Models.Configs) != 1 {
		t.Fatalf("valid provider was discarded: %#v", doc.Models)
	}
	if _, ok := modelprofile.Lookup(doc.ModelProfiles, modelprofile.BuildProviderID(normalizedFixtureModel().ID)); !ok {
		t.Fatalf("provider profile missing: %#v", doc.ModelProfiles)
	}
	if _, ok := agentbinding.Lookup(doc.AgentBindings, agentbinding.HandleOrbit); ok {
		t.Fatalf("bad binding survived: %#v", doc.AgentBindings)
	}
	backup, err := os.ReadFile(path + ".v1.bak")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("legacy backup = %q, %v; want original bytes", backup, err)
	}
	report := store.MigrationReport()
	if !report.Migrated || report.SourcePreserved || report.BackupPath != path+".v1.bak" {
		t.Fatalf("MigrationReport() = %#v, want migrated backup", report)
	}
	requireMigrationDrop(t, report, "acp_discovery", "agent_roster.discoveries[0]", "invalid_or_ambiguous_discovery")
	requireMigrationDrop(t, report, "delegation_binding", "delegation.bindings[0]", "unresolved_profile")
	report.Dropped[0].Reason = "caller-mutated"
	if current := store.MigrationReport(); current.Dropped[0].Reason == "caller-mutated" {
		t.Fatalf("MigrationReport() retained caller slice: %#v", current)
	}
	report = store.MigrationReport()

	if _, err := store.Load(); err != nil {
		t.Fatalf("second Load() error = %v", err)
	}
	if err := store.Save(doc); err != nil {
		t.Fatalf("Save(current) error = %v", err)
	}
	afterCurrentOperations := store.MigrationReport()
	if len(afterCurrentOperations.Dropped) != len(report.Dropped) || !afterCurrentOperations.Migrated {
		t.Fatalf("MigrationReport() after current Load/Save = %#v, want %#v", afterCurrentOperations, report)
	}
}

func TestStoreLoadAtomicallyMigratesOnce(t *testing.T) {
	root := t.TempDir()
	legacyRaw, err := json.MarshalIndent(migrationFixture(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, legacyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := New(root).Load()
	if err != nil {
		t.Fatal(err)
	}
	migratedRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.SchemaVersion != SchemaVersionV2 || bytes.Equal(legacyRaw, migratedRaw) {
		t.Fatalf("config was not migrated: %s", migratedRaw)
	}
	second, err := New(root).Load()
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(migratedRaw, secondRaw) {
		t.Fatalf("second Load changed persisted bytes:\nfirst %s\nsecond %s", migratedRaw, secondRaw)
	}
	firstJSON, _ := json.Marshal(first)
	secondJSON, _ := json.Marshal(second)
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("second Load changed document:\nfirst %s\nsecond %s", firstJSON, secondJSON)
	}
}

func TestStoreLoadPreservesBytesWhenLegacyHasNoSafeContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	original := []byte(`{"unknown":{"future":true},"models":{"configs":[{"provider":"","model":""}]},"delegation":{"bindings":[{"profile":"orbit","target":"agent","agent_id":"missing"}]}}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	loadStore := New(root)
	doc, err := loadStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != SchemaVersionV2 || len(doc.Models.Configs) != 0 || len(doc.ModelProfiles.Profiles) != 0 {
		t.Fatalf("Load() document = %#v, want empty current config", doc)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatalf("unrecognized legacy bytes changed:\n%s", after)
	}
	report := loadStore.MigrationReport()
	if !report.SourcePreserved || report.Migrated || report.BackupPath != "" {
		t.Fatalf("MigrationReport() = %#v, want preserved source without backup", report)
	}
	saveStore := New(root)
	if err := saveStore.Save(AppConfig{}); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".v1.bak")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("explicit replacement backup = %q, %v; want original bytes", backup, err)
	}
	replacementReport := saveStore.MigrationReport()
	if !replacementReport.ExplicitReplacement || replacementReport.Migrated || replacementReport.SourcePreserved {
		t.Fatalf("MigrationReport() after explicit replacement = %#v", replacementReport)
	}
	afterSave, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(original, afterSave) || !bytes.Contains(afterSave, []byte(`"schema_version": 2`)) {
		t.Fatalf("first explicit current save did not replace legacy bytes: %s", afterSave)
	}
}

func TestStoreLoadRejectsMalformedAndFutureSchemasWithoutRewrite(t *testing.T) {
	for name, original := range map[string][]byte{
		"malformed": []byte(`{"models":`),
		"future":    []byte(`{"schema_version":99}`),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.json")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := New(root).Load(); err == nil {
				t.Fatal("Load() error = nil")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, after) {
				t.Fatalf("rejected config was rewritten: %s", after)
			}
		})
	}
}

func TestStoreLoadRejectsCredentialSourceConflict(t *testing.T) {
	root := t.TempDir()
	legacy := migrationFixture()
	legacy.Models.Configs[0].CredentialRef = "apikey:test:shared"
	legacy.Models.Configs[0].Token = "legacy-secret"
	original, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, err := credentialstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.Put(context.Background(), "apikey:test:shared", "existing-secret"); err != nil {
		t.Fatal(err)
	}

	if _, err := New(root).Load(); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("Load() error = %v, want credential source conflict", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatalf("credential conflict changed config: %s", after)
	}
	if got, err := credentials.Get(context.Background(), "apikey:test:shared"); err != nil || got != "existing-secret" {
		t.Fatalf("existing credential = %q, %v", got, err)
	}
}

func TestStoreLoadRejectsCurrentCredentialMaterial(t *testing.T) {
	root := t.TempDir()
	doc, err := migrateV1ToV2(migrationFixture())
	if err != nil {
		t.Fatal(err)
	}
	doc.Models.ProviderEndpoints[0].Token = "must-not-load"
	doc.Models.ProviderEndpoints[0].CredentialRef = "apikey:test:opaque"
	original, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root).Load(); err == nil || !strings.Contains(err.Error(), "credential store") {
		t.Fatalf("Load() error = %v, want credential-store boundary", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("rejected current config was rewritten: %s", after)
	}
}
