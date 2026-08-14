package codexauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
)

const (
	codexUsageURL      = "https://chatgpt.com/backend-api/wham/usage"
	codexUsageTimeout  = 5 * time.Second
	maxUsageBodyBytes  = 1 << 20
	codexUsageLimitID  = "codex"
	codexUsageProvider = "openai-codex"
)

type usageResponse struct {
	PlanType             string                 `json:"plan_type"`
	RateLimit            *usageRateLimit        `json:"rate_limit"`
	AdditionalRateLimits []usageAdditionalLimit `json:"additional_rate_limits"`
}

type usageAdditionalLimit struct {
	LimitName      string          `json:"limit_name"`
	MeteredFeature string          `json:"metered_feature"`
	RateLimit      *usageRateLimit `json:"rate_limit"`
}

type usageRateLimit struct {
	PrimaryWindow   *usageWindow `json:"primary_window"`
	SecondaryWindow *usageWindow `json:"secondary_window"`
}

type usageWindow struct {
	UsedPercent       float64 `json:"used_percent"`
	LimitWindowSecond int64   `json:"limit_window_seconds"`
	ResetAt           int64   `json:"reset_at"`
}

// SubscriptionUsage reads the account-scoped Codex subscription windows.
// Missing windows remain absent so callers never invent a five-hour or weekly
// limit that the provider did not return.
func (m *Manager) SubscriptionUsage(ctx context.Context) (providerusage.Snapshot, error) {
	if m == nil {
		return providerusage.Snapshot{}, fmt.Errorf("codexauth: manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client, err := m.AuthenticatedClient(m.httpClient)
	if err != nil {
		return providerusage.Snapshot{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, codexUsageTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, codexUsageURL, nil)
	if err != nil {
		return providerusage.Snapshot{}, fmt.Errorf("codexauth: build usage request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("originator", "caelis")
	response, err := client.Do(request)
	if err != nil {
		return providerusage.Snapshot{}, fmt.Errorf("codexauth: fetch usage: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return providerusage.Snapshot{}, fmt.Errorf("codexauth: fetch usage failed with status %d", response.StatusCode)
	}
	var payload usageResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxUsageBodyBytes))
	if err := decoder.Decode(&payload); err != nil {
		return providerusage.Snapshot{}, fmt.Errorf("codexauth: decode usage response: %w", err)
	}
	return usageSnapshot(payload, m.now()), nil
}

func usageSnapshot(payload usageResponse, capturedAt time.Time) providerusage.Snapshot {
	snapshot := providerusage.Snapshot{
		Provider:   codexUsageProvider,
		Plan:       strings.TrimSpace(payload.PlanType),
		CapturedAt: capturedAt,
	}
	appendLimit := func(id, name string, rateLimit *usageRateLimit) {
		if rateLimit == nil {
			return
		}
		limit := providerusage.Limit{ID: strings.TrimSpace(id), Name: strings.TrimSpace(name)}
		appendWindow := func(kind string, window *usageWindow) {
			if window == nil || window.LimitWindowSecond <= 0 {
				return
			}
			used := window.UsedPercent
			if used < 0 {
				used = 0
			} else if used > 100 {
				used = 100
			}
			normalized := providerusage.Window{
				Kind:        kind,
				UsedPercent: used,
				Duration:    time.Duration(window.LimitWindowSecond) * time.Second,
			}
			if window.ResetAt > 0 {
				normalized.ResetsAt = time.Unix(window.ResetAt, 0)
			}
			limit.Windows = append(limit.Windows, normalized)
		}
		appendWindow("primary", rateLimit.PrimaryWindow)
		appendWindow("secondary", rateLimit.SecondaryWindow)
		if len(limit.Windows) > 0 {
			snapshot.Limits = append(snapshot.Limits, limit)
		}
	}
	appendLimit(codexUsageLimitID, "", payload.RateLimit)
	for _, additional := range payload.AdditionalRateLimits {
		appendLimit(additional.MeteredFeature, additional.LimitName, additional.RateLimit)
	}
	return snapshot
}
