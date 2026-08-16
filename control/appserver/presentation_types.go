package appserver

import "time"

// ResumeCandidate is the product-owned Session list projection rendered by
// presentation clients.
type ResumeCandidate struct {
	SessionID string    `json:"session_id"`
	Title     string    `json:"title,omitempty"`
	Prompt    string    `json:"prompt,omitempty"`
	Model     string    `json:"model,omitempty"`
	Workspace string    `json:"workspace,omitempty"`
	Age       string    `json:"age,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type CompletionCandidate struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Path    string `json:"path,omitempty"`
}

type SlashArgCandidate struct {
	Value                 string `json:"value"`
	Display               string `json:"display,omitempty"`
	Detail                string `json:"detail,omitempty"`
	NoAuth                bool   `json:"no_auth,omitempty"`
	ModelMetadataComplete bool   `json:"model_metadata_complete,omitempty"`
	ModelImageInputKnown  bool   `json:"model_image_input_known,omitempty"`
}

// SkillResolveResult distinguishes a canonical skill match from an ambiguous
// set of candidates. An empty result means the reference was not found.
type SkillResolveResult struct {
	Canonical string   `json:"canonical,omitempty"`
	Matches   []string `json:"matches,omitempty"`
}

type AgentCandidate struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type AgentParticipantSnapshot struct {
	ID        string `json:"id"`
	Label     string `json:"label,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Role      string `json:"role,omitempty"`
	Source    string `json:"source,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type AgentStatusSnapshot struct {
	SessionID                 string                     `json:"session_id,omitempty"`
	ControllerKind            string                     `json:"controller_kind,omitempty"`
	ControllerLabel           string                     `json:"controller_label,omitempty"`
	ControllerEpoch           string                     `json:"controller_epoch,omitempty"`
	ControllerModel           string                     `json:"controller_model,omitempty"`
	ControllerReasoningEffort string                     `json:"controller_reasoning_effort,omitempty"`
	ControllerCommands        []string                   `json:"controller_commands,omitempty"`
	ControllerModels          []SlashArgCandidate        `json:"controller_models,omitempty"`
	ControllerEfforts         []SlashArgCandidate        `json:"controller_efforts,omitempty"`
	HasActiveTurn             bool                       `json:"has_active_turn,omitempty"`
	ActiveTurnKind            string                     `json:"active_turn_kind,omitempty"`
	AvailableAgents           []AgentCandidate           `json:"available_agents,omitempty"`
	Participants              []AgentParticipantSnapshot `json:"participants,omitempty"`
	DelegatedParticipants     []AgentParticipantSnapshot `json:"delegated_participants,omitempty"`
}

// ConnectConfig is the transport-neutral model provider connection request.
type ConnectConfig struct {
	Provider                       string   `json:"provider"`
	EndpointID                     string   `json:"endpoint_id,omitempty"`
	Model                          string   `json:"model"`
	BaseURL                        string   `json:"base_url,omitempty"`
	TimeoutSeconds                 int      `json:"timeout_seconds,omitempty"`
	StreamFirstEventTimeoutSeconds int      `json:"stream_first_event_timeout_seconds,omitempty"`
	APIKey                         string   `json:"api_key,omitempty"`
	AuthType                       string   `json:"auth_type,omitempty"`
	ContextWindowTokens            int      `json:"context_window_tokens,omitempty"`
	MaxOutputTokens                int      `json:"max_output_tokens,omitempty"`
	ReasoningEffort                string   `json:"reasoning_effort,omitempty"`
	ReasoningLevels                []string `json:"reasoning_levels,omitempty"`
	ImageInput                     *bool    `json:"image_input,omitempty"`
}

type MCPServerSnapshot struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Tools   []string `json:"tools,omitempty"`
	Warning string   `json:"warning,omitempty"`
}

type PluginSnapshot struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Version     string              `json:"version"`
	Description string              `json:"description"`
	Root        string              `json:"root"`
	Enabled     bool                `json:"enabled"`
	Skills      []string            `json:"skills"`
	Hooks       []string            `json:"hooks"`
	Agents      []string            `json:"agents,omitempty"`
	MCPServers  []MCPServerSnapshot `json:"mcp_servers,omitempty"`
	Status      string              `json:"status"`
	Warning     string              `json:"warning,omitempty"`
}

type MarketplaceSnapshot struct {
	Name                              string   `json:"name"`
	Description                       string   `json:"description,omitempty"`
	Owner                             string   `json:"owner,omitempty"`
	Source                            string   `json:"source,omitempty"`
	Root                              string   `json:"root,omitempty"`
	Version                           string   `json:"version,omitempty"`
	PluginRoot                        string   `json:"plugin_root,omitempty"`
	AllowCrossMarketplaceDependencies []string `json:"allow_cross_marketplace_dependencies,omitempty"`
	PluginCount                       int      `json:"plugin_count,omitempty"`
}
