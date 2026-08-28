package controlprompt

import (
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	controlstatus "github.com/caelis-labs/caelis/control/status"
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

type ResumeCandidate = appserver.ResumeCandidate
type CompletionCandidate = appserver.CompletionCandidate
type SlashArgCandidate = appserver.SlashArgCandidate

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
	Name        string   `json:"name,omitempty"`
	Usage       string   `json:"usage,omitempty"`
	Description string   `json:"description,omitempty"`
	Details     []string `json:"details,omitempty"`
	Dynamic     bool     `json:"dynamic,omitempty"`
	Known       bool     `json:"known,omitempty"`
}

type AgentCandidate = appserver.AgentCandidate
type AgentParticipantSnapshot = appserver.AgentParticipantSnapshot
type AgentStatusSnapshot = appserver.AgentStatusSnapshot
type ConnectConfig = appserver.ConnectConfig

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

type MCPServerSnapshot = appserver.MCPServerSnapshot
type PluginSnapshot = appserver.PluginSnapshot
type MarketplaceSnapshot = appserver.MarketplaceSnapshot
