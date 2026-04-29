package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultModelRequestMaxRetries     = 5
	defaultModelRetryBaseDelay        = 1 * time.Second
	defaultModelRetryMaxDelay         = 3 * time.Minute
	defaultRateLimitRequestMaxRetries = 7
	defaultRateLimitRetryBaseDelay    = 5 * time.Second
	defaultRateLimitRetryMaxDelay     = 3 * time.Minute
)

type RetryConfig struct {
	MaxRetries          int
	BaseDelay           time.Duration
	MaxDelay            time.Duration
	RateLimitMaxRetries int
	RateLimitBaseDelay  time.Duration
	RateLimitMaxDelay   time.Duration
}

type retryPolicy struct {
	maxRetries   int
	baseDelay    time.Duration
	maxDelay     time.Duration
	backpressure bool
}

func normalizeRetryConfig(cfg RetryConfig) RetryConfig {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = defaultModelRequestMaxRetries
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = defaultModelRetryBaseDelay
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = defaultModelRetryMaxDelay
	}
	if cfg.RateLimitMaxRetries <= 0 {
		cfg.RateLimitMaxRetries = defaultRateLimitRequestMaxRetries
	}
	if cfg.RateLimitBaseDelay <= 0 {
		cfg.RateLimitBaseDelay = defaultRateLimitRetryBaseDelay
	}
	if cfg.RateLimitMaxDelay <= 0 {
		cfg.RateLimitMaxDelay = defaultRateLimitRetryMaxDelay
	}
	return cfg
}

func retryDelayForAttemptWithBounds(retry int, baseDelay, maxDelay time.Duration) time.Duration {
	if retry < 0 {
		retry = 0
	}
	if baseDelay <= 0 {
		baseDelay = time.Second
	}
	if maxDelay <= 0 {
		maxDelay = baseDelay
	}
	delay := baseDelay
	for i := 0; i < retry; i++ {
		delay *= 2
		if delay >= maxDelay {
			return maxDelay
		}
	}
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func retryPolicyForError(cfg RetryConfig, err error) retryPolicy {
	if isRateLimitError(err) {
		return retryPolicy{
			maxRetries:   cfg.RateLimitMaxRetries,
			baseDelay:    cfg.RateLimitBaseDelay,
			maxDelay:     cfg.RateLimitMaxDelay,
			backpressure: true,
		}
	}
	return retryPolicy{
		maxRetries: cfg.MaxRetries,
		baseDelay:  cfg.BaseDelay,
		maxDelay:   cfg.MaxDelay,
	}
}

func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if text == "" {
		return false
	}
	return strings.Contains(text, "http status 429") ||
		strings.Contains(text, "http status 529") ||
		strings.Contains(text, "rate limit") ||
		strings.Contains(text, "ratelimit") ||
		strings.Contains(text, "too many requests") ||
		strings.Contains(text, "overloaded_error") ||
		strings.Contains(text, "server overloaded")
}

func isNonRetryableHTTPError(err error) bool {
	status, ok := httpStatusCodeFromError(err)
	if !ok {
		return false
	}
	if status < 400 || status >= 500 {
		return false
	}
	switch status {
	case 408, 409, 429:
		return false
	default:
		return true
	}
}

func httpStatusCodeFromError(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	text := strings.TrimSpace(err.Error())
	idx := strings.Index(strings.ToLower(text), "http status ")
	if idx < 0 {
		return 0, false
	}
	rest := text[idx+len("http status "):]
	if rest == "" {
		return 0, false
	}
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	var status int
	if _, scanErr := fmt.Sscanf(rest[:end], "%d", &status); scanErr != nil || status <= 0 {
		return 0, false
	}
	return status, true
}

func retryWarningText(attempt int, maxRetries int, delay time.Duration, cause error) string {
	if isRateLimitError(cause) {
		return fmt.Sprintf(
			"warn: llm request hit provider backpressure (rate limit or overload), retrying in %s (%d/%d). Waiting longer before retrying.",
			formatRetryDelay(delay),
			attempt,
			maxRetries,
		)
	}
	return fmt.Sprintf(
		"warn: llm request failed, retrying in %s (%d/%d): %s",
		formatRetryDelay(delay),
		attempt,
		maxRetries,
		summarizeRetryCause(cause),
	)
}

func formatRetryDelay(delay time.Duration) string {
	if delay <= 0 {
		return "0s"
	}
	if delay < time.Second {
		return delay.Round(100 * time.Millisecond).String()
	}
	return delay.Round(time.Second).String()
}

func summarizeRetryCause(err error) string {
	if err == nil {
		return "unknown error"
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return "unknown error"
	}
	prefix, body, found := strings.Cut(text, " body=")
	if !found {
		return text
	}
	prefix = strings.TrimSpace(prefix)
	body = strings.TrimSpace(body)
	bodySummary := summarizeRetryBody(body)
	switch {
	case bodySummary == "":
		return text
	case prefix == "":
		return bodySummary
	default:
		return prefix + ": " + bodySummary
	}
}

func summarizeRetryBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return ""
	}
	for _, key := range []string{"error", "message", "detail"} {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if text := strings.TrimSpace(typed); text != "" {
				return text
			}
		case map[string]any:
			if text, _ := typed["message"].(string); strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return !isNonRetryableHTTPError(err)
}
