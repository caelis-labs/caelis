package agentsdk

// ApprovalRole and ApprovalEndpoint are independent execution provenance axes.
// Owning Runtime and endpoint adapters bind these values, never model arguments
// or external protocol metadata.
type ApprovalRole string
type ApprovalEndpoint string

const (
	ApprovalRoleMain            ApprovalRole     = "main"
	ApprovalRoleSubagent        ApprovalRole     = "subagent"
	ApprovalRoleParticipant     ApprovalRole     = "participant"
	ApprovalEndpointBuiltin     ApprovalEndpoint = "builtin"
	ApprovalEndpointExternalACP ApprovalEndpoint = "external_acp"
)

// ApprovalOrigin identifies the actual producer of a permission request. Empty
// fields mean unknown. SessionID is the requesting endpoint Session; ParentSessionID
// is its controlling Session when delegated. Transport alone does not imply an
// external Runtime: a managed builtin child can use ACP.
type ApprovalOrigin struct {
	WorkingDirectory string           `json:"working_directory,omitempty"`
	Role             ApprovalRole     `json:"role"`
	Endpoint         ApprovalEndpoint `json:"endpoint"`
	SessionID        string           `json:"session_id,omitempty"`
	ParentSessionID  string           `json:"parent_session_id,omitempty"`
	TaskID           string           `json:"task_id,omitempty"`
	ParentCallID     string           `json:"parent_call_id,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ParticipantID    string           `json:"participant_id,omitempty"`
	Agent            string           `json:"agent,omitempty"`
}
