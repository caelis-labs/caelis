package providers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"google.golang.org/genai"
)

func TestGoogleRPCSDKAndTransportRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		status, calls int
		wait          time.Duration
	}{
		{"minute", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"60s"}]}}`, 429, 2, time.Minute},
		{"daily", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"43200s"}]}}`, 429, 2, 12 * time.Hour},
		{"billing", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"BILLING_DISABLED","domain":"googleapis.com"}]}}`, 429, 1, 0},
		{"credits", `{"error":{"code":402,"status":"RESOURCE_EXHAUSTED"}}`, 402, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var native struct {
					Error genai.APIError `json:"error"`
				}
				if err := json.Unmarshal([]byte(tc.body), &native); err != nil {
					t.Fatal(err)
				}
				nativeErr := classifyGeminiImageError(native.Error)
				var after model.RetryAfterError
				if model.IsRetryableLLMError(nativeErr) != (tc.calls == 2) || !errors.As(nativeErr, &after) || after.RetryAfter() != tc.wait {
					t.Fatalf("native SDK classification lost: %v", nativeErr)
				}
				calls := 0
				client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls++
					status, body := tc.status, tc.body
					if calls > 1 {
						status = 200
						body = `{"candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]},"finishReason":"STOP"}]}`
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				llm := model.WithRetry(newGemini(Config{Model: "gemini-fixture", HTTPClient: client}, "fixture"), model.RetryConfig{})
				start := time.Now()
				var failure error
				for _, err := range llm.Generate(t.Context(), &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "fixture")}}) {
					if err != nil {
						failure = err
					}
				}
				if calls != tc.calls || time.Since(start) != tc.wait || (failure != nil) != (tc.calls == 1) {
					t.Fatalf("calls=%d elapsed=%s err=%v", calls, time.Since(start), failure)
				}
			})
		})
	}
}
