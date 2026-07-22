package configstore

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	controlagents "github.com/caelis-labs/caelis/control/agents"
)

// The types in this file are the complete private wire contract for the
// one-way pre-v2 migration. They intentionally have no behavior and must never
// be used by current product code.

type legacyAppConfig struct {
	Models             legacyPersistedModelConfig    `json:"models,omitempty"`
	AgentRoster        legacyAgentConfiguration      `json:"agent_roster,omitempty"`
	Delegation         legacyDelegationConfiguration `json:"delegation,omitempty"`
	SystemAgents       legacySystemConfiguration     `json:"system_agents,omitempty"`
	Sandbox            SandboxConfig                 `json:"sandbox,omitempty"`
	Runtime            RuntimeConfig                 `json:"runtime,omitempty"`
	Plugins            []PluginConfig                `json:"plugins,omitempty"`
	PluginMarketplaces []MarketplaceConfig           `json:"plugin_marketplaces,omitempty"`
}

func (c *legacyAppConfig) UnmarshalJSON(data []byte) error {
	decoded, err := decodeLegacyWireAppConfig(data)
	if err != nil {
		return err
	}
	*c = decoded
	return nil
}

type legacyPersistedModelConfig struct {
	DefaultAlias string                   `json:"default_alias,omitempty"`
	DefaultID    string                   `json:"default_model_id,omitempty"`
	Profiles     []legacyProviderEndpoint `json:"profiles,omitempty"`
	Configs      []legacyModelConfig      `json:"configs,omitempty"`
}

func (c *legacyPersistedModelConfig) UnmarshalJSON(data []byte) error {
	object, err := decodeLegacyObject(data)
	if err != nil {
		return err
	}
	*c = decodeLegacyModels(object, nil)
	return nil
}

type legacyProviderEndpoint struct {
	ID                      string             `json:"id,omitempty"`
	Provider                string             `json:"provider,omitempty"`
	EndpointID              string             `json:"endpoint_id,omitempty"`
	API                     providers.APIType  `json:"api,omitempty"`
	BaseURL                 string             `json:"base_url,omitempty"`
	Token                   string             `json:"token,omitempty"`
	TokenEnv                string             `json:"token_env,omitempty"`
	CredentialRef           string             `json:"credential_ref,omitempty"`
	PersistToken            bool               `json:"persist_token,omitempty"`
	AuthType                providers.AuthType `json:"auth_type,omitempty"`
	HeaderKey               string             `json:"header_key,omitempty"`
	Timeout                 time.Duration      `json:"timeout,omitempty"`
	StreamFirstEventTimeout time.Duration      `json:"stream_first_event_timeout,omitempty"`
}

type legacyModelConfig struct {
	ID                      string             `json:"id,omitempty"`
	Alias                   string             `json:"alias,omitempty"`
	Provider                string             `json:"provider,omitempty"`
	ProfileID               string             `json:"profile_id,omitempty"`
	EndpointID              string             `json:"endpoint_id,omitempty"`
	API                     providers.APIType  `json:"api,omitempty"`
	Model                   string             `json:"model,omitempty"`
	BaseURL                 string             `json:"base_url,omitempty"`
	Token                   string             `json:"token,omitempty"`
	TokenEnv                string             `json:"token_env,omitempty"`
	CredentialRef           string             `json:"credential_ref,omitempty"`
	PersistToken            bool               `json:"persist_token,omitempty"`
	AuthType                providers.AuthType `json:"auth_type,omitempty"`
	HeaderKey               string             `json:"header_key,omitempty"`
	ContextWindowTokens     int                `json:"context_window_tokens,omitempty"`
	ReasoningEffort         string             `json:"reasoning_effort,omitempty"`
	DefaultReasoningEffort  string             `json:"default_reasoning_effort,omitempty"`
	ReasoningLevels         []string           `json:"reasoning_levels,omitempty"`
	ReasoningMode           string             `json:"reasoning_mode,omitempty"`
	MaxOutputTok            int                `json:"max_output_tokens,omitempty"`
	Timeout                 time.Duration      `json:"timeout,omitempty"`
	StreamFirstEventTimeout time.Duration      `json:"stream_first_event_timeout,omitempty"`
}

type legacyAgentConfiguration struct {
	Connections []controlagents.Connection        `json:"connections,omitempty"`
	Agents      []legacyAgent                     `json:"agents,omitempty"`
	Discoveries []controlagents.DiscoverySnapshot `json:"discoveries,omitempty"`
}

type legacyAgent struct {
	ID       string               `json:"id,omitempty"`
	Name     string               `json:"name,omitempty"`
	Backing  legacyAgentBacking   `json:"backing,omitempty"`
	Defaults legacySessionOptions `json:"defaults,omitempty"`
}

type legacyAgentBacking struct {
	ModelAlias   string `json:"model_alias,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
}

type legacySessionOptions struct {
	ModelID                 string            `json:"model_id,omitempty"`
	ConfigValues            map[string]string `json:"config_values,omitempty"`
	ReasoningEffortConfigID string            `json:"reasoning_effort_config_id,omitempty"`
}

type legacyDelegationHandle string

const (
	legacyDelegationSelf   legacyDelegationHandle = "self"
	legacyDelegationBreeze legacyDelegationHandle = "breeze"
	legacyDelegationOrbit  legacyDelegationHandle = "orbit"
	legacyDelegationZenith legacyDelegationHandle = "zenith"
)

type legacyDelegationTarget string

const (
	legacyDelegationTargetSelf  legacyDelegationTarget = "self"
	legacyDelegationTargetAgent legacyDelegationTarget = "agent"
)

type legacyDelegationBinding struct {
	Handle          legacyDelegationHandle `json:"profile,omitempty"`
	Target          legacyDelegationTarget `json:"target,omitempty"`
	AgentID         string                 `json:"agent_id,omitempty"`
	ReasoningEffort string                 `json:"reasoning_effort,omitempty"`
}

type legacyDelegationConfiguration struct {
	Bindings []legacyDelegationBinding `json:"bindings,omitempty"`
}

type legacySystemHandle string

const (
	legacySystemGuardian legacySystemHandle = "guardian"
	legacySystemReviewer legacySystemHandle = "reviewer"
)

type legacySystemBinding struct {
	Handle          legacySystemHandle `json:"id,omitempty"`
	AgentID         string             `json:"agent_id,omitempty"`
	ReasoningEffort string             `json:"reasoning_effort,omitempty"`
}

type legacySystemConfiguration struct {
	Bindings []legacySystemBinding `json:"bindings,omitempty"`
}

func decodeLegacyWireAppConfig(data []byte) (legacyAppConfig, error) {
	decoded, _, err := decodeLegacyWireAppConfigWithReport(data)
	return decoded, err
}

func decodeLegacyWireAppConfigWithReport(data []byte) (legacyAppConfig, MigrationReport, error) {
	root, err := decodeLegacyObject(data)
	if err != nil {
		return legacyAppConfig{}, MigrationReport{}, err
	}
	var decoded legacyAppConfig
	report := MigrationReport{}
	if models, ok := decodeLegacyNestedObject(root["models"], "models", &report); ok {
		decoded.Models = decodeLegacyModels(models, &report)
	}
	if roster, ok := decodeLegacyNestedObject(root["agent_roster"], "agent_roster", &report); ok {
		decoded.AgentRoster = decodeLegacyAgentRoster(roster, &report)
	}
	if delegation, ok := decodeLegacyNestedObject(root["delegation"], "delegation", &report); ok {
		decoded.Delegation.Bindings = decodeLegacyRecords[legacyDelegationBinding](delegation["bindings"], "delegation.bindings", &report)
	}
	if system, ok := decodeLegacyNestedObject(root["system_agents"], "system_agents", &report); ok {
		decoded.SystemAgents.Bindings = decodeLegacyRecords[legacySystemBinding](system["bindings"], "system_agents.bindings", &report)
	}
	decodeLegacyValue(root["sandbox"], &decoded.Sandbox, "sandbox", &report)
	decodeLegacyValue(root["runtime"], &decoded.Runtime, "runtime", &report)
	decoded.Plugins = decodeLegacyRecords[PluginConfig](root["plugins"], "plugins", &report)
	decoded.PluginMarketplaces = decodeLegacyRecords[MarketplaceConfig](root["plugin_marketplaces"], "plugin_marketplaces", &report)
	return decoded, report, nil
}

func decodeLegacyObject(data []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	return object, nil
}

func decodeLegacyNestedObject(data json.RawMessage, category string, report *MigrationReport) (map[string]json.RawMessage, bool) {
	if len(data) == 0 {
		return nil, false
	}
	object, err := decodeLegacyObject(data)
	if err != nil || object == nil {
		report.addDrop(category, category, "malformed_object")
		return nil, false
	}
	return object, true
}

func decodeLegacyModels(object map[string]json.RawMessage, report *MigrationReport) legacyPersistedModelConfig {
	var decoded legacyPersistedModelConfig
	decodeLegacyValue(object["default_alias"], &decoded.DefaultAlias, "models.default_alias", report)
	decodeLegacyValue(object["default_model_id"], &decoded.DefaultID, "models.default_model_id", report)
	decoded.Profiles = decodeLegacyRecords[legacyProviderEndpoint](object["profiles"], "models.profiles", report)
	decoded.Configs = decodeLegacyRecords[legacyModelConfig](object["configs"], "models.configs", report)
	return decoded
}

func decodeLegacyAgentRoster(object map[string]json.RawMessage, report *MigrationReport) legacyAgentConfiguration {
	return legacyAgentConfiguration{
		Connections: decodeLegacyRecords[controlagents.Connection](object["connections"], "agent_roster.connections", report),
		Agents:      decodeLegacyRecords[legacyAgent](object["agents"], "agent_roster.agents", report),
		Discoveries: decodeLegacyRecords[controlagents.DiscoverySnapshot](object["discoveries"], "agent_roster.discoveries", report),
	}
}

func decodeLegacyRecords[T any](data json.RawMessage, category string, report *MigrationReport) []T {
	if len(data) == 0 {
		return nil
	}
	var records []json.RawMessage
	if err := json.Unmarshal(data, &records); err != nil {
		report.addDrop(category, category, "malformed_collection")
		return nil
	}
	decoded := make([]T, 0, len(records))
	for index, record := range records {
		var value T
		if err := json.Unmarshal(record, &value); err != nil {
			report.addDrop(category, fmt.Sprintf("%s[%d]", category, index), "malformed_record")
			continue
		}
		decoded = append(decoded, value)
	}
	return decoded
}

func decodeLegacyValue(data json.RawMessage, out any, category string, report *MigrationReport) {
	if len(data) == 0 {
		return
	}
	if err := json.Unmarshal(data, out); err != nil {
		report.addDrop(category, category, "malformed_value")
	}
}
