package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/protocol/acp/control"
)

func TestRenderSlashStatusShowsSubscriptionLimits(t *testing.T) {
	lines := renderSlashStatusLines(control.StatusSnapshot{RateLimits: control.StatusRateLimits{
		Provider: "openai-codex",
		Plan:     "pro",
		Limits: []control.StatusRateLimit{{
			ID: "codex",
			Windows: []control.StatusRateLimitWindow{{
				UsedPercent: 5, DurationMinutes: int64((7 * 24 * time.Hour) / time.Minute),
			}},
		}},
	}})
	texts := make([]string, 0, len(lines))
	for _, line := range lines {
		texts = append(texts, line.Text)
	}
	rendered := strings.Join(texts, "\n")
	for _, want := range []string{"Limits", "Plan:", "pro", "Weekly limit:", "95% left"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered status = %q, want %q", rendered, want)
		}
	}
	if strings.Contains(rendered, "5h limit") {
		t.Fatalf("rendered absent five-hour window: %q", rendered)
	}
}
