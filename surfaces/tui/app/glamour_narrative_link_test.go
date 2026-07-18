package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

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
	if !strings.Contains(rendered, "\x1b]8;;https://sh.weather.com.cn/gdtp/07/4722387.shtml") {
		t.Fatalf("rendered source URL is not an OSC 8 hyperlink: %q", rendered)
	}
}
