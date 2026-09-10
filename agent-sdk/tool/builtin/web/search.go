package web

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/argparse"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

const SearchToolName = "WebSearch"

type SearchTool struct{}

func NewSearch() *SearchTool {
	return &SearchTool{}
}

func (t *SearchTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        SearchToolName,
		Description: "Search the web for current, external, or unknown information when no URL is available. Use WebFetch to read a specific result. Cite final sources with visible Markdown links, and do not expose result IDs or private citation markers.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Concise keyword query with key entities, dates, versions, freshness context, or error text. Add operators such as site:, filetype:, quotes, OR, or minus terms when useful.",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     10,
					"description": "Best-effort result limit for search backends that support it. Provider-native server search returns the complete provider result set. Defaults to 5.",
				},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
		Metadata:     toolutil.AnnotationMetadata(true, false, false, true),
		Capabilities: tool.Capabilities{ParallelSafe: true},
	}
}

func (t *SearchTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return tool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	if err := tool.RejectUnknownArgs(args, "query", "max_results"); err != nil {
		return tool.Result{}, err
	}
	query, err := argparse.String(args, "query", true)
	if err != nil {
		return tool.Result{}, err
	}
	maxResults, err := argparse.Int(args, "max_results", 5)
	if err != nil {
		return tool.Result{}, err
	}
	req := model.NormalizeWebSearchRequest(model.WebSearchRequest{Query: query, MaxResults: maxResults})
	llm, _ := tool.RuntimeModel(call)
	provider := runtimeProviderName(llm)
	if reasoner, ok := llm.(model.WebSearchAvailability); ok {
		if reason := strings.TrimSpace(reasoner.WebSearchUnavailableReason()); reason != "" {
			return webSearchUnavailableResult(req, provider, reason)
		}
	}
	searcher, ok := llm.(model.WebSearcher)
	if !ok {
		return webSearchUnavailableResult(req, provider, "Web search is unavailable for this provider. Use WebFetch with a known URL, or configure a search backend.")
	}
	resp, err := searcher.SearchWeb(ctx, req)
	if err != nil {
		return webSearchFailedResult(req, provider, err)
	}
	if resp.Provider == "" {
		resp.Provider = provider
	}
	if resp.Query == "" {
		resp.Query = req.Query
	}
	results, citations := webSearchSourcesPayload(resp)
	payload := map[string]any{"status": "completed", "results": results}
	if resp.Answer != "" {
		payload["answer"] = resp.Answer
	}
	if len(citations) != 0 {
		payload["citations"] = citations
	}
	return toolutil.JSONResult(SearchToolName, payload, map[string]any{
		"query": resp.Query, "provider": resp.Provider, "model": resp.Model,
		"usage": usagePayload(resp.Usage),
	})
}

// Keep the result order used by legacy positional references. Citations reuse
// matching results by zero-based index; citation-only sources remain inline.
func webSearchSourcesPayload(resp model.WebSearchResponse) ([]map[string]any, []webSearchCitation) {
	indices := make(map[model.WebSearchResult]int)
	position := 0
	for _, source := range resp.Results {
		source = normalizedSearchSource(source)
		if source == (model.WebSearchResult{}) {
			continue
		}
		if _, ok := indices[source]; !ok {
			indices[source] = position
		}
		position++
	}
	citations := make([]webSearchCitation, 0, len(resp.Citations))
	for _, citation := range resp.Citations {
		item := webSearchCitation{StartIndex: citation.StartIndex, EndIndex: citation.EndIndex}
		for _, source := range citation.Sources {
			if index, ok := indices[normalizedSearchSource(model.WebSearchResult(source))]; ok {
				item.ResultIndices = append(item.ResultIndices, index)
			} else {
				item.Sources = append(item.Sources, source)
			}
		}
		citations = append(citations, item)
	}
	return webSearchResultsPayload(resp.Results), citations
}

func normalizedSearchSource(source model.WebSearchResult) model.WebSearchResult {
	return model.WebSearchResult{
		RefID: strings.TrimSpace(source.RefID), Title: strings.TrimSpace(source.Title),
		URL: strings.TrimSpace(source.URL), Snippet: strings.TrimSpace(source.Snippet),
		Source: strings.TrimSpace(source.Source), PublishedAt: strings.TrimSpace(source.PublishedAt),
	}
}

type webSearchCitation struct {
	StartIndex    int                    `json:"start_index,omitempty"`
	EndIndex      int                    `json:"end_index,omitempty"`
	ResultIndices []int                  `json:"result_indices,omitempty"`
	Sources       []model.CitationSource `json:"sources,omitempty"`
}

func webSearchFailedResult(req model.WebSearchRequest, provider string, err error) (tool.Result, error) {
	message := strings.TrimSpace("Web search failed. " + webSearchErrorMessage(err))
	result, resultErr := toolutil.JSONResult(SearchToolName, map[string]any{
		"status":   "failed",
		"query":    req.Query,
		"provider": provider,
		"message":  message,
		"results":  []any{},
	}, map[string]any{
		"query":    req.Query,
		"provider": provider,
	})
	result.IsError = true
	return result, resultErr
}

func webSearchUnavailableResult(req model.WebSearchRequest, provider string, message string) (tool.Result, error) {
	return toolutil.JSONResult(SearchToolName, map[string]any{
		"status":   "unavailable",
		"query":    req.Query,
		"provider": provider,
		"message":  strings.TrimSpace(message),
		"results":  []any{},
	}, map[string]any{
		"query":    req.Query,
		"provider": provider,
	})
}

func webSearchErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "Use WebFetch with a known URL, or retry later."
	}
	return msg + ". Use WebFetch with a known URL, or retry later."
}

func webSearchResultsPayload(results []model.WebSearchResult) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, result := range results {
		item := map[string]any{}
		putNonEmpty(item, "ref_id", result.RefID)
		putNonEmpty(item, "title", result.Title)
		putNonEmpty(item, "url", result.URL)
		putNonEmpty(item, "snippet", result.Snippet)
		putNonEmpty(item, "source", result.Source)
		putNonEmpty(item, "published_at", result.PublishedAt)
		if len(item) > 0 {
			out = append(out, item)
		}
	}
	return out
}

func runtimeProviderName(llm model.LLM) string {
	if llm == nil {
		return ""
	}
	if provider, ok := llm.(interface{ ProviderName() string }); ok {
		return strings.TrimSpace(provider.ProviderName())
	}
	return ""
}

func usagePayload(usage model.Usage) map[string]any {
	out := map[string]any{}
	if usage.PromptTokens != 0 {
		out["prompt_tokens"] = usage.PromptTokens
	}
	if usage.CachedInputTokens != 0 {
		out["cached_input_tokens"] = usage.CachedInputTokens
	}
	if usage.CompletionTokens != 0 {
		out["completion_tokens"] = usage.CompletionTokens
	}
	if usage.ReasoningTokens != 0 {
		out["reasoning_tokens"] = usage.ReasoningTokens
	}
	if usage.TotalTokens != 0 {
		out["total_tokens"] = usage.TotalTokens
	}
	return out
}

func putNonEmpty(dst map[string]any, key string, value string) {
	if text := strings.TrimSpace(value); text != "" {
		dst[key] = text
	}
}

var _ tool.Tool = (*SearchTool)(nil)
