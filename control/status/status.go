package status

import (
	"maps"
	"strings"
	"time"
)

// UsageSnapshot is one provider/model token-usage aggregate.
type UsageSnapshot struct {
	PromptTokens      int `json:"prompt_tokens,omitempty"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	CompletionTokens  int `json:"completion_tokens,omitempty"`
	ReasoningTokens   int `json:"reasoning_tokens,omitempty"`
	TotalTokens       int `json:"total_tokens,omitempty"`
}

// ModelUsageSnapshot associates token usage with one provider model.
type ModelUsageSnapshot struct {
	Provider string        `json:"provider,omitempty"`
	Model    string        `json:"model,omitempty"`
	Usage    UsageSnapshot `json:"usage"`
}

// StatusSession describes the active product Session for status consumers.
type StatusSession struct {
	ID          string `json:"id,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
	StoreDir    string `json:"store_dir,omitempty"`
	ModeLabel   string `json:"mode_label,omitempty"`
	SessionMode string `json:"session_mode,omitempty"`
	Surface     string `json:"surface,omitempty"`
}

// StatusModel describes the currently selected model.
type StatusModel struct {
	Display         string `json:"display,omitempty"`
	Provider        string `json:"provider,omitempty"`
	Name            string `json:"name,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	MissingAPIKey   bool   `json:"missing_api_key,omitempty"`
}

// StatusSandbox describes the resolved sandbox route and setup state.
type StatusSandbox struct {
	Type             string             `json:"type,omitempty"`
	RequestedBackend string             `json:"requested_backend,omitempty"`
	ResolvedBackend  string             `json:"resolved_backend,omitempty"`
	Route            string             `json:"route,omitempty"`
	FallbackReason   string             `json:"fallback_reason,omitempty"`
	InstallHint      string             `json:"install_hint,omitempty"`
	Setup            SandboxSetupStatus `json:"setup"`
	SecuritySummary  string             `json:"security_summary,omitempty"`
	HostExecution    bool               `json:"host_execution,omitempty"`
	FullAccessMode   bool               `json:"full_access_mode,omitempty"`
}

// StatusUsage describes current-context plus cumulative total and per-model
// Session token usage.
type StatusUsage struct {
	PromptTokens        int                  `json:"prompt_tokens,omitempty"`
	CompletionTokens    int                  `json:"completion_tokens,omitempty"`
	TotalTokens         int                  `json:"total_tokens,omitempty"`
	ContextWindowTokens int                  `json:"context_window_tokens,omitempty"`
	SessionUsageTotal   UsageSnapshot        `json:"session_usage_total"`
	SessionUsageByModel []ModelUsageSnapshot `json:"session_usage_by_model,omitempty"`
}

// StatusRuntime describes the live execution state exposed to status surfaces.
type StatusRuntime struct {
	ActiveJobs     int    `json:"active_jobs,omitempty"`
	ActiveTurnKind string `json:"active_turn_kind,omitempty"`
	Running        bool   `json:"running,omitempty"`
}

// StatusRateLimits is the provider-neutral account usage projection exposed
// to TUI, headless, Control Host clients, and future GUI status surfaces.
type StatusRateLimits struct {
	Provider   string            `json:"provider,omitempty"`
	Plan       string            `json:"plan,omitempty"`
	CapturedAt time.Time         `json:"captured_at,omitempty"`
	Limits     []StatusRateLimit `json:"limits,omitempty"`
}

// StatusRateLimit is one provider account quota bucket.
type StatusRateLimit struct {
	ID      string                  `json:"id,omitempty"`
	Name    string                  `json:"name,omitempty"`
	Windows []StatusRateLimitWindow `json:"windows,omitempty"`
}

// StatusRateLimitWindow is one provider quota window.
type StatusRateLimitWindow struct {
	Kind            string    `json:"kind,omitempty"`
	Label           string    `json:"label,omitempty"`
	UsedPercent     float64   `json:"used_percent,omitempty"`
	DurationMinutes int64     `json:"duration_minutes,omitempty"`
	ResetsAt        time.Time `json:"resets_at,omitempty"`
}

// SandboxSetupStatus is the normalized setup result for one sandbox backend.
type SandboxSetupStatus struct {
	Required bool                `json:"required,omitempty"`
	Error    string              `json:"error,omitempty"`
	Details  map[string]string   `json:"details,omitempty"`
	Counts   map[string]int      `json:"counts,omitempty"`
	Checks   []SandboxSetupCheck `json:"checks,omitempty"`
}

// SandboxSetupCheck is one named sandbox setup check.
type SandboxSetupCheck struct {
	Name      string            `json:"name,omitempty"`
	Scope     string            `json:"scope,omitempty"`
	Current   bool              `json:"current,omitempty"`
	Required  bool              `json:"required,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Error     string            `json:"error,omitempty"`
	Version   int               `json:"version,omitempty"`
	Root      string            `json:"root,omitempty"`
	UpdatedAt time.Time         `json:"updated_at,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
	Counts    map[string]int    `json:"counts,omitempty"`
}

// CloneSandboxSetupStatus returns an isolated normalized copy.
func CloneSandboxSetupStatus(in SandboxSetupStatus) SandboxSetupStatus {
	out := in
	out.Error = strings.TrimSpace(in.Error)
	out.Details = cloneTrimmedStringMap(in.Details)
	out.Counts = maps.Clone(in.Counts)
	if len(in.Checks) > 0 {
		out.Checks = make([]SandboxSetupCheck, len(in.Checks))
		for i, check := range in.Checks {
			out.Checks[i] = CloneSandboxSetupCheck(check)
		}
	}
	return out
}

// CloneSandboxSetupCheck returns an isolated normalized copy.
func CloneSandboxSetupCheck(in SandboxSetupCheck) SandboxSetupCheck {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Scope = strings.TrimSpace(in.Scope)
	out.Reason = strings.TrimSpace(in.Reason)
	out.Error = strings.TrimSpace(in.Error)
	out.Root = strings.TrimSpace(in.Root)
	out.Details = cloneTrimmedStringMap(in.Details)
	out.Counts = maps.Clone(in.Counts)
	return out
}

// Check returns an isolated normalized named check.
func (s SandboxSetupStatus) Check(name string) (SandboxSetupCheck, bool) {
	name = strings.TrimSpace(name)
	for _, check := range s.Checks {
		if strings.TrimSpace(check.Name) == name {
			return CloneSandboxSetupCheck(check), true
		}
	}
	return SandboxSetupCheck{}, false
}

func cloneTrimmedStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	return out
}

// StatusSnapshot is the product-level status read model.
type StatusSnapshot struct {
	Session       StatusSession    `json:"session"`
	ModelStatus   StatusModel      `json:"model_status"`
	SandboxStatus StatusSandbox    `json:"sandbox_status"`
	Usage         StatusUsage      `json:"usage"`
	RateLimits    StatusRateLimits `json:"rate_limits"`
	Runtime       StatusRuntime    `json:"runtime"`
}

// SandboxSetupView describes whether the current sandbox needs setup or repair.
type SandboxSetupView struct {
	GlobalRequired    bool
	WorkspaceRequired bool
	AnyRequired       bool
	RepairRequired    bool
	IsWindows         bool
	SetupError        string
	GlobalDetail      string
	WorkspaceDetail   string
}

// SandboxSetupViewFromStatus derives setup/repair state from a status snapshot.
func SandboxSetupViewFromStatus(snapshot StatusSnapshot) SandboxSetupView {
	global, hasGlobal := snapshot.SandboxStatus.Setup.Check("global")
	workspace, hasWorkspace := snapshot.SandboxStatus.Setup.Check("workspace")
	view := SandboxSetupView{
		GlobalRequired:    hasGlobal && global.Required,
		WorkspaceRequired: hasWorkspace && workspace.Required,
		IsWindows:         sandboxStatusIsWindows(snapshot.SandboxStatus),
		SetupError: firstNonEmpty(
			snapshot.SandboxStatus.Setup.Error,
			global.Error,
			workspace.Error,
		),
		GlobalDetail: firstNonEmpty(
			snapshot.SandboxStatus.Setup.Error,
			global.Error,
			global.Reason,
			"global setup required",
		),
		WorkspaceDetail: firstNonEmpty(
			snapshot.SandboxStatus.Setup.Error,
			workspace.Error,
			workspace.Reason,
			"workspace ACL setup required",
		),
	}
	view.AnyRequired = snapshot.SandboxStatus.Setup.Required ||
		view.GlobalRequired ||
		view.WorkspaceRequired
	view.RepairRequired = view.IsWindows && view.AnyRequired
	return view
}

func sandboxStatusIsWindows(snapshot StatusSandbox) bool {
	for _, value := range []string{snapshot.ResolvedBackend, snapshot.RequestedBackend, snapshot.Type} {
		if strings.EqualFold(strings.TrimSpace(value), "windows") {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
