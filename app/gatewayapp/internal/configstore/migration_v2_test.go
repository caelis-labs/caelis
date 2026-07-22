package configstore

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestMigrateV1ToV2ProducesCurrentProfilesAgentsAndBindings(t *testing.T) {
	legacy := migrationFixture()
	migrated, err := migrateLegacyAppConfig(legacy)
	if err != nil {
		t.Fatal(err)
	}
	got := migrated.Document
	if got.SchemaVersion != SchemaVersionV2 {
		t.Fatalf("SchemaVersion = %d", got.SchemaVersion)
	}
	if len(got.ExternalAgents.Agents) != 1 || got.ExternalAgents.Agents[0].ID != "claude" || got.ExternalAgents.Agents[0].ConnectionID != "claude" {
		t.Fatalf("ExternalAgents = %#v", got.ExternalAgents)
	}
	if len(got.ModelProfiles.Profiles) != 3 {
		t.Fatalf("ModelProfiles = %#v", got.ModelProfiles.Profiles)
	}
	opus := findProfileByRemoteModel(t, got.ModelProfiles, "Opus-V4")
	if opus.Backend.ACP.AgentID != "claude" || opus.Effort.ACPConfigID != "thought_level" || opus.Effort.DefaultEffort != "xhigh" {
		t.Fatalf("migrated Opus profile = %#v", opus)
	}
	if wire, ok := opus.WireEffort("xhigh"); !ok || wire != "very-high" {
		t.Fatalf("Opus xhigh wire = %q, %v", wire, ok)
	}
	if !reflect.DeepEqual(opus.Backend.ACP.SessionDefaults, map[string]string{"mode": "code"}) {
		t.Fatalf("Opus defaults = %#v", opus.Backend.ACP.SessionDefaults)
	}
	orbit, ok := agentbinding.Lookup(got.AgentBindings, agentbinding.HandleOrbit)
	if !ok || orbit.ProfileID != opus.ID || orbit.Effort != "xhigh" {
		t.Fatalf("Orbit binding = %#v, %v", orbit, ok)
	}
	guardian, ok := agentbinding.Lookup(got.AgentBindings, agentbinding.HandleGuardian)
	if !ok || guardian.ProfileID != modelprofile.BuildProviderID(normalizedFixtureModel().ID) || guardian.Effort != "high" {
		t.Fatalf("Guardian binding = %#v, %v", guardian, ok)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"agent_roster", "delegation", "system_agents", "must-not-persist", "remote_session_id"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("current JSON contains %q: %s", forbidden, raw)
		}
	}
	var wire struct {
		Models struct {
			Configs []map[string]json.RawMessage `json:"configs"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, exists := wire.Models.Configs[0]["profile_id"]; exists {
		t.Fatalf("current model config retained legacy profile_id: %s", raw)
	}
	for _, required := range []string{"schema_version", "provider_endpoints", "provider_endpoint_id", "model_profiles", "agent_bindings", "external_agents", "credential_ref"} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("current JSON missing %q: %s", required, raw)
		}
	}
}

func TestMigrateV1ToV2SkipsAmbiguousACPDiscoveryAndKeepsProvider(t *testing.T) {
	legacy := migrationFixture()
	ambiguous := legacy.AgentRoster.Discoveries[0]
	ambiguous.ConfigOptions = append([]controlagents.ConfigOption(nil), ambiguous.ConfigOptions...)
	ambiguous.ConfigOptions[0].Options = append([]controlagents.ConfigChoice(nil), ambiguous.ConfigOptions[0].Options...)
	ambiguous.ConfigOptions[0].CurrentValue = "high"
	ambiguous.CWD = "/other"
	ambiguous.DiscoveredAt = ambiguous.DiscoveredAt.Add(time.Minute)
	legacy.AgentRoster.Discoveries = append(legacy.AgentRoster.Discoveries, ambiguous)

	migrated, err := migrateLegacyAppConfig(legacy)
	if err != nil {
		t.Fatal(err)
	}
	got := migrated.Document
	if _, ok := modelprofile.Lookup(got.ModelProfiles, modelprofile.BuildProviderID(normalizedFixtureModel().ID)); !ok {
		t.Fatalf("provider profile was lost: %#v", got.ModelProfiles)
	}
	for _, profile := range got.ModelProfiles.Profiles {
		if profile.Backend.ACP != nil && profile.Backend.ACP.RemoteModelID == "Opus-V4" {
			t.Fatalf("ambiguous ACP profile was migrated: %#v", profile)
		}
	}
	if _, ok := agentbinding.Lookup(got.AgentBindings, agentbinding.HandleOrbit); ok {
		t.Fatalf("binding to skipped ACP profile survived: %#v", got.AgentBindings)
	}
	requireMigrationDrop(t, migrated.Report, "acp_agent", "agent_roster.agents[1]", "ambiguous_discovery")
	requireMigrationDrop(t, migrated.Report, "delegation_binding", "delegation.bindings[0]", "unresolved_profile")
}

func TestMigrateV1ToV2SkipsBadBinding(t *testing.T) {
	legacy := migrationFixture()
	legacy.Delegation.Bindings[0].AgentID = "missing"

	migrated, err := migrateLegacyAppConfig(legacy)
	if err != nil {
		t.Fatal(err)
	}
	got := migrated.Document
	if _, ok := agentbinding.Lookup(got.AgentBindings, agentbinding.HandleOrbit); ok {
		t.Fatalf("bad binding survived: %#v", got.AgentBindings)
	}
	if len(got.Models.Configs) != 1 {
		t.Fatalf("bad binding discarded provider config: %#v", got.Models)
	}
	requireMigrationDrop(t, migrated.Report, "delegation_binding", "delegation.bindings[0]", "unresolved_profile")
}

func TestDecodeLegacyAppConfigSkipsMalformedRecordsAndKeepsProvider(t *testing.T) {
	raw, err := json.Marshal(migrationFixture())
	if err != nil {
		t.Fatal(err)
	}
	raw = appendLegacyRawRecord(t, raw, "models", "configs", json.RawMessage(`{"provider":"openai","model":"broken","timeout":"not-a-duration"}`))
	raw = appendLegacyRawRecord(t, raw, "agent_roster", "discoveries", json.RawMessage(`{"connection_id":"claude","models":"not-an-array"}`))
	raw = appendLegacyRawRecord(t, raw, "delegation", "bindings", json.RawMessage(`{"profile":["orbit"],"target":"agent","agent_id":"opus"}`))

	migrated, err := decodeLegacyAppConfig(raw)
	if err != nil {
		t.Fatalf("decodeLegacyAppConfig() error = %v", err)
	}
	if !migrated.HasSafeContent || len(migrated.Document.Models.Configs) != 1 {
		t.Fatalf("valid provider was discarded: %#v", migrated)
	}
	providerID := modelprofile.BuildProviderID(normalizedFixtureModel().ID)
	if _, ok := modelprofile.Lookup(migrated.Document.ModelProfiles, providerID); !ok {
		t.Fatalf("provider profile %q missing: %#v", providerID, migrated.Document.ModelProfiles)
	}
	requireMigrationDrop(t, migrated.Report, "models.configs", "models.configs[1]", "malformed_record")
	requireMigrationDrop(t, migrated.Report, "agent_roster.discoveries", "agent_roster.discoveries[2]", "malformed_record")
	requireMigrationDrop(t, migrated.Report, "delegation.bindings", "delegation.bindings[1]", "malformed_record")
	reportJSON, err := json.Marshal(migrated.Report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(reportJSON), "must-not-persist") {
		t.Fatalf("migration report leaked credential material: %s", reportJSON)
	}
}

func TestDecodeLegacyAppConfigIgnoresUnknownFieldsWithoutDrop(t *testing.T) {
	migrated, err := decodeLegacyAppConfig([]byte(`{"unknown":{"future":true},"Models":{"configs":[{"token":"not-inspected"}]}}`))
	if err != nil {
		t.Fatalf("decodeLegacyAppConfig() error = %v", err)
	}
	if len(migrated.Report.Dropped) != 0 {
		t.Fatalf("MigrationReport.Dropped = %#v, want unknown fields ignored", migrated.Report.Dropped)
	}
}

func TestMigrateV1ToV2RejectsCredentialSourcesWithinRecord(t *testing.T) {
	legacy := migrationFixture()
	legacy.Models.Configs[0].CredentialRef = ""
	legacy.Models.Configs[0].Token = "inline-secret"
	legacy.Models.Configs[0].TokenEnv = "OPENAI_API_KEY"

	if _, err := migrateV1ToV2(legacy); err == nil || !strings.Contains(err.Error(), "conflicting inline and environment sources") {
		t.Fatalf("migrateV1ToV2() error = %v, want credential source conflict", err)
	}
}

func TestMigrateV1ToV2RejectsCredentialSourcesAcrossRecords(t *testing.T) {
	legacy := migrationFixture()
	legacy.Models.Configs[0].CredentialRef = "apikey:test:shared"
	legacy.Models.Configs[0].Token = "first-secret"
	conflicting := legacy.Models.Configs[0]
	conflicting.Token = "second-secret"
	legacy.Models.Configs = append(legacy.Models.Configs, conflicting)

	if _, err := migrateV1ToV2(legacy); err == nil || !strings.Contains(err.Error(), "conflicting sources") {
		t.Fatalf("migrateV1ToV2() error = %v, want cross-record credential source conflict", err)
	}
}

func TestMigrateV1ToV2ReplacesPlaintextProviderCredentialWithOpaqueReference(t *testing.T) {
	legacy := migrationFixture()
	legacy.Models.Configs[0].CredentialRef = ""
	legacy.Models.Configs[0].Token = "plaintext"
	legacy.Models.Configs[0].PersistToken = true
	got, err := migrateV1ToV2(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models.ProviderEndpoints) != 1 || !strings.HasPrefix(got.Models.ProviderEndpoints[0].CredentialRef, "apikey:") {
		t.Fatalf("migrated provider endpoints = %#v", got.Models.ProviderEndpoints)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "plaintext") {
		t.Fatalf("migrated config contains plaintext: %s", raw)
	}
}

func TestValidateRejectsMultipleAgentIdentitiesForOneConnection(t *testing.T) {
	doc, err := migrateV1ToV2(migrationFixture())
	if err != nil {
		t.Fatal(err)
	}
	duplicate := doc.ExternalAgents.Agents[0]
	duplicate.ID = "claude-sibling"
	doc.ExternalAgents.Agents = append(doc.ExternalAgents.Agents, duplicate)
	if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "multiple Agent identities") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func migrationFixture() legacyAppConfig {
	model := fixtureLegacyModel()
	connection := controlagents.Connection{
		ID: "claude", Name: "Claude", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindExecutable, Command: "claude-agent"},
	}
	effortOptions := []controlagents.ConfigChoice{{Value: "high", Name: "High"}, {Value: "very-high", Name: "Very High"}}
	return legacyAppConfig{
		Models: legacyPersistedModelConfig{
			DefaultID: model.ID,
			Configs:   []legacyModelConfig{model},
		},
		AgentRoster: legacyAgentConfiguration{
			Connections: []controlagents.Connection{connection},
			Agents: []legacyAgent{
				{ID: "codex", Name: "Codex", Backing: legacyAgentBacking{ModelAlias: model.ID}},
				{ID: "opus", Name: "Claude", Backing: legacyAgentBacking{ConnectionID: "claude"}, Defaults: legacySessionOptions{ModelID: "Opus-V4", ConfigValues: map[string]string{"mode": "code", "thought_level": "very-high"}}},
				{ID: "sonnet", Name: "Claude", Backing: legacyAgentBacking{ConnectionID: "claude"}, Defaults: legacySessionOptions{ModelID: "Sonnet-V4", ConfigValues: map[string]string{"mode": "chat", "thought_level": "high"}}},
			},
			Discoveries: []controlagents.DiscoverySnapshot{
				{
					ConnectionID: "claude", LaunchFingerprint: controlagents.LaunchFingerprint(connection.Launcher), CWD: "/workspace", SelectedModelID: "Opus-V4", CurrentModelID: "Opus-V4",
					Models:        []controlagents.RemoteModel{{ID: "Opus-V4", Name: "Opus"}},
					ConfigOptions: []controlagents.ConfigOption{{ID: "thought_level", CurrentValue: "very-high", Purpose: controlagents.ConfigOptionPurposeReasoningEffort, Options: effortOptions}},
				},
				{
					ConnectionID: "claude", LaunchFingerprint: controlagents.LaunchFingerprint(connection.Launcher), CWD: "/workspace", SelectedModelID: "Sonnet-V4", CurrentModelID: "Sonnet-V4",
					Models:        []controlagents.RemoteModel{{ID: "Sonnet-V4", Name: "Sonnet"}},
					ConfigOptions: []controlagents.ConfigOption{{ID: "thought_level", CurrentValue: "high", Purpose: controlagents.ConfigOptionPurposeReasoningEffort, Options: effortOptions}},
				},
			},
		},
		Delegation: legacyDelegationConfiguration{Bindings: []legacyDelegationBinding{{
			Handle: legacyDelegationOrbit, Target: legacyDelegationTargetAgent, AgentID: "opus",
		}}},
		SystemAgents: legacySystemConfiguration{Bindings: []legacySystemBinding{{
			Handle: legacySystemGuardian, AgentID: "codex", ReasoningEffort: "high",
		}}},
	}
}

func fixtureLegacyModel() legacyModelConfig {
	current := normalizedFixtureModel()
	return legacyModelConfig{
		ID: current.ID, Alias: current.Alias, Provider: "openai-codex", ProfileID: current.ProviderEndpointID,
		Model: "gpt-5.6", CredentialRef: modelconfig.CodexOAuthCredentialRef,
		Token: "must-not-persist", PersistToken: true,
		ReasoningMode: "effort", ReasoningLevels: []string{"low", "high", "xhigh"}, DefaultReasoningEffort: "high",
	}
}

func normalizedFixtureModel() modelconfig.Config {
	return modelconfig.NormalizeConfig(modelconfig.Config{
		Provider: "openai-codex", Model: "gpt-5.6", CredentialRef: modelconfig.CodexOAuthCredentialRef,
		ReasoningMode: "effort", ReasoningLevels: []string{"low", "high", "xhigh"}, DefaultReasoningEffort: "high",
	})
}

func findProfileByRemoteModel(t *testing.T, profiles modelprofile.Configuration, modelID string) modelprofile.ModelProfile {
	t.Helper()
	for _, profile := range profiles.Profiles {
		if profile.Backend.ACP != nil && profile.Backend.ACP.RemoteModelID == modelID {
			return profile
		}
	}
	t.Fatalf("remote model profile %q not found", modelID)
	return modelprofile.ModelProfile{}
}

func requireMigrationDrop(t *testing.T, report MigrationReport, category, identity, reason string) {
	t.Helper()
	for _, dropped := range report.Dropped {
		if dropped.Category == category && dropped.Identity == identity && dropped.Reason == reason {
			return
		}
	}
	t.Fatalf("MigrationReport.Dropped = %#v, want category=%q identity=%q reason=%q", report.Dropped, category, identity, reason)
}

func appendLegacyRawRecord(
	t *testing.T,
	document []byte,
	section string,
	collection string,
	record json.RawMessage,
) []byte {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(document, &root); err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(root[section], &object); err != nil {
		t.Fatal(err)
	}
	var records []json.RawMessage
	if err := json.Unmarshal(object[collection], &records); err != nil {
		t.Fatal(err)
	}
	records = append(records, record)
	object[collection] = mustMarshalLegacyTestValue(t, records)
	root[section] = mustMarshalLegacyTestValue(t, object)
	return mustMarshalLegacyTestValue(t, root)
}

func mustMarshalLegacyTestValue(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
