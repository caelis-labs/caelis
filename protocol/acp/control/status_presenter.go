package control

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// FormatStatusSnapshot renders the shared text status view used by prompt
// surfaces.
func FormatStatusSnapshot(status StatusSnapshot) string {
	view := StatusDisplayFromSnapshot(status)
	lines := make([]string, 0, len(view.Fields)+len(view.Warnings)+len(view.RateLimits.Rows)+len(view.Usage.Rows)+6)
	for _, field := range view.Fields {
		appendStatusField(&lines, field.Label, field.Value)
	}
	if len(view.Warnings) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		for _, warning := range view.Warnings {
			appendStatusField(&lines, "Warning", warning)
		}
	}
	if !view.RateLimits.Empty() {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		appendStatusField(&lines, "Plan", view.RateLimits.Plan)
		for _, row := range view.RateLimits.Rows {
			appendStatusField(&lines, row.Label, row.Value)
		}
	}
	if usage := FormatTokenUsageDisplay(view.Usage); usage != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		for _, line := range strings.Split(usage, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			lines = append(lines, "  "+line)
		}
	}
	return strings.Join(lines, "\n")
}

// DisplayField is a surface-neutral label/value row.
type DisplayField struct {
	Label string `json:"label,omitempty"`
	Value string `json:"value,omitempty"`
}

// StatusDisplay is the canonical display model for /status. It contains
// derived user-facing strings but no surface-specific layout or styling.
type StatusDisplay struct {
	Fields     []DisplayField     `json:"fields,omitempty"`
	Warnings   []string           `json:"warnings,omitempty"`
	RateLimits RateLimitUsageView `json:"rate_limits,omitempty"`
	Usage      TokenUsageView     `json:"usage,omitempty"`
}

// RateLimitUsageView is the canonical display model for provider subscription
// windows. The structured StatusSnapshot remains available to richer GUI
// clients that need progress bars or localized reset timestamps.
type RateLimitUsageView struct {
	Provider string                  `json:"provider,omitempty"`
	Plan     string                  `json:"plan,omitempty"`
	Rows     []RateLimitUsageViewRow `json:"rows,omitempty"`
}

func (v RateLimitUsageView) Empty() bool { return len(v.Rows) == 0 }

type RateLimitUsageViewRow struct {
	Label string `json:"label,omitempty"`
	Value string `json:"value,omitempty"`
}

// TokenUsageView is the canonical display model for session token usage.
type TokenUsageView struct {
	Rows          []TokenUsageViewRow `json:"rows,omitempty"`
	ShowReasoning bool                `json:"show_reasoning,omitempty"`
}

// Empty reports whether the usage view has no visible rows.
func (v TokenUsageView) Empty() bool {
	return len(v.Rows) == 0
}

// TokenUsageViewRow carries preformatted token usage cells.
type TokenUsageViewRow struct {
	Scope     string `json:"scope,omitempty"`
	Total     string `json:"total,omitempty"`
	Input     string `json:"input,omitempty"`
	Cached    string `json:"cached,omitempty"`
	Output    string `json:"output,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
}

// StatusDisplayFromSnapshot derives the canonical display model for /status.
func StatusDisplayFromSnapshot(status StatusSnapshot) StatusDisplay {
	fields := make([]DisplayField, 0, 10)
	appendDisplayField := func(label, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		fields = append(fields, DisplayField{Label: strings.TrimSpace(label), Value: value})
	}
	appendDisplayField("Model", statusHeaderModelText(status))
	appendDisplayField("Mode", firstNonEmpty(strings.TrimSpace(status.Session.ModeLabel), strings.TrimSpace(status.Session.SessionMode), "auto-review"))
	appendDisplayField("Sandbox", formatStatusSandbox(status))
	appendDisplayField("Workspace", firstNonEmpty(strings.TrimSpace(status.Session.Workspace), "-"))
	appendDisplayField("Session", strings.TrimSpace(status.Session.ID))
	appendDisplayField("Context", formatContextUsage(status.Usage.TotalTokens, status.Usage.ContextWindowTokens))
	if status.SandboxStatus.FallbackReason != "" {
		appendDisplayField("Fallback", strings.TrimSpace(status.SandboxStatus.FallbackReason))
	}
	if status.SandboxStatus.InstallHint != "" {
		appendDisplayField("Install", strings.TrimSpace(status.SandboxStatus.InstallHint))
	}
	setup := SandboxSetupViewFromStatus(status)
	if setup.GlobalRequired {
		if setup.SetupError != "" {
			appendDisplayField("Setup", "Windows sandbox infrastructure repair failed")
		} else {
			appendDisplayField("Setup", "Windows sandbox infrastructure repair is pending")
		}
	} else if setup.WorkspaceRequired {
		if setup.SetupError != "" {
			appendDisplayField("Setup", "current workspace ACL repair failed")
		} else {
			appendDisplayField("Setup", "current workspace ACL repair is pending")
		}
	}
	if setup.SetupError != "" {
		appendDisplayField("Error", compactStatusDetail(setup.SetupError, 180))
	}
	warnings := make([]string, 0, 6)
	if strings.TrimSpace(status.ModelStatus.Display) == "" && strings.TrimSpace(status.ModelStatus.Provider) == "" && strings.TrimSpace(status.ModelStatus.Name) == "" {
		warnings = append(warnings, "Run /connect to configure a provider and model")
	}
	if status.ModelStatus.MissingAPIKey {
		warnings = append(warnings, "API key is missing; reconnect with a key or use env:YOUR_API_KEY")
	}
	if status.SandboxStatus.HostExecution || status.SandboxStatus.FullAccessMode {
		warnings = append(warnings, "Commands may run on the host with reduced sandbox isolation")
		warnings = append(warnings, "Auto-Review remains enabled and can approve host execution; switch approval mode to manual for sensitive work")
	}
	if setup.GlobalRequired {
		warnings = append(warnings, sandboxSetupWarning(setup, "Windows sandbox setup"))
	} else if setup.WorkspaceRequired {
		warnings = append(warnings, sandboxSetupWarning(setup, "current workspace ACLs"))
	}
	if strings.TrimSpace(status.SandboxStatus.FallbackReason) != "" {
		warnings = append(warnings, "Requested sandbox backend is unavailable and a fallback is in effect")
	}
	return StatusDisplay{
		Fields:     fields,
		Warnings:   warnings,
		RateLimits: rateLimitUsageView(status.RateLimits),
		Usage:      sessionTokenUsageView(status),
	}
}

func rateLimitUsageView(status StatusRateLimits) RateLimitUsageView {
	view := RateLimitUsageView{
		Provider: strings.TrimSpace(status.Provider),
		Plan:     strings.TrimSpace(status.Plan),
	}
	for _, limit := range status.Limits {
		bucket := strings.TrimSpace(firstNonEmpty(limit.Name, limit.ID))
		for _, window := range limit.Windows {
			label := rateLimitWindowLabel(window)
			if bucket != "" && !strings.EqualFold(bucket, "codex") {
				label = bucket + " " + label
			}
			remaining := 100 - math.Max(0, math.Min(100, window.UsedPercent))
			value := fmt.Sprintf("%.0f%% left", remaining)
			if !window.ResetsAt.IsZero() {
				value += " · resets " + window.ResetsAt.Local().Format("2006-01-02 15:04 MST")
			}
			view.Rows = append(view.Rows, RateLimitUsageViewRow{Label: label, Value: value})
		}
	}
	return view
}

func rateLimitWindowLabel(window StatusRateLimitWindow) string {
	if label := strings.TrimSpace(window.Label); label != "" {
		return label
	}
	switch window.DurationMinutes {
	case int64((5 * time.Hour) / time.Minute):
		return "5h limit"
	case int64((24 * time.Hour) / time.Minute):
		return "Daily limit"
	case int64((7 * 24 * time.Hour) / time.Minute):
		return "Weekly limit"
	case int64((30 * 24 * time.Hour) / time.Minute):
		return "Monthly limit"
	}
	if window.DurationMinutes > 0 && window.DurationMinutes%int64(time.Hour/time.Minute) == 0 {
		hours := window.DurationMinutes / int64(time.Hour/time.Minute)
		if hours%24 == 0 {
			return fmt.Sprintf("%dd limit", hours/24)
		}
		return fmt.Sprintf("%dh limit", hours)
	}
	if window.DurationMinutes > 0 {
		return fmt.Sprintf("%dm limit", window.DurationMinutes)
	}
	if kind := strings.TrimSpace(window.Kind); kind != "" {
		return strings.ToUpper(kind[:1]) + kind[1:] + " limit"
	}
	return "Limit"
}

// FormatDoctorSnapshot renders the shared doctor output used by prompt
// surfaces.
func FormatDoctorSnapshot(status StatusSnapshot) string {
	lines := []string{"doctor:"}
	provider := strings.TrimSpace(firstNonEmpty(status.ModelStatus.Provider, status.ModelStatus.Display))
	modelName := strings.TrimSpace(firstNonEmpty(status.ModelStatus.Name, status.ModelStatus.Display))
	switch {
	case status.ModelStatus.MissingAPIKey:
		lines = append(lines, "  warn provider key missing - run /connect")
	case provider == "" && modelName == "":
		lines = append(lines, "  warn model not configured - run /connect")
	default:
		lines = append(lines, "  ok provider/model: "+joinNonEmpty([]string{provider, modelName}, " / "))
	}
	if storeDir := strings.TrimSpace(status.Session.StoreDir); storeDir != "" {
		lines = append(lines, "  ok session store: "+storeDir)
	} else {
		lines = append(lines, "  warn session store path unavailable")
	}
	if sessionID := strings.TrimSpace(status.Session.ID); sessionID != "" {
		lines = append(lines, "  ok session: "+sessionID)
	}
	sandbox := strings.TrimSpace(firstNonEmpty(status.SandboxStatus.ResolvedBackend, status.SandboxStatus.RequestedBackend, status.SandboxStatus.Type))
	setup := SandboxSetupViewFromStatus(status)
	switch {
	case status.SandboxStatus.HostExecution || status.SandboxStatus.FullAccessMode:
		detail := strings.TrimSpace(firstNonEmpty(status.SandboxStatus.SecuritySummary, sandbox, "host execution"))
		lines = append(lines, "  warn sandbox: "+detail)
	case setup.GlobalRequired:
		lines = append(lines, "  warn sandbox global repair pending: "+compactStatusDetail(setup.GlobalDetail, 180))
		if setup.IsWindows {
			lines = append(lines, "  info fix: /doctor")
		}
	case setup.WorkspaceRequired:
		lines = append(lines, "  warn sandbox workspace repair pending: "+compactStatusDetail(setup.WorkspaceDetail, 180))
		if setup.IsWindows {
			lines = append(lines, "  info fix: /doctor")
		}
	case sandbox != "":
		lines = append(lines, "  ok sandbox: "+sandbox)
	default:
		lines = append(lines, "  warn sandbox status unavailable")
	}
	if route := strings.TrimSpace(status.SandboxStatus.Route); route != "" {
		lines = append(lines, "  ok route: "+route)
	}
	if status.Runtime.ActiveJobs > 0 || status.Runtime.Running {
		lines = append(lines, fmt.Sprintf("  info active jobs: %d", status.Runtime.ActiveJobs))
	}
	return strings.Join(lines, "\n")
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
func SandboxSetupViewFromStatus(status StatusSnapshot) SandboxSetupView {
	global, hasGlobal := status.SandboxStatus.Setup.Check("global")
	workspace, hasWorkspace := status.SandboxStatus.Setup.Check("workspace")
	view := SandboxSetupView{
		GlobalRequired:    status.SandboxStatus.GlobalSetupRequired || (hasGlobal && global.Required),
		WorkspaceRequired: status.SandboxStatus.WorkspaceSetupRequired || (hasWorkspace && workspace.Required),
		IsWindows:         sandboxStatusIsWindows(status.SandboxStatus),
		SetupError:        firstNonEmpty(status.SandboxStatus.Setup.Error, global.Error, workspace.Error, status.SandboxStatus.SetupError),
		GlobalDetail:      firstNonEmpty(status.SandboxStatus.SetupError, global.Error, global.Reason, status.SandboxStatus.GlobalSetupReason, status.SandboxStatus.SetupMarkerReason, "global setup required"),
		WorkspaceDetail:   firstNonEmpty(status.SandboxStatus.SetupError, workspace.Error, workspace.Reason, status.SandboxStatus.WorkspaceSetupReason, "workspace ACL setup required"),
	}
	view.AnyRequired = status.SandboxStatus.SetupRequired ||
		status.SandboxStatus.Setup.Required ||
		view.GlobalRequired ||
		view.WorkspaceRequired
	view.RepairRequired = view.IsWindows && view.AnyRequired
	return view
}

func sandboxStatusIsWindows(status StatusSandbox) bool {
	for _, value := range []string{status.ResolvedBackend, status.RequestedBackend, status.Type} {
		if strings.EqualFold(strings.TrimSpace(value), "windows") {
			return true
		}
	}
	return false
}

func sandboxSetupWarning(view SandboxSetupView, target string) string {
	if !view.IsWindows {
		return target + " repair is pending"
	}
	if view.SetupError != "" {
		return "Run /doctor to repair " + target
	}
	return "Run /doctor to repair " + target + " now, or retry a sandboxed command to repair lazily"
}

func compactStatusDetail(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if maxRunes <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 1 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-1]) + "..."
}

func appendStatusField(lines *[]string, label string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	*lines = append(*lines, fmt.Sprintf("  %-10s %s", label+":", value))
}

func formatStatusSandbox(status StatusSnapshot) string {
	sandbox := firstNonEmpty(strings.TrimSpace(status.SandboxStatus.ResolvedBackend), strings.TrimSpace(status.SandboxStatus.Type), strings.TrimSpace(status.SandboxStatus.RequestedBackend), "auto")
	security := strings.TrimSpace(status.SandboxStatus.SecuritySummary)
	switch {
	case status.SandboxStatus.FullAccessMode:
		return firstNonEmpty(security, "full access")
	case status.SandboxStatus.HostExecution:
		return firstNonEmpty(security, "host execution")
	}
	route := strings.ToLower(strings.TrimSpace(status.SandboxStatus.Route))
	switch route {
	case "", "-":
		return sandbox
	case "sandbox":
		if strings.Contains(strings.ToLower(sandbox), "sandbox") {
			return sandbox
		}
		return sandbox + " sandbox"
	case "host":
		return "host execution"
	default:
		return sandbox + " (" + strings.TrimSpace(status.SandboxStatus.Route) + ")"
	}
}

func sessionTokenUsageView(status StatusSnapshot) TokenUsageView {
	total := normalizedUsageSnapshot(status.Usage.SessionUsageTotal)
	if usageSnapshotZero(total) {
		total = normalizedUsageSnapshot(UsageSnapshot{
			PromptTokens:      status.Usage.SessionInputTokens,
			CachedInputTokens: status.Usage.SessionCachedInputTokens,
			CompletionTokens:  status.Usage.SessionOutputTokens,
			ReasoningTokens:   status.Usage.SessionReasoningTokens,
			TotalTokens:       status.Usage.SessionTotalTokens,
		})
	}
	if usageSnapshotZero(total) {
		return TokenUsageView{}
	}
	rows := []tokenUsageStatusRow{{scope: "total", usage: total}}
	for _, item := range status.Usage.SessionUsageByModel {
		usage := normalizedUsageSnapshot(item.Usage)
		if usageSnapshotZero(usage) {
			continue
		}
		rows = append(rows, tokenUsageStatusRow{scope: modelUsageStatusLabel(item), usage: usage})
	}
	return tokenUsageView(rows)
}

func modelUsageStatusLabel(item ModelUsageSnapshot) string {
	provider := strings.TrimSpace(item.Provider)
	model := strings.TrimSpace(item.Model)
	switch {
	case provider != "" && model != "":
		return provider + "/" + model
	case model != "":
		return model
	case provider != "":
		return provider
	default:
		return "unknown-model"
	}
}

type tokenUsageStatusRow struct {
	scope string
	usage UsageSnapshot
}

func tokenUsageView(rows []tokenUsageStatusRow) TokenUsageView {
	if len(rows) == 0 {
		return TokenUsageView{}
	}
	showReasoning := false
	for _, row := range rows {
		if normalizedUsageSnapshot(row.usage).ReasoningTokens > 0 {
			showReasoning = true
			break
		}
	}
	view := TokenUsageView{Rows: make([]TokenUsageViewRow, 0, len(rows)), ShowReasoning: showReasoning}
	for _, row := range rows {
		usage := normalizedUsageSnapshot(row.usage)
		view.Rows = append(view.Rows, TokenUsageViewRow{
			Scope:     row.scope,
			Total:     formatTokenUsageNumber(usage.TotalTokens),
			Input:     formatTokenUsageNumber(usage.PromptTokens),
			Cached:    formatTokenUsageNumber(usage.CachedInputTokens),
			Output:    formatTokenUsageNumber(usage.CompletionTokens),
			Reasoning: formatTokenUsageNumber(usage.ReasoningTokens),
		})
	}
	return view
}

// FormatTokenUsageDisplay renders token usage rows as padded plain text.
func FormatTokenUsageDisplay(usage TokenUsageView) string {
	if usage.Empty() {
		return ""
	}
	return formatPaddedRows(tokenUsagePlainRows(usage))
}

func tokenUsagePlainRows(usage TokenUsageView) [][]string {
	header := []string{"Scope", "Total", "Input", "Cached", "Output"}
	if usage.ShowReasoning {
		header = append(header, "Reasoning")
	}
	rows := make([][]string, 0, len(usage.Rows)+1)
	rows = append(rows, header)
	for _, row := range usage.Rows {
		values := []string{row.Scope, row.Total, row.Input, row.Cached, row.Output}
		if usage.ShowReasoning {
			values = append(values, row.Reasoning)
		}
		rows = append(rows, values)
	}
	return rows
}

func formatPaddedRows(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	widths := paddedRowWidths(rows)
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		parts := make([]string, len(row))
		for i, col := range row {
			parts[i] = padRightRunes(col, widths[i])
		}
		lines = append(lines, strings.TrimRight(strings.Join(parts, "  "), " "))
	}
	return strings.Join(lines, "\n")
}

func paddedRowWidths(rows [][]string) []int {
	widths := make([]int, 0)
	for _, row := range rows {
		for len(widths) < len(row) {
			widths = append(widths, 0)
		}
		for i, col := range row {
			if n := len([]rune(col)); n > widths[i] {
				widths[i] = n
			}
		}
	}
	return widths
}

func normalizedUsageSnapshot(usage UsageSnapshot) UsageSnapshot {
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return usage
}

func usageSnapshotZero(usage UsageSnapshot) bool {
	return usage.PromptTokens == 0 &&
		usage.CachedInputTokens == 0 &&
		usage.CompletionTokens == 0 &&
		usage.ReasoningTokens == 0 &&
		usage.TotalTokens == 0
}

func formatTokenUsageNumber(value int) string {
	if value <= 0 {
		return "-"
	}
	return strconv.Itoa(value)
}

func statusHeaderModelText(status StatusSnapshot) string {
	model := firstNonEmpty(strings.TrimSpace(status.ModelStatus.Display), strings.TrimSpace(status.ModelStatus.Name), "not configured")
	provider := firstNonEmpty(strings.TrimSpace(status.ModelStatus.Provider), deriveProviderFromAlias(status.ModelStatus.Display), "not configured")
	if provider != "" && provider != "not configured" && !strings.EqualFold(provider, "acp") && !strings.Contains(strings.ToLower(model), strings.ToLower(provider)+"/") {
		model = provider + "/" + model
	}
	if effort := strings.TrimSpace(status.ModelStatus.ReasoningEffort); effort != "" && !strings.Contains(model, "["+effort+"]") {
		model += " [" + effort + "]"
	}
	if status.ModelStatus.MissingAPIKey {
		model += " · key missing"
	}
	return strings.TrimSpace(model)
}

func deriveProviderFromAlias(model string) string {
	model = strings.TrimSpace(model)
	if before, _, ok := strings.Cut(model, "/"); ok {
		return strings.TrimSpace(before)
	}
	return ""
}

func formatContextUsage(totalTokens, contextWindowTokens int) string {
	if contextWindowTokens <= 0 {
		if totalTokens <= 0 {
			return ""
		}
		return compactTokenCount(totalTokens)
	}
	if totalTokens < 0 {
		totalTokens = 0
	}
	percent := totalTokens * 100 / contextWindowTokens
	return fmt.Sprintf("%s / %s · %d%%", compactTokenCount(totalTokens), compactTokenCount(contextWindowTokens), percent)
}

func compactTokenCount(tokens int) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(tokens)/1_000_000)
	case tokens >= 10_000:
		return fmt.Sprintf("%.0fk", float64(tokens)/1_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

func padRightRunes(value string, width int) string {
	count := len([]rune(value))
	if count >= width {
		return value
	}
	return value + strings.Repeat(" ", width-count)
}

func joinNonEmpty(parts []string, sep string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, strings.TrimSpace(p))
		}
	}
	return strings.Join(out, sep)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
