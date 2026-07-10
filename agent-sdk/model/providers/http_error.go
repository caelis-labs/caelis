package providers

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
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
		return errorcode.New(errorcode.Internal, "model: empty http response")
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	body := strings.TrimSpace(string(raw))
	message := ""
	if body == "" {
		message = fmt.Sprintf("model: http status %d", resp.StatusCode)
	} else {
		message = fmt.Sprintf("model: http status %d body=%s", resp.StatusCode, body)
	}
	code := errorcode.InvalidArgument
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		code = errorcode.Unauthenticated
	case resp.StatusCode == http.StatusForbidden:
		code = errorcode.PermissionDenied
	case resp.StatusCode == http.StatusNotFound:
		code = errorcode.NotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		code = errorcode.RateLimited
	case resp.StatusCode == 529:
		code = errorcode.Overloaded
	case resp.StatusCode >= 500:
		code = errorcode.Unavailable
	}
	base := errorcode.New(code, message)
	if looksLikeContextOverflow(body, resp.StatusCode) {
		return &model.ContextOverflowError{Cause: base}
	}
	return base
}

func looksLikeContextOverflow(body string, statusCode int) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusRequestEntityTooLarge && statusCode != http.StatusFailedDependency {
		return false
	}
	if strings.TrimSpace(body) == "" {
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
