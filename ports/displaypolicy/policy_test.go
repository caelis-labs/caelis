package displaypolicy

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

func TestSummarizeToolCallTitleIncludesSpawnPrompt(t *testing.T) {
	raw := map[string]any{
		"agent":  "self",
		"prompt": "创建 hello_spawn.txt",
	}
	if got := SummarizeToolCallTitle("SPAWN", raw); got != "SPAWN self: 创建 hello_spawn.txt" {
		t.Fatalf("SummarizeToolCallTitle(SPAWN) = %q", got)
	}
}

func TestSpawnDisplayInputForResultMergesPromptFromLifecycleOutput(t *testing.T) {
	input := map[string]any{"agent": "codex"}
	output := map[string]any{"text": `{"prompt":"inspect repo","task_id":"task-1"} running`}
	got := SpawnDisplayInputForResult(input, output)
	if got["agent"] != "codex" || got["prompt"] != "inspect repo" || got["task_id"] != "task-1" {
		t.Fatalf("SpawnDisplayInputForResult() = %#v", got)
	}
	normalized := NormalizeSpawnDisplayRawMap(output)
	if normalized["text"] != "running" {
		t.Fatalf("text remainder = %#v", normalized["text"])
	}
}

func TestDisplayTerminalInitialOutputForSpawn(t *testing.T) {
	got := DisplayTerminalInitialOutput("SPAWN", map[string]any{
		"agent":  "codex",
		"prompt": "explain the patch",
	})
	if got != "SPAWN agent=codex\nexplain the patch\n" {
		t.Fatalf("DisplayTerminalInitialOutput() = %q", got)
	}
}

func TestCleanSubagentFinalOutput(t *testing.T) {
	raw := "### Done\n- `hello.txt` **created**\n| File | State |\n| --- | --- |\n| `hello.txt` | **ok** |"
	got := CleanSubagentFinalOutput(raw)
	want := "Done\nhello.txt created\nFile  State\nhello.txt  ok"
	if got != want {
		t.Fatalf("CleanSubagentFinalOutput() = %q, want %q", got, want)
	}
}

func TestTaskMetadataDisplayPolicy(t *testing.T) {
	meta := map[string]any{"caelis": map[string]any{"runtime": map[string]any{"tool": map[string]any{
		"target_id":   "sidecar",
		"action":      "write",
		"input":       "continue",
		"target_kind": "subagent",
	}}}}
	if got := ToolTaskID(nil, nil, meta); got != "sidecar" {
		t.Fatalf("ToolTaskID() = %q", got)
	}
	if got := ToolTaskAction(nil, nil, meta); got != "write" {
		t.Fatalf("ToolTaskAction() = %q", got)
	}
	if got := ToolTaskInput(nil, nil, meta); got != "continue" {
		t.Fatalf("ToolTaskInput() = %q", got)
	}
	if got := ToolTaskTargetKind(nil, nil, meta); got != "subagent" {
		t.Fatalf("ToolTaskTargetKind() = %q", got)
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
	if got := WebSearchDisplayArg(map[string]any{"query": "Does DeepSeek API provide a native search tool for agents?"}); got != `"Does DeepSeek API provide a native search tool for agents?"` {
		t.Fatalf("WebSearchDisplayArg() = %q", got)
	}
	if got := WebFetchDisplayArg(map[string]any{"url": "https://api-docs.deepseek.com/guides/claude_code"}); got != "https://api-docs.deepseek.com/guides/claude_code" {
		t.Fatalf("WebFetchDisplayArg() = %q", got)
	}
}

func TestExplorationVerbForWebTools(t *testing.T) {
	if got := ExplorationVerbForTool("WEB_SEARCH"); got != "Search" {
		t.Fatalf("ExplorationVerbForTool(WEB_SEARCH) = %q", got)
	}
	if got := ExplorationVerbForTool("WEB_FETCH"); got != "Fetch" {
		t.Fatalf("ExplorationVerbForTool(WEB_FETCH) = %q", got)
	}
}
