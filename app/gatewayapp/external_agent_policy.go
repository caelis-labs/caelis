package gatewayapp

import (
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func externalAgentNameAllowed(id string) bool {
	return !forbiddenExternalAgentID(id)
}

func forbiddenExternalAgentID(id string) bool {
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
	case "guardian", "reviewer", "self", "breeze", "orbit", "zenith", "lead", "local", "main", "kernel", "sandbox":
		return true
	default:
		return false
	}
}
