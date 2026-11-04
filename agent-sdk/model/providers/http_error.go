package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	base := newProviderHTTPError(resp.StatusCode, body, resp.Header, errorcode.New(httpErrorCode(resp.StatusCode), message))
	if looksLikeContextOverflow(body, resp.StatusCode) {
		return &model.ContextOverflowError{Cause: base}
	}
	return classifyImageInputError(base, body, resp.StatusCode)
}

func httpErrorCode(status int) errorcode.Code {
	code := errorcode.InvalidArgument
	switch {
	case status == http.StatusUnauthorized:
		code = errorcode.Unauthenticated
	case status == http.StatusForbidden:
		code = errorcode.PermissionDenied
	case status == http.StatusNotFound:
		code = errorcode.NotFound
	case status == http.StatusRequestTimeout:
		code = errorcode.Timeout
	case status == http.StatusTooManyRequests:
		code = errorcode.RateLimited
	case status == 529:
		code = errorcode.Overloaded
	case status >= 500 && status != http.StatusNotImplemented && status != http.StatusHTTPVersionNotSupported:
		code = errorcode.Unavailable
	}
	return code
}

type providerHTTPError struct {
	cause error
	code  errorcode.Code
	after time.Duration
}

func (e *providerHTTPError) Error() string             { return e.cause.Error() }
func (e *providerHTTPError) Unwrap() error             { return e.cause }
func (e *providerHTTPError) ErrorCode() errorcode.Code { return e.code }
func (e *providerHTTPError) RetryAfter() time.Duration { return e.after }
func (e *providerHTTPError) Retryable() bool {
	return e.code == errorcode.Unavailable || e.code == errorcode.Timeout || e.code == errorcode.RateLimited || e.code == errorcode.Overloaded
}

func newProviderHTTPError(status int, body string, header http.Header, cause error) error {
	code := httpErrorCode(status)
	// HTTP status alone cannot distinguish exhausted billing from rate limiting.
	// Reuse Responses' structured terminal codes rather than message matching.
	var payload struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden && json.Unmarshal([]byte(body), &payload) == nil {
		if class, ok := responsesTerminalErrorCodes[strings.ToLower(payload.Error.Code)]; ok {
			code = class.errorCode
		} else if class, ok := responsesTerminalErrorTypes[strings.ToLower(payload.Error.Type)]; ok {
			code = class.errorCode
		} else if class, ok := responsesTransientErrorCodes[strings.ToLower(payload.Error.Code)]; ok {
			code = class.errorCode
		} else {
			switch strings.ToLower(payload.Error.Type) {
			case "overloaded_error":
				code = errorcode.Overloaded
			case "rate_limit_error":
				code = errorcode.RateLimited
			case "api_error":
				code = errorcode.Unavailable
			case "timeout_error":
				code = errorcode.Timeout
			}
		}
	}
	return &providerHTTPError{cause: cause, code: code, after: retryAfter(header, time.Now())}
}

func retryAfter(header http.Header, now time.Time) time.Duration {
	var delay time.Duration
	if millis, err := strconv.ParseInt(header.Get("Retry-After-Ms"), 10, 64); err == nil && millis > 0 && millis <= int64((1<<63-1)/time.Millisecond) {
		delay = time.Duration(millis) * time.Millisecond
	}
	value := header.Get("Retry-After")
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 && seconds <= int64((1<<63-1)/time.Second) {
		return max(delay, time.Duration(seconds)*time.Second)
	}
	if date, err := http.ParseTime(value); err == nil {
		delay = max(delay, date.Sub(now))
	}
	return delay
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
	if looksLikeImageInputError(body) {
		return &imageInputError{cause: err}
	}
	return err
}

// HTTP bodies and Responses SSE envelopes use the same permanent image codes
// and messages, including gateways that supply only an underscore-delimited code.
func looksLikeImageInputError(detail string) bool {
	lower := strings.ReplaceAll(strings.ToLower(detail), "_", " ")
	for _, phrase := range []string{
		"unsupported image", "invalid image", "image parse error", "image too small",
		"could not decode image", "unable to decode image", "image could not be decoded",
		"image exceeds", "image too large", "image file too large", "image dimensions exceed", "image size exceeds",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
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
