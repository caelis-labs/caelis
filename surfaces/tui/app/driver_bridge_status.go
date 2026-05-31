package tuiapp

import (
	"fmt"
	"strconv"
	"strings"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
)

func formatStatusSnapshot(status tuidriver.StatusSnapshot) string {
	model := statusViewModelFromSnapshot(status).HeaderModelText(firstNonEmpty(strings.TrimSpace(status.Model), strings.TrimSpace(status.ModelName), deriveModelNameFromAlias(status.Model), "not configured"))
	lines := []string{}
	appendStatusField(&lines, "Model", model)
	appendStatusField(&lines, "Mode", firstNonEmpty(strings.TrimSpace(status.ModeLabel), "auto-review"))
	appendStatusField(&lines, "Sandbox", formatStatusSandbox(status))
	appendStatusField(&lines, "Workspace", firstNonEmpty(strings.TrimSpace(status.Workspace), "-"))
	if usage := formatContextUsageStatus(status.TotalTokens, status.ContextWindowTokens); usage != "" {
		appendStatusField(&lines, "Context", usage)
	}
	if status.PermissionGrantCount > 0 {
		appendStatusField(&lines, "Grants", fmt.Sprintf("%d approved, read roots %d, write roots %d", status.PermissionGrantCount, status.PermissionReadRootCount, status.PermissionWriteRootCount))
	}
	if status.FallbackReason != "" {
		appendStatusField(&lines, "Fallback", strings.TrimSpace(status.FallbackReason))
	}
	if status.SandboxInstallHint != "" {
		appendStatusField(&lines, "Install", strings.TrimSpace(status.SandboxInstallHint))
	}
	globalSetup, hasGlobalSetup := status.SandboxSetup.Check("global")
	workspaceSetup, hasWorkspaceSetup := status.SandboxSetup.Check("workspace")
	globalSetupRequired := status.SandboxGlobalSetupRequired || (hasGlobalSetup && globalSetup.Required)
	workspaceSetupRequired := status.SandboxWorkspaceSetupRequired || (hasWorkspaceSetup && workspaceSetup.Required)
	setupError := firstNonEmpty(status.SandboxSetup.Error, globalSetup.Error, workspaceSetup.Error, status.SandboxSetupError)
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
	if strings.TrimSpace(status.Model) == "" && strings.TrimSpace(status.Provider) == "" && strings.TrimSpace(status.ModelName) == "" {
		warnings = append(warnings, "Run /connect to configure a provider and model")
	}
	if status.MissingAPIKey {
		warnings = append(warnings, "API key is missing; reconnect with a key or use env:YOUR_API_KEY")
	}
	if status.HostExecution || status.FullAccessMode {
		warnings = append(warnings, "Commands may run on the host with reduced sandbox isolation")
		warnings = append(warnings, "Auto-Review remains enabled and can approve host execution; use /approval manual for sensitive work")
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
	if strings.TrimSpace(status.FallbackReason) != "" {
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

func formatStatusSandbox(status tuidriver.StatusSnapshot) string {
	sandbox := firstNonEmpty(strings.TrimSpace(status.SandboxResolvedBackend), strings.TrimSpace(status.SandboxType), strings.TrimSpace(status.SandboxRequestedBackend), "auto")
	security := strings.TrimSpace(status.SecuritySummary)
	switch {
	case status.FullAccessMode:
		return firstNonEmpty(security, "full access")
	case status.HostExecution:
		return firstNonEmpty(security, "host execution")
	}
	route := strings.ToLower(strings.TrimSpace(status.Route))
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
		return sandbox + " (" + strings.TrimSpace(status.Route) + ")"
	}
}

func formatSessionTokenUsageStatus(status tuidriver.StatusSnapshot) string {
	total := normalizedTokenUsage(status.SessionUsageTotal)
	if tokenUsageZero(total) {
		total = normalizedTokenUsage(appviewmodel.TokenUsage{
			InputTokens:       status.SessionInputTokens,
			CachedInputTokens: status.SessionCachedInputTokens,
			OutputTokens:      status.SessionOutputTokens,
			ReasoningTokens:   status.SessionReasoningTokens,
			TotalTokens:       status.SessionTotalTokens,
		})
	}
	if tokenUsageZero(total) {
		return ""
	}
	rows := []tokenUsageStatusRow{{scope: "total", usage: total}}
	main := normalizedTokenUsage(status.SessionUsageMain)
	subagents := normalizedTokenUsage(status.SessionUsageSubagents)
	autoReview := normalizedTokenUsage(status.SessionUsageAutoReview)
	compaction := normalizedTokenUsage(status.SessionUsageCompaction)
	if !tokenUsageZero(main) {
		rows = append(rows, tokenUsageStatusRow{scope: "main", usage: main})
	}
	if !tokenUsageZero(subagents) {
		rows = append(rows, tokenUsageStatusRow{scope: "sub-agent", usage: subagents})
	}
	if !tokenUsageZero(autoReview) {
		rows = append(rows, tokenUsageStatusRow{scope: "auto-review", usage: autoReview})
	}
	if !tokenUsageZero(compaction) {
		rows = append(rows, tokenUsageStatusRow{scope: "compaction", usage: compaction})
	}
	return formatTokenUsageTable(rows)
}

type tokenUsageStatusRow struct {
	scope string
	usage appviewmodel.TokenUsage
}

func formatTokenUsageTable(rows []tokenUsageStatusRow) string {
	if len(rows) == 0 {
		return ""
	}
	table := make([][]string, 0, len(rows)+2)
	table = append(table, []string{"Scope", "Total", "Input", "Cached", "Output", "Reasoning"})
	for _, row := range rows {
		usage := normalizedTokenUsage(row.usage)
		table = append(table, []string{
			row.scope,
			formatTokenUsageNumber(usage.TotalTokens),
			formatTokenUsageNumber(usage.InputTokens),
			formatTokenUsageNumber(usage.CachedInputTokens),
			formatTokenUsageNumber(usage.OutputTokens),
			formatTokenUsageNumber(usage.ReasoningTokens),
		})
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

func normalizedTokenUsage(usage appviewmodel.TokenUsage) appviewmodel.TokenUsage {
	return appviewmodel.NormalizeTokenUsage(usage)
}

func tokenUsageZero(usage appviewmodel.TokenUsage) bool {
	return appviewmodel.TokenUsageZero(usage)
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
