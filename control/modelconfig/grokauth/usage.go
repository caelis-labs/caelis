package grokauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
)

const (
	grokUsageURL          = DefaultAPIBaseURL + "/billing?format=credits"
	grokUserURL           = DefaultAPIBaseURL + "/user"
	grokUsageTimeout      = 15 * time.Second
	grokUserTimeout       = 5 * time.Second
	maxUsageBodyBytes     = 1 << 20
	grokUsageProvider     = "xai"
	grokUsageLimitID      = "xai"
	grokClientModeHeader  = "x-grok-client-mode"
	grokInteractiveClient = "interactive"
)

type usageResponse struct {
	Config           *usageConfig `json:"config"`
	SubscriptionTier string       `json:"subscriptionTier"`
}

type usageConfig struct {
	CreditUsagePercent *float64     `json:"creditUsagePercent"`
	CurrentPeriod      *usagePeriod `json:"currentPeriod"`

	MonthlyLimit       *usageCent `json:"monthlyLimit"`
	Used               *usageCent `json:"used"`
	BillingPeriodStart string     `json:"billingPeriodStart"`
	BillingPeriodEnd   string     `json:"billingPeriodEnd"`
}

type usagePeriod struct {
	Type  string `json:"type"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type usageCent struct {
	Val int64 `json:"val"`
}

type usageUserResponse struct {
	UserID string `json:"userId"`
}

// SubscriptionUsage reads the account-scoped Grok Build credits window.
// The current percentage-based credits response is preferred, with the legacy
// monthly cent-based response retained as a compatibility fallback.
func (m *Manager) SubscriptionUsage(ctx context.Context) (providerusage.Snapshot, error) {
	if m == nil {
		return providerusage.Snapshot{}, fmt.Errorf("grokauth: manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, grokUsageTimeout)
	defer cancel()

	credentials, err := m.accessToken(requestCtx, nil)
	if err != nil {
		return providerusage.Snapshot{}, err
	}
	client, err := m.AuthenticatedClient(m.httpClient)
	if err != nil {
		return providerusage.Snapshot{}, err
	}
	userID, userErr := fetchUsageUserID(requestCtx, client)
	if userErr != nil {
		userID = usageUserID(credentials.token)
	}
	if userID == "" {
		return providerusage.Snapshot{}, userErr
	}

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, grokUsageURL, nil)
	if err != nil {
		return providerusage.Snapshot{}, fmt.Errorf("grokauth: build usage request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("x-userid", userID)
	request.Header.Set(grokClientModeHeader, grokInteractiveClient)
	response, err := client.Do(request)
	if err != nil {
		return providerusage.Snapshot{}, fmt.Errorf("grokauth: fetch usage: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return providerusage.Snapshot{}, fmt.Errorf("grokauth: fetch usage failed with status %d", response.StatusCode)
	}
	var payload usageResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxUsageBodyBytes)).Decode(&payload); err != nil {
		return providerusage.Snapshot{}, fmt.Errorf("grokauth: decode usage response: %w", err)
	}
	return usageSnapshot(payload, m.now()), nil
}

func fetchUsageUserID(ctx context.Context, client *http.Client) (string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, grokUserTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, grokUserURL, nil)
	if err != nil {
		return "", fmt.Errorf("grokauth: build user request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set(grokClientModeHeader, grokInteractiveClient)
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("grokauth: fetch user: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return "", fmt.Errorf("grokauth: fetch user failed with status %d", response.StatusCode)
	}
	var payload usageUserResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxUsageBodyBytes)).Decode(&payload); err != nil {
		return "", fmt.Errorf("grokauth: decode user response: %w", err)
	}
	userID := strings.TrimSpace(payload.UserID)
	if userID == "" {
		return "", fmt.Errorf("grokauth: user response omitted user id")
	}
	return userID, nil
}

func usageUserID(accessToken string) string {
	claims, err := decodeJWTClaims(accessToken)
	if err != nil {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(claims.PrincipalType), "team") {
		if principalID := strings.TrimSpace(claims.PrincipalID); principalID != "" {
			return principalID
		}
	}
	if subject := strings.TrimSpace(claims.Subject); subject != "" {
		return subject
	}
	return strings.TrimSpace(claims.PrincipalID)
}

func usageSnapshot(payload usageResponse, capturedAt time.Time) providerusage.Snapshot {
	snapshot := providerusage.Snapshot{
		Provider:   grokUsageProvider,
		Plan:       strings.TrimSpace(payload.SubscriptionTier),
		CapturedAt: capturedAt,
	}
	if payload.Config == nil {
		return snapshot
	}
	usedPercent, ok := grokUsedPercent(*payload.Config)
	if !ok {
		return snapshot
	}
	window := grokUsageWindow(*payload.Config)
	window.UsedPercent = usedPercent
	snapshot.Limits = []providerusage.Limit{{
		ID:      grokUsageLimitID,
		Windows: []providerusage.Window{window},
	}}
	return snapshot
}

func grokUsedPercent(config usageConfig) (float64, bool) {
	if config.CreditUsagePercent != nil && !math.IsNaN(*config.CreditUsagePercent) && !math.IsInf(*config.CreditUsagePercent, 0) {
		return clampUsagePercent(*config.CreditUsagePercent), true
	}
	if config.MonthlyLimit == nil || config.Used == nil || config.MonthlyLimit.Val <= 0 {
		return 0, false
	}
	used := float64(config.Used.Val) * 100 / float64(config.MonthlyLimit.Val)
	return clampUsagePercent(used), true
}

func clampUsagePercent(value float64) float64 {
	return math.Max(0, math.Min(100, value))
}

func grokUsageWindow(config usageConfig) providerusage.Window {
	periodType := ""
	startText := strings.TrimSpace(config.BillingPeriodStart)
	endText := strings.TrimSpace(config.BillingPeriodEnd)
	if config.CurrentPeriod != nil {
		periodType = strings.TrimSpace(config.CurrentPeriod.Type)
		startText = strings.TrimSpace(config.CurrentPeriod.Start)
		endText = strings.TrimSpace(config.CurrentPeriod.End)
	} else if startText != "" || endText != "" {
		periodType = "USAGE_PERIOD_TYPE_MONTHLY"
	}

	window := providerusage.Window{}
	switch {
	case strings.Contains(strings.ToUpper(periodType), "WEEKLY"):
		window.Kind = "weekly"
		window.Label = "Weekly limit"
		window.Duration = 7 * 24 * time.Hour
	case strings.Contains(strings.ToUpper(periodType), "MONTHLY"):
		window.Kind = "monthly"
		window.Label = "Monthly limit"
		window.Duration = 30 * 24 * time.Hour
	case strings.Contains(strings.ToUpper(periodType), "DAILY"):
		window.Kind = "daily"
		window.Label = "Daily limit"
		window.Duration = 24 * time.Hour
	default:
		window.Kind = "credits"
		window.Label = "Credit limit"
	}

	start, startOK := parseUsageTime(startText)
	end, endOK := parseUsageTime(endText)
	if startOK && endOK && end.After(start) {
		window.Duration = end.Sub(start)
	}
	if endOK {
		window.ResetsAt = end
	}
	return window
}

func parseUsageTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}
