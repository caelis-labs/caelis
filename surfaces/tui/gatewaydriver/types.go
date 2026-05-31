package gatewaydriver

import (
	"errors"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
)

var errNoActiveRun = errors.New("surfaces/tui/gatewaydriver: no active core turn for session")

type SubmissionMode = tuidriver.SubmissionMode

const (
	SubmissionModeDefault = tuidriver.SubmissionModeDefault
	SubmissionModeOverlay = tuidriver.SubmissionModeOverlay
)

type Attachment = tuidriver.Attachment

type Submission = tuidriver.Submission

type StatusSnapshot = tuidriver.StatusSnapshot

type ResumeCandidate = tuidriver.ResumeCandidate

type CompletionCandidate = tuidriver.CompletionCandidate

type SlashArgCandidate = tuidriver.SlashArgCandidate

type AgentCandidate = tuidriver.AgentCandidate

type AgentParticipantSnapshot = tuidriver.AgentParticipantSnapshot

type AgentStatusSnapshot = tuidriver.AgentStatusSnapshot

type ConnectConfig struct {
	Provider            string
	EndpointID          string
	Model               string
	BaseURL             string
	TimeoutSeconds      int
	APIKey              string
	TokenEnv            string
	AuthType            string
	ContextWindowTokens int
	MaxOutputTokens     int
	ReasoningEffort     string
	ReasoningLevels     []string
}

type CommandExecutionOptions = tuidriver.CommandExecutionOptions

type CommandExecutionView = tuidriver.CommandExecutionView

type TaskListView = appviewmodel.TaskListView

type TaskItem = appviewmodel.TaskItem

type TaskOutputView = appviewmodel.TaskOutputView

type TaskListOptions struct {
	Limit          int
	IncludeHistory bool
}

type TaskOutputOptions struct {
	TaskID       string
	StdoutCursor int64
	StderrCursor int64
}

type TaskStartOptions struct {
	Command string
	Args    []string
	Dir     string
	Env     map[string]string
}

type TaskWaitOptions struct {
	TaskOutputOptions
	YieldTimeMS int
}

type TaskWriteOptions struct {
	TaskOutputOptions
	Input       string
	YieldTimeMS int
}

type Turn = tuidriver.Turn

type Driver = tuidriver.Driver
