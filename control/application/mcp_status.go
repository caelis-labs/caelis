package application

// MCPStatus reports the current desired revision's MCP services and explicitly
// selected Skills. Inactive means no resident Runtime has assembled that
// revision. Reading status does not activate a Runtime or grant authority.
type MCPStatus struct {
	SessionID             string            `json:"session_id"`
	ConfigurationRevision string            `json:"configuration_revision"`
	Servers               []MCPServerStatus `json:"servers"`
	Skills                []SkillStatus     `json:"skills"`
}

type MCPServerStatus struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"` // inactive, connecting, running, failed
	Tools   []string `json:"tools,omitempty"`
	Warning string   `json:"warning,omitempty"`
}

// SkillStatus describes one selected directory or one candidate Skill root.
// Failed roots are excluded from the model-facing catalog for this revision.
type SkillStatus struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"` // directory, skill
	Name    string `json:"name,omitempty"`
	Status  string `json:"status"` // inactive, ready, failed
	Warning string `json:"warning,omitempty"`
}
