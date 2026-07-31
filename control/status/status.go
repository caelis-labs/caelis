package status

import (
	"maps"
	"strings"
	"time"
)

// UsageSnapshot is one provider/model token-usage aggregate.
type UsageSnapshot struct {
	PromptTokens      int
	CachedInputTokens int
	CompletionTokens  int
	ReasoningTokens   int
	TotalTokens       int
}

// ModelUsageSnapshot associates token usage with one provider model.
type ModelUsageSnapshot struct {
	Provider string
	Model    string
	Usage    UsageSnapshot
}

// StatusSession describes the active product Session for status consumers.
type StatusSession struct {
	ID          string
	Workspace   string
	StoreDir    string
	ModeLabel   string
	SessionMode string
	Surface     string
}

// StatusModel describes the currently selected model.
type StatusModel struct {
	Display         string
	Provider        string
	Name            string
	ReasoningEffort string
	MissingAPIKey   bool
}

// StatusSandbox describes the resolved sandbox route and setup state.
type StatusSandbox struct {
	Type             string
	RequestedBackend string
	ResolvedBackend  string
	Route            string
	FallbackReason   string
	InstallHint      string
	Setup            SandboxSetupStatus
	SecuritySummary  string
	HostExecution    bool
	FullAccessMode   bool
}

// StatusUsage describes current-context plus cumulative total and per-model
// Session token usage.
type StatusUsage struct {
	PromptTokens        int
	CompletionTokens    int
	TotalTokens         int
	ContextWindowTokens int
	SessionUsageTotal   UsageSnapshot
	SessionUsageByModel []ModelUsageSnapshot
}

// StatusRuntime describes the live execution state exposed to status surfaces.
type StatusRuntime struct {
	ActiveJobs     int
	ActiveTurnKind string
	Running        bool
}

// StatusRateLimits is the provider-neutral account usage projection exposed
// to TUI, headless, Control Host clients, and future GUI status surfaces.
type StatusRateLimits struct {
	Provider   string
	Plan       string
	CapturedAt time.Time
	Limits     []StatusRateLimit
}

// StatusRateLimit is one provider account quota bucket.
type StatusRateLimit struct {
	ID      string
	Name    string
	Windows []StatusRateLimitWindow
}

// StatusRateLimitWindow is one provider quota window.
type StatusRateLimitWindow struct {
	Kind            string
	Label           string
	UsedPercent     float64
	DurationMinutes int64
	ResetsAt        time.Time
}

// SandboxSetupStatus is the normalized setup result for one sandbox backend.
type SandboxSetupStatus struct {
	Required bool
	Error    string
	Details  map[string]string
	Counts   map[string]int
	Checks   []SandboxSetupCheck
}

// SandboxSetupCheck is one named sandbox setup check.
type SandboxSetupCheck struct {
	Name      string
	Scope     string
	Current   bool
	Required  bool
	Reason    string
	Error     string
	Version   int
	Root      string
	UpdatedAt time.Time
	Details   map[string]string
	Counts    map[string]int
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
	Session       StatusSession
	ModelStatus   StatusModel
	SandboxStatus StatusSandbox
	Usage         StatusUsage
	RateLimits    StatusRateLimits
	Runtime       StatusRuntime
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
