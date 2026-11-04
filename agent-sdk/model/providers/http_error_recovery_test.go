package providers

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestHTTPMixedErrorCodePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		retry      bool
	}{
		{"specific transient", `{"error":{"type":"invalid_request_error","code":"server_error"}}`, true},
		{"specific auth service transient", `{"error":{"type":"authentication_error","code":"auth_service_unavailable"}}`, true},
		{"specific terminal", `{"error":{"type":"api_error","code":"insufficient_quota"}}`, false},
		{"specific permission terminal", `{"error":{"type":"overloaded_error","code":"permission_denied"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := statusError(&http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(tc.body))})
			if got := model.IsRetryableLLMError(err); got != tc.retry {
				t.Fatalf("retry=%t want=%t err=%v", got, tc.retry, err)
			}
		})
	}
}

func TestGoogleRPCStructuredErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		retry      bool
		after      time.Duration
	}{
		{"numeric code RetryInfo", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"120.250s"}]}}`, true, 120250 * time.Millisecond},
		{"minute quota", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests","quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"60s"}]}}`, true, time.Minute},
		{"daily quota server reset", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"43200s"}]}}`, true, 12 * time.Hour},
		{"billing disabled", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"BILLING_DISABLED","domain":"googleapis.com"}]}}`, false, 0},
		{"native terminal status", `{"error":{"code":429,"status":"FAILED_PRECONDITION"}}`, false, 0},
		{"unknown exhaustion", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota billing daily credit exhausted"}}`, true, 0},
		{"daily missing reset", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]}]}}`, true, 0},
		{"untrusted billing domain", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"BILLING_DISABLED","domain":"unknown.invalid"}]}}`, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := statusError(&http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(tc.body))})
			var after model.RetryAfterError
			if model.IsRetryableLLMError(err) != tc.retry || !errors.As(err, &after) || after.RetryAfter() != tc.after {
				t.Fatalf("retry=%t delay=%v want retry=%t delay=%s err=%v", model.IsRetryableLLMError(err), after, tc.retry, tc.after, err)
			}
		})
	}
}

func TestGoogleRPCStructuredCauseBeyondDiagnosticExcerpt(t *testing.T) {
	body := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"` + strings.Repeat("x", 5000) + `","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"BILLING_DISABLED","domain":"googleapis.com"}]}}`
	err := statusError(&http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(body))})
	if model.IsRetryableLLMError(err) || len(err.Error()) > 4200 {
		t.Fatal("late structured cause was discarded or expanded diagnostic bound")
	}
}

func TestGoogleRPCMalformedDetailDoesNotDiscardKnownCause(t *testing.T) {
	body := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":42},{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"BILLING_DISABLED","domain":"googleapis.com"}]}}`
	err := statusError(&http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(body))})
	if model.IsRetryableLLMError(err) {
		t.Fatal("malformed unrelated detail discarded known billing cause")
	}
}

func TestGoogleRPCRetryInfoAndHeaderUseLongestMinimum(t *testing.T) {
	body := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"120.250s"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"-1s"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"1h"}]}}`
	err := statusError(&http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"180"}}, Body: io.NopCloser(strings.NewReader(body))})
	var after model.RetryAfterError
	if !errors.As(err, &after) || after.RetryAfter() != 3*time.Minute {
		t.Fatalf("lost longest valid minimum: %v", err)
	}
}
