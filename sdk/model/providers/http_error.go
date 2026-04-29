package providers

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/OnslaughtSnail/caelis/sdk/model"
)

// contextOverflowKeywords are vendor-agnostic patterns that indicate a context
// window overflow in HTTP error responses. Individual providers may also
// detect overflow in their own response parsing.
var contextOverflowKeywords = []string{
	"context length",
	"context window",
	"prompt is too long",
	"too many tokens",
	"maximum context",
	"input is too long",
	"token limit",
	"max context",
}

func statusError(resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("model: empty http response")
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	body := strings.TrimSpace(string(raw))
	var base error
	if body == "" {
		base = fmt.Errorf("model: http status %d", resp.StatusCode)
	} else {
		base = fmt.Errorf("model: http status %d body=%s", resp.StatusCode, body)
	}
	if looksLikeContextOverflow(body, resp.StatusCode) {
		return &model.ContextOverflowError{Cause: base}
	}
	return base
}

func looksLikeContextOverflow(body string, statusCode int) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusRequestEntityTooLarge {
		return false
	}
	lower := strings.ToLower(body)
	for _, kw := range contextOverflowKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
