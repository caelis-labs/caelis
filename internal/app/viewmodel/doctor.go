package viewmodel

type DoctorView struct {
	Status    StatusView           `json:"status"`
	Checks    []DoctorCheck        `json:"checks,omitempty"`
	Actions   []DoctorAction       `json:"actions,omitempty"`
	Lifecycle *DoctorLifecycleView `json:"lifecycle,omitempty"`
}

type DoctorCheck struct {
	ID        string   `json:"id,omitempty"`
	Label     string   `json:"label,omitempty"`
	Severity  string   `json:"severity,omitempty"`
	Message   string   `json:"message,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	ActionIDs []string `json:"action_ids,omitempty"`
}

type DoctorAction struct {
	ID                   string `json:"id,omitempty"`
	Label                string `json:"label,omitempty"`
	Description          string `json:"description,omitempty"`
	Kind                 string `json:"kind,omitempty"`
	Command              string `json:"command,omitempty"`
	Enabled              bool   `json:"enabled"`
	Destructive          bool   `json:"destructive,omitempty"`
	RequiresConfirmation bool   `json:"requires_confirmation,omitempty"`
}

type DoctorLifecycleView struct {
	Action         string `json:"action,omitempty"`
	Backend        string `json:"backend,omitempty"`
	Supported      bool   `json:"supported,omitempty"`
	Attempted      bool   `json:"attempted,omitempty"`
	Noop           bool   `json:"noop,omitempty"`
	FallbackAction string `json:"fallback_action,omitempty"`
	Message        string `json:"message,omitempty"`
	Error          string `json:"error,omitempty"`
}
