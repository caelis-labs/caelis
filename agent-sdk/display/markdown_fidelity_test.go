package display

import "testing"

func TestCleanSubagentFinalOutputPreservesStructuredMarkdown(t *testing.T) {
	t.Parallel()

	raw := "# 结果\r\n\r\n中文第一行\r\n中文第二行\r\n\r\n- 创建文件\r\n- 保留换行\r\n\r\n| 文件 | 状态 |\r\n| --- | --- |\r\n| `hello.go` | **完成** |\r\n\r\n```go\r\nfmt.Println(\"你好\")\r\n```\r\n\r\n---\r\n\r\n> **结果**"
	want := "# 结果\n\n中文第一行\n中文第二行\n\n- 创建文件\n- 保留换行\n\n| 文件 | 状态 |\n| --- | --- |\n| `hello.go` | **完成** |\n\n```go\nfmt.Println(\"你好\")\n```\n\n---\n\n> **结果**"

	if got := CleanSubagentFinalOutput(raw); got != want {
		t.Fatalf("CleanSubagentFinalOutput() = %q, want exact Markdown %q", got, want)
	}
}
