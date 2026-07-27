package controlprompt

import (
	"strings"

	controlstatus "github.com/caelis-labs/caelis/control/status"
)

// NewHelpSlashResult builds a structured /help result.
func NewHelpSlashResult(help CommandHelpSnapshot) SlashCommandResult {
	return SlashCommandResult{
		Command: "help",
		Kind:    SlashCommandResultHelp,
		Help:    help,
	}
}

// NewStatusSlashResult builds a structured /status result.
func NewStatusSlashResult(status controlstatus.StatusSnapshot) SlashCommandResult {
	return SlashCommandResult{
		Command: "status",
		Kind:    SlashCommandResultStatus,
		Status:  status,
	}
}

// NewDoctorSlashResult builds a structured /doctor result.
func NewDoctorSlashResult(status controlstatus.StatusSnapshot) SlashCommandResult {
	return SlashCommandResult{
		Command: "doctor",
		Kind:    SlashCommandResultDoctor,
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
