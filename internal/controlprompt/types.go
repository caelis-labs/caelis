package controlprompt

import (
	"time"

	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// SubmissionMode controls how a surface routes a user submission.
//
// The default mode starts a new turn. Surfaces that intend to steer a running
// turn must opt in with SubmissionModeActiveTurn; adapters must not infer that
// from ambient active-run state.
type SubmissionMode string

const (
	SubmissionModeDefault SubmissionMode = ""
	SubmissionModeOverlay SubmissionMode = "overlay"
	// SubmissionModeActiveTurn appends input to the currently running turn.
	SubmissionModeActiveTurn SubmissionMode = "active_turn"
)

// Attachment describes media associated with a prompt at a rune offset.
// Name is used as the display name and, for local surfaces, as the image path.
// ACP inline media should use MimeType and base64 Data instead of overloading Name.
type Attachment struct {
	Name     string
	Offset   int
	MimeType string
	Data     string
}

type Submission struct {
	Text        string
	DisplayText string
	Mode        SubmissionMode
	Attachments []Attachment
}

type SessionSnapshot struct {
	SessionID string
	Reconnect SessionReconnect
}

type ResumeCandidate struct {
	SessionID string
	Title     string
	Prompt    string
	Model     string
	Workspace string
	Age       string
	UpdatedAt time.Time
}

type CompletionCandidate struct {
	Value   string
	Display string
	Kind    string
	Detail  string
	Path    string
}

type SlashArgCandidate struct {
	Value                 string
	Display               string
	Detail                string
	NoAuth                bool
	ModelMetadataComplete bool
}

// SlashCommandResultKind identifies the domain payload produced by a slash
// command. Surfaces decide how to render each payload.
type SlashCommandResultKind string

const (
	SlashCommandResultHelp   SlashCommandResultKind = "help"
	SlashCommandResultStatus SlashCommandResultKind = "status"
	SlashCommandResultDoctor SlashCommandResultKind = "doctor"
	SlashCommandResultTable  SlashCommandResultKind = "table"
)

// SlashCommandResult carries structured slash-command data without prescribing
// surface-specific styling or terminal layout.
type SlashCommandResult struct {
	Command string                       `json:"command,omitempty"`
	Kind    SlashCommandResultKind       `json:"kind,omitempty"`
	Status  controlstatus.StatusSnapshot `json:"status,omitempty"`
	Help    CommandHelpSnapshot          `json:"help,omitempty"`
	Table   SlashTableSnapshot           `json:"table,omitempty"`
}

// SlashTableSnapshot is structured tabular output for a slash command. Rich
// surfaces choose styling while plain-text surfaces retain aligned columns.
type SlashTableSnapshot struct {
	Title    string              `json:"title,omitempty"`
	Sections []SlashTableSection `json:"sections,omitempty"`
}

// SlashTableSection groups one set of columns and rows under a heading.
type SlashTableSection struct {
	Title   string     `json:"title,omitempty"`
	Columns []string   `json:"columns,omitempty"`
	Rows    [][]string `json:"rows,omitempty"`
}

// CommandHelpSnapshot is the slash command catalog available to the current
// surface/session.
type CommandHelpSnapshot struct {
	Items []CommandHelpItem `json:"items,omitempty"`
}

// CommandHelpItem describes one slash command or dynamic agent command.
type CommandHelpItem struct {
	Name           string   `json:"name,omitempty"`
	Usage          string   `json:"usage,omitempty"`
	Description    string   `json:"description,omitempty"`
	Details        []string `json:"details,omitempty"`
	Dynamic        bool     `json:"dynamic,omitempty"`
	Known          bool     `json:"known,omitempty"`
	LocalDuringACP bool     `json:"local_during_acp,omitempty"`
}

type AgentCandidate struct {
	Name        string
	Description string
}

type AgentParticipantSnapshot struct {
	ID        string
	Label     string
	AgentName string
	Kind      string
	Role      string
	Source    string
	SessionID string
}

type AgentStatusSnapshot struct {
	SessionID                 string
	ControllerKind            string
	ControllerLabel           string
	ControllerEpoch           string
	ControllerModel           string
	ControllerReasoningEffort string
	ControllerCommands        []string
	ControllerModels          []SlashArgCandidate
	ControllerEfforts         []SlashArgCandidate
	HasActiveTurn             bool
	ActiveTurnKind            string
	AvailableAgents           []AgentCandidate
	Participants              []AgentParticipantSnapshot
	DelegatedParticipants     []AgentParticipantSnapshot
}

type ConnectConfig struct {
	Provider                       string
	EndpointID                     string
	Model                          string
	BaseURL                        string
	TimeoutSeconds                 int
	StreamFirstEventTimeoutSeconds int
	APIKey                         string
	TokenEnv                       string
	AuthType                       string
	ContextWindowTokens            int
	MaxOutputTokens                int
	ReasoningEffort                string
	ReasoningLevels                []string
}

type ApprovalDecision struct {
	// RequestID must match the approval_request_id on the permission Envelope
	// being resolved. Control uses it to route a decision to exactly one pending
	// main, participant, or child approval waiter.
	RequestID  eventstream.ApprovalRequestID
	Outcome    string
	OptionID   string
	Approved   bool
	Reason     string
	ReviewText string
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
