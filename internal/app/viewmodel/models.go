package viewmodel

type ModelSelectionView struct {
	Current       *ModelChoice          `json:"current,omitempty"`
	Configured    []ModelChoice         `json:"configured,omitempty"`
	Providers     []ModelProviderOption `json:"providers,omitempty"`
	Candidates    []ModelCandidate      `json:"candidates,omitempty"`
	Provider      string                `json:"provider,omitempty"`
	DiscoveryErr  string                `json:"discovery_error,omitempty"`
	RemoteEnabled bool                  `json:"remote_enabled,omitempty"`
}

type ModelProviderOption struct {
	ID                   string `json:"id,omitempty"`
	Name                 string `json:"name,omitempty"`
	Description          string `json:"description,omitempty"`
	Uses                 string `json:"uses,omitempty"`
	Builtin              bool   `json:"builtin,omitempty"`
	Plugin               bool   `json:"plugin,omitempty"`
	Configured           bool   `json:"configured,omitempty"`
	RemoteDiscovery      bool   `json:"remote_discovery,omitempty"`
	ConfiguredModelCount int    `json:"configured_model_count,omitempty"`
	CatalogModelCount    int    `json:"catalog_model_count,omitempty"`
}

type ModelCandidate struct {
	Provider          string          `json:"provider,omitempty"`
	Model             string          `json:"model,omitempty"`
	Configured        bool            `json:"configured,omitempty"`
	Catalog           bool            `json:"catalog,omitempty"`
	Remote            bool            `json:"remote,omitempty"`
	CapabilitiesKnown bool            `json:"capabilities_known,omitempty"`
	Capabilities      ModelCapability `json:"capabilities,omitempty"`
	ReasoningLevels   []string        `json:"reasoning_levels,omitempty"`
}

type ModelCapability struct {
	ContextWindowTokens    int      `json:"context_window_tokens,omitempty"`
	MaxOutputTokens        int      `json:"max_output_tokens,omitempty"`
	DefaultMaxOutputTokens int      `json:"default_max_output_tokens,omitempty"`
	SupportsImages         bool     `json:"supports_images,omitempty"`
	SupportsToolCalls      bool     `json:"supports_tool_calls,omitempty"`
	SupportsReasoning      bool     `json:"supports_reasoning,omitempty"`
	ReasoningMode          string   `json:"reasoning_mode,omitempty"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	SupportsJSONOutput     bool     `json:"supports_json_output,omitempty"`
}
