package configstore

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/plugin"
)

// SchemaVersionV2 is the first AppConfig schema with one ModelProfile catalog
// and one handle-binding table.
const SchemaVersionV2 = 2

// AppConfig is the current persisted application configuration. Retired roster,
// delegation, and system-Agent fields exist only in the private legacy wire
// document used by the one-way migration.
type AppConfig struct {
	SchemaVersion      int                         `json:"schema_version"`
	Models             PersistedModelConfig        `json:"models,omitempty"`
	ExternalAgents     controlagents.Configuration `json:"external_agents,omitempty"`
	ModelProfiles      modelprofile.Configuration  `json:"model_profiles,omitempty"`
	AgentBindings      agentbinding.Configuration  `json:"agent_bindings,omitempty"`
	Sandbox            SandboxConfig               `json:"sandbox,omitempty"`
	Runtime            RuntimeConfig               `json:"runtime,omitempty"`
	Plugins            []PluginConfig              `json:"plugins,omitempty"`
	PluginMarketplaces []MarketplaceConfig         `json:"plugin_marketplaces,omitempty"`
}

// Validate checks the current persisted truth without reconstructing legacy
// Agent or binding projections.
func Validate(doc AppConfig) error {
	if doc.SchemaVersion != SchemaVersionV2 {
		return fmt.Errorf("gatewayapp: unsupported AppConfig schema version %d", doc.SchemaVersion)
	}
	if err := rejectPersistedProviderCredentials(doc.Models); err != nil {
		return err
	}
	if err := controlagents.ValidateConfiguration(doc.ExternalAgents); err != nil {
		return fmt.Errorf("gatewayapp: invalid external Agents: %w", err)
	}
	agentByConnection := make(map[string]string, len(doc.ExternalAgents.Agents))
	for _, agent := range controlagents.ListAgents(doc.ExternalAgents) {
		connectionID := strings.TrimSpace(agent.ConnectionID)
		if connectionID == "" {
			return fmt.Errorf("gatewayapp: external Agent %q must reference an ACP connection", agent.ID)
		}
		if previous := agentByConnection[connectionID]; previous != "" {
			return fmt.Errorf("gatewayapp: ACP connection %q has multiple Agent identities %q and %q", connectionID, previous, agent.ID)
		}
		agentByConnection[connectionID] = agent.ID
	}
	if err := modelprofile.ValidateConfiguration(doc.ModelProfiles); err != nil {
		return fmt.Errorf("gatewayapp: invalid model profiles: %w", err)
	}
	if err := agentbinding.ValidateConfiguration(doc.AgentBindings, doc.ModelProfiles); err != nil {
		return fmt.Errorf("gatewayapp: invalid Agent bindings: %w", err)
	}

	endpointIDs := make(map[string]struct{}, len(doc.Models.ProviderEndpoints))
	for _, raw := range doc.Models.ProviderEndpoints {
		endpoint := modelconfig.NormalizeProviderEndpoint(raw)
		if endpoint.ID == "" || endpoint.Provider == "" {
			return fmt.Errorf("gatewayapp: provider endpoint requires an ID and provider")
		}
		if _, exists := endpointIDs[endpoint.ID]; exists {
			return fmt.Errorf("gatewayapp: duplicate provider endpoint %q", endpoint.ID)
		}
		endpointIDs[endpoint.ID] = struct{}{}
	}
	modelIDs := make(map[string]struct{}, len(doc.Models.Configs))
	for _, raw := range doc.Models.Configs {
		configured := modelconfig.NormalizeConfig(raw)
		if configured.ID == "" || strings.TrimSpace(configured.Model) == "" {
			return fmt.Errorf("gatewayapp: provider model requires an ID and model")
		}
		if _, exists := modelIDs[configured.ID]; exists {
			return fmt.Errorf("gatewayapp: duplicate provider model %q", configured.ID)
		}
		modelIDs[configured.ID] = struct{}{}
		if _, ok := endpointIDs[configured.ProviderEndpointID]; !ok {
			return fmt.Errorf(
				"gatewayapp: provider model %q references unknown provider endpoint %q",
				configured.ID,
				configured.ProviderEndpointID,
			)
		}
	}
	if defaultID := strings.ToLower(strings.TrimSpace(doc.Models.DefaultID)); defaultID != "" {
		if _, ok := modelIDs[defaultID]; !ok {
			return fmt.Errorf("gatewayapp: default model references unknown model %q", defaultID)
		}
	}
	for _, profile := range modelprofile.NormalizeConfiguration(doc.ModelProfiles).Profiles {
		switch profile.Kind() {
		case modelprofile.BackendProvider:
			if _, ok := modelIDs[profile.Backend.Provider.ModelConfigID]; !ok {
				return fmt.Errorf("gatewayapp: provider profile %q references unknown model %q", profile.ID, profile.Backend.Provider.ModelConfigID)
			}
		case modelprofile.BackendACP:
			if _, ok := controlagents.LookupAgent(doc.ExternalAgents, profile.Backend.ACP.AgentID); !ok {
				return fmt.Errorf("gatewayapp: ACP profile %q references unknown external Agent %q", profile.ID, profile.Backend.ACP.AgentID)
			}
		}
	}
	return nil
}

// Normalize returns a detached deterministic current document after its raw
// current-schema record identities have been checked for conflicts.
func Normalize(doc AppConfig) AppConfig {
	doc.SchemaVersion = SchemaVersionV2
	doc.Models = normalizePersistedModelsForSave(doc.Models)
	doc.Models.Configs = dedupeModelConfigsForSave(doc.Models.Configs)
	doc.Models.ProviderEndpoints = dedupeProviderEndpointsForSave(doc.Models.ProviderEndpoints)
	doc.ExternalAgents = controlagents.NormalizeConfiguration(doc.ExternalAgents)
	doc.ModelProfiles = modelprofile.NormalizeConfiguration(doc.ModelProfiles)
	doc.AgentBindings = agentbinding.NormalizeConfiguration(doc.AgentBindings)
	doc.Sandbox = NormalizeSandboxConfig(doc.Sandbox)
	doc.Runtime = NormalizeRuntimeConfig(doc.Runtime)
	doc.Plugins = plugin.DedupeConfigs(doc.Plugins)
	doc.PluginMarketplaces = plugin.DedupeMarketplaceConfigs(doc.PluginMarketplaces)
	return doc
}

// validateCurrentRecordIdentities rejects collisions that the deterministic
// normalizers would otherwise resolve by keeping the first record. Full
// semantic validation still runs after normalization so current callers may
// use supported canonicalization such as provider-endpoint extraction.
func validateCurrentRecordIdentities(doc AppConfig) error {
	if err := rejectPersistedProviderCredentials(doc.Models); err != nil {
		return err
	}

	endpointIDs := make(map[string]struct{}, len(doc.Models.ProviderEndpoints))
	endpointsByID := make(map[string]modelconfig.ProviderEndpointConfig, len(doc.Models.ProviderEndpoints))
	for _, raw := range doc.Models.ProviderEndpoints {
		endpoint := modelconfig.SanitizePersistedProviderEndpoint(raw)
		id := endpoint.ID
		if id != "" && recordIdentityExists(endpointIDs, id) {
			return fmt.Errorf("gatewayapp: duplicate provider endpoint %q", id)
		}
		if id != "" {
			endpointsByID[id] = endpoint
		}
	}
	modelIDs := make(map[string]struct{}, len(doc.Models.Configs))
	for _, raw := range doc.Models.Configs {
		configured := modelconfig.NormalizeConfig(raw)
		id := configured.ID
		if id != "" && recordIdentityExists(modelIDs, id) {
			return fmt.Errorf("gatewayapp: duplicate provider model %q", id)
		}
		if !modelconfig.ConfigCarriesProviderEndpointFields(raw) {
			continue
		}
		endpoint := modelconfig.SanitizePersistedProviderEndpoint(modelconfig.ProviderEndpointFromConfig(raw))
		if endpoint.ID == "" {
			continue
		}
		if previous, ok := endpointsByID[endpoint.ID]; ok && !reflect.DeepEqual(previous, endpoint) {
			return fmt.Errorf("gatewayapp: provider model %q conflicts with provider endpoint %q", id, endpoint.ID)
		}
		endpointsByID[endpoint.ID] = endpoint
	}

	connectionIDs := make(map[string]struct{}, len(doc.ExternalAgents.Connections))
	for _, raw := range doc.ExternalAgents.Connections {
		id := controlagents.NormalizeConnection(raw).ID
		if id != "" && recordIdentityExists(connectionIDs, id) {
			return fmt.Errorf("gatewayapp: duplicate ACP connection %q", id)
		}
	}
	agentIDs := make(map[string]struct{}, len(doc.ExternalAgents.Agents))
	for _, raw := range doc.ExternalAgents.Agents {
		id := controlagents.NormalizeAgent(raw).ID
		if id != "" && recordIdentityExists(agentIDs, id) {
			return fmt.Errorf("gatewayapp: duplicate external Agent %q", id)
		}
	}
	discoveryIDs := make(map[string]struct{}, len(doc.ExternalAgents.Discoveries))
	for _, raw := range doc.ExternalAgents.Discoveries {
		discovery := controlagents.NormalizeDiscoverySnapshot(raw)
		id := strings.Join([]string{
			discovery.ConnectionID,
			discovery.LaunchFingerprint,
			discovery.CWD,
			discovery.SelectedModelID,
		}, "\x00")
		if discovery.ConnectionID != "" && recordIdentityExists(discoveryIDs, id) {
			return fmt.Errorf("gatewayapp: duplicate ACP discovery for connection %q", discovery.ConnectionID)
		}
	}

	profileIDs := make(map[string]struct{}, len(doc.ModelProfiles.Profiles))
	for _, raw := range doc.ModelProfiles.Profiles {
		id := modelprofile.NormalizeID(raw.ID)
		if id != "" && recordIdentityExists(profileIDs, id) {
			return fmt.Errorf("gatewayapp: duplicate model profile %q", id)
		}
	}
	bindingHandles := make(map[string]struct{}, len(doc.AgentBindings.Bindings))
	for _, raw := range doc.AgentBindings.Bindings {
		handle := string(agentbinding.NormalizeHandle(raw.Handle))
		if handle != "" && recordIdentityExists(bindingHandles, handle) {
			return fmt.Errorf("gatewayapp: duplicate Agent binding for handle %q", handle)
		}
	}

	pluginIDs := make(map[string]struct{}, len(doc.Plugins))
	for _, raw := range doc.Plugins {
		id := strings.ToLower(strings.TrimSpace(plugin.NormalizeConfig(raw).ID))
		if id != "" && recordIdentityExists(pluginIDs, id) {
			return fmt.Errorf("gatewayapp: duplicate plugin %q", id)
		}
	}
	marketplaceNames := make(map[string]struct{}, len(doc.PluginMarketplaces))
	for _, raw := range doc.PluginMarketplaces {
		name := strings.ToLower(strings.TrimSpace(plugin.NormalizeMarketplaceConfig(raw).Name))
		if name != "" && recordIdentityExists(marketplaceNames, name) {
			return fmt.Errorf("gatewayapp: duplicate plugin marketplace %q", name)
		}
	}
	return nil
}

func recordIdentityExists(seen map[string]struct{}, id string) bool {
	if _, ok := seen[id]; ok {
		return true
	}
	seen[id] = struct{}{}
	return false
}
