package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/ports/model"
)

func (l *anthropicSDKLLM) WebSearchUnavailableReason() string {
	if l == nil {
		return "Provider-native web_search is unavailable because the model provider is not configured."
	}
	if anthropicRuntimeProviderSupportsWebSearch(l.provider) {
		return ""
	}
	provider := strings.TrimSpace(l.provider)
	if provider == "" {
		provider = "this provider"
	}
	return fmt.Sprintf("%s does not support provider-native web_search through Caelis. Use web_fetch with a known URL, or choose a DeepSeek/Anthropic provider.", provider)
}

func (l *anthropicSDKLLM) SearchWeb(ctx context.Context, req model.WebSearchRequest) (model.WebSearchResponse, error) {
	req = model.NormalizeWebSearchRequest(req)
	if req.Query == "" {
		return model.WebSearchResponse{}, fmt.Errorf("model: web search query is required")
	}
	if reason := strings.TrimSpace(l.WebSearchUnavailableReason()); reason != "" {
		return model.WebSearchResponse{}, fmt.Errorf("model: %s", reason)
	}
	runCtx, cancel := context.WithTimeout(ctx, firstPositiveDuration(l.requestTimeout, defaultWebSearchTimeout))
	defer cancel()

	maxUses := req.MaxResults
	if maxUses <= 0 {
		maxUses = 1
	}
	if maxUses > 5 {
		maxUses = 5
	}
	searchReq := &model.Request{
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, anthropicWebSearchPrompt(req)),
		},
		Tools: []model.ToolSpec{
			model.NewProviderExecutedToolSpec(l.provider, anthropicWebSearchToolName, map[string]json.RawMessage{
				"max_uses": mustRawJSON(maxUses),
			}),
		},
		Output: &model.OutputSpec{MaxOutputTokens: 512},
	}

	var final *model.Response
	for event, err := range l.Generate(runCtx, searchReq) {
		if err != nil {
			return model.WebSearchResponse{}, err
		}
		if event != nil && event.Response != nil {
			final = event.Response
		}
	}
	if final == nil {
		return model.WebSearchResponse{}, fmt.Errorf("model: empty web search response")
	}
	return model.WebSearchResponse{
		Query:    req.Query,
		Provider: firstNonEmptyString(final.Provider, l.provider),
		Model:    firstNonEmptyString(final.Model, l.name),
		Answer:   strings.TrimSpace(final.Message.TextContent()),
		Results:  anthropicWebSearchResultsFromMessage(final.Message, req.MaxResults),
		Usage:    final.Usage,
	}, nil
}

func anthropicRuntimeProviderSupportsWebSearch(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "anthropic-compatible", "deepseek":
		return true
	default:
		return false
	}
}

func anthropicWebSearchPrompt(req model.WebSearchRequest) string {
	return fmt.Sprintf("Use provider-native web search for this exact query and return a concise answer with the most relevant source URLs. Query: %s", req.Query)
}
