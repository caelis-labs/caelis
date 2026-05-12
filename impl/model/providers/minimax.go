package providers

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

const miniMaxDefaultBaseURL = "https://api.minimaxi.com/anthropic"

func newMiniMax(cfg Config, token string) model.LLM {
	cfg = withMiniMaxDefaults(cfg)
	return newAnthropicWithDefaults(cfg, token, anthropicProviderDefaults{
		provider:     "minimax",
		baseURL:      miniMaxDefaultBaseURL,
		maxOutputTok: 4096,
	})
}

func withMiniMaxDefaults(cfg Config) Config {
	if strings.TrimSpace(cfg.Provider) == "" {
		cfg.Provider = "minimax"
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = miniMaxDefaultBaseURL
	}
	if cfg.Auth.Type == "" {
		cfg.Auth.Type = AuthBearerToken
	}
	return cfg
}
