package display

import "testing"

func TestSpawnFullDisplayArgsUsesAgentAndPrompt(t *testing.T) {
	raw := map[string]any{
		"agent":  "codex",
		"prompt": "inspect the transcript",
	}
	if got := SpawnFullDisplayArgs(raw); got != "codex: inspect the transcript" {
		t.Fatalf("SpawnFullDisplayArgs() = %q", got)
	}
}

func TestSpawnFullDisplayArgsUsesHandleWithAgentAnnotation(t *testing.T) {
	raw := map[string]any{
		"agent":   "self",
		"task_id": "jeff",
		"prompt":  "inspect the transcript",
	}
	if got := SpawnFullDisplayArgs(raw); got != "jeff[self]: inspect the transcript" {
		t.Fatalf("SpawnFullDisplayArgs() = %q", got)
	}
}

func TestSpawnDisplayInputForResultMergesPromptFromLifecycleOutput(t *testing.T) {
	input := map[string]any{"agent": "codex"}
	output := map[string]any{"text": `{"prompt":"inspect repo","task_id":"task-1"} running`}
	got := SpawnDisplayInputForResult(input, output)
	if got["agent"] != "codex" || got["prompt"] != "inspect repo" || got["task_id"] != "task-1" {
		t.Fatalf("SpawnDisplayInputForResult() = %#v", got)
	}
	normalized := normalizeSpawnDisplayRawMap(output)
	if normalized["text"] != "running" {
		t.Fatalf("text remainder = %#v", normalized["text"])
	}
}

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
