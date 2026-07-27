package tuiapp

import (
	"slices"
	"strings"
	"testing"
	"time"

	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestRenderSlashStatusShowsSubscriptionLimits(t *testing.T) {
	reset := time.Date(2026, time.July, 29, 7, 12, 0, 0, time.Local)
	lines := renderSlashStatusLines(controlstatus.StatusSnapshot{RateLimits: controlstatus.StatusRateLimits{
		Provider: "openai-codex",
		Plan:     "pro",
		Limits: []controlstatus.StatusRateLimit{{
			ID: "codex",
			Windows: []controlstatus.StatusRateLimitWindow{{
				UsedPercent: 22, DurationMinutes: int64((7 * 24 * time.Hour) / time.Minute), ResetsAt: reset,
			}},
		}, {
			ID:   "codex_spark",
			Name: "GPT-5.3-Codex-Spark",
			Windows: []controlstatus.StatusRateLimitWindow{{
				UsedPercent: 0, DurationMinutes: int64((7 * 24 * time.Hour) / time.Minute),
			}},
		}},
	}})
	limitsStart := slices.IndexFunc(lines, func(line slashOutputLine) bool {
		return strings.TrimSpace(line.Text) == "Limits"
	})
	if limitsStart < 0 {
		t.Fatalf("rendered status has no Limits section: %#v", lines)
	}
	limitLines := lines[limitsStart:]
	rendered := slashOutputPlainForTest(limitLines)
	want := strings.Join([]string{
		"Limits",
		"  Plan:                             pro",
		"  Weekly limit:                     78% left · resets 2026-07-29 07:12 " + reset.Format("MST"),
		"  GPT-5.3-Codex-Spark Weekly limit: 100% left",
	}, "\n")
	if rendered != want {
		t.Fatalf("rendered status mismatch:\n--- got ---\n%s\n--- want ---\n%s", rendered, want)
	}
	if strings.Contains(rendered, "5h limit") {
		t.Fatalf("rendered absent five-hour window: %q", rendered)
	}
	for _, line := range limitLines[1:] {
		if line.Style != tuikit.LineStyleKeyValue {
			t.Fatalf("limit line style = %v, want key-value: %#v", line.Style, line)
		}
	}
}
