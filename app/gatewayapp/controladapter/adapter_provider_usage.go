package controladapter

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	controlstatus "github.com/caelis-labs/caelis/control/status"
)

func statusRateLimitsFromProviderUsage(snapshot providerusage.Snapshot) controlstatus.StatusRateLimits {
	out := controlstatus.StatusRateLimits{
		Provider:   strings.TrimSpace(snapshot.Provider),
		Plan:       strings.TrimSpace(snapshot.Plan),
		CapturedAt: snapshot.CapturedAt,
	}
	for _, limit := range snapshot.Limits {
		normalized := controlstatus.StatusRateLimit{
			ID:   strings.TrimSpace(limit.ID),
			Name: strings.TrimSpace(limit.Name),
		}
		for _, window := range limit.Windows {
			label := strings.TrimSpace(window.Label)
			if window.Duration <= 0 && label == "" {
				continue
			}
			duration := window.Duration
			if duration < 0 {
				duration = 0
			}
			used := window.UsedPercent
			if used < 0 {
				used = 0
			} else if used > 100 {
				used = 100
			}
			normalized.Windows = append(normalized.Windows, controlstatus.StatusRateLimitWindow{
				Kind:            strings.TrimSpace(window.Kind),
				Label:           label,
				UsedPercent:     used,
				DurationMinutes: int64(duration / time.Minute),
				ResetsAt:        window.ResetsAt,
			})
		}
		if len(normalized.Windows) > 0 {
			out.Limits = append(out.Limits, normalized)
		}
	}
	return out
}
