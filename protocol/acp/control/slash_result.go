package control

import "strings"

// NewHelpSlashResult builds a structured /help result.
func NewHelpSlashResult(help CommandHelpSnapshot) SlashCommandResult {
	return SlashCommandResult{
		Command: "help",
		Kind:    SlashCommandResultHelp,
		Help:    help,
	}
}

// NewStatusSlashResult builds a structured /status result.
func NewStatusSlashResult(status StatusSnapshot) SlashCommandResult {
	return SlashCommandResult{
		Command: "status",
		Kind:    SlashCommandResultStatus,
		Status:  status,
	}
}

// NewTableSlashResult builds structured tabular output for a slash command.
func NewTableSlashResult(command string, table SlashTableSnapshot) SlashCommandResult {
	return SlashCommandResult{
		Command: strings.ToLower(strings.TrimSpace(command)),
		Kind:    SlashCommandResultTable,
		Table:   table,
	}
}

// FormatSlashResult renders a structured slash result for plain-text surfaces.
// Rich surfaces should consume the structured payload directly.
func FormatSlashResult(result SlashCommandResult) string {
	switch result.Kind {
	case SlashCommandResultHelp:
		return FormatCommandHelp(result.Help)
	case SlashCommandResultStatus:
		return FormatStatusSnapshot(result.Status)
	case SlashCommandResultTable:
		return FormatSlashTable(result.Table)
	default:
		command := strings.TrimSpace(result.Command)
		if command == "" {
			return "slash command produced an unsupported result"
		}
		return "slash command produced an unsupported result: " + command
	}
}

// FormatSlashTable renders a table snapshot for plain-text surfaces.
func FormatSlashTable(table SlashTableSnapshot) string {
	lines := make([]string, 0)
	if title := strings.TrimSpace(table.Title); title != "" {
		lines = append(lines, title)
	}
	writtenSections := 0
	for _, section := range table.Sections {
		if len(section.Columns) == 0 && len(section.Rows) == 0 {
			continue
		}
		if writtenSections > 0 {
			lines = append(lines, "")
		}
		if title := strings.TrimSpace(section.Title); title != "" {
			lines = append(lines, title)
		}
		lines = append(lines, formatSlashTableRows(section.Columns, section.Rows)...)
		writtenSections++
	}
	return strings.Join(lines, "\n")
}

func formatSlashTableRows(columns []string, rows [][]string) []string {
	table := make([][]string, 0, len(rows)+1)
	if len(columns) > 0 {
		table = append(table, append([]string(nil), columns...))
	}
	for _, row := range rows {
		table = append(table, append([]string(nil), row...))
	}
	if len(table) == 0 {
		return nil
	}
	widths := make([]int, 0)
	for i := range table {
		for j := range table[i] {
			table[i][j] = strings.TrimSpace(table[i][j])
			for len(widths) <= j {
				widths = append(widths, 0)
			}
			if width := len([]rune(table[i][j])); width > widths[j] {
				widths[j] = width
			}
		}
	}
	out := make([]string, 0, len(table)+1)
	for rowIndex, row := range table {
		parts := make([]string, len(row))
		for columnIndex, value := range row {
			parts[columnIndex] = padRightRunes(value, widths[columnIndex])
		}
		out = append(out, "  "+strings.TrimRight(strings.Join(parts, "  "), " "))
		if len(columns) > 0 && rowIndex == 0 {
			divider := make([]string, len(row))
			for columnIndex := range row {
				divider[columnIndex] = strings.Repeat("─", widths[columnIndex])
			}
			out = append(out, "  "+strings.Join(divider, "  "))
		}
	}
	return out
}

// FormatCommandHelp renders a help snapshot for plain-text surfaces.
func FormatCommandHelp(help CommandHelpSnapshot) string {
	type row struct {
		usage       string
		description string
		details     []string
	}
	rows := make([]row, 0, len(help.Items))
	for _, item := range help.Items {
		rows = append(rows, row{
			usage:       strings.TrimSpace(item.Usage),
			description: strings.TrimSpace(item.Description),
			details:     item.Details,
		})
	}
	width := 0
	for _, row := range rows {
		if n := len([]rune(row.usage)); n > width {
			width = n
		}
	}
	if width < 12 {
		width = 12
	}
	if width > 24 {
		width = 24
	}
	lines := []string{"Commands:"}
	for _, row := range rows {
		usage := strings.TrimSpace(row.usage)
		description := strings.TrimSpace(row.description)
		if description == "" {
			lines = append(lines, "  "+usage)
		} else {
			lines = append(lines, "  "+padRightRunes(usage, width)+"  "+description)
		}
		for _, detail := range row.details {
			detail = strings.TrimSpace(detail)
			if detail != "" {
				lines = append(lines, "  "+strings.Repeat(" ", width)+"  "+detail)
			}
		}
	}
	return strings.Join(lines, "\n")
}
