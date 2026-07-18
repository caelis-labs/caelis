package codexauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionUsagePreservesOnlyReturnedWindows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{
		Version: credentialSchemaVersion, RefreshToken: "refresh", AccountID: "account-pro",
		AccessToken: "access", ExpiresAt: now.Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	base := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != codexUsageURL {
			t.Errorf("usage request = %s %s", request.Method, request.URL)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer access" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("ChatGPT-Account-ID"); got != "account-pro" {
			t.Errorf("ChatGPT-Account-ID = %q", got)
		}
		body := `{
			"plan_type":"pro",
			"rate_limit":{"allowed":true,"limit_reached":false,
				"primary_window":null,
				"secondary_window":{"used_percent":35,"limit_window_seconds":604800,"reset_after_seconds":3600,"reset_at":1784419200}
			},
			"additional_rate_limits":[{
				"limit_name":"Fast review","metered_feature":"fast_review",
				"rate_limit":{"primary_window":{"used_percent":12,"limit_window_seconds":1800,"reset_after_seconds":10,"reset_at":1784415600}}
			}]
		}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	manager, err := NewManager(Options{
		CredentialPath: credentialPath,
		Clock:          func() time.Time { return now },
		HTTPClient:     base,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := manager.SubscriptionUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Provider != "openai-codex" || snapshot.Plan != "pro" || !snapshot.CapturedAt.Equal(now) {
		t.Fatalf("snapshot header = %#v", snapshot)
	}
	if len(snapshot.Limits) != 2 || len(snapshot.Limits[0].Windows) != 1 {
		t.Fatalf("limits = %#v", snapshot.Limits)
	}
	weekly := snapshot.Limits[0].Windows[0]
	if weekly.Kind != "secondary" || weekly.Duration != 7*24*time.Hour || weekly.UsedPercent != 35 {
		t.Fatalf("weekly window = %#v", weekly)
	}
	for _, limit := range snapshot.Limits {
		for _, window := range limit.Windows {
			if window.Duration == 5*time.Hour {
				t.Fatalf("invented absent five-hour window: %#v", snapshot)
			}
		}
	}
}

func TestAuthenticatedTransportAllowsOnlyExactUsageEndpoint(t *testing.T) {
	allowed, _ := http.NewRequest(http.MethodGet, codexUsageURL, nil)
	if !allowedCodexRequest(allowed) {
		t.Fatal("exact Codex usage endpoint was rejected")
	}
	for _, target := range []string{
		"https://chatgpt.com/backend-api/wham/usage/extra",
		"https://chatgpt.com/backend-api/wham/rate-limit-reset-credits",
		"https://chatgpt.com/backend-api/wham/usageevil",
	} {
		request, _ := http.NewRequest(http.MethodGet, target, nil)
		if allowedCodexRequest(request) {
			t.Fatalf("allowedCodexRequest(%q) = true", target)
		}
	}
}
