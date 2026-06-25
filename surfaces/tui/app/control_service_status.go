package tuiapp

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
	"github.com/OnslaughtSnail/caelis/surfaces/statusbar"
)

func formatStatusSnapshot(status control.StatusSnapshot) string {
	model := statusViewModelFromSnapshot(status).HeaderModelText("")
	lines := []string{}
	appendStatusField(&lines, "Model", model)
	appendStatusField(&lines, "Mode", firstNonEmpty(strings.TrimSpace(status.Session.ModeLabel), "auto-review"))
	appendStatusField(&lines, "Sandbox", formatStatusSandbox(status))
	appendStatusField(&lines, "Workspace", firstNonEmpty(strings.TrimSpace(status.Session.Workspace), "-"))
	appendStatusField(&lines, "Session", strings.TrimSpace(status.Session.ID))
	if usage := statusbar.FormatContextUsage(status.Usage.TotalTokens, status.Usage.ContextWindowTokens); usage != "" {
		appendStatusField(&lines, "Context", usage)
	}
	if status.SandboxStatus.FallbackReason != "" {
		appendStatusField(&lines, "Fallback", strings.TrimSpace(status.SandboxStatus.FallbackReason))
	}
	if status.SandboxStatus.InstallHint != "" {
		appendStatusField(&lines, "Install", strings.TrimSpace(status.SandboxStatus.InstallHint))
	}
	globalSetup, hasGlobalSetup := status.SandboxStatus.Setup.Check("global")
	workspaceSetup, hasWorkspaceSetup := status.SandboxStatus.Setup.Check("workspace")
	globalSetupRequired := status.SandboxStatus.GlobalSetupRequired || (hasGlobalSetup && globalSetup.Required)
	workspaceSetupRequired := status.SandboxStatus.WorkspaceSetupRequired || (hasWorkspaceSetup && workspaceSetup.Required)
	setupError := firstNonEmpty(status.SandboxStatus.Setup.Error, globalSetup.Error, workspaceSetup.Error, status.SandboxStatus.SetupError)
	if globalSetupRequired {
		if setupError != "" {
			appendStatusField(&lines, "Setup", "Windows sandbox infrastructure repair failed")
		} else {
			appendStatusField(&lines, "Setup", "Windows sandbox infrastructure repair is pending")
		}
	} else if workspaceSetupRequired {
		if setupError != "" {
			appendStatusField(&lines, "Setup", "current workspace ACL repair failed")
		} else {
			appendStatusField(&lines, "Setup", "current workspace ACL repair is pending")
		}
	}
	if setupError != "" {
		appendStatusField(&lines, "Error", compactStatusDetail(setupError, 180))
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
	if globalSetupRequired {
		if setupError != "" {
			warnings = append(warnings, "Run /doctor fix to repair Windows sandbox setup")
		} else {
			warnings = append(warnings, "Windows sandbox infrastructure will be repaired lazily before sandboxed commands run")
		}
	} else if workspaceSetupRequired {
		if setupError != "" {
			warnings = append(warnings, "Run /doctor fix to repair current workspace ACLs")
		} else {
			warnings = append(warnings, "Current workspace ACLs will be repaired lazily before sandboxed commands run")
		}
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

func formatStatusSandbox(status control.StatusSnapshot) string {
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

func formatSessionTokenUsageStatus(status control.StatusSnapshot) string {
	total := normalizedUsageSnapshot(status.Usage.SessionUsageTotal)
	if usageSnapshotZero(total) {
		total = normalizedUsageSnapshot(control.UsageSnapshot{
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

func modelUsageStatusLabel(item control.ModelUsageSnapshot) string {
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
	usage control.UsageSnapshot
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
	for _, cols := range table {
		for i, col := range cols {
			if len(col) > widths[i] {
				widths[i] = len(col)
			}
		}
	}
	var b strings.Builder
	for rowIndex, cols := range table {
		if rowIndex > 0 {
			b.WriteByte('\n')
		}
		if rowIndex == 1 {
			for colIndex, width := range widths {
				if colIndex > 0 {
					b.WriteString("  ")
				}
				b.WriteString(strings.Repeat("-", width))
			}
			b.WriteByte('\n')
		}
		for colIndex, col := range cols {
			if colIndex > 0 {
				b.WriteString("  ")
			}
			if colIndex == 0 {
				fmt.Fprintf(&b, "%-*s", widths[colIndex], col)
				continue
			}
			fmt.Fprintf(&b, "%*s", widths[colIndex], col)
		}
	}
	return b.String()
}

func normalizedUsageSnapshot(usage control.UsageSnapshot) control.UsageSnapshot {
	if usage.PromptTokens < 0 {
		usage.PromptTokens = 0
	}
	if usage.CachedInputTokens < 0 {
		usage.CachedInputTokens = 0
	}
	if usage.CompletionTokens < 0 {
		usage.CompletionTokens = 0
	}
	if usage.ReasoningTokens < 0 {
		usage.ReasoningTokens = 0
	}
	if usage.TotalTokens < 0 {
		usage.TotalTokens = 0
	}
	if usage.TotalTokens == 0 && (usage.PromptTokens != 0 || usage.CompletionTokens != 0) {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return usage
}

func usageSnapshotZero(usage control.UsageSnapshot) bool {
	return usage.PromptTokens == 0 &&
		usage.CachedInputTokens == 0 &&
		usage.CompletionTokens == 0 &&
		usage.ReasoningTokens == 0 &&
		usage.TotalTokens == 0
}

func formatTokenUsageNumber(tokens int) string {
	if tokens < 0 {
		tokens = 0
	}
	text := strconv.Itoa(tokens)
	if len(text) <= 3 {
		return text
	}
	var b strings.Builder
	prefix := len(text) % 3
	if prefix == 0 {
		prefix = 3
	}
	b.WriteString(text[:prefix])
	for i := prefix; i < len(text); i += 3 {
		b.WriteByte(',')
		b.WriteString(text[i : i+3])
	}
	return b.String()
}
