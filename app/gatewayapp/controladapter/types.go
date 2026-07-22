package controladapter

import (
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/protocol/acp/control"
)

var (
	_ control.Service                   = (*Adapter)(nil)
	_ controlagents.Connector           = (*Adapter)(nil)
	_ controlagents.Disconnector        = (*Adapter)(nil)
	_ agentbinding.Service              = (*Adapter)(nil)
	_ control.StatusService             = (*Adapter)(nil)
	_ control.TurnService               = (*Adapter)(nil)
	_ control.SessionService            = (*Adapter)(nil)
	_ control.SessionModeService        = (*Adapter)(nil)
	_ control.ModelService              = (*Adapter)(nil)
	_ control.SandboxService            = (*Adapter)(nil)
	_ control.AgentService              = (*Adapter)(nil)
	_ control.ReviewService             = (*Adapter)(nil)
	_ control.CompletionService         = (*Adapter)(nil)
	_ control.PluginService             = (*Adapter)(nil)
	_ control.LightweightStatusProvider = (*Adapter)(nil)
)

type SubmissionMode = control.SubmissionMode

const (
	SubmissionModeDefault    = control.SubmissionModeDefault
	SubmissionModeOverlay    = control.SubmissionModeOverlay
	SubmissionModeActiveTurn = control.SubmissionModeActiveTurn
)

type Attachment = control.Attachment

type Submission = control.Submission

type StatusSnapshot = control.StatusSnapshot

type SessionSnapshot = control.SessionSnapshot

type UsageSnapshot = control.UsageSnapshot

type ModelUsageSnapshot = control.ModelUsageSnapshot

type SandboxSetupStatus = control.SandboxSetupStatus

type SandboxSetupCheck = control.SandboxSetupCheck

type ResumeCandidate = control.ResumeCandidate

type CompletionCandidate = control.CompletionCandidate

type SlashArgCandidate = control.SlashArgCandidate

type AgentCandidate = control.AgentCandidate

type AgentParticipantSnapshot = control.AgentParticipantSnapshot

type AgentStatusSnapshot = control.AgentStatusSnapshot

type ConnectConfig = control.ConnectConfig

type ApprovalDecision = control.ApprovalDecision

type Turn = control.Turn

type PluginSnapshot = control.PluginSnapshot

type MCPServerSnapshot = control.MCPServerSnapshot

type MarketplaceSnapshot = control.MarketplaceSnapshot
