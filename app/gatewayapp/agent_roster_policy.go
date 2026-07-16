package gatewayapp

import (
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func rosterAgentNameAllowed(id string) bool {
	return !forbiddenRosterAgentID(id)
}

func forbiddenRosterAgentID(id string) bool {
	id = controlagents.NormalizeName(id)
	if id == "" || !controlagents.IsName(id) {
		return true
	}
	if _, _, ok := controlagents.ParseRunName(id); ok {
		return true
	}
	if reservedSlashCommandName(id) {
		return true
	}
	switch strings.ToLower(id) {
	case "guardian", "reviewer", "self", "breeze", "orbit", "zenith", "local", "main", "kernel", "sandbox":
		return true
	default:
		return false
	}
}
