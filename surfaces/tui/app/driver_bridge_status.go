package tuiapp

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
)

func formatStatusSnapshot(status tuidriver.StatusSnapshot) string {
	model := firstNonEmpty(strings.TrimSpace(status.Model), strings.TrimSpace(status.ModelName), deriveModelNameFromAlias(status.Model), "not configured")
	provider := firstNonEmpty(strings.TrimSpace(status.Provider), deriveProviderFromAlias(status.Model), "not configured")
	sandbox := firstNonEmpty(strings.TrimSpace(status.SandboxResolvedBackend), strings.TrimSpace(status.SandboxType), "auto")
	route := strings.TrimSpace(status.Route)
	if route != "" && route != "-" {
		sandbox += " via " + route
	}
	lines := []string{"Session"}
	lines = append(lines, fmt.Sprintf("  Session    %s", firstNonEmpty(strings.TrimSpace(status.SessionID), "-")))
	lines = append(lines, fmt.Sprintf("  Provider   %s", provider))
	lines = append(lines, fmt.Sprintf("  Model      %s", model))
	lines = append(lines, fmt.Sprintf("  Mode       %s", firstNonEmpty(strings.TrimSpace(status.ModeLabel), "auto-review")))
	lines = append(lines, fmt.Sprintf("  Sandbox    %s", sandbox))
	lines = append(lines, fmt.Sprintf("  Workspace  %s", firstNonEmpty(strings.TrimSpace(status.Workspace), "-")))
	lines = append(lines, fmt.Sprintf("  Store      %s", firstNonEmpty(strings.TrimSpace(status.StoreDir), "-")))
	if usage := formatContextUsageStatus(status.TotalTokens, status.ContextWindowTokens); usage != "" {
		lines = append(lines, fmt.Sprintf("  Context    %s", usage))
	}
	if usage := formatSessionTokenUsageStatus(status); usage != "" {
		lines = append(lines, "  Token usage")
		for _, line := range strings.Split(usage, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			lines = append(lines, "    "+line)
		}
	}
	if status.PermissionGrantCount > 0 {
		lines = append(lines, fmt.Sprintf("  Grants     %d approved, read roots %d, write roots %d", status.PermissionGrantCount, status.PermissionReadRootCount, status.PermissionWriteRootCount))
	}
	if status.FallbackReason != "" {
		lines = append(lines, "  Fallback   "+strings.TrimSpace(status.FallbackReason))
	}
	if status.SandboxInstallHint != "" {
		lines = append(lines, "  Install    "+strings.TrimSpace(status.SandboxInstallHint))
	}
	globalSetup, hasGlobalSetup := status.SandboxSetup.Check("global")
	workspaceSetup, hasWorkspaceSetup := status.SandboxSetup.Check("workspace")
	globalSetupRequired := status.SandboxGlobalSetupRequired || (hasGlobalSetup && globalSetup.Required)
	workspaceSetupRequired := status.SandboxWorkspaceSetupRequired || (hasWorkspaceSetup && workspaceSetup.Required)
	globalSetupReason := firstNonEmpty(globalSetup.Reason, status.SandboxGlobalSetupReason, status.SandboxSetupMarkerReason)
	workspaceSetupReason := firstNonEmpty(workspaceSetup.Reason, status.SandboxWorkspaceSetupReason)
	setupError := firstNonEmpty(status.SandboxSetup.Error, globalSetup.Error, workspaceSetup.Error, status.SandboxSetupError)
	if globalSetupRequired {
		lines = append(lines, "  Setup      Windows sandbox infrastructure required; run /sandbox setup")
	} else if workspaceSetupRequired {
		lines = append(lines, "  Setup      current workspace ACL required; run /sandbox setup")
	}
	if globalSetupReason != "" {
		lines = append(lines, "  Reason     "+strings.TrimSpace(globalSetupReason))
	} else if workspaceSetupReason != "" {
		lines = append(lines, "  Reason     "+strings.TrimSpace(workspaceSetupReason))
	}
	if setupError != "" {
		lines = append(lines, "  Error      "+strings.TrimSpace(setupError))
	}
	if strings.TrimSpace(status.Model) == "" && strings.TrimSpace(status.Provider) == "" && strings.TrimSpace(status.ModelName) == "" {
		lines = append(lines, "note: Run /connect to configure a provider and model")
	}
	if status.MissingAPIKey {
		lines = append(lines, "warn: API key is missing; reconnect with a key or use env:YOUR_API_KEY")
	}
	if status.HostExecution || status.FullAccessMode {
		lines = append(lines, "warn: Commands may run on the host with reduced sandbox isolation")
		lines = append(lines, "warn: Auto-Review remains enabled and can approve host execution; use /approval manual for sensitive work")
	}
	if globalSetupRequired {
		lines = append(lines, "warn: Windows sandbox infrastructure setup is required before sandboxed commands can run")
	} else if workspaceSetupRequired {
		lines = append(lines, "warn: Current workspace needs Windows sandbox ACL setup before sandboxed commands can run")
	}
	if strings.TrimSpace(status.FallbackReason) != "" {
		lines = append(lines, "warn: Requested sandbox backend is unavailable and a fallback is in effect")
	}
	return strings.Join(lines, "\n")
}

func formatSessionTokenUsageStatus(status tuidriver.StatusSnapshot) string {
	total := normalizedUsageSnapshot(status.SessionUsageTotal)
	if usageSnapshotZero(total) {
		total = normalizedUsageSnapshot(kernel.UsageSnapshot{
			PromptTokens:      status.SessionInputTokens,
			CachedInputTokens: status.SessionCachedInputTokens,
			CompletionTokens:  status.SessionOutputTokens,
			ReasoningTokens:   status.SessionReasoningTokens,
			TotalTokens:       status.SessionTotalTokens,
		})
	}
	if usageSnapshotZero(total) {
		return ""
	}
	rows := []tokenUsageStatusRow{{scope: "total", usage: total}}
	main := normalizedUsageSnapshot(status.SessionUsageMain)
	subagents := normalizedUsageSnapshot(status.SessionUsageSubagents)
	autoReview := normalizedUsageSnapshot(status.SessionUsageAutoReview)
	if !usageSnapshotZero(main) {
		rows = append(rows, tokenUsageStatusRow{scope: "main", usage: main})
	}
	if !usageSnapshotZero(subagents) {
		rows = append(rows, tokenUsageStatusRow{scope: "sub-agent", usage: subagents})
	}
	if !usageSnapshotZero(autoReview) {
		rows = append(rows, tokenUsageStatusRow{scope: "auto-review", usage: autoReview})
	}
	return formatTokenUsageTable(rows)
}

type tokenUsageStatusRow struct {
	scope string
	usage kernel.UsageSnapshot
}

func formatTokenUsageTable(rows []tokenUsageStatusRow) string {
	if len(rows) == 0 {
		return ""
	}
	table := make([][]string, 0, len(rows)+1)
	table = append(table, []string{"Scope", "Total", "Input", "Cached", "Output", "Reasoning"})
	for _, row := range rows {
		usage := normalizedUsageSnapshot(row.usage)
		table = append(table, []string{
			row.scope,
			formatTokenUsageNumber(usage.TotalTokens),
			formatTokenUsageNumber(usage.PromptTokens),
			formatTokenUsageNumber(usage.CachedInputTokens),
			formatTokenUsageNumber(usage.CompletionTokens),
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

func normalizedUsageSnapshot(usage kernel.UsageSnapshot) kernel.UsageSnapshot {
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

func usageSnapshotZero(usage kernel.UsageSnapshot) bool {
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
