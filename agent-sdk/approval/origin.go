package approval

// Role identifies the requesting agent's place in the conversation.
type Role string

// Endpoint identifies the implementation that produced the request.
type Endpoint string

const (
	RoleMain            Role     = "main"
	RoleSubagent        Role     = "subagent"
	RoleParticipant     Role     = "participant"
	EndpointBuiltin     Endpoint = "builtin"
	EndpointExternalACP Endpoint = "external_acp"
)

// Origin identifies the actual producer of a permission request. Empty
// fields mean unknown. SessionID is the requesting endpoint Session; ParentSessionID
// is its controlling Session when delegated. Transport alone does not imply an
// external Runtime: a managed builtin child can use ACP.
type Origin struct {
	WorkingDirectory string   `json:"working_directory,omitempty"`
	Role             Role     `json:"role"`
	Endpoint         Endpoint `json:"endpoint"`
	SessionID        string   `json:"session_id,omitempty"`
	ParentSessionID  string   `json:"parent_session_id,omitempty"`
	TaskID           string   `json:"task_id,omitempty"`
	ParentCallID     string   `json:"parent_call_id,omitempty"`
	ToolCallID       string   `json:"tool_call_id,omitempty"`
	ParticipantID    string   `json:"participant_id,omitempty"`
	Agent            string   `json:"agent,omitempty"`
}
