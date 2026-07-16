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

// FormatSlashResult renders a structured slash result for plain-text surfaces.
// Rich surfaces should consume the structured payload directly.
func FormatSlashResult(result SlashCommandResult) string {
	switch result.Kind {
	case SlashCommandResultHelp:
		return FormatCommandHelp(result.Help)
	case SlashCommandResultStatus:
		return FormatStatusSnapshot(result.Status)
	default:
		command := strings.TrimSpace(result.Command)
		if command == "" {
			return "slash command produced an unsupported result"
		}
		return "slash command produced an unsupported result: " + command
	}
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
