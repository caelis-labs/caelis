package configstore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelprofile"
	profilebuilder "github.com/caelis-labs/caelis/control/modelprofile/builder"
)

type legacyMigration struct {
	Document         AppConfig
	CredentialWrites []legacyCredentialWrite
	HasSafeContent   bool
	Report           MigrationReport
}

// MigrationReport describes one best-effort legacy AppConfig conversion. It is
// operational state held by Store and is never part of the live AppConfig wire
// schema. Dropped entries contain only fixed categories, source indexes, and
// reason codes so legacy credential material cannot leak into diagnostics.
type MigrationReport struct {
	FromSchema          int
	Migrated            bool
	SourcePreserved     bool
	ExplicitReplacement bool
	BackupPath          string
	Dropped             []MigrationDrop
}

// MigrationDrop identifies one legacy record that could not be mapped safely.
type MigrationDrop struct {
	Category string
	Identity string
	Reason   string
}

func (r *MigrationReport) addDrop(category, identity, reason string) {
	if r == nil {
		return
	}
	r.Dropped = append(r.Dropped, MigrationDrop{
		Category: strings.TrimSpace(category),
		Identity: strings.TrimSpace(identity),
		Reason:   strings.TrimSpace(reason),
	})
}

func mergeMigrationReports(left, right MigrationReport) MigrationReport {
	out := left
	if out.FromSchema == 0 {
		out.FromSchema = right.FromSchema
	}
	out.Migrated = out.Migrated || right.Migrated
	out.SourcePreserved = out.SourcePreserved || right.SourcePreserved
	out.ExplicitReplacement = out.ExplicitReplacement || right.ExplicitReplacement
	if strings.TrimSpace(right.BackupPath) != "" {
		out.BackupPath = strings.TrimSpace(right.BackupPath)
	}
	out.Dropped = append(append([]MigrationDrop(nil), left.Dropped...), right.Dropped...)
	return out
}

func cloneMigrationReport(in MigrationReport) MigrationReport {
	out := in
	out.Dropped = append([]MigrationDrop(nil), in.Dropped...)
	return out
}

type legacyCredentialWrite struct {
	Ref    string
	Source credentialstore.Source
}

// decodeLegacyAppConfig decodes the private pre-v2 wire shape and performs a
// best-effort, record-by-record conversion. Unknown fields and records that
// cannot be mapped unambiguously are ignored.
func decodeLegacyAppConfig(data []byte) (legacyMigration, error) {
	legacy, wireReport, err := decodeLegacyWireAppConfigWithReport(data)
	if err != nil {
		return legacyMigration{}, fmt.Errorf("gatewayapp: decode legacy app config: %w", err)
	}
	migrated, err := migrateLegacyAppConfig(legacy)
	if err != nil {
		return legacyMigration{}, err
	}
	migrated.Report = mergeMigrationReports(wireReport, migrated.Report)
	return migrated, nil
}

func migrateV1ToV2(legacy legacyAppConfig) (AppConfig, error) {
	migrated, err := migrateLegacyAppConfig(legacy)
	return migrated.Document, err
}

func migrateLegacyAppConfig(legacy legacyAppConfig) (legacyMigration, error) {
	report := MigrationReport{}
	models, providerProfiles, oldModelProfiles, credentialWrites, err := migrateLegacyProviders(legacy.Models, &report)
	if err != nil {
		return legacyMigration{}, err
	}
	externalAgents, acpProfiles, oldACPProfiles := migrateLegacyACP(legacy.AgentRoster, &report)

	profilesByID := make(map[string]modelprofile.ModelProfile, len(providerProfiles)+len(acpProfiles))
	for index, profile := range append(providerProfiles, acpProfiles...) {
		if previous, exists := profilesByID[profile.ID]; exists && !reflect.DeepEqual(previous, profile) {
			report.addDrop("model_profile", fmt.Sprintf("model_profiles[%d]", index), "conflicting_identity")
			continue
		}
		profilesByID[profile.ID] = profile
	}
	profiles := modelprofile.Configuration{}
	for _, profile := range profilesByID {
		profiles.Profiles = append(profiles.Profiles, profile)
	}
	if defaultID := strings.ToLower(strings.TrimSpace(models.DefaultID)); defaultID != "" {
		profiles.DefaultProfileID = oldModelProfiles[defaultID]
		if profiles.DefaultProfileID == "" {
			report.addDrop("model_default", "models.default", "unresolved_reference")
		}
	}
	profiles = modelprofile.NormalizeConfiguration(profiles)

	oldAgentProfiles := make(map[string]string, len(oldModelProfiles)+len(oldACPProfiles))
	for oldAgentID, profileID := range oldACPProfiles {
		oldAgentProfiles[oldAgentID] = profileID
	}
	for index, oldAgent := range legacy.AgentRoster.Agents {
		modelAlias := strings.ToLower(strings.TrimSpace(oldAgent.Backing.ModelAlias))
		if modelAlias == "" {
			continue
		}
		if profileID := oldModelProfiles[modelAlias]; profileID != "" {
			oldAgentProfiles[controlagents.NormalizeName(oldAgent.ID)] = profileID
		} else {
			report.addDrop("provider_agent", fmt.Sprintf("agent_roster.agents[%d]", index), "unresolved_profile")
		}
	}
	bindings := migrateLegacyBindings(legacy.Delegation, legacy.SystemAgents, oldAgentProfiles, profiles, &report)

	doc := Normalize(AppConfig{
		SchemaVersion:      SchemaVersionV2,
		Models:             models,
		ExternalAgents:     externalAgents,
		ModelProfiles:      profiles,
		AgentBindings:      bindings,
		Sandbox:            legacy.Sandbox,
		Runtime:            legacy.Runtime,
		Plugins:            legacy.Plugins,
		PluginMarketplaces: legacy.PluginMarketplaces,
	})
	hasSafeContent := legacyDocumentHasSafeContent(doc)
	if !hasSafeContent {
		return legacyMigration{
			Document: AppConfig{SchemaVersion: SchemaVersionV2},
			Report:   report,
		}, nil
	}
	if err := Validate(doc); err != nil {
		return legacyMigration{}, fmt.Errorf("gatewayapp: validate migrated app config: %w", err)
	}
	return legacyMigration{
		Document:         doc,
		CredentialWrites: credentialWrites,
		HasSafeContent:   true,
		Report:           report,
	}, nil
}

func migrateLegacyProviders(models legacyPersistedModelConfig, report *MigrationReport) (
	PersistedModelConfig,
	[]modelprofile.ModelProfile,
	map[string]string,
	[]legacyCredentialWrite,
	error,
) {
	endpointsByID := map[string]modelconfig.ProviderEndpointConfig{}
	credentialSources := map[string]credentialstore.Source{}

	for index, raw := range models.Profiles {
		endpoint, source, hasSource, ok, err := convertLegacyProviderEndpoint(raw)
		if err != nil {
			return PersistedModelConfig{}, nil, nil, nil, err
		}
		if !ok {
			report.addDrop("provider_endpoint", fmt.Sprintf("models.profiles[%d]", index), "invalid_record")
			continue
		}
		if hasSource {
			if err := mergeLegacyCredentialSource(credentialSources, endpoint.CredentialRef, source); err != nil {
				return PersistedModelConfig{}, nil, nil, nil, err
			}
		}
		if previous, exists := endpointsByID[endpoint.ID]; exists {
			if !reflect.DeepEqual(previous, endpoint) {
				report.addDrop("provider_endpoint", fmt.Sprintf("models.profiles[%d]", index), "conflicting_identity")
				continue
			}
		} else {
			endpointsByID[endpoint.ID] = endpoint
		}
	}

	configsByID := map[string]modelconfig.Config{}
	profilesByID := map[string]modelprofile.ModelProfile{}
	oldModelProfiles := map[string]string{}
	aliasToCurrentID := map[string]string{}
	for index, raw := range models.Configs {
		configured, endpoint, source, hasSource, ok, err := convertLegacyProviderModel(raw)
		if err != nil {
			return PersistedModelConfig{}, nil, nil, nil, err
		}
		if !ok {
			report.addDrop("provider_model", fmt.Sprintf("models.configs[%d]", index), "invalid_record")
			continue
		}
		if hasSource {
			if err := mergeLegacyCredentialSource(credentialSources, endpoint.CredentialRef, source); err != nil {
				return PersistedModelConfig{}, nil, nil, nil, err
			}
		}
		if previous, exists := endpointsByID[endpoint.ID]; exists {
			if !legacyEndpointsCompatible(previous, endpoint) {
				report.addDrop("provider_model", fmt.Sprintf("models.configs[%d]", index), "incompatible_endpoint")
				continue
			}
			if endpoint.CredentialRef != "" && previous.CredentialRef != "" && endpoint.CredentialRef != previous.CredentialRef {
				report.addDrop("provider_model", fmt.Sprintf("models.configs[%d]", index), "credential_reference_conflict")
				continue
			}
			if previous.CredentialRef == "" && endpoint.CredentialRef != "" {
				previous.CredentialRef = endpoint.CredentialRef
				endpointsByID[endpoint.ID] = previous
			}
			configured = modelconfig.MergeConfigProviderEndpoint(configured, previous)
			endpoint = previous
		} else {
			endpointsByID[endpoint.ID] = endpoint
		}
		profile, err := profilebuilder.FromProvider(configured)
		if err != nil {
			report.addDrop("provider_model", fmt.Sprintf("models.configs[%d]", index), "profile_build_failed")
			continue
		}
		if previous, exists := configsByID[configured.ID]; exists && !reflect.DeepEqual(previous, configured) {
			report.addDrop("provider_model", fmt.Sprintf("models.configs[%d]", index), "conflicting_identity")
			continue
		}
		if previous, exists := profilesByID[profile.ID]; exists && !reflect.DeepEqual(previous, profile) {
			report.addDrop("provider_model", fmt.Sprintf("models.configs[%d]", index), "conflicting_profile")
			continue
		}
		configsByID[configured.ID] = configured
		profilesByID[profile.ID] = profile
		for _, key := range []string{raw.ID, raw.Alias, configured.ID, configured.Alias} {
			if key = strings.ToLower(strings.TrimSpace(key)); key != "" {
				oldModelProfiles[key] = profile.ID
				aliasToCurrentID[key] = configured.ID
			}
		}
	}

	currentDefaultID := ""
	for _, key := range []string{models.DefaultID, models.DefaultAlias} {
		if key = strings.ToLower(strings.TrimSpace(key)); key != "" {
			if candidate := aliasToCurrentID[key]; candidate != "" {
				currentDefaultID = candidate
				break
			}
		}
	}
	if (strings.TrimSpace(models.DefaultID) != "" || strings.TrimSpace(models.DefaultAlias) != "") && currentDefaultID == "" {
		report.addDrop("model_default", "models.default", "unresolved_reference")
	}
	out := PersistedModelConfig{DefaultID: currentDefaultID}
	if currentDefaultID != "" {
		out.DefaultAlias = strings.TrimSpace(models.DefaultAlias)
		oldModelProfiles[strings.ToLower(strings.TrimSpace(currentDefaultID))] = modelprofile.BuildProviderID(currentDefaultID)
	}
	for _, endpoint := range endpointsByID {
		out.ProviderEndpoints = append(out.ProviderEndpoints, endpoint)
	}
	for _, configured := range configsByID {
		out.Configs = append(out.Configs, configured)
	}
	profiles := make([]modelprofile.ModelProfile, 0, len(profilesByID))
	for _, profile := range profilesByID {
		profiles = append(profiles, profile)
	}
	sort.Slice(out.ProviderEndpoints, func(i, j int) bool { return out.ProviderEndpoints[i].ID < out.ProviderEndpoints[j].ID })
	sort.Slice(out.Configs, func(i, j int) bool { return out.Configs[i].ID < out.Configs[j].ID })
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
	out = normalizePersistedModelsForSave(out)
	out.Configs = dedupeModelConfigsForSave(out.Configs)
	out.ProviderEndpoints = dedupeProviderEndpointsForSave(out.ProviderEndpoints)

	refs := make([]string, 0, len(credentialSources))
	for ref := range credentialSources {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	writes := make([]legacyCredentialWrite, 0, len(refs))
	for _, ref := range refs {
		writes = append(writes, legacyCredentialWrite{Ref: ref, Source: credentialSources[ref]})
	}
	return out, profiles, oldModelProfiles, writes, nil
}

func convertLegacyProviderEndpoint(raw legacyProviderEndpoint) (
	modelconfig.ProviderEndpointConfig,
	credentialstore.Source,
	bool,
	bool,
	error,
) {
	if strings.TrimSpace(raw.Provider) == "" {
		return modelconfig.ProviderEndpointConfig{}, credentialstore.Source{}, false, false, nil
	}
	endpoint := modelconfig.NormalizeProviderEndpoint(modelconfig.ProviderEndpointConfig{
		ID:                      raw.ID,
		Provider:                raw.Provider,
		EndpointID:              raw.EndpointID,
		API:                     raw.API,
		BaseURL:                 raw.BaseURL,
		CredentialRef:           raw.CredentialRef,
		AuthType:                raw.AuthType,
		HeaderKey:               raw.HeaderKey,
		Timeout:                 raw.Timeout,
		StreamFirstEventTimeout: raw.StreamFirstEventTimeout,
	})
	ref, source, hasSource, err := prepareLegacyCredential(
		endpoint.Provider,
		endpoint.ID,
		raw.CredentialRef,
		raw.Token,
		raw.TokenEnv,
	)
	if err != nil {
		return modelconfig.ProviderEndpointConfig{}, credentialstore.Source{}, false, false, err
	}
	endpoint.CredentialRef = ref
	endpoint = modelconfig.SanitizePersistedProviderEndpoint(endpoint)
	if endpoint.ID == "" || endpoint.Provider == "" {
		return modelconfig.ProviderEndpointConfig{}, credentialstore.Source{}, false, false, nil
	}
	return endpoint, source, hasSource, true, nil
}

func convertLegacyProviderModel(raw legacyModelConfig) (
	modelconfig.Config,
	modelconfig.ProviderEndpointConfig,
	credentialstore.Source,
	bool,
	bool,
	error,
) {
	if strings.TrimSpace(raw.Provider) == "" || strings.TrimSpace(raw.Model) == "" {
		return modelconfig.Config{}, modelconfig.ProviderEndpointConfig{}, credentialstore.Source{}, false, false, nil
	}
	configured := modelconfig.NormalizeConfig(modelconfig.Config{
		ID:                      raw.ID,
		Alias:                   raw.Alias,
		Provider:                raw.Provider,
		ProviderEndpointID:      raw.ProfileID,
		EndpointID:              raw.EndpointID,
		API:                     raw.API,
		Model:                   raw.Model,
		BaseURL:                 raw.BaseURL,
		CredentialRef:           raw.CredentialRef,
		AuthType:                raw.AuthType,
		HeaderKey:               raw.HeaderKey,
		ContextWindowTokens:     raw.ContextWindowTokens,
		ReasoningEffort:         raw.ReasoningEffort,
		DefaultReasoningEffort:  raw.DefaultReasoningEffort,
		ReasoningLevels:         raw.ReasoningLevels,
		ReasoningMode:           raw.ReasoningMode,
		MaxOutputTok:            raw.MaxOutputTok,
		Timeout:                 raw.Timeout,
		StreamFirstEventTimeout: raw.StreamFirstEventTimeout,
	})
	if configured.ID == "" || configured.ProviderEndpointID == "" {
		return modelconfig.Config{}, modelconfig.ProviderEndpointConfig{}, credentialstore.Source{}, false, false, nil
	}
	ref, source, hasSource, err := prepareLegacyCredential(
		configured.Provider,
		configured.ProviderEndpointID,
		raw.CredentialRef,
		raw.Token,
		raw.TokenEnv,
	)
	if err != nil {
		return modelconfig.Config{}, modelconfig.ProviderEndpointConfig{}, credentialstore.Source{}, false, false, err
	}
	configured.CredentialRef = ref
	endpoint := modelconfig.ProviderEndpointFromConfig(configured)
	endpoint.CredentialRef = ref
	endpoint = modelconfig.SanitizePersistedProviderEndpoint(endpoint)
	configured = modelconfig.MergeConfigProviderEndpoint(configured, endpoint)
	return configured, endpoint, source, hasSource, true, nil
}

func legacyEndpointsCompatible(left, right modelconfig.ProviderEndpointConfig) bool {
	left = modelconfig.NormalizeProviderEndpoint(left)
	right = modelconfig.NormalizeProviderEndpoint(right)
	return left.ID == right.ID &&
		left.Provider == right.Provider &&
		left.EndpointID == right.EndpointID &&
		left.API == right.API &&
		normalizeLegacyURL(left.BaseURL) == normalizeLegacyURL(right.BaseURL)
}

func normalizeLegacyURL(value string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(value)), "/")
}

func migrateLegacyACP(legacy legacyAgentConfiguration, report *MigrationReport) (
	controlagents.Configuration,
	[]modelprofile.ModelProfile,
	map[string]string,
) {
	connectionsByID := map[string]controlagents.Connection{}
	for index, raw := range legacy.Connections {
		connection := controlagents.NormalizeConnection(raw)
		if err := controlagents.ValidateConnection(connection); err != nil || !controlagents.IsName(connection.ID) {
			report.addDrop("external_connection", fmt.Sprintf("agent_roster.connections[%d]", index), "invalid_record")
			continue
		}
		if previous, exists := connectionsByID[connection.ID]; exists {
			if !reflect.DeepEqual(previous, connection) {
				report.addDrop("external_connection", fmt.Sprintf("agent_roster.connections[%d]", index), "conflicting_identity")
				continue
			}
			continue
		}
		connectionsByID[connection.ID] = connection
	}

	out := controlagents.Configuration{}
	for _, connection := range connectionsByID {
		agent := controlagents.Agent{ID: connection.ID, Name: connection.Name, ConnectionID: connection.ID}
		if err := controlagents.ValidateAgent(agent, []controlagents.Connection{connection}); err != nil {
			report.addDrop("external_agent", "external_agents", "invalid_record")
			continue
		}
		out.Connections = append(out.Connections, connection)
		out.Agents = append(out.Agents, agent)
	}
	validConnections := make(map[string]controlagents.Connection, len(out.Connections))
	for _, connection := range out.Connections {
		validConnections[connection.ID] = connection
	}
	for index, raw := range legacy.Discoveries {
		snapshot := controlagents.NormalizeDiscoverySnapshot(raw)
		connection, ok := validConnections[snapshot.ConnectionID]
		if !ok {
			report.addDrop("acp_discovery", fmt.Sprintf("agent_roster.discoveries[%d]", index), "missing_connection")
			continue
		}
		if !legacyDiscoveryUsable(snapshot) {
			report.addDrop("acp_discovery", fmt.Sprintf("agent_roster.discoveries[%d]", index), "invalid_or_ambiguous_discovery")
			continue
		}
		if snapshot.LaunchFingerprint == "" {
			snapshot.LaunchFingerprint = controlagents.LaunchFingerprint(connection.Launcher)
		}
		out.Discoveries = append(out.Discoveries, snapshot)
	}
	out = controlagents.NormalizeConfiguration(out)

	profilesByID := map[string]modelprofile.ModelProfile{}
	oldAgentProfiles := map[string]string{}
	for index, oldAgent := range legacy.Agents {
		connectionID := strings.ToLower(strings.TrimSpace(oldAgent.Backing.ConnectionID))
		if connectionID == "" {
			if strings.TrimSpace(oldAgent.Backing.ModelAlias) == "" {
				report.addDrop("acp_agent", fmt.Sprintf("agent_roster.agents[%d]", index), "missing_backend")
			}
			continue
		}
		connection, ok := validConnections[connectionID]
		if !ok {
			report.addDrop("acp_agent", fmt.Sprintf("agent_roster.agents[%d]", index), "missing_connection")
			continue
		}
		agent, ok := controlagents.LookupAgent(out, connectionID)
		if !ok {
			report.addDrop("acp_agent", fmt.Sprintf("agent_roster.agents[%d]", index), "missing_agent")
			continue
		}
		remote, discovery, ok := resolveLegacyACPModel(oldAgent, out.Discoveries)
		if !ok {
			report.addDrop("acp_agent", fmt.Sprintf("agent_roster.agents[%d]", index), "ambiguous_discovery")
			continue
		}
		profile, err := profilebuilder.FromACP(
			agent,
			connection,
			remote,
			controlagents.SessionOptions{
				ModelID:                 oldAgent.Defaults.ModelID,
				ConfigValues:            oldAgent.Defaults.ConfigValues,
				ReasoningEffortConfigID: oldAgent.Defaults.ReasoningEffortConfigID,
			},
			discovery,
		)
		if err != nil {
			report.addDrop("acp_agent", fmt.Sprintf("agent_roster.agents[%d]", index), "profile_build_failed")
			continue
		}
		if previous, exists := profilesByID[profile.ID]; exists && !reflect.DeepEqual(previous, profile) {
			report.addDrop("acp_agent", fmt.Sprintf("agent_roster.agents[%d]", index), "conflicting_profile")
			continue
		}
		profilesByID[profile.ID] = profile
		if oldID := controlagents.NormalizeName(oldAgent.ID); oldID != "" {
			oldAgentProfiles[oldID] = profile.ID
		}
	}
	profiles := make([]modelprofile.ModelProfile, 0, len(profilesByID))
	for _, profile := range profilesByID {
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
	return out, profiles, oldAgentProfiles
}

func legacyDiscoveryUsable(snapshot controlagents.DiscoverySnapshot) bool {
	if snapshot.SelectedModelID == "" && snapshot.CurrentModelID == "" && len(snapshot.Models) == 0 {
		return false
	}
	seenOptions := map[string]struct{}{}
	effortOptions := 0
	for _, option := range snapshot.ConfigOptions {
		id := strings.ToLower(strings.TrimSpace(option.ID))
		if id == "" {
			return false
		}
		if _, exists := seenOptions[id]; exists {
			return false
		}
		seenOptions[id] = struct{}{}
		if option.Purpose == controlagents.ConfigOptionPurposeReasoningEffort {
			effortOptions++
		}
	}
	return effortOptions <= 1
}

func resolveLegacyACPModel(
	agent legacyAgent,
	discoveries []controlagents.DiscoverySnapshot,
) (controlagents.RemoteModel, controlagents.DiscoverySnapshot, bool) {
	connectionID := strings.ToLower(strings.TrimSpace(agent.Backing.ConnectionID))
	modelID := strings.TrimSpace(agent.Defaults.ModelID)
	if modelID == "" {
		models := map[string]struct{}{}
		for _, snapshot := range discoveries {
			if snapshot.ConnectionID != connectionID {
				continue
			}
			candidate := firstNonEmptyString(snapshot.SelectedModelID, snapshot.CurrentModelID)
			if candidate != "" {
				models[candidate] = struct{}{}
			}
		}
		if len(models) != 1 {
			return controlagents.RemoteModel{}, controlagents.DiscoverySnapshot{}, false
		}
		for candidate := range models {
			modelID = candidate
		}
	}
	var candidates []controlagents.DiscoverySnapshot
	for _, snapshot := range discoveries {
		if snapshot.ConnectionID != connectionID || !legacyDiscoveryHasModel(snapshot, modelID) {
			continue
		}
		candidates = append(candidates, snapshot)
	}
	if modelID == "" || len(candidates) == 0 {
		return controlagents.RemoteModel{}, controlagents.DiscoverySnapshot{}, false
	}
	signature := legacyDiscoveryPlacementSignature(candidates[0])
	for _, candidate := range candidates[1:] {
		if legacyDiscoveryPlacementSignature(candidate) != signature {
			return controlagents.RemoteModel{}, controlagents.DiscoverySnapshot{}, false
		}
	}
	remote := controlagents.RemoteModel{ID: modelID, Name: modelID}
	for _, candidate := range candidates[0].Models {
		if candidate.ID == modelID {
			remote = candidate
			break
		}
	}
	return remote, candidates[0], true
}

func legacyDiscoveryHasModel(snapshot controlagents.DiscoverySnapshot, modelID string) bool {
	if snapshot.SelectedModelID == modelID || snapshot.CurrentModelID == modelID {
		return true
	}
	for _, remote := range snapshot.Models {
		if remote.ID == modelID {
			return true
		}
	}
	return false
}

func legacyDiscoveryPlacementSignature(snapshot controlagents.DiscoverySnapshot) string {
	payload, _ := json.Marshal(struct {
		ConfigOptions []controlagents.ConfigOption `json:"config_options"`
		ModelControl  controlagents.ModelControl   `json:"model_control"`
	}{
		ConfigOptions: snapshot.ConfigOptions,
		ModelControl:  snapshot.ModelControl,
	})
	return string(payload)
}

func migrateLegacyBindings(
	delegation legacyDelegationConfiguration,
	system legacySystemConfiguration,
	oldAgentProfiles map[string]string,
	profiles modelprofile.Configuration,
	report *MigrationReport,
) agentbinding.Configuration {
	out := agentbinding.Configuration{}
	seen := map[agentbinding.Handle]struct{}{}
	bind := func(handle agentbinding.Handle, oldAgentID, effort, category, identity string) {
		handle = agentbinding.NormalizeHandle(handle)
		if _, exists := seen[handle]; exists {
			report.addDrop(category, identity, "duplicate_handle")
			return
		}
		profileID := oldAgentProfiles[controlagents.NormalizeName(oldAgentID)]
		profile, ok := modelprofile.Lookup(profiles, profileID)
		if !ok {
			report.addDrop(category, identity, "unresolved_profile")
			return
		}
		effort = firstNonEmptyString(effort, profile.Effort.DefaultEffort)
		next, err := agentbinding.Bind(out, agentbinding.Binding{
			Handle: handle, ProfileID: profile.ID, Effort: effort,
		}, profiles)
		if err != nil {
			report.addDrop(category, identity, "invalid_effort")
			return
		}
		out = next
		seen[handle] = struct{}{}
	}
	for index, raw := range delegation.Bindings {
		handle := agentbinding.Handle(strings.ToLower(strings.TrimSpace(string(raw.Handle))))
		target := strings.ToLower(strings.TrimSpace(string(raw.Target)))
		if target != string(legacyDelegationTargetAgent) {
			continue
		}
		switch handle {
		case agentbinding.HandleBreeze, agentbinding.HandleOrbit, agentbinding.HandleZenith:
			bind(handle, raw.AgentID, raw.ReasoningEffort, "delegation_binding", fmt.Sprintf("delegation.bindings[%d]", index))
		}
	}
	for index, raw := range system.Bindings {
		handle := agentbinding.Handle(strings.ToLower(strings.TrimSpace(string(raw.Handle))))
		switch handle {
		case agentbinding.HandleGuardian, agentbinding.HandleReviewer:
			bind(handle, raw.AgentID, raw.ReasoningEffort, "system_binding", fmt.Sprintf("system_agents.bindings[%d]", index))
		}
	}
	return agentbinding.NormalizeConfiguration(out)
}

func prepareLegacyCredential(provider, endpointID, existingRef, token, environment string) (
	string,
	credentialstore.Source,
	bool,
	error,
) {
	existingRef = strings.ToLower(strings.TrimSpace(existingRef))
	token = strings.TrimSpace(token)
	environment = strings.TrimSpace(environment)
	if token != "" && environment != "" {
		return "", credentialstore.Source{}, false, fmt.Errorf("gatewayapp: migrate provider credential has conflicting inline and environment sources")
	}
	if token == "" && environment == "" {
		return existingRef, credentialstore.Source{}, false, nil
	}
	if existingRef != "" && !strings.HasPrefix(existingRef, "apikey:") {
		return existingRef, credentialstore.Source{}, false, nil
	}
	ref := existingRef
	if ref == "" {
		ref = credentialstore.BuildReference(provider, endpointID)
	}
	if ref == "" {
		return "", credentialstore.Source{}, false, fmt.Errorf("gatewayapp: cannot derive an opaque credential reference")
	}
	if token != "" {
		return ref, credentialstore.Source{APIKey: token}, true, nil
	}
	return ref, credentialstore.Source{Environment: environment}, true, nil
}

func mergeLegacyCredentialSource(byRef map[string]credentialstore.Source, ref string, source credentialstore.Source) error {
	if previous, ok := byRef[ref]; ok && previous != source {
		return fmt.Errorf("gatewayapp: migrate provider credentials: conflicting sources for %q", ref)
	}
	byRef[ref] = source
	return nil
}

func rejectPersistedProviderCredentials(models PersistedModelConfig) error {
	for _, endpoint := range models.ProviderEndpoints {
		if strings.TrimSpace(endpoint.Token) != "" || strings.TrimSpace(endpoint.TokenEnv) != "" || endpoint.PersistToken {
			return fmt.Errorf("gatewayapp: provider endpoint %q must keep credentials only in the Control credential store", strings.TrimSpace(endpoint.ID))
		}
	}
	for _, configured := range models.Configs {
		if strings.TrimSpace(configured.Token) != "" || strings.TrimSpace(configured.TokenEnv) != "" || configured.PersistToken {
			return fmt.Errorf("gatewayapp: provider model %q must keep credentials only in the Control credential store", strings.TrimSpace(configured.ID))
		}
	}
	return nil
}

func legacyDocumentHasSafeContent(doc AppConfig) bool {
	return len(doc.Models.ProviderEndpoints) > 0 ||
		len(doc.Models.Configs) > 0 ||
		len(doc.ExternalAgents.Connections) > 0 ||
		len(doc.ExternalAgents.Agents) > 0 ||
		len(doc.ModelProfiles.Profiles) > 0 ||
		len(doc.AgentBindings.Bindings) > 0 ||
		doc.Sandbox.RequestedType != "" ||
		doc.Sandbox.HelperPath != "" ||
		len(doc.Sandbox.ReadableRoots) > 0 ||
		len(doc.Sandbox.WritableRoots) > 0 ||
		len(doc.Sandbox.ReadOnlySubpaths) > 0 ||
		doc.Sandbox.NetworkEnabled != nil ||
		doc.Runtime.ApprovalMode != "auto-review" ||
		doc.Runtime.PolicyProfile != "" ||
		len(doc.Plugins) > 0 ||
		len(doc.PluginMarketplaces) > 0
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
