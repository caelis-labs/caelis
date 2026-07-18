package control

import (
	"strings"
	"testing"
	"time"
)

func TestStatusDisplayShowsOnlyReturnedSubscriptionWindows(t *testing.T) {
	reset := time.Date(2026, time.July, 25, 11, 31, 0, 0, time.Local)
	status := StatusSnapshot{RateLimits: StatusRateLimits{
		Provider: "openai-codex",
		Plan:     "pro",
		Limits: []StatusRateLimit{{
			ID: "codex",
			Windows: []StatusRateLimitWindow{{
				Kind: "primary", UsedPercent: 5, DurationMinutes: int64((7 * 24 * time.Hour) / time.Minute), ResetsAt: reset,
			}},
		}, {
			ID:   "codex_bengalfox",
			Name: "GPT-5.3-Codex-Spark",
			Windows: []StatusRateLimitWindow{{
				Kind: "primary", UsedPercent: 0, DurationMinutes: int64((7 * 24 * time.Hour) / time.Minute),
			}},
		}},
	}}

	view := StatusDisplayFromSnapshot(status)
	if view.RateLimits.Plan != "pro" || len(view.RateLimits.Rows) != 2 {
		t.Fatalf("rate limit view = %#v", view.RateLimits)
	}
	if view.RateLimits.Rows[0].Label != "Weekly limit" || !strings.Contains(view.RateLimits.Rows[0].Value, "95% left") || !strings.Contains(view.RateLimits.Rows[0].Value, "resets") {
		t.Fatalf("weekly row = %#v", view.RateLimits.Rows[0])
	}
	if view.RateLimits.Rows[1].Label != "GPT-5.3-Codex-Spark Weekly limit" || view.RateLimits.Rows[1].Value != "100% left" {
		t.Fatalf("additional row = %#v", view.RateLimits.Rows[1])
	}
	if rendered := FormatStatusSnapshot(status); strings.Contains(rendered, "5h limit") {
		t.Fatalf("rendered absent five-hour window: %q", rendered)
	}
}
