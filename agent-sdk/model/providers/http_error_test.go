package providers

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestStatusErrorTreatsProviderContextWindow424AsOverflow(t *testing.T) {
	t.Parallel()

	err := statusError(&http.Response{
		StatusCode: 424,
		Body:       io.NopCloser(strings.NewReader("exceeds the context window")),
	})
	if !model.IsContextOverflow(err) {
		t.Fatalf("statusError() = %v, want context overflow", err)
	}
}

func TestStatusErrorKeepsUnrelatedBadRequestRetryable(t *testing.T) {
	t.Parallel()

	err := statusError(&http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"error":"Multimodal data is corrupted"}`)),
	})
	if model.IsContextOverflow(err) {
		t.Fatalf("statusError() = %v, did not want context overflow", err)
	}
	if !model.IsRetryableLLMError(err) {
		t.Fatalf("unclassified gateway failure should remain retryable: %v", err)
	}
}

func TestStatusErrorDoesNotRetryPermanentImageFailures(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"error":{"code":"invalid_request_error","message":"DeepSeek: input[498].image[0]: You have uploaded an unsupported image. Please make sure your image is valid and has one of the following formats: webp, png, jpeg, and gif."}}`,
		`{"error":{"message":"image dimensions exceed max allowed size"}}`,
		`{"error":{"code":"invalid_image_format"}}`,
	} {
		err := statusError(&http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(body))})
		if model.IsRetryableLLMError(err) || model.IsContextOverflow(err) || errorcode.CodeOf(err) != errorcode.InvalidArgument || !strings.Contains(err.Error(), body) {
			t.Fatalf("classification lost permanent image error or its diagnostic: %v", err)
		}
	}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		err := statusError(&http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("invalid image service temporarily unavailable"))})
		if !model.IsRetryableLLMError(err) {
			t.Fatalf("transient status %d was incorrectly made permanent", status)
		}
	}
}

func TestStatusErrorDoesNotTreatBackpressureTokenMessageAsOverflow(t *testing.T) {
	t.Parallel()

	err := statusError(&http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       io.NopCloser(strings.NewReader("too many tokens queued; retry later")),
	})
	if model.IsContextOverflow(err) {
		t.Fatalf("statusError() = %v, did not want context overflow for retryable backpressure", err)
	}
	if !model.IsRetryableLLMError(err) {
		t.Fatalf("statusError() = %v, want retryable error", err)
	}
	if got := errorcode.CodeOf(err); got != errorcode.RateLimited {
		t.Fatalf("CodeOf() = %q, want rate_limited", got)
	}
}
