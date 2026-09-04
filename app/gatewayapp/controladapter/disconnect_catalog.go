package controladapter

import (
	"context"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func (d *assembler) completeDisconnectProviderModels(ctx context.Context, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	choices, err := listModelChoices(ctx, d.deps.Model, session.SessionRef{})
	if err != nil {
		return nil, err
	}
	providers := make([]ModelChoice, 0, len(choices))
	for _, choice := range choices {
		if choice.Backend != string(modelprofile.BackendACP) {
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
	return modelChoiceCandidates(providers, query, limit), nil
}
