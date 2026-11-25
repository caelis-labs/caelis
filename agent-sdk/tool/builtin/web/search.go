package web

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/internal/toolpresentation"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/argparse"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/names"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

const SearchToolName = names.WebSearch

type SearchTool struct{}

// SearchBackend supplies only the provider-native web search operation and its
// availability information. A zero backend reports search as unavailable.
type SearchBackend struct {
	Provider          string
	UnavailableReason string
	SearchWeb         func(context.Context, model.WebSearchRequest) (model.WebSearchResponse, error)
}

func NewSearch() *SearchTool {
	return &SearchTool{}
}

func (t *SearchTool) Definition() tool.Definition {
	return tool.Definition{
		Name:         SearchToolName,
		Description:  "Search the web for current, external, or unknown information when no URL is available. Use WebFetch to read a specific result. Cite final sources with visible Markdown links, and do not expose result IDs or private citation markers.",
		OutputSchema: map[string]any{"type": "object"},
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
	}
}

func (t *SearchTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	return t.CallWithBackend(ctx, call, SearchBackend{})
}

// CallWithBackend binds provider search for only this invocation.
func (t *SearchTool) CallWithBackend(ctx context.Context, call tool.Call, backend SearchBackend) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
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
	provider := strings.TrimSpace(backend.Provider)
	reason := strings.TrimSpace(backend.UnavailableReason)
	if reason != "" || backend.SearchWeb == nil {
		if reason == "" {
			reason = "Web search is unavailable for this provider."
		}
		return webSearchUnavailableResult(ctx, req, provider, reason)
	}
	resp, err := backend.SearchWeb(ctx, req)
	if err != nil {
		return webSearchFailedResult(ctx, req, provider, err)
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
	toolpresentation.WebSearch(ctx, resp.Query, resp.Provider, resp.Model, usagePayload(resp.Usage))
	return toolutil.JSONResult(payload)
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

func webSearchFailedResult(ctx context.Context, req model.WebSearchRequest, provider string, err error) (tool.Result, error) {
	message := strings.TrimSpace("Web search failed. " + webSearchErrorMessage(err))
	toolpresentation.WebSearch(ctx, req.Query, provider, "", nil)
	result, resultErr := toolutil.JSONResult(map[string]any{
		"status":   "failed",
		"query":    req.Query,
		"provider": provider,
		"message":  message,
		"results":  []any{},
	})
	if resultErr != nil {
		return tool.Result{}, resultErr
	}
	return result, tool.FailedReply(message)
}

func webSearchUnavailableResult(ctx context.Context, req model.WebSearchRequest, provider string, reason string) (tool.Result, error) {
	toolpresentation.WebSearch(ctx, req.Query, provider, "", nil)
	message := reason + " Use " + FetchToolName + " with a known URL if available, or choose a provider with native web search."
	return toolutil.JSONResult(map[string]any{
		"status":   "unavailable",
		"query":    req.Query,
		"provider": provider,
		"message":  message,
		"results":  []any{},
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

func usagePayload(usage model.Usage) map[string]any {
	out := map[string]any{}
	if usage.PromptTokens != 0 {
		out["prompt_tokens"] = usage.PromptTokens
	}
	if usage.CacheWriteTokens != 0 || usage.CacheWriteTokensReported {
		out["cache_write_tokens"] = usage.CacheWriteTokens
	}
	if usage.CacheWrite5mTokens != 0 || usage.CacheWrite5mTokensReported {
		out["cache_write_5m_tokens"] = usage.CacheWrite5mTokens
	}
	if usage.CacheWrite1hTokens != 0 || usage.CacheWrite1hTokensReported {
		out["cache_write_1h_tokens"] = usage.CacheWrite1hTokens
	}
	if usage.CachedInputTokensReported {
		out["cached_input_tokens_reported"] = true
	}
	if usage.CacheWriteTokensReported {
		out["cache_write_tokens_reported"] = true
	}
	if usage.CacheWrite5mTokensReported {
		out["cache_write_5m_tokens_reported"] = true
	}
	if usage.CacheWrite1hTokensReported {
		out["cache_write_1h_tokens_reported"] = true
	}
	if usage.CachedInputTokens != 0 || usage.CachedInputTokensReported {
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
