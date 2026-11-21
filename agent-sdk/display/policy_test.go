package display

import "testing"

func TestWebSearchSummaryShowsQueryOnly(t *testing.T) {
	input := map[string]any{"query": "上海 天气"}
	output := map[string]any{
		"results": []map[string]string{
			{"title": "one"},
			{"title": "two"},
		},
		"status": "completed",
	}
	if got := WebSearchSummary(input, output); got != `"上海 天气"` {
		t.Fatalf("WebSearchSummary() = %q", got)
	}

	output["status"] = "failed"
	if got := WebSearchSummary(input, output); got != `"上海 天气"` {
		t.Fatalf("WebSearchSummary(failed) = %q", got)
	}
}

func TestWebFetchSummaryShowsURLOnly(t *testing.T) {
	if got := WebFetchSummary(map[string]any{"url": "https://example.com/a/very/long/path"}, map[string]any{
		"title":       "Example",
		"status_code": 200,
	}); got != "https://example.com/a/very/long/path" {
		t.Fatalf("WebFetchSummary(url) = %q", got)
	}
	if got := WebFetchSummary(nil, map[string]any{"url": "https://example.com/final", "title": "Example", "status": "failed"}); got != "https://example.com/final" {
		t.Fatalf("WebFetchSummary(fallback url) = %q", got)
	}
}

func TestWebDisplayArgs(t *testing.T) {
	if got := webSearchDisplayArg(map[string]any{"query": "Does DeepSeek API provide a native search tool for agents?"}); got != `"Does DeepSeek API provide a native search tool for agents?"` {
		t.Fatalf("webSearchDisplayArg() = %q", got)
	}
	if got := WebFetchDisplayArg(map[string]any{"url": "https://api-docs.deepseek.com/guides/claude_code"}); got != "https://api-docs.deepseek.com/guides/claude_code" {
		t.Fatalf("WebFetchDisplayArg() = %q", got)
	}
}
