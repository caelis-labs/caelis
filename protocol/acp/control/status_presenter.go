package control

import (
	"fmt"
	"strconv"
	"strings"
)

// FormatStatusSnapshot renders the shared text status view used by prompt
// surfaces.
func FormatStatusSnapshot(status StatusSnapshot) string {
	model := statusHeaderModelText(status)
	lines := []string{}
	appendStatusField(&lines, "Model", model)
	appendStatusField(&lines, "Mode", firstNonEmpty(strings.TrimSpace(status.Session.ModeLabel), "auto-review"))
	appendStatusField(&lines, "Sandbox", formatStatusSandbox(status))
	appendStatusField(&lines, "Workspace", firstNonEmpty(strings.TrimSpace(status.Session.Workspace), "-"))
	appendStatusField(&lines, "Session", strings.TrimSpace(status.Session.ID))
	if usage := formatContextUsage(status.Usage.TotalTokens, status.Usage.ContextWindowTokens); usage != "" {
		appendStatusField(&lines, "Context", usage)
	}
	if status.SandboxStatus.FallbackReason != "" {
		appendStatusField(&lines, "Fallback", strings.TrimSpace(status.SandboxStatus.FallbackReason))
	}
	if status.SandboxStatus.InstallHint != "" {
		appendStatusField(&lines, "Install", strings.TrimSpace(status.SandboxStatus.InstallHint))
	}
	setup := SandboxSetupViewFromStatus(status)
	if setup.GlobalRequired {
		if setup.SetupError != "" {
			appendStatusField(&lines, "Setup", "Windows sandbox infrastructure repair failed")
		} else {
			appendStatusField(&lines, "Setup", "Windows sandbox infrastructure repair is pending")
		}
	} else if setup.WorkspaceRequired {
		if setup.SetupError != "" {
			appendStatusField(&lines, "Setup", "current workspace ACL repair failed")
		} else {
			appendStatusField(&lines, "Setup", "current workspace ACL repair is pending")
		}
	}
	if setup.SetupError != "" {
		appendStatusField(&lines, "Error", compactStatusDetail(setup.SetupError, 180))
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
	if len(warnings) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		for _, warning := range warnings {
			appendStatusField(&lines, "Warning", warning)
		}
	}
	if usage := formatSessionTokenUsageStatus(status); usage != "" {
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

func formatSessionTokenUsageStatus(status StatusSnapshot) string {
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
		return ""
	}
	rows := []tokenUsageStatusRow{{scope: "total", usage: total}}
	for _, item := range status.Usage.SessionUsageByModel {
		usage := normalizedUsageSnapshot(item.Usage)
		if usageSnapshotZero(usage) {
			continue
		}
		rows = append(rows, tokenUsageStatusRow{scope: modelUsageStatusLabel(item), usage: usage})
	}
	return formatTokenUsageTable(rows)
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

func formatTokenUsageTable(rows []tokenUsageStatusRow) string {
	if len(rows) == 0 {
		return ""
	}
	showReasoning := false
	for _, row := range rows {
		if normalizedUsageSnapshot(row.usage).ReasoningTokens > 0 {
			showReasoning = true
			break
		}
	}
	table := make([][]string, 0, len(rows)+2)
	header := []string{"Scope", "Total", "Input", "Cached", "Output"}
	if showReasoning {
		header = append(header, "Reasoning")
	}
	table = append(table, header)
	for _, row := range rows {
		usage := normalizedUsageSnapshot(row.usage)
		cols := []string{
			row.scope,
			formatTokenUsageNumber(usage.TotalTokens),
			formatTokenUsageNumber(usage.PromptTokens),
			formatTokenUsageNumber(usage.CachedInputTokens),
			formatTokenUsageNumber(usage.CompletionTokens),
		}
		if showReasoning {
			cols = append(cols, formatTokenUsageNumber(usage.ReasoningTokens))
		}
		table = append(table, cols)
	}
	widths := make([]int, len(table[0]))
	for _, row := range table {
		for i, col := range row {
			if n := len([]rune(col)); n > widths[i] {
				widths[i] = n
			}
		}
	}
	lines := make([]string, 0, len(table))
	for _, row := range table {
		parts := make([]string, len(row))
		for i, col := range row {
			parts[i] = padRightRunes(col, widths[i])
		}
		lines = append(lines, strings.TrimRight(strings.Join(parts, "  "), " "))
	}
	return strings.Join(lines, "\n")
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
