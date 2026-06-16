package controladapter

import "github.com/OnslaughtSnail/caelis/protocol/acp/control"

var (
	_ control.Service                   = (*Adapter)(nil)
	_ control.StreamSubscriber          = (*Adapter)(nil)
	_ control.LightweightStatusProvider = (*Adapter)(nil)
)

type SubmissionMode = control.SubmissionMode

const (
	SubmissionModeDefault = control.SubmissionModeDefault
	SubmissionModeOverlay = control.SubmissionModeOverlay
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

type AgentProfileSnapshot = control.AgentProfileSnapshot

type AgentProfileStatusSnapshot = control.AgentProfileStatusSnapshot

type AgentProfileBindingConfig = control.AgentProfileBindingConfig

type AgentAddOptions = control.AgentAddOptions

type CustomAgentConfig = control.CustomAgentConfig

type ConnectConfig = control.ConnectConfig

type ApprovalDecision = control.ApprovalDecision

type Turn = control.Turn

type PluginSnapshot = control.PluginSnapshot

type MCPServerSnapshot = control.MCPServerSnapshot

type MarketplaceSnapshot = control.MarketplaceSnapshot
