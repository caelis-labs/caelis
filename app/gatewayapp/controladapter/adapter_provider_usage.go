package controladapter

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	"github.com/caelis-labs/caelis/protocol/acp/control"
)

func statusRateLimitsFromProviderUsage(snapshot providerusage.Snapshot) control.StatusRateLimits {
	out := control.StatusRateLimits{
		Provider:   strings.TrimSpace(snapshot.Provider),
		Plan:       strings.TrimSpace(snapshot.Plan),
		CapturedAt: snapshot.CapturedAt,
	}
	for _, limit := range snapshot.Limits {
		normalized := control.StatusRateLimit{
			ID:   strings.TrimSpace(limit.ID),
			Name: strings.TrimSpace(limit.Name),
		}
		for _, window := range limit.Windows {
			if window.Duration <= 0 {
				continue
			}
			used := window.UsedPercent
			if used < 0 {
				used = 0
			} else if used > 100 {
				used = 100
			}
			normalized.Windows = append(normalized.Windows, control.StatusRateLimitWindow{
				Kind:            strings.TrimSpace(window.Kind),
				Label:           strings.TrimSpace(window.Label),
				UsedPercent:     used,
				DurationMinutes: int64(window.Duration / time.Minute),
				ResetsAt:        window.ResetsAt,
			})
		}
		if len(normalized.Windows) > 0 {
			out.Limits = append(out.Limits, normalized)
		}
	}
	return out
}
