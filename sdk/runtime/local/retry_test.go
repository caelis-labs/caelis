package local

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIsRateLimitErrorTreatsProviderOverloadAsBackpressure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "too many requests",
			err:  errors.New("model: http status 429 body={\"error\":\"too many requests\"}"),
			want: true,
		},
		{
			name: "provider overload status",
			err:  errors.New("model: http status 529 body={\"error\":\"overloaded_error\"}"),
			want: true,
		},
		{
			name: "provider overload text",
			err:  errors.New("model: server overloaded, try again later"),
			want: true,
		},
		{
			name: "plain transport failure",
			err:  errors.New("dial tcp: connection reset by peer"),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isRateLimitError(tc.err); got != tc.want {
				t.Fatalf("isRateLimitError(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRetryPolicyForErrorUsesBackpressureBudgetForProviderOverload(t *testing.T) {
	t.Parallel()

	cfg := RetryConfig{
		MaxRetries:          2,
		BaseDelay:           25 * time.Millisecond,
		MaxDelay:            50 * time.Millisecond,
		RateLimitMaxRetries: 7,
		RateLimitBaseDelay:  5 * time.Second,
		RateLimitMaxDelay:   45 * time.Second,
	}
	policy := retryPolicyForError(cfg, errors.New("model: http status 529 body={\"error\":\"overloaded_error\"}"))
	if !policy.backpressure {
		t.Fatal("policy.backpressure = false, want true")
	}
	if got, want := policy.maxRetries, cfg.RateLimitMaxRetries; got != want {
		t.Fatalf("policy.maxRetries = %d, want %d", got, want)
	}
	if got, want := policy.baseDelay, cfg.RateLimitBaseDelay; got != want {
		t.Fatalf("policy.baseDelay = %s, want %s", got, want)
	}
	if got, want := policy.maxDelay, cfg.RateLimitMaxDelay; got != want {
		t.Fatalf("policy.maxDelay = %s, want %s", got, want)
	}
}

func TestRetryWarningTextUsesBackpressureWording(t *testing.T) {
	t.Parallel()

	text := retryWarningText(2, 8, 5*time.Second, errors.New("model: http status 529 body={\"error\":\"overloaded_error\"}"))
	if !strings.Contains(strings.ToLower(text), "provider backpressure") {
		t.Fatalf("retryWarningText() = %q, want provider backpressure wording", text)
	}
}
