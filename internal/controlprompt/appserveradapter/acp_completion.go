package appserveradapter

import (
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func projectConnectACPModels(snapshot controlagents.DiscoverySnapshot, query string, limit int) []controlprompt.SlashArgCandidate {
	if len(snapshot.Models) == 0 {
		return filterSlashArgCandidates([]controlprompt.SlashArgCandidate{{
			Value: controlagents.DefaultRemoteModelID, Display: "Agent default",
			Detail:                "Use the ACP Agent's default model without sending a model selection",
			ModelMetadataComplete: true,
		}}, query, limit)
	}
	candidates := make([]controlprompt.SlashArgCandidate, 0, len(snapshot.Models))
	for _, model := range snapshot.Models {
		candidates = append(candidates, controlprompt.SlashArgCandidate{
			Value: model.ID, Display: firstNonEmpty(model.Name, model.ID),
			Detail: firstNonEmpty(model.Description, "remote ACP model"), ModelMetadataComplete: true,
		})
	}
	return filterSlashArgCandidates(candidates, query, limit)
}

func filterSlashArgCandidates(candidates []controlprompt.SlashArgCandidate, query string, limit int) []controlprompt.SlashArgCandidate {
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]controlprompt.SlashArgCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if query != "" && !hasCandidatePrefix(query, candidate.Value, candidate.Display, candidate.Detail) {
			continue
		}
		out = append(out, candidate)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func hasCandidatePrefix(query string, values ...string) bool {
	if query == "" {
		return true
	}
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized != "" && strings.HasPrefix(normalized, query) {
			return true
		}
	}
	return false
}
