package controladapter

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func (d *assembler) completeDisconnectModels(ctx context.Context, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	choices, err := listModelChoices(ctx, d.deps.Model, session.SessionRef{})
	if err != nil {
		return nil, err
	}
	choices = slices.Clone(choices)
	sort.SliceStable(choices, func(i, j int) bool {
		if left, right := choices[i].Provider, choices[j].Provider; left != right {
			return left < right
		}
		return choices[i].Model < choices[j].Model
	})
	candidates := make([]controlprompt.SlashArgCandidate, 0, len(choices))
	for _, choice := range choices {
		cfg := modelChoiceConfig(choice)
		id := choice.ProfileID
		detail := choice.Detail
		if choice.Backend != string(modelprofile.BackendACP) {
			id = firstNonEmpty(id, modelprofile.BuildProviderID(cfg.ID))
			detail = modelconfig.ChoiceDetail(cfg)
		}
		if id == "" {
			continue
		}
		if choice.Current {
			detail = "current · " + detail
		}
		display := modelChoiceDisplay(choice, cfg)
		if query != "" && !strings.Contains(strings.ToLower(display), query) &&
			!strings.Contains(strings.ToLower(id), query) && !strings.Contains(strings.ToLower(detail), query) {
			continue
		}
		candidates = append(candidates, controlprompt.SlashArgCandidate{Value: id, Display: display, Detail: detail})
		if len(candidates) >= limit {
			break
		}
	}
	return candidates, nil
}

func (d *assembler) completeDisconnectProviderModels(ctx context.Context, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	choices, err := listModelChoices(ctx, d.deps.Model, session.SessionRef{})
	if err != nil {
		return nil, err
	}
	providers := make([]ModelChoice, 0, len(choices))
	for _, choice := range choices {
		if choice.Backend != string(modelprofile.BackendACP) {
			choice.Detail = modelconfig.ChoiceDetail(modelChoiceConfig(choice))
			if choice.Current {
				choice.Detail = "current · " + choice.Detail
			}
			providers = append(providers, choice)
		}
	}
	sort.SliceStable(providers, func(i, j int) bool {
		left, right := providers[i], providers[j]
		for _, pair := range [][2]string{
			{left.Provider, right.Provider},
			{firstNonEmpty(left.Alias, left.ID), firstNonEmpty(right.Alias, right.ID)},
			{left.ID, right.ID},
		} {
			a, b := strings.ToLower(strings.TrimSpace(pair[0])), strings.ToLower(strings.TrimSpace(pair[1]))
			if a != b {
				return a < b
			}
		}
		return false
	})
	return modelChoiceCandidates(providers, query, limit)
}

func completeDisconnectACPAgents(ctx context.Context, driver *assembler, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	if driver == nil {
		return nil, missingRuntimeDependency("ACP Agent disconnect")
	}
	connected, err := driver.DisconnectCandidates(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]controlprompt.SlashArgCandidate, 0, len(connected))
	for _, candidate := range connected {
		detail := firstNonEmpty(candidate.Name, candidate.ConnectionID, "local ACP Agent")
		if candidate.LastOnConnection {
			detail += " · last Agent on this connection; keeps the installed adapter"
		} else {
			detail += fmt.Sprintf(" · %d other %s will remain", candidate.SiblingCount, pluralAgent(candidate.SiblingCount))
		}
		candidates = append(candidates, controlprompt.SlashArgCandidate{
			Value: candidate.AgentID, Display: "/" + candidate.AgentID, Detail: detail,
		})
	}
	return filterSlashArgCandidates(candidates, query, limit), nil
}

func pluralAgent(count int) string {
	if count == 1 {
		return "Agent"
	}
	return "Agents"
}
