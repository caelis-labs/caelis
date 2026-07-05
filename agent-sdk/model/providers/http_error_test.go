package providers

import (
	"io"
	"net/http"
	"strings"
	"testing"

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
}
