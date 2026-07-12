package web

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/argparse"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

const SearchToolName = names.WebSearch

type SearchTool struct{}

func NewSearch() *SearchTool {
	return &SearchTool{}
}

func (t *SearchTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        SearchToolName,
		Description: "Search the web for current, external, or unknown information when you do not already have a URL. Use concise keyword queries with the key entity, location, date/time, version, error text, or product name instead of long prose. Put common search operators directly in query, such as site:, filetype:, quoted phrases, OR, or minus terms. Use WebFetch after search when you need to read a specific result URL. If provider-native web search is unavailable, fall back to WebFetch with a known URL or ask for a search backend.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Search query. Prefer compact keywords and include freshness context when relevant, for example \"上海 天气 2026-06-23\", \"Gemini API web search tool calling\", or \"site:gov.cn 上海 人口 2026\".",
				},
				"max_results": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     10,
					"description": "Maximum number of search results or citations to request. Providers may return fewer. Defaults to 5.",
				},
			},
			"required":             []string{"query"},
			"additionalProperties": false,
		},
		Metadata: toolutil.AnnotationMetadata(true, false, false, true),
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
	return toolutil.JSONResult(SearchToolName, map[string]any{
		"status":   "completed",
		"query":    resp.Query,
		"provider": resp.Provider,
		"model":    resp.Model,
		"answer":   resp.Answer,
		"results":  webSearchResultsPayload(resp.Results),
		"usage":    usagePayload(resp.Usage),
	}, map[string]any{
		"query":    resp.Query,
		"provider": resp.Provider,
	})
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
