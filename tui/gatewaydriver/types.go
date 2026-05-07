package gatewaydriver

import (
	"errors"

	"github.com/OnslaughtSnail/caelis/tui/driver"
)

var ErrMigrationPending = errors.New("tui/gatewaydriver: legacy tui migration wiring pending")

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

type AgentAddOptions = tuidriver.AgentAddOptions

type CustomAgentConfig = tuidriver.CustomAgentConfig

type ConnectConfig = tuidriver.ConnectConfig

type Turn = tuidriver.Turn

type Driver = tuidriver.Driver
