package providers

import (
	"context"
	"fmt"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/caelis-labs/caelis/agent-sdk/model"
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
	searchReq := &model.Request{
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, anthropicWebSearchPrompt(req)),
		},
		Tools: []model.ToolSpec{
			model.NewProviderExecutedToolSpec(l.provider, anthropicWebSearchToolName, nil),
		},
		Output: &model.OutputSpec{MaxOutputTokens: 512},
	}
	params, err := l.buildRequest(searchReq)
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	runCtx, cancel := webSearchRequestContext(ctx, l.requestTimeout)
	defer cancel()
	opts := []option.RequestOption(nil)
	if l.requestTimeout > 0 {
		opts = append(opts, option.WithRequestTimeout(l.requestTimeout))
	}
	// Messages.New adds an SDK-owned ten-minute deadline to every non-streaming
	// request. Use the SDK's generic JSON path so SearchWeb follows the shared
	// caller/explicit-timeout contract without changing the provider wire shape.
	cli := l.clientOrZero()
	var providerResponse anthropic.Message
	if err := cli.Post(runCtx, "v1/messages", params, &providerResponse, opts...); err != nil {
		return model.WebSearchResponse{}, err
	}
	message, _, _, usage, err := anthropicResponseToMessage(l.provider, &providerResponse)
	if err != nil {
		return model.WebSearchResponse{}, err
	}
	rawAnswer := message.TextContent()
	results := anthropicWebSearchResultsFromMessage(message)
	citations := message.TextContentCitations()
	if len(citations) == 0 && len(results) > 0 {
		citations = []model.Citation{{
			StartIndex: len(rawAnswer),
			EndIndex:   len(rawAnswer),
			Sources:    citationSourcesFromSearchResults(results),
		}}
	}
	answer, citations := trimCitedText(rawAnswer, citations)
	return model.WebSearchResponse{
		Query:     req.Query,
		Provider:  l.provider,
		Model:     firstNonEmptyString(providerResponse.Model, l.name),
		Answer:    answer,
		Results:   results,
		Citations: citations,
		Usage:     usage,
	}, nil
}

func anthropicTextCitations(text string, citations []anthropic.TextCitationUnion) []model.Citation {
	out := make([]model.Citation, 0, len(citations))
	searchFrom := 0
	for index, citation := range citations {
		if citation.Type != "web_search_result_location" || strings.TrimSpace(citation.URL) == "" {
			continue
		}
		start := len(text)
		end := len(text)
		citedText := citation.CitedText
		if citedText != "" {
			if relative := strings.Index(text[searchFrom:], citedText); relative >= 0 {
				start = searchFrom + relative
				end = start + len(citedText)
				searchFrom = end
			} else if absolute := strings.Index(text, citedText); absolute >= 0 {
				start = absolute
				end = absolute + len(citedText)
			}
		}
		out = append(out, model.Citation{
			StartIndex: start,
			EndIndex:   end,
			Sources: []model.CitationSource{{
				RefID:  fmt.Sprintf("citation-%d", index),
				Title:  strings.TrimSpace(citation.Title),
				URL:    strings.TrimSpace(citation.URL),
				Source: hostFromURL(citation.URL),
			}},
		})
	}
	return model.NormalizeCitations(text, out)
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
