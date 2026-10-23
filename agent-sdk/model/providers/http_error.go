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
	return classifyImageInputError(base, body, resp.StatusCode)
}

// An invalid image cannot become valid by replaying the same request. Keep
// the original provider diagnostic and code while excluding this specific
// failure from the otherwise broad compatible-gateway retry policy.
type imageInputError struct{ cause error }

func (e *imageInputError) Error() string           { return e.cause.Error() }
func (e *imageInputError) Unwrap() error           { return e.cause }
func (*imageInputError) Retryable() bool           { return false }
func (*imageInputError) ErrorCode() errorcode.Code { return errorcode.InvalidArgument }

func classifyImageInputError(err error, body string, status int) error {
	if status != http.StatusBadRequest && status != http.StatusRequestEntityTooLarge && status != http.StatusUnprocessableEntity {
		return err
	}
	lower := strings.ToLower(body)
	for _, phrase := range []string{
		"unsupported image", "invalid image", "invalid_image", "image_too_large",
		"could not decode image", "unable to decode image", "image could not be decoded",
		"image exceeds", "image too large", "image dimensions exceed", "image size exceeds",
	} {
		if strings.Contains(lower, phrase) {
			return &imageInputError{cause: err}
		}
	}
	return err
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
