package projection

import "testing"

func TestProjectedSpawnResultPreservesStructuredMarkdown(t *testing.T) {
	t.Parallel()

	raw := "# 结果\r\n\r\n中文第一行\r\n中文第二行\r\n\r\n- 创建文件\r\n- 保留换行\r\n\r\n| 文件 | 状态 |\r\n| --- | --- |\r\n| `hello.go` | **完成** |\r\n\r\n```go\r\nfmt.Println(\"你好\")\r\n```\r\n\r\n---\r\n\r\n> **结果**"
	want := "# 结果\n\n中文第一行\n中文第二行\n\n- 创建文件\n- 保留换行\n\n| 文件 | 状态 |\n| --- | --- |\n| `hello.go` | **完成** |\n\n```go\nfmt.Println(\"你好\")\n```\n\n---\n\n> **结果**"

	if got := projectedSpawnResultText(map[string]any{"final_message": raw}, "completed", false); got != want {
		t.Fatalf("projectedSpawnResultText() = %q, want exact Markdown %q", got, want)
	}
}

func TestProjectedSpawnFinalMessageKeepsJSONAndMarkdownIndentation(t *testing.T) {
	t.Parallel()

	// The child can legitimately begin an answer with JSON that looks like
	// the historical Spawn result wrapper.
	raw := "    {\"agent\":\"reviewer\"}\r\n\r\n    code\r\n"
	want := "    {\"agent\":\"reviewer\"}\n\n    code\n"
	output := map[string]any{"final_message": raw, "result": "older result"}
	if got := projectedSpawnResultText(output, "completed", false); got != want {
		t.Fatalf("canonical final = %q, want %q", got, want)
	}

	legacy := map[string]any{"finalMessage": "{\"agent\":\"reviewer\"}\r\n\r\nlegacy answer"}
	if got := projectedSpawnResultText(legacy, "completed", false); got != "legacy answer" {
		t.Fatalf("legacy result = %q, want the answer after the wrapper", got)
	}
}
