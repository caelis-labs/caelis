package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

var benchmarkNarrativeRows []RenderedRow

func BenchmarkAssistantStreamingTail(b *testing.B) {
	theme := tuikit.DefaultTheme()
	ctx := BlockRenderContext{
		Width:    96,
		Theme:    theme,
		ThemeKey: themeRenderCacheKey(theme),
	}
	fixtures := map[string]string{
		"plain-8KiB":          strings.Repeat("streaming assistant prose with no paragraph boundary ", 160),
		"markdown-cjk-8KiB":   strings.Repeat("**结论**：用 `go test` 验证 [链接](https://example.com)。", 150),
		"unclosed-fence-8KiB": "```go\n" + strings.Repeat("fmt.Println(\"stream\")\n", 360),
	}

	for name, raw := range fixtures {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				result := RenderTextWithContext(ctx, TextRenderRequest{
					Kind:           TextAssistant,
					Mode:           RenderStream,
					MarkdownPolicy: MarkdownStableTail,
					Raw:            raw,
					Prefix:         "· ",
					Width:          ctx.Width,
					BlockID:        "benchmark-tail-" + name,
					LineStyle:      tuikit.LineStyleAssistant,
				})
				benchmarkNarrativeRows = result.Rows
			}
		})
	}
}

func BenchmarkAssistantStreamingLifecycle(b *testing.B) {
	theme := tuikit.DefaultTheme()
	ctx := BlockRenderContext{
		Width:    96,
		Theme:    theme,
		ThemeKey: themeRenderCacheKey(theme),
	}
	chunks := make([]string, 0, 96)
	for range 96 {
		chunks = append(chunks, "**流式内容** with `code` and [link](https://example.com) keeps growing. ")
	}

	b.ReportAllocs()
	for b.Loop() {
		var buffer activeNarrativeBuffer
		for _, chunk := range chunks {
			buffer.Append(chunk)
			benchmarkNarrativeRows = buffer.RenderRowsAtWidth(
				"benchmark-lifecycle",
				"· ",
				tuikit.LineStyleAssistant,
				ctx.Width,
				ctx,
			)
		}
	}
}

func BenchmarkAssistantStreamingPromotedPrefix(b *testing.B) {
	theme := tuikit.DefaultTheme()
	var glamourCalls int64
	ctx := BlockRenderContext{
		Width:                96,
		Theme:                theme,
		ThemeKey:             themeRenderCacheKey(theme),
		ObserveGlamourRender: func() { glamourCalls++ },
	}
	chunks := make([]string, 0, 48)
	for range 48 {
		chunks = append(chunks, "## 已完成段落\n\n- [x] stable prefix with `code` and [link](https://example.com).\n\n")
	}

	b.ReportAllocs()
	for b.Loop() {
		var buffer activeNarrativeBuffer
		for _, chunk := range chunks {
			buffer.Append(chunk)
			benchmarkNarrativeRows = buffer.RenderRowsAtWidth(
				"benchmark-promoted-prefix",
				"· ",
				tuikit.LineStyleAssistant,
				ctx.Width,
				ctx,
			)
		}
	}
	b.ReportMetric(float64(glamourCalls)/float64(b.N), "glamour/op")
}

func BenchmarkAssistantFinalMarkdown(b *testing.B) {
	theme := tuikit.DefaultTheme()
	raw := strings.Repeat("## 结论\n\n- [x] 用 `go test` 验证 [链接](https://example.com)。\n\n", 40)

	b.ReportAllocs()
	for b.Loop() {
		benchmarkNarrativeRows = glamourNarrativeRows(
			"benchmark-final",
			raw,
			"· ",
			tuikit.LineStyleAssistant,
			96,
			theme,
		)
	}
}

func BenchmarkAssistantStreamSeal(b *testing.B) {
	theme := tuikit.DefaultTheme()
	ctx := BlockRenderContext{
		Width:    96,
		Theme:    theme,
		ThemeKey: themeRenderCacheKey(theme),
	}
	raw := strings.Repeat("## 结论\n\n- [x] 用 `go test` 验证 [链接](https://example.com)。\n\n", 20)

	b.ReportAllocs()
	for b.Loop() {
		var buffer activeNarrativeBuffer
		buffer.SetText(raw)
		benchmarkNarrativeRows = buffer.RenderRowsAtWidth(
			"benchmark-seal",
			"· ",
			tuikit.LineStyleAssistant,
			ctx.Width,
			ctx,
		)
		buffer.Seal()
		benchmarkNarrativeRows = buffer.RenderRowsAtWidth(
			"benchmark-seal",
			"· ",
			tuikit.LineStyleAssistant,
			ctx.Width,
			ctx,
		)
	}
}
