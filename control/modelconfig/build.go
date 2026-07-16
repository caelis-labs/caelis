package modelconfig

import (
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
)

// ModelResolution is the fully constructed SDK model and its Control-selected
// reasoning defaults.
type ModelResolution struct {
	Model                  model.LLM
	ReasoningEffort        string
	DefaultReasoningEffort string
}

// BuildModel constructs an SDK model from a complete Control configuration.
// overrideContextWindow wins over the configured and host fallback values.
func BuildModel(cfg Config, fallbackContextWindow int, overrideContextWindow int) (ModelResolution, error) {
	cfg = NormalizeConfig(cfg)
	effectiveContextWindow := fallbackContextWindow
	if cfg.ContextWindowTokens > 0 {
		effectiveContextWindow = cfg.ContextWindowTokens
	}
	if overrideContextWindow > 0 {
		effectiveContextWindow = overrideContextWindow
	}
	record := providers.Config{
		Alias:                     cfg.ID,
		Provider:                  cfg.Provider,
		API:                       cfg.API,
		Model:                     cfg.Model,
		BaseURL:                   cfg.BaseURL,
		HTTPClient:                cfg.HTTPClient,
		Timeout:                   cfg.Timeout,
		StreamFirstEventTimeout:   cfg.StreamFirstEventTimeout,
		MaxOutputTok:              cfg.MaxOutputTok,
		ContextWindowTokens:       effectiveContextWindow,
		ReasoningLevels:           append([]string(nil), cfg.ReasoningLevels...),
		ReasoningMode:             cfg.ReasoningMode,
		DefaultReasoningEffort:    cfg.DefaultReasoningEffort,
		ReasoningEffort:           cfg.ReasoningEffort,
		SupportedReasoningEfforts: append([]string(nil), cfg.ReasoningLevels...),
		Auth: providers.AuthConfig{
			Type:          cfg.AuthType,
			Token:         cfg.Token,
			TokenEnv:      cfg.TokenEnv,
			CredentialRef: cfg.CredentialRef,
			HeaderKey:     cfg.HeaderKey,
		},
	}
	factory := providers.NewFactory()
	if err := factory.Register(record); err != nil {
		return ModelResolution{}, err
	}
	llm, err := factory.NewByAlias(cfg.ID)
	if err != nil {
		return ModelResolution{}, err
	}
	return ModelResolution{
		Model:                  llm,
		ReasoningEffort:        cfg.ReasoningEffort,
		DefaultReasoningEffort: cfg.DefaultReasoningEffort,
	}, nil
}
