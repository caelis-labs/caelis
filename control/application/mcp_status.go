package application

// MCPStatus reports the current desired revision's independently managed MCP
// services. Inactive means no resident Runtime has started that revision. It
// does not activate a Runtime or grant dispatch authority.
type MCPStatus struct {
	SessionID             string            `json:"session_id"`
	ConfigurationRevision string            `json:"configuration_revision"`
	Servers               []MCPServerStatus `json:"servers"`
}

type MCPServerStatus struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"` // inactive, connecting, running, failed
	Tools   []string `json:"tools,omitempty"`
	Warning string   `json:"warning,omitempty"`
}
