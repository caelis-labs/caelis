package providers

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestProviderRecoveryHTTPRetryAfterAndTerminalCodes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, test := range []struct {
		header http.Header
		want   time.Duration
	}{
		{http.Header{"Retry-After": []string{"120"}}, 120 * time.Second},
		{http.Header{"Retry-After": []string{now.Add(3 * time.Minute).Format(http.TimeFormat)}}, 3 * time.Minute},
		{http.Header{"Retry-After-Ms": []string{"2500"}}, 2500 * time.Millisecond},
	} {
		if got := retryAfter(test.header, now); got != test.want {
			t.Fatalf("Retry-After=%s want %s", got, test.want)
		}
	}
	for _, test := range []struct {
		status int
		body   string
		retry  bool
	}{
		{408, `{}`, true}, {429, `{}`, true}, {503, `{}`, true},
		{401, `{}`, false}, {403, `{}`, false}, {400, `{}`, false}, {404, `{}`, false},
		{429, `{"error":{"code":"insufficient_quota"}}`, false},
		{200, `{"error":{"type":"overloaded_error"}}`, true},
	} {
		err := statusError(&http.Response{StatusCode: test.status, Header: http.Header{"Retry-After": []string{"120"}}, Body: io.NopCloser(strings.NewReader(test.body))})
		if got := model.IsRetryableLLMError(err); got != test.retry {
			t.Fatalf("status=%d body=%s retry=%t err=%v", test.status, test.body, got, err)
		}
		var after model.RetryAfterError
		if !errors.As(err, &after) || after.RetryAfter() != 120*time.Second {
			t.Fatal("HTTP error lost server delay")
		}
	}
}

func TestProviderRecoveryActualTransportBeyondFiveAttempts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if calls <= 12 {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}]}`))}, nil
		})}
		llm := model.WithRetry(newOpenAICompat(Config{Model: "fixture", HTTPClient: client}, "fixture"), model.RetryConfig{})
		for _, err := range llm.Generate(t.Context(), &model.Request{}) {
			if err != nil {
				t.Fatal(err)
			}
		}
		if calls != 13 {
			t.Fatalf("transport calls=%d", calls)
		}
	})
}

func TestProviderRecoveryDefaultBudgetsAllow600Seconds(t *testing.T) {
	if defaultStreamResponseHeaderTimeout < 600*time.Second || defaultStreamFirstEventTimeout < 600*time.Second || defaultStreamIdleTimeout < 600*time.Second {
		t.Fatal("default provider budget silently truncates compaction")
	}
	options := buildGeminiHTTPOptions("", nil)
	if options.RetryOptions == nil || options.RetryOptions.Attempts == nil || *options.RetryOptions.Attempts != 1 {
		t.Fatal("Gemini retains hidden SDK retries")
	}
}

func TestProviderRecoveryGeminiRetainsRetryAfterWithoutSDKRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"120"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":429,"message":"busy"}}`))}, nil
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]},"finishReason":"STOP"}]}`))}, nil
		})}
		llm := model.WithRetry(newGemini(Config{Model: "gemini-fixture", HTTPClient: client}, "fixture"), model.RetryConfig{})
		var wait int64
		for event, err := range llm.Generate(t.Context(), &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "fixture")}}) {
			if err != nil {
				t.Fatal(err)
			}
			if event != nil && event.AttemptReset != nil {
				wait = event.AttemptReset.RetryDelayMillis
			}
		}
		if calls != 2 || wait != 120000 {
			t.Fatalf("calls=%d retry delay=%d", calls, wait)
		}
	})
}
