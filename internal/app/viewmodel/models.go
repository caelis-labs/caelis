package viewmodel

type ModelSelectionView struct {
	Current       *ModelChoice           `json:"current,omitempty"`
	Configured    []ModelChoice          `json:"configured,omitempty"`
	Providers     []ModelProviderOption  `json:"providers,omitempty"`
	Candidates    []ModelCandidate       `json:"candidates,omitempty"`
	Actions       []ModelSelectionAction `json:"actions,omitempty"`
	Provider      string                 `json:"provider,omitempty"`
	DiscoveryErr  string                 `json:"discovery_error,omitempty"`
	RemoteEnabled bool                   `json:"remote_enabled,omitempty"`
}

type ModelSelectionAction struct {
	ID            string `json:"id,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Label         string `json:"label,omitempty"`
	ModelID       string `json:"model_id,omitempty"`
	Command       string `json:"command,omitempty"`
	Enabled       bool   `json:"enabled"`
	Destructive   bool   `json:"destructive,omitempty"`
	RequiresInput bool   `json:"requires_input,omitempty"`
}

type ModelConnectView struct {
	Current     *ModelChoice             `json:"current,omitempty"`
	Configured  []ModelChoice            `json:"configured,omitempty"`
	Providers   []ModelConnectProvider   `json:"providers,omitempty"`
	Wizard      WizardFlowView           `json:"wizard,omitempty"`
	Diagnostics []ModelConnectDiagnostic `json:"diagnostics,omitempty"`
}

type ModelConnectProvider struct {
	ID                   string                 `json:"id,omitempty"`
	Label                string                 `json:"label,omitempty"`
	Provider             string                 `json:"provider,omitempty"`
	API                  string                 `json:"api,omitempty"`
	Command              string                 `json:"command,omitempty"`
	Description          string                 `json:"description,omitempty"`
	DefaultBaseURL       string                 `json:"default_base_url,omitempty"`
	DefaultEndpointID    string                 `json:"default_endpoint_id,omitempty"`
	TokenEnv             string                 `json:"token_env,omitempty"`
	NoAuthRequired       bool                   `json:"no_auth_required,omitempty"`
	Configured           bool                   `json:"configured,omitempty"`
	ConfiguredModelCount int                    `json:"configured_model_count,omitempty"`
	CatalogModelCount    int                    `json:"catalog_model_count,omitempty"`
	CommonModels         []string               `json:"common_models,omitempty"`
	Endpoints            []ModelConnectEndpoint `json:"endpoints,omitempty"`
}

type ModelConnectEndpoint struct {
	ID           string `json:"id,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	Display      string `json:"display,omitempty"`
	Detail       string `json:"detail,omitempty"`
	API          string `json:"api,omitempty"`
	TokenEnv     string `json:"token_env,omitempty"`
	NoAuth       bool   `json:"no_auth,omitempty"`
	ReusableAuth bool   `json:"reusable_auth,omitempty"`
}

type ModelConnectDiagnostic struct {
	Severity string `json:"severity,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Provider string `json:"provider,omitempty"`
	Message  string `json:"message,omitempty"`
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

type PromptCapabilitiesView struct {
	Audio           bool `json:"audio,omitempty"`
	EmbeddedContext bool `json:"embedded_context,omitempty"`
	Image           bool `json:"image,omitempty"`
}
