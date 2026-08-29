package grokauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionUsageReadsCurrentWeeklyCredits(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	reset := time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC)
	accessToken := usageTestJWT(t, map[string]any{
		"exp": now.Add(time.Hour).Unix(),
		"sub": "user-123",
	})
	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{
		Version:      credentialSchemaVersion,
		RefreshToken: "refresh",
		AccessToken:  accessToken,
		ExpiresAt:    now.Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	client := newInMemoryHTTPClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/user":
			http.Error(writer, "temporary profile outage", http.StatusServiceUnavailable)
		case "/v1/billing":
			if request.Method != http.MethodGet || request.URL.Query().Get("format") != "credits" {
				t.Errorf("usage request = %s %s", request.Method, request.URL)
			}
			for header, want := range map[string]string{
				"Authorization":            "Bearer " + accessToken,
				"X-XAI-Token-Auth":         "xai-grok-cli",
				"x-authenticateresponse":   "authenticate-response",
				"x-userid":                 "user-123",
				"x-grok-client-version":    grokBuildProtocolVersion,
				"x-grok-client-identifier": "caelis",
				grokClientModeHeader:       grokInteractiveClient,
			} {
				if got := request.Header.Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
			writeJSON(t, writer, map[string]any{
				"subscriptionTier": "SuperGrok Heavy",
				"config": map[string]any{
					"creditUsagePercent": 42.5,
					"currentPeriod": map[string]any{
						"type":  "USAGE_PERIOD_TYPE_WEEKLY",
						"start": "2026-07-27T00:00:00Z",
						"end":   reset.Format(time.RFC3339),
					},
				},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	manager, err := NewManager(Options{
		CredentialPath: credentialPath,
		Clock:          func() time.Time { return now },
		HTTPClient:     client,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := manager.SubscriptionUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Provider != "xai" || snapshot.Plan != "SuperGrok Heavy" || !snapshot.CapturedAt.Equal(now) {
		t.Fatalf("snapshot header = %#v", snapshot)
	}
	if len(snapshot.Limits) != 1 || snapshot.Limits[0].ID != "xai" || len(snapshot.Limits[0].Windows) != 1 {
		t.Fatalf("limits = %#v", snapshot.Limits)
	}
	window := snapshot.Limits[0].Windows[0]
	if window.Kind != "weekly" || window.Label != "Weekly limit" || window.UsedPercent != 42.5 ||
		window.Duration != 7*24*time.Hour || !window.ResetsAt.Equal(reset) {
		t.Fatalf("weekly window = %#v", window)
	}
}

func TestSubscriptionUsageUsesUserLookupForOpaqueTokenAndLegacyMonthlyCredits(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	credentialPath := DefaultCredentialPath(t.TempDir())
	if err := writeStoredCredentials(credentialPath, storedCredentials{
		Version:      credentialSchemaVersion,
		RefreshToken: "refresh",
		AccessToken:  "opaque-access-token",
		ExpiresAt:    now.Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	var requests []string
	client := newInMemoryHTTPClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.Path)
		switch request.URL.Path {
		case "/v1/user":
			writeJSON(t, writer, map[string]any{"userId": "user-from-profile"})
		case "/v1/billing":
			if got := request.Header.Get("x-userid"); got != "user-from-profile" {
				t.Errorf("x-userid = %q", got)
			}
			writeJSON(t, writer, map[string]any{
				"config": map[string]any{
					"monthlyLimit":       map[string]any{"val": 2000},
					"used":               map[string]any{"val": 500},
					"billingPeriodStart": "2026-07-01T00:00:00Z",
					"billingPeriodEnd":   "2026-08-01T00:00:00Z",
				},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	manager, err := NewManager(Options{
		CredentialPath: credentialPath,
		Clock:          func() time.Time { return now },
		HTTPClient:     client,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := manager.SubscriptionUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(requests, ","); got != "/v1/user,/v1/billing" {
		t.Fatalf("requests = %q", got)
	}
	if len(snapshot.Limits) != 1 || len(snapshot.Limits[0].Windows) != 1 {
		t.Fatalf("limits = %#v", snapshot.Limits)
	}
	window := snapshot.Limits[0].Windows[0]
	if window.Label != "Monthly limit" || window.UsedPercent != 25 || window.Duration != 31*24*time.Hour ||
		!window.ResetsAt.Equal(time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("monthly window = %#v", window)
	}
}

func TestUsageUserIDPrefersTeamPrincipal(t *testing.T) {
	t.Parallel()

	token := usageTestJWT(t, map[string]any{
		"sub":            "user-123",
		"principal_type": "Team",
		"principal_id":   "team-456",
	})
	if got := usageUserID(token); got != "team-456" {
		t.Fatalf("usageUserID() = %q", got)
	}
}

func usageTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
