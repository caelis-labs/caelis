package providers

import (
	"context"
	"strings"
	"time"
)

// webSearchRequestContext applies only the request timeout explicitly
// configured for this provider. Provider-native search can legitimately take
// longer than an ordinary request, so zero preserves the caller context
// without introducing a hidden whole-request deadline.
func webSearchRequestContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}
