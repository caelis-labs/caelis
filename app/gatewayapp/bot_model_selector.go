package gatewayapp

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

// botModelSelectorProjector decorates the durable Bot read with the public
// selector Control publishes for each Bot's configured model. The selector is
// display data only: Config.Model remains the durable identity used for
// resolution, saving, and model requests. A model the current catalog does not
// contain leaves ModelSelector empty, so clients fall back to the durable ID
// rather than parsing it.
type botModelSelectorProjector struct {
	reader appserver.BotReader
	lookup *modelLookup
}

func (p botModelSelectorProjector) ListBots(ctx context.Context, ownerID string) ([]bot.Bot, error) {
	bots, err := p.reader.ListBots(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	selectors := p.selectors()
	for i := range bots {
		bots[i].ModelSelector = selectors[selectorKey(bots[i].Config.Model)]
	}
	return bots, nil
}

func (p botModelSelectorProjector) GetBot(ctx context.Context, id string) (bot.Bot, error) {
	value, err := p.reader.GetBot(ctx, id)
	if err != nil {
		return bot.Bot{}, err
	}
	value.ModelSelector = p.selectors()[selectorKey(value.Config.Model)]
	return value, nil
}

// selectors projects one catalog snapshot into durable-ID to public-selector
// pairs. Reading the snapshot once per Bot read keeps a list consistent and
// never parses a durable ID.
func (p botModelSelectorProjector) selectors() map[string]string {
	if p.lookup == nil {
		return nil
	}
	configs := p.lookup.Snapshot().Configs
	selectors := make(map[string]string, len(configs))
	for _, cfg := range configs {
		if id := selectorKey(cfg.ID); id != "" {
			selectors[id] = modelconfig.PublicSelector(cfg)
		}
	}
	return selectors
}

// selectorKey normalizes a durable model config ID for catalog lookup. It does
// not interpret the ID.
func selectorKey(modelID string) string {
	return strings.ToLower(strings.TrimSpace(modelID))
}

var _ appserver.BotReader = botModelSelectorProjector{}
