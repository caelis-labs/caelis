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
	llm, err := providers.New(providers.Config{
		Provider:                cfg.Provider,
		API:                     cfg.API,
		Model:                   cfg.Model,
		BaseURL:                 cfg.BaseURL,
		HTTPClient:              cfg.HTTPClient,
		Timeout:                 cfg.Timeout,
		StreamFirstEventTimeout: cfg.StreamFirstEventTimeout,
		MaxOutputTok:            cfg.MaxOutputTok,
		ContextWindowTokens:     effectiveContextWindow,
		ImageInput:              ModelSupportsImages(cfg),
		ReasoningLevels:         append([]string(nil), cfg.ReasoningLevels...),
		ReasoningMode:           cfg.ReasoningMode,
		Auth: providers.AuthConfig{
			Type:      cfg.AuthType,
			Token:     cfg.Token,
			HeaderKey: cfg.HeaderKey,
		},
	})
	if err != nil {
		return ModelResolution{}, err
	}
	return ModelResolution{
		Model:                  llm,
		ReasoningEffort:        cfg.ReasoningEffort,
		DefaultReasoningEffort: cfg.DefaultReasoningEffort,
	}, nil
}
