package tuiapp

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/glamour/v2"
	"github.com/charmbracelet/x/ansi"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestGlamourNarrativeUnicodeLinks(t *testing.T) {
	for name, raw := range map[string]string{
		"image":     "见 ![当前执行](https://example.com/服务/图.png) 结束",
		"label":     "见 [当前执行](https://example.com/服务/执行.go#L195) 结束",
		"autolink":  "来源 https://example.com/服务/执行.go#L195 结束",
		"angle":     "来源 <https://example.com/服务/执行.go#L195> 结束",
		"reference": "见 [当前执行][source] 结束\n\n[source]: https://example.com/服务/执行.go#L195",
		"citation":  "见 [1](<https://example.com/服务/执行.go#L195>) 结束",
		"table":     "| 来源 |\n| --- |\n| [当前执行](https://example.com/服务/执行.go#L195) |",
		"nested":    "- **见 [当前执行](https://example.com/服务/执行.go#L195)** 结束",
		"relative":  "见 [当前执行](服务/执行.go#L195) 结束",
	} {
		for _, width := range []int{24, 40, 80} {
			t.Run(fmt.Sprintf("%s/%d", name, width), func(t *testing.T) {
				rendered := glamourRenderNarrative(raw, width, tuikit.DefaultTheme(), tuikit.LineStyleAssistant)
				assertSafeNarrativeLinks(t, rendered)
				// GFM's bare-URL parser stops at the first CJK character; the
				// angle form exercises a complete Unicode AutoLink destination.
				if name != "autolink" && !strings.Contains(rendered, "%E6%9C%8D%E5%8A%A1") {
					t.Fatalf("link target was not URI encoded: %q", rendered)
				}
				if strings.Contains(raw, "当前执行") && !strings.Contains(ansi.Strip(rendered), "当前执行") {
					t.Fatalf("link label changed: %q", rendered)
				}
			})
		}
	}
}

func TestNarrativeMarkdownPreservesNonLinkSource(t *testing.T) {
	const raw = "# 中文标题\n\n" +
		"见 [**中文标签**](https://example.com/already%20encoded?q=a&b=c#section)。\n\n" +
		"`https://example.com/服务`\n\nhttps://example.com/服务\n\n```text\n[源码](https://example.com/服务)\n```\n\n" +
		"> 中文引用\n\n- 列表\n\n| 表格 |\n| --- |\n| [链接](https://example.com) |"
	for _, width := range []int{24, 40, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			baseline, err := glamour.NewTermRenderer(
				glamour.WithStyles(narrativeStyleConfig(tuikit.DefaultTheme(), tuikit.LineStyleAssistant)),
				glamour.WithWordWrap(width), glamour.WithTableWrap(true), glamour.WithInlineTableLinks(true),
			)
			if err != nil {
				t.Fatal(err)
			}
			want, err := baseline.Render(raw)
			if err != nil {
				t.Fatal(err)
			}
			got := glamourRenderNarrative(raw, width, tuikit.DefaultTheme(), tuikit.LineStyleAssistant)
			if got != strings.TrimRight(want, "\n") {
				t.Fatalf("non-link source or layout changed:\ngot: %q\nwant: %q", got, want)
			}
		})
	}
}

func assertSafeNarrativeLinks(t *testing.T, rendered string) {
	t.Helper()
	if !utf8.ValidString(rendered) {
		t.Fatalf("invalid UTF-8 in rendered markdown: %q", rendered)
	}
	for rest := rendered; ; {
		_, payload, ok := strings.Cut(rest, "\x1b]8;")
		if !ok {
			break
		}
		end := strings.IndexAny(payload, "\a\x1b")
		if end < 0 {
			t.Fatalf("unterminated hyperlink: %q", payload)
		}
		for _, b := range []byte(payload[:end]) {
			if b < 0x20 || b >= 0x7f {
				t.Fatalf("unsafe byte in OSC 8 payload: %q", payload[:end])
			}
		}
		rest = payload[end+1:]
	}
	for _, r := range ansi.Strip(rendered) {
		if r != '\n' && (r < 0x20 || r >= 0x7f && r <= 0x9f) {
			t.Fatalf("control character leaked into visible text: %q", rendered)
		}
	}
}

func TestGlamourNarrativeMakesCitationSourceURLClickable(t *testing.T) {
	rendered := glamourRenderNarrative(
		"信息来源：\n\n• 上海市气象服务中心 https://sh.weather.com.cn/gdtp/07/4722387.shtml",
		120,
		tuikit.DefaultTheme(),
		tuikit.LineStyleAssistant,
	)
	if !strings.Contains(rendered, "上海市气象服务中心") {
		t.Fatalf("rendered source label missing: %q", rendered)
	}
	if !strings.Contains(rendered, "\x1b]8;") || !strings.Contains(rendered, ";https://sh.weather.com.cn/gdtp/07/4722387.shtml\a") {
		t.Fatalf("rendered source URL is not an OSC 8 hyperlink: %q", rendered)
	}
}
