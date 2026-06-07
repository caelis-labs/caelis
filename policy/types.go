package policy

import "github.com/OnslaughtSnail/caelis/sandbox"

// Request is the input to Engine.Evaluate.
type Request struct {
	ToolName     string
	ToolArgs     map[string]any
	AgentName    string
	SessionID    string
	InvocationID string
	SandboxPerm  SandboxPermission
	Metadata     map[string]any
}

// Decision is the output of Engine.Evaluate.
type Decision struct {
	Outcome     Outcome
	Reason      string
	Constraints *SandboxConstraints
	Metadata    map[string]any
}

// SandboxConstraints describes the sandbox boundaries for a tool call.
type SandboxConstraints struct {
	Backend    string
	Permission SandboxPermission
	Network    bool
	Paths      []PathRule
}

// PathRule describes a path access rule.
type PathRule struct {
	Path   string
	Access sandbox.PathAccess
}

// Outcome identifies the policy decision.
type Outcome string

const (
	OutcomeAllow          Outcome = "allow"
	OutcomeDeny           Outcome = "deny"
	OutcomeApprovalNeeded Outcome = "approval_needed"
)

// SandboxPermission describes the sandbox permission level for a tool call.
type SandboxPermission string

const (
	SandboxPermDefault          SandboxPermission = "default"
	SandboxPermRequireEscalated SandboxPermission = "require_escalated"
)

// ModeOptions holds policy mode configuration.
type ModeOptions struct {
	AutoApprove  bool
	MaxToolCalls int
	Metadata     map[string]any
}
